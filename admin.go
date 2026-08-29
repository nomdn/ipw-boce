package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 节点远端配置托管 ====================
//
// 本服务作为节点 remote-config-url 的提供方：
//   GET /remote-config            → 全局缺省配置（nodeId="global"）
//   GET /remote-config/:nodeId    → 全局配置 + 该节点覆盖项（节点配置优先，逐键合并）
//
// 配置内容为 setting.json 同构 JSON（middlewareConfig 形状），由管理 API 维护：
//   PUT /admin/node-configs/:nodeId  body = 任意合法 JSON 对象
// api-keys / ws-keys 等密钥不会由本服务自动注入——托管内容即所见即所得，密钥管理归节点本地。

// registerRemoteConfigRoutes 注册配置托管路由（公开访问，与原版节点拉取远端配置的语义一致）
func registerRemoteConfigRoutes(router *gin.Engine) {
	router.GET("/remote-config", func(c *gin.Context) {
		serveNodeConfig(c, "global")
	})
	router.GET("/remote-config/:nodeId", func(c *gin.Context) {
		serveNodeConfig(c, c.Param("nodeId"))
	})
}

// serveNodeConfig 输出合并后的节点配置 JSON；无任何配置时返回 404
func serveNodeConfig(c *gin.Context, nodeID string) {
	merged, found := mergeNodeConfig(nodeID)
	if !found {
		apiError(c, http.StatusNotFound, "no config hosted for node "+nodeID)
		return
	}
	c.Data(http.StatusOK, "application/json", merged)
}

// mergeNodeConfig 取 global 配置为底、nodeId 配置逐键覆盖；仅存在其一则直接返回该份
func mergeNodeConfig(nodeID string) ([]byte, bool) {
	ctx, cancel := dbCtx()
	defer cancel()
	var rows []NodeConfig
	if err := db.WithContext(ctx).Where("node_id IN ?", []string{"global", nodeID}).Find(&rows).Error; err != nil {
		return nil, false
	}
	var globalObj, nodeObj map[string]any
	for _, r := range rows {
		var obj map[string]any
		if json.Unmarshal([]byte(r.Config), &obj) != nil {
			continue // 存了非法 JSON 的记录跳过（管理 API 写入时已校验，此处防御）
		}
		if r.NodeID == "global" {
			globalObj = obj
		} else {
			nodeObj = obj
		}
	}
	switch {
	case nodeObj == nil && globalObj == nil:
		return nil, false
	case nodeObj == nil:
		return mustJSON(globalObj), true
	case globalObj == nil:
		return mustJSON(nodeObj), true
	default:
		for k, v := range nodeObj {
			globalObj[k] = v
		}
		return mustJSON(globalObj), true
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// ==================== 管理 API ====================

// registerAdminRoutes 注册 /admin/* 路由；配置 admin-token 时校验 Authorization: Bearer
func registerAdminRoutes(router *gin.Engine) {
	admin := router.Group("/admin")
	if ADMIN_TOKEN != "" {
		admin.Use(func(c *gin.Context) {
			if c.GetHeader("Authorization") != "Bearer "+ADMIN_TOKEN {
				apiError(c, http.StatusUnauthorized, "Unauthorized")
				c.Abort()
				return
			}
		})
	}

	// 服务状态：版本 / 运行时长 / WS 在线数 / 数据库驱动 / 队列堆积
	admin.GET("/status", func(c *gin.Context) {
		wsPeers := 0
		if wsSrv != nil {
			wsSrv.mu.Lock()
			wsPeers = len(wsSrv.peers)
			wsSrv.mu.Unlock()
		}
		c.JSON(http.StatusOK, gin.H{
			"version":       VERSION,
			"commit":        COMMIT,
			"buildTime":     BUILD_TIME,
			"uptimeSeconds": int64(time.Since(STARTED_AT).Seconds()),
			"wsPeers":       wsPeers,
			"wsEnabled":     WS_PORT > 0,
			"dbDriver":      db.driver,
		})
	})

	// 节点在线快照（nodes 表）
	admin.GET("/nodes", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var nodes []Node
		if err := db.WithContext(ctx).Order("online desc, node_id asc").Find(&nodes).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, nodes)
	})

	// 节点在线/离线历史
	admin.GET("/nodes/:nodeId/events", func(c *gin.Context) {
		limit := clampLimit(c.Query("limit"), 100)
		ctx, cancel := dbCtx()
		defer cancel()
		var events []NodeEvent
		if err := db.WithContext(ctx).Where("node_id = ?", c.Param("nodeId")).
			Order("id desc").Limit(limit).Find(&events).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, events)
	})

	// 拨测记录（probe_results）
	admin.GET("/probes", func(c *gin.Context) {
		limit := clampLimit(c.Query("limit"), 100)
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&ProbeResult{})
		if v := c.Query("node"); v != "" {
			q = q.Where("node_id = ?", v)
		}
		if v := c.Query("type"); v != "" {
			q = q.Where("api_type = ?", v)
		}
		if v := c.Query("since"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				q = q.Where("created_at >= ?", t.UTC())
			}
		}
		var rows []ProbeResult
		if err := q.Order("id desc").Limit(limit).Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, rows)
	})

	// 统计汇总：按 API 类型 / 节点维度聚合（request_stats）
	admin.GET("/stats/summary", func(c *gin.Context) {
		hours := clampFloat(c.Query("hours"), 24, 1, 24*90)
		since := time.Now().UTC().Add(-time.Duration(hours*float64(time.Hour))).Unix() / 60
		ctx, cancel := dbCtx()
		defer cancel()

		var byAPI []struct {
			APIType      string `json:"apiType"`
			Total        int64  `json:"total"`
			Errors       int64  `json:"errors"`
			LatencySumMs int64  `json:"latencySumMs"`
		}
		if err := db.WithContext(ctx).Model(&RequestStat{}).
			Select("api_type, SUM(total) as total, SUM(errors) as errors, SUM(latency_sum_ms) as latency_sum_ms").
			Where("minute >= ?", since).Group("api_type").Find(&byAPI).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		var byNode []struct {
			NodeID       string `json:"nodeId"`
			Total        int64  `json:"total"`
			Errors       int64  `json:"errors"`
			LatencySumMs int64  `json:"latencySumMs"`
		}
		if err := db.WithContext(ctx).Model(&RequestStat{}).
			Select("node_id, SUM(total) as total, SUM(errors) as errors, SUM(latency_sum_ms) as latency_sum_ms").
			Where("minute >= ?", since).Group("node_id").Find(&byNode).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"hours": hours, "byApiType": byAPI, "byNode": byNode})
	})

	// 统计时间序列（按分钟桶）
	admin.GET("/stats/timeseries", func(c *gin.Context) {
		hours := clampFloat(c.Query("hours"), 24, 1, 24*90)
		since := time.Now().UTC().Add(-time.Duration(hours*float64(time.Hour))).Unix() / 60
		ctx, cancel := dbCtx()
		defer cancel()
		var rows []struct {
			Minute int64 `json:"minute"`
			Total  int64 `json:"total"`
			Errors int64 `json:"errors"`
		}
		if err := db.WithContext(ctx).Model(&RequestStat{}).
			Select("minute, SUM(total) as total, SUM(errors) as errors").
			Where("minute >= ?", since).Group("minute").Order("minute asc").Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, rows)
	})

	// ===== 节点配置 CRUD =====

	// 列出全部托管配置（含 global）
	admin.GET("/node-configs", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var rows []NodeConfig
		if err := db.WithContext(ctx).Order("node_id asc").Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]gin.H, 0, len(rows))
		for _, r := range rows {
			out = append(out, gin.H{"nodeId": r.NodeID, "updatedAt": r.UpdatedAt, "sizeBytes": len(r.Config)})
		}
		c.JSON(http.StatusOK, out)
	})

	// 读取某节点配置原文
	admin.GET("/node-configs/:nodeId", func(c *gin.Context) {
		var row NodeConfig
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Where("node_id = ?", c.Param("nodeId")).First(&row).Error; err != nil {
			apiError(c, http.StatusNotFound, "config not found for node "+c.Param("nodeId"))
			return
		}
		c.Data(http.StatusOK, "application/json", []byte(row.Config))
	})

	// 写入/更新某节点配置（body 须为合法 JSON 对象；nodeId=global 即全局缺省配置）
	admin.PUT("/node-configs/:nodeId", func(c *gin.Context) {
		nodeID := c.Param("nodeId")
		body, err := c.GetRawData()
		if err != nil {
			apiError(c, http.StatusBadRequest, "read body: "+err.Error())
			return
		}
		var obj map[string]any
		if err := json.Unmarshal(body, &obj); err != nil {
			apiError(c, http.StatusBadRequest, "body must be a valid JSON object: "+err.Error())
			return
		}
		normalized := mustJSON(obj)
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Where("node_id = ?", nodeID).
			Assign(map[string]any{"config": string(normalized), "updated_at": time.Now().UTC()}).
			FirstOrCreate(&NodeConfig{NodeID: nodeID, Config: string(normalized)}).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"nodeId": nodeID, "updated": true, "sizeBytes": len(normalized)})
	})

	// 删除某节点配置
	admin.DELETE("/node-configs/:nodeId", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		res := db.WithContext(ctx).Where("node_id = ?", c.Param("nodeId")).Delete(&NodeConfig{})
		if res.Error != nil {
			apiError(c, http.StatusInternalServerError, res.Error.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"nodeId": c.Param("nodeId"), "deleted": res.RowsAffected > 0})
	})
}

// clampLimit 解析 limit 参数（缺省 def，上限 1000）
func clampLimit(raw string, def int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > 1000 {
		return 1000
	}
	return n
}

// clampFloat 解析浮点参数（缺省 def，夹在 [min,max]）
func clampFloat(raw string, def, min, max float64) float64 {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f <= 0 {
		return def
	}
	if f < min {
		return min
	}
	if f > max {
		return max
	}
	return f
}

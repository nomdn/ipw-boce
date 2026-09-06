package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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

// registerAdminRoutes 注册 /admin/* 路由；配置 admin-token 或 jwt-secret 时校验 Authorization: Bearer
func registerAdminRoutes(router *gin.Engine) {
	// 登录端点（免鉴权）：校验 admin-user/admin-password 签发 JWT，见 auth.go
	if jwtEnabled() {
		router.POST("/admin/login", loginHandler)
		// 公开自助注册（邮箱验证码）——依赖 JWT 启用（注册出的账号需能登录），见 register.go
		registerPublicRoutes(router)
	}

	admin := router.Group("/admin")
	if ADMIN_TOKEN != "" || jwtEnabled() {
		admin.Use(adminAuthMiddleware())
	}

	// 运维/管理类接口（节点拓扑、节点配置托管、全站流量大盘）仅 admin 可见；
	// 其余登录接口（拨测工具 / 自己任务 / 站内信 / 个人资料）user 亦可用。
	restricted := admin.Group("", adminOnly())

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

	// 节点在线快照（nodes 表）—— admin only
	restricted.GET("/nodes", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var nodes []Node
		if err := db.WithContext(ctx).Order("online desc, node_id asc").Find(&nodes).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, nodes)
	})

	// 节点在线/离线历史 —— admin only
	restricted.GET("/nodes/:nodeId/events", func(c *gin.Context) {
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

	// 一键拨测：对节点池全部（或 body.nodes 子集）同步批量下发拨测并聚合返回（见 admin_probe.go）
	admin.POST("/nodes/probe/:apiType/*raw", batchProbeHandler)

	// 拨测记录（probe_results）；cat=类别过滤：sched=定时拨测(source=sched)，biz=业务拨测(source!=sched)
	// user 只能看"自己创建任务"的定时拨测明细（source=sched 且 task_id 归自己）；biz/ws/http/他人 sched 一律不可见。
	admin.GET("/probes", func(c *gin.Context) {
		limit := clampLimit(c.Query("limit"), 100)
		ctx, cancel := dbCtx()
		defer cancel()
		uid, role, _ := currentUserFromCtx(c)
		q := db.WithContext(ctx).Model(&ProbeResult{})
		if role == RoleUser {
			ownIDs, err := ownTaskIDs(db.WithContext(ctx), uid)
			if err != nil {
				apiError(c, http.StatusInternalServerError, err.Error())
				return
			}
			if len(ownIDs) == 0 {
				// 该用户没有任何任务 → 无可见 sched 明细
				c.JSON(http.StatusOK, []ProbeResult{})
				return
			}
			q = q.Where("source = ? AND task_id IN ?", sourceSched, ownIDs)
		}
		if v := c.Query("node"); v != "" {
			q = q.Where("node_id = ?", v)
		}
		if v := c.Query("type"); v != "" {
			q = q.Where("api_type = ?", v)
		}
		if v := c.Query("cat"); v != "" {
			switch v {
			case "sched":
				if role != RoleUser {
					q = q.Where("source = ?", sourceSched)
				}
			case "biz":
				if role != RoleUser {
					q = q.Where("source <> ?", sourceSched)
				} else {
					// user 已强制只看自己 sched，biz 请求无可见行
					c.JSON(http.StatusOK, []ProbeResult{})
					return
				}
			default:
				apiError(c, http.StatusBadRequest, "invalid cat (sched|biz)")
				return
			}
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

	// 统计汇总：按 API 类型 / 节点维度聚合（request_stats）—— admin only（user 大盘走自己的任务报告）
	restricted.GET("/stats/summary", func(c *gin.Context) {
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

	// 统计时间序列（按分钟桶）—— admin only
	restricted.GET("/stats/timeseries", func(c *gin.Context) {
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

	// 列出全部托管配置（含 global）—— admin only
	restricted.GET("/node-configs", func(c *gin.Context) {
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

	// 读取某节点配置原文 —— admin only
	restricted.GET("/node-configs/:nodeId", func(c *gin.Context) {
		var row NodeConfig
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Where("node_id = ?", c.Param("nodeId")).First(&row).Error; err != nil {
			apiError(c, http.StatusNotFound, "config not found for node "+c.Param("nodeId"))
			return
		}
		c.Data(http.StatusOK, "application/json", []byte(row.Config))
	})

	// 写入/更新某节点配置（body 须为合法 JSON 对象；nodeId=global 即全局缺省配置）—— admin only
	restricted.PUT("/node-configs/:nodeId", func(c *gin.Context) {
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

	// 删除某节点配置 —— admin only
	restricted.DELETE("/node-configs/:nodeId", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		res := db.WithContext(ctx).Where("node_id = ?", c.Param("nodeId")).Delete(&NodeConfig{})
		if res.Error != nil {
			apiError(c, http.StatusInternalServerError, res.Error.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"nodeId": c.Param("nodeId"), "deleted": res.RowsAffected > 0})
	})

	// ===== 定时拨测任务 CRUD（SLA 数据源；调度器见 probe_task.go） =====

	// 任务可用拨测类型元信息（前端表单选项用）
	admin.GET("/tasks/meta", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"types": knownProbeTaskTypes(), "defaultInterval": 60, "minInterval": 10})
	})

	// 列表全部任务（user 仅见自己创建的；admin/静态 token 见全量）
	admin.GET("/tasks", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&ProbeTask{})
		if uid, role, _ := currentUserFromCtx(c); role == RoleUser {
			q = q.Where("owner_id = ?", uid)
		}
		var rows []ProbeTask
		if err := q.Order("id asc").Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, tasksWithOwner(rows))
	})

	// 读取单个任务（user 仅自己创建的）
	admin.GET("/tasks/:id", func(c *gin.Context) {
		var t ProbeTask
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).First(&t, idParam(c)).Error; err != nil {
			apiError(c, http.StatusNotFound, "task not found")
			return
		}
		if taskOwnedByUserButNot(c, &t) {
			apiError(c, http.StatusForbidden, "not your task")
			return
		}
		c.JSON(http.StatusOK, taskWithOwner(&t))
	})

	// 新建任务（创建者 = 当前登录用户；静态 token/无 uid 视为无归属 ownerId=0）
	admin.POST("/tasks", func(c *gin.Context) {
		// 邮箱未验证的普通用户禁止建 SLA 任务（其掉线告警邮件需可靠邮箱）
		if msg := emailVerifiedBlocked(c); msg != "" {
			apiError(c, http.StatusForbidden, msg)
			return
		}
		var t ProbeTask
		if err := c.ShouldBindJSON(&t); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if msg := validateTask(&t); msg != "" {
			apiError(c, http.StatusBadRequest, msg)
			return
		}
		uid, _, _ := currentUserFromCtx(c)
		t.OwnerID = uid // 创建者即所有者
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Create(&t).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, t)
	})

	// 更新任务（全量覆盖；user 仅自己创建的）
	admin.PUT("/tasks/:id", func(c *gin.Context) {
		var t ProbeTask
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).First(&t, idParam(c)).Error; err != nil {
			apiError(c, http.StatusNotFound, "task not found")
			return
		}
		if taskOwnedByUserButNot(c, &t) {
			apiError(c, http.StatusForbidden, "not your task")
			return
		}
		var in ProbeTask
		if err := c.ShouldBindJSON(&in); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		in.ID = t.ID
		if msg := validateTask(&in); msg != "" {
			apiError(c, http.StatusBadRequest, msg)
			return
		}
		in.CreatedAt = t.CreatedAt
		in.OwnerID = t.OwnerID // 编辑不改所有者（保持创建者）
		if err := db.WithContext(ctx).Save(&in).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, taskWithOwner(&in))
	})

	// 启停单个任务（前端开关；只改 enabled，保留其余配置；user 仅自己创建的）
	admin.PATCH("/tasks/:id/enabled", func(c *gin.Context) {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body")
			return
		}
		// 未验证邮箱的 user 不允许"启用"任务（停用放行）
		if body.Enabled {
			if msg := emailVerifiedBlocked(c); msg != "" {
				apiError(c, http.StatusForbidden, msg)
				return
			}
		}
		ctx, cancel := dbCtx()
		defer cancel()
		var t ProbeTask
		if err := db.WithContext(ctx).First(&t, idParam(c)).Error; err != nil {
			apiError(c, http.StatusNotFound, "task not found")
			return
		}
		if taskOwnedByUserButNot(c, &t) {
			apiError(c, http.StatusForbidden, "not your task")
			return
		}
		res := db.WithContext(ctx).Model(&ProbeTask{}).Where("id = ?", idParam(c)).
			Update("enabled", body.Enabled)
		if res.Error != nil {
			apiError(c, http.StatusInternalServerError, res.Error.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": idParam(c), "enabled": body.Enabled, "updated": res.RowsAffected > 0})
	})

	// 删除任务（user 仅自己创建的）
	admin.DELETE("/tasks/:id", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var t ProbeTask
		if err := db.WithContext(ctx).First(&t, idParam(c)).Error; err != nil {
			apiError(c, http.StatusNotFound, "task not found")
			return
		}
		if taskOwnedByUserButNot(c, &t) {
			apiError(c, http.StatusForbidden, "not your task")
			return
		}
		res := db.WithContext(ctx).Delete(&ProbeTask{}, idParam(c))
		if res.Error != nil {
			apiError(c, http.StatusInternalServerError, res.Error.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": res.RowsAffected > 0})
	})

	// SLA 聚合读取（source=sched；解析 detail/ssl 双栈与特殊字段，见 sla.go）
	registerTaskSlaRoutes(admin)

	// 我的任务时序曲线（普通用户大盘趋势，见 tasks_series.go）
	registerTaskMineSeriesRoutes(admin)

	// 用户管理（仅 admin，见 users.go）
	registerUserRoutes(admin)

	// 站内信（本人通知；铃铛入口，见 notices.go）
	registerNoticeRoutes(admin)

	// 个人资料（本人邮箱/口令，登录即可，见 profile.go）
	registerProfileRoutes(admin)
}

// ownTaskIDs 返回某 uid 拥有(owner_id=uid)的全部定时任务 id。role=admin 时通常不调用(admin 看全量)。
func ownTaskIDs(g *gorm.DB, uid uint) ([]uint, error) {
	var ids []uint
	if err := g.Model(&ProbeTask{}).Where("owner_id = ?", uid).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// taskOwnedByUserButNot 返回 true 表示"当前是普通 user 且不是该任务 owner"，应拒绝访问。
// admin / 静态 token(role=admin) 一律可访问；role=user 仅 owner_id == 自己 uid 可访问。
func taskOwnedByUserButNot(c *gin.Context, t *ProbeTask) bool {
	uid, role, _ := currentUserFromCtx(c)
	if role != RoleUser {
		return false
	}
	return t.OwnerID != uid
}


// ownerUsernames 批量查一组 users.id → username（用于任务返回 owner 名；查询失败返回空 map）
func ownerUsernames(ids []uint) map[uint]string {
	out := map[uint]string{}
	seen := map[uint]bool{}
	var keep []uint
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		keep = append(keep, id)
	}
	if len(keep) == 0 || db == nil {
		return out
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var rows []User
	if err := db.WithContext(ctx).Select("id, username").Where("id IN ?", keep).Find(&rows).Error; err != nil {
		return out
	}
	for i := range rows {
		out[rows[i].ID] = rows[i].Username
	}
	return out
}

// taskWithOwner 单条任务 → JSON（附 ownerUsername；ownerId=0 或无此用户时为 ""）
func taskWithOwner(t *ProbeTask) map[string]any {
	h := t.toGinH()
	names := ownerUsernames([]uint{t.OwnerID})
	h["ownerUsername"] = names[t.OwnerID]
	return h
}

// tasksWithOwner 任务列表 → JSON 数组（各自附 ownerUsername）
func tasksWithOwner(rows []ProbeTask) []map[string]any {
	ids := make([]uint, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].OwnerID)
	}
	names := ownerUsernames(ids)
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		h := rows[i].toGinH()
		h["ownerUsername"] = names[rows[i].OwnerID]
		out = append(out, h)
	}
	return out
}

// idParam 解析 :id 为 uint（非法返回 0，交由查询自然匹配不到）
func idParam(c *gin.Context) uint {
	n, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	return uint(n)
}

// validateTask 校验任务字段，返回错误文案（空 = 合法）
func validateTask(t *ProbeTask) string {
	if strings.TrimSpace(t.Name) == "" {
		return "name is required"
	}
	validType := false
	for _, k := range knownProbeTaskTypes() {
		if t.APIType == k {
			validType = true
			break
		}
	}
	if !validType {
		return "apiType must be one of " + strings.Join(knownProbeTaskTypes(), ",")
	}
	if strings.TrimSpace(t.Target) == "" {
		return "target is required"
	}
	if t.Interval <= 0 {
		return "intervalSec is required"
	}
	if t.Interval < 10 {
		t.Interval = 10
	}
	if t.NodeScope != "custom" {
		t.NodeScope = "all"
	}
	if t.ExpectStatus == "" {
		t.ExpectStatus = "2xx"
	}
	if t.SlowMs < 0 {
		t.SlowMs = 0
	}
	return ""
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

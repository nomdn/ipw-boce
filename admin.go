package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== /admin/* 管理接口（路由与报文与原版一致） ====================

func registerAdminRoutes(r *gin.Engine) {
	if jwtEnabled() {
		r.POST("/admin/login", loginHandler)
		registerPublicRoutes(r)
	}
	admin := r.Group("/admin", adminAuthMiddleware())
	restricted := admin.Group("", adminOnly())
	registerSiteRoutes(r, restricted)

	admin.GET("/status", func(c *gin.Context) {
		peers := 0
		if wsSrv != nil {
			peers = wsSrv.PeerCount()
		}
		c.JSON(http.StatusOK, gin.H{"version": VERSION, "commit": COMMIT, "buildTime": BUILD_TIME,
			"uptimeSeconds": int64(time.Since(startedAt).Seconds()), "wsPeers": peers,
			"wsEnabled": WS_PORT > 0, "dbDriver": db.driver})
	})
	registerTimeRangeRoutes(admin)
	registerNodeDefRoutes(restricted)

	// ---- 节点 ----
	restricted.GET("/nodes", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var nodes []Node
		if err := db.WithContext(ctx).Order("online desc, node_id asc").Find(&nodes).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		labels := poolLabelMap()
		out := make([]gin.H, 0, len(nodes))
		for _, n := range nodes {
			label := labels[n.NodeID]
			if label == "" {
				label = n.Label
			}
			out = append(out, gin.H{"nodeId": n.NodeID, "label": label, "online": n.Online, "remoteAddr": n.RemoteAddr,
				"version": n.Version, "capabilities": n.Capabilities, "firstSeenAt": n.FirstSeenAt, "lastSeenAt": n.LastSeenAt})
		}
		c.JSON(http.StatusOK, out)
	})
	restricted.GET("/nodes/:nodeId/events", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var ev []NodeEvent
		db.WithContext(ctx).Where("node_id = ?", c.Param("nodeId")).Order("id desc").Limit(clampLimit(c.Query("limit"), 100)).Find(&ev)
		c.JSON(http.StatusOK, ev)
	})
	restricted.DELETE("/nodes/:nodeId", func(c *gin.Context) {
		id := c.Param("nodeId")
		if strings.TrimSpace(id) == "" || id == "global" {
			apiError(c, http.StatusBadRequest, "invalid node id")
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		res := db.WithContext(ctx).Where("node_id = ?", id).Delete(&Node{})
		if res.RowsAffected == 0 {
			apiError(c, http.StatusNotFound, "node not found")
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": id})
	})
	admin.GET("/nodes/brief", func(c *gin.Context) { c.JSON(http.StatusOK, nodeBriefItems()) })
	admin.POST("/nodes/probe/:apiType/*raw", batchProbeHandler)

	// ---- 拨测明细 ----
	admin.GET("/probes", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		q, ok := probeListQuery(c, db.WithContext(ctx))
		if !ok {
			return
		}
		var rows []ProbeResult
		if err := q.Order("id desc").Limit(clampLimit(c.Query("limit"), 100)).Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, rows)
	})

	// ---- 统计（admin only） ----
	restricted.GET("/stats/summary", statsSummary)
	restricted.GET("/stats/timeseries", func(c *gin.Context) {
		tr := parseTimeRange(c, 24)
		step := stepForWindow(tr.Hours)
		bucket := fmt.Sprintf("(minute / %d) * %d", step, step)
		ctx, cancel := dbCtx()
		defer cancel()
		var rows []struct {
			Minute int64 `json:"minute"`
			Total  int64 `json:"total"`
			Errors int64 `json:"errors"`
		}
		q := db.WithContext(ctx).Model(&RequestStat{}).Select(bucket+" as minute, SUM(total) as total, SUM(errors) as errors").
			Where("minute >= ? AND minute <= ?", tr.From.Unix()/60, tr.To.Unix()/60)
		if nodes := statsNodes(c); len(nodes) > 0 {
			q = q.Where("node_id IN ?", nodes)
		}
		q.Group(bucket).Order("minute asc").Find(&rows)
		c.JSON(http.StatusOK, rows)
	})

	// ---- 节点托管配置 ----
	restricted.GET("/node-configs", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var rows []NodeConfig
		db.WithContext(ctx).Order("node_id asc").Find(&rows)
		out := make([]gin.H, 0, len(rows))
		for _, r := range rows {
			out = append(out, gin.H{"nodeId": r.NodeID, "updatedAt": r.UpdatedAt, "sizeBytes": len(r.Config)})
		}
		c.JSON(http.StatusOK, out)
	})
	restricted.GET("/node-configs/:nodeId", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var row NodeConfig
		if err := db.WithContext(ctx).Where("node_id = ?", c.Param("nodeId")).First(&row).Error; err != nil {
			apiError(c, http.StatusNotFound, "config not found for node "+c.Param("nodeId"))
			return
		}
		c.Data(http.StatusOK, "application/json", []byte(row.Config))
	})
	restricted.PUT("/node-configs/:nodeId", func(c *gin.Context) {
		body, _ := c.GetRawData()
		var obj map[string]any
		if err := json.Unmarshal(body, &obj); err != nil {
			apiError(c, http.StatusBadRequest, "body must be a valid JSON object: "+err.Error())
			return
		}
		norm := mustRaw(obj)
		if err := saveNodeConfig(c.Param("nodeId"), norm); err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"nodeId": c.Param("nodeId"), "updated": true, "sizeBytes": len(norm)})
	})
	restricted.DELETE("/node-configs/:nodeId", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		res := db.WithContext(ctx).Where("node_id = ?", c.Param("nodeId")).Delete(&NodeConfig{})
		c.JSON(http.StatusOK, gin.H{"nodeId": c.Param("nodeId"), "deleted": res.RowsAffected > 0})
	})

	registerTaskRoutes(admin)
	registerUserRoutes(admin)
	registerNoticeRoutes(admin)
	registerProfileRoutes(admin)
	registerPointsRoutes(admin, restricted)

	registerUptimeRoutes(restricted)
	registerMaintenanceRoutes(restricted)
	registerOTARoutes(restricted)
	registerNodeRuntimeConfigRoutes(restricted)
	registerExportRoutes(admin, restricted)
	registerDonateRoutes(admin, restricted)
	registerAgentTokenRoutes(admin)
}

func saveNodeConfig(nodeID string, cfg []byte) error {
	ctx, cancel := dbCtx()
	defer cancel()
	return db.WithContext(ctx).Where("node_id = ?", nodeID).
		Assign(map[string]any{"config": string(cfg), "updated_at": time.Now().UTC()}).
		FirstOrCreate(&NodeConfig{NodeID: nodeID, Config: string(cfg)}).Error
}

// registerRemoteConfigRoutes 公开：GET /remote-config（global）与 /remote-config/:nodeId（global 底 + 节点覆盖）
func registerRemoteConfigRoutes(r *gin.Engine) {
	serve := func(c *gin.Context, nodeID string) {
		ctx, cancel := dbCtx()
		defer cancel()
		var rows []NodeConfig
		db.WithContext(ctx).Where("node_id IN ?", []string{"global", nodeID}).Find(&rows)
		var g, n map[string]any
		for _, row := range rows {
			var obj map[string]any
			if json.Unmarshal([]byte(row.Config), &obj) != nil {
				continue
			}
			if row.NodeID == "global" {
				g = obj
			} else {
				n = obj
			}
		}
		if g == nil && n == nil {
			apiError(c, http.StatusNotFound, "no config hosted for node "+nodeID)
			return
		}
		if g == nil {
			g = map[string]any{}
		}
		for k, v := range n {
			g[k] = v
		}
		c.Data(http.StatusOK, "application/json", mustRaw(g))
	}
	r.GET("/remote-config", func(c *gin.Context) { serve(c, "global") })
	r.GET("/remote-config/:nodeId", func(c *gin.Context) { serve(c, c.Param("nodeId")) })
}

func statsNodes(c *gin.Context) []string {
	var out []string
	for _, id := range splitAndTrim(c.Query("nodes"), ",") {
		if len(out) < 200 && !contains(out, id) {
			out = append(out, truncateStr(id, 64))
		}
	}
	return out
}

func statsSummary(c *gin.Context) {
	tr := parseTimeRange(c, 24)
	since, until := tr.From.Unix()/60, tr.To.Unix()/60
	nodes := statsNodes(c)
	ctx, cancel := dbCtx()
	defer cancel()
	type apiAgg struct {
		APIType      string `json:"apiType"`
		Total        int64  `json:"total"`
		Errors       int64  `json:"errors"`
		LatencySumMs int64  `json:"latencySumMs"`
	}
	type nodeAgg struct {
		NodeID       string `json:"nodeId"`
		Label        string `json:"label"`
		Total        int64  `json:"total"`
		Errors       int64  `json:"errors"`
		LatencySumMs int64  `json:"latencySumMs"`
	}
	base := func() *gorm.DB {
		return db.WithContext(ctx).Model(&RequestStat{}).Where("minute >= ? AND minute <= ?", since, until)
	}
	filter := func(q *gorm.DB) *gorm.DB {
		if len(nodes) > 0 {
			return q.Where("node_id IN ?", nodes)
		}
		return q
	}
	byAPI, byNode, allNodes := []apiAgg{}, []nodeAgg{}, []nodeAgg{}
	filter(base()).Select("api_type, SUM(total) as total, SUM(errors) as errors, SUM(latency_sum_ms) as latency_sum_ms").Group("api_type").Find(&byAPI)
	filter(base()).Select("node_id, SUM(total) as total, SUM(errors) as errors, SUM(latency_sum_ms) as latency_sum_ms").Group("node_id").Find(&byNode)
	base().Select("node_id, SUM(total) as total, SUM(errors) as errors, SUM(latency_sum_ms) as latency_sum_ms").Group("node_id").Find(&allNodes)
	lm := poolLabelMap()
	for i := range byNode {
		byNode[i].Label = lm[byNode[i].NodeID]
	}
	for i := range allNodes {
		allNodes[i].Label = lm[allNodes[i].NodeID]
	}
	if nodes == nil {
		nodes = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"hours": tr.Hours, "from": tr.From, "to": tr.To, "selectedNodes": nodes,
		"allNodes": allNodes, "byApiType": byAPI, "byNode": byNode})
}

// probeListQuery /admin/probes 查询口径：user 只见自己任务的 sched + 自己发起的 biz
func probeListQuery(c *gin.Context, g *gorm.DB) (*gorm.DB, bool) {
	uid, role, _ := currentUserFromCtx(c)
	q := g.Model(&ProbeResult{})
	if role == RoleUser {
		var ids []uint
		g.Model(&ProbeTask{}).Where("owner_id = ?", uid).Pluck("id", &ids)
		if len(ids) > 0 {
			q = q.Where("(source = ? AND task_id IN ?) OR (source = ? AND owner_id = ?)", sourceSched, ids, sourceBiz, uid)
		} else {
			q = q.Where("source = ? AND owner_id = ?", sourceBiz, uid)
		}
	}
	if v := c.Query("node"); v != "" {
		q = q.Where("node_id = ?", v)
	}
	if v := c.Query("type"); v != "" {
		q = q.Where("api_type = ?", v)
	}
	if v := c.Query("target"); v != "" {
		q = q.Where("raw LIKE ?", "%"+v+"%")
	}
	switch c.Query("cat") {
	case "sched":
		q = q.Where("source = ?", sourceSched)
	case "biz":
		q = q.Where("source <> ?", sourceSched)
	}
	if v := c.Query("since"); v != "" {
		t, ok := parseTimeArg(v)
		if !ok {
			failBy(c, http.StatusBadRequest, "bad_request", "since 须为 RFC3339 时间")
			return nil, false
		}
		q = q.Where("created_at >= ?", t)
	}
	return q, true
}

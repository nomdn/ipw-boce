package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"gorm.io/gorm"
)

// userTaskLimit 普通用户任务数上限（user-task-limit / 环境变量 USER_TASK_LIMIT，缺省 20；0 = 不限）。
// 放权给普通用户的护栏：防止账号被用来刷任务打爆调度器和节点。
func userTaskLimit() int {
	v := strings.TrimSpace(os.Getenv("USER_TASK_LIMIT"))
	if v == "" {
		v = strings.TrimSpace(viper.GetString("user-task-limit"))
	}
	if v == "" {
		return 20
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 20
	}
	return n
}

// userMinInterval 普通用户任务最小间隔秒（user-min-interval / USER_MIN_INTERVAL，缺省 0 = 不额外限制，
// 仍受全局最小 10s 约束）。>0 时普通用户新建/修改任务的 interval 不得低于该值。
func userMinInterval() int {
	v := strings.TrimSpace(os.Getenv("USER_MIN_INTERVAL"))
	if v == "" {
		v = strings.TrimSpace(viper.GetString("user-min-interval"))
	}
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// nodeBriefItems 节点池简表（enabled 节点 + 在线/版本，脱敏无上游地址）。
// /admin/nodes/brief 与 /api/v1/nodes 共用（见 rest.go）。
func nodeBriefItems() ([]gin.H, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var defs []NodeDef
	if err := db.WithContext(ctx).Where("enabled = ?", true).Order("sort_order asc, id asc").Find(&defs).Error; err != nil {
		return nil, err
	}
	var snaps []Node
	if err := db.WithContext(ctx).Find(&snaps).Error; err != nil {
		return nil, err
	}
	online := make(map[string]*Node, len(snaps))
	for i := range snaps {
		online[snaps[i].NodeID] = &snaps[i]
	}
	out := make([]gin.H, 0, len(defs))
	for _, d := range defs {
		item := gin.H{
			"nodeId": d.NodeID, "label": d.Label, "ws": d.WS,
			"pools": d.Pool, "stack": d.Stack, "online": false, "version": "",
		}
		if s, ok := online[d.NodeID]; ok {
			item["online"] = s.Online
			item["version"] = s.Version
		}
		out = append(out, item)
	}
	return out, nil
}

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

// statsNodes 解析统计大盘的节点过滤参数 ?nodes=a,b,c（缺省或空 = 全部节点，nil 表示不过滤）。
// 节点 ID 由节点池定义（不含逗号），此处仍做去空、去重、长度与条数截断，
// 避免脏参数拼进 IN 子句（超长占位符 / 重复项拖慢查询）。
func statsNodes(c *gin.Context) []string {
	raw := strings.TrimSpace(c.Query("nodes"))
	if raw == "" {
		return nil
	}
	seen := make(map[string]struct{}, 8)
	out := make([]string, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if len(id) > 64 {
			id = id[:64]
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= 200 {
			break
		}
	}
	return out
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

	// 节点可用率报表（由 node_events 还原离线区间）与计划维护窗口（告警免打扰）
	registerUptimeRoutes(restricted)
	registerMaintenanceRoutes(restricted)
	// 节点上下线事件导出 CSV
	restricted.GET("/nodes/:nodeId/events/export", exportNodeEventsHandler)

	// 数据可用范围（数据最早时间 / 生效保留期上限）—— 时间范围选择器据此裁剪预设清单。
	// 挂在登录组而非 admin 组：普通用户的大盘页也要用，且内容只是元信息（不含业务数据）。
	registerTimeRangeRoutes(admin)

	// 上游节点池 CRUD：数据库托管，取代 setting.json 的 api-base-url / ip-location-api（见 node_defs.go）
	registerNodeDefRoutes(restricted)

	// 节点运行时配置读写（控制台 → 节点进程，WS 优先、HTTP 回退）
	registerNodeRuntimeConfigRoutes(restricted)

	// 节点 OTA 升级（控制台建任务下发，节点下载/替换/重启；任务追踪见 ota.go）
	registerOTARoutes(restricted)

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

	// 节点简表（登录即可，含 user）：任务表单"指定节点"勾选数据源。
	// 脱敏：只给池内节点的基本信息与在线/版本，不给远端地址/上游 URL（那是 admin 视角）。
	// 构建逻辑与 /api/v1/nodes 共用（见 rest.go nodeBriefItems）。
	admin.GET("/nodes/brief", func(c *gin.Context) {
		items, err := nodeBriefItems()
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, items)
	})

	// 一键拨测：对节点池全部（或 body.nodes 子集）同步批量下发拨测并聚合返回（见 admin_probe.go）
	admin.POST("/nodes/probe/:apiType/*raw", batchProbeHandler)

	// 拨测记录（probe_results）；cat=类别过滤：sched=定时拨测(source=sched)，biz=业务拨测(source!=sched)
	// user 只能看"自己任务"的定时拨测明细（source=sched 且 task_id 归自己）+ 自己发起的一键拨测
	// （source=biz 且 owner_id=自己，见 persistManualProbes）；他人的 sched/biz 一律不可见。
	// 查询条件由 probeListQuery 统一构造（见 export.go），与 /probes/export 共用一份口径。
	admin.GET("/probes", func(c *gin.Context) {
		limit := clampLimit(c.Query("limit"), 100)
		ctx, cancel := dbCtx()
		defer cancel()
		q, ok := probeListQuery(c, ctx)
		if !ok {
			return
		}
		var rows []ProbeResult
		if err := q.Order("id desc").Limit(limit).Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, rows)
	})

	// 拨测明细导出 CSV：参数与权限范围同 /admin/probes（见 export.go）
	admin.GET("/probes/export", exportProbesHandler)

	// 统计汇总：按 API 类型 / 节点维度聚合（request_stats）—— admin only（user 大盘走自己的任务报告）
	// 可选 ?nodes=a,b,c 只看部分节点（缺省 = 全部）；byApiType / byNode 均按选中节点过滤，
	// allNodes 恒为窗口内有统计的全部节点（不过滤），前端用它渲染节点选择器。
	restricted.GET("/stats/summary", func(c *gin.Context) {
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
			Total        int64  `json:"total"`
			Errors       int64  `json:"errors"`
			LatencySumMs int64  `json:"latencySumMs"`
		}
		byAPI := []apiAgg{}
		qAPI := db.WithContext(ctx).Model(&RequestStat{}).
			Select("api_type, SUM(total) as total, SUM(errors) as errors, SUM(latency_sum_ms) as latency_sum_ms").
			Where("minute >= ? AND minute <= ?", since, until)
		if len(nodes) > 0 {
			qAPI = qAPI.Where("node_id IN ?", nodes)
		}
		if err := qAPI.Group("api_type").Find(&byAPI).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}

		qNode := db.WithContext(ctx).Model(&RequestStat{}).
			Select("node_id, SUM(total) as total, SUM(errors) as errors, SUM(latency_sum_ms) as latency_sum_ms").
			Where("minute >= ? AND minute <= ?", since, until)
		if len(nodes) > 0 {
			qNode = qNode.Where("node_id IN ?", nodes)
		}
		byNode := []nodeAgg{}
		if err := qNode.Group("node_id").Find(&byNode).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}

		// allNodes：窗口内有统计的全部节点（不受过滤影响），供前端渲染节点选择器与计数
		allNodes := []nodeAgg{}
		if err := db.WithContext(ctx).Model(&RequestStat{}).
			Select("node_id, SUM(total) as total, SUM(errors) as errors, SUM(latency_sum_ms) as latency_sum_ms").
			Where("minute >= ? AND minute <= ?", since, until).Group("node_id").Find(&allNodes).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}

		selected := nodes
		if selected == nil {
			selected = []string{}
		}
		c.JSON(http.StatusOK, gin.H{
			"hours":         tr.Hours,
			"from":          tr.From,
			"to":            tr.To,
			"selectedNodes": selected,
			"allNodes":      allNodes,
			"byApiType":     byAPI,
			"byNode":        byNode,
		})
	})

	// 统计时间序列（按分钟桶聚合，桶粒度随窗口放大）—— admin only；可选 ?nodes=a,b,c 只看部分节点
	restricted.GET("/stats/timeseries", func(c *gin.Context) {
		tr := parseTimeRange(c, 24)
		since, until := tr.From.Unix()/60, tr.To.Unix()/60
		// request_stats 落库是分钟粒度：窗口放宽到 30/90 天后原样返回是几万个点，
		// 曲线画不动、JSON 也大。复用 stepForWindow（与 SLA 曲线同一套档位）做等宽分桶。
		// 桶表达式直接写进 SQL：step 只由 stepForWindow 产生（1/5/30/120/360），不含外部输入。
		// GROUP BY 必须用同一个表达式而不是别名 —— 别名会被当作表列引用，分桶静默失效。
		step := stepForWindow(tr.Hours)
		bucket := fmt.Sprintf("(minute / %d) * %d", step, step)
		nodes := statsNodes(c)
		ctx, cancel := dbCtx()
		defer cancel()
		var rows []struct {
			Minute int64 `json:"minute"`
			Total  int64 `json:"total"`
			Errors int64 `json:"errors"`
		}
		q := db.WithContext(ctx).Model(&RequestStat{}).
			Select(bucket + " as minute, SUM(total) as total, SUM(errors) as errors").
			Where("minute >= ? AND minute <= ?", since, until)
		if len(nodes) > 0 {
			q = q.Where("node_id IN ?", nodes)
		}
		if err := q.Group(bucket).Order("minute asc").Find(&rows).Error; err != nil {
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

	// 运行时配置改动合并进托管配置（持久化）—— admin only。
	// 背景：运行时 patch 只改节点内存（persist 也只写节点本地 setting.json），
	// 节点重启后 ENV > setting.json 会顶掉改动；托管远端配置优先级最高（远端 > ENV > 本地），
	// 把改动键值合并进该节点的托管配置即可让重启后依旧生效（节点 remote-config-url 指向本中心时）。
	// 恒写节点级配置（不允许 nodeId=global）：单节点的改动合并进全局会扩散到所有节点。
	restricted.POST("/node-configs/:nodeId/merge", func(c *gin.Context) {
		nodeID := c.Param("nodeId")
		if nodeID == "" || nodeID == "global" {
			apiError(c, http.StatusBadRequest, "global 不支持合并：这里持久化的是单节点覆盖值")
			return
		}
		var body struct {
			Config map[string]any `json:"config"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "请求体不是合法 JSON："+err.Error())
			return
		}
		if len(body.Config) == 0 {
			apiError(c, http.StatusBadRequest, "config 不能为空")
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		merged := map[string]any{}
		var row NodeConfig
		if err := db.WithContext(ctx).Where("node_id = ?", nodeID).First(&row).Error; err == nil {
			_ = json.Unmarshal([]byte(row.Config), &merged) // 存量非法 JSON 时从空对象开始
		}
		for k, v := range body.Config {
			merged[k] = v
		}
		normalized := mustJSON(merged)
		if err := db.WithContext(ctx).Where("node_id = ?", nodeID).
			Assign(map[string]any{"config": string(normalized), "updated_at": time.Now().UTC()}).
			FirstOrCreate(&NodeConfig{NodeID: nodeID, Config: string(normalized)}).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[node-configs] merged %d key(s) into hosted config of %s", len(body.Config), nodeID)
		c.JSON(http.StatusOK, gin.H{"nodeId": nodeID, "merged": len(body.Config), "sizeBytes": len(normalized)})
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
		// 标签过滤（C1）：?tag= 子串匹配
		if v := strings.TrimSpace(c.Query("tag")); v != "" {
			q = q.Where("tags LIKE ?", "%"+v+"%")
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
		uid, role, _ := currentUserFromCtx(c)
		// 普通用户配额护栏：任务数上限 + 最小间隔（user-task-limit / user-min-interval，0 = 不限）
		if role == RoleUser {
			if min := userMinInterval(); min > 0 && t.Interval < min {
				apiError(c, http.StatusForbidden, fmt.Sprintf("普通用户任务间隔不能低于 %d 秒", min))
				return
			}
			if limit := userTaskLimit(); limit > 0 {
				ctx, cancel := dbCtx()
				var n int64
				if err := db.WithContext(ctx).Model(&ProbeTask{}).Where("owner_id = ?", uid).Count(&n).Error; err != nil {
					cancel()
					apiError(c, http.StatusInternalServerError, err.Error())
					return
				}
				cancel()
				if n >= int64(limit) {
					apiError(c, http.StatusForbidden, fmt.Sprintf("任务数已达普通用户上限（%d 个），请清理不需要的任务或联系管理员", limit))
					return
				}
			}
		}
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
		// 普通用户改任务同样受最小间隔约束（数量配额只在新建时生效）
		if _, role, _ := currentUserFromCtx(c); role == RoleUser {
			if min := userMinInterval(); min > 0 && in.Interval < min {
				apiError(c, http.StatusForbidden, fmt.Sprintf("普通用户任务间隔不能低于 %d 秒", min))
				return
			}
		}
		in.CreatedAt = t.CreatedAt
		in.OwnerID = t.OwnerID  // 编辑不改所有者（保持创建者）
		in.Enabled = t.Enabled // 编辑不改运行状态：启停走专用 PATCH /tasks/:id/enabled。
		// （否则前端编辑表单不带 enabled 字段时，全量 Save 会把任务静默停用）
		in.ShareToken = t.ShareToken // 编辑不清空分享令牌（share 经 /tasks/:id/share 专门管理，否则全量 Save 会抹掉令牌）
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
	// 级联清退该任务的历史拨测数据（source=sched 样本全部删掉，含 body），
	// 避免任务删了、SLA 明细与明细页仍残留孤儿样本；先删数据后删任务，DB 失败不影响任务已删结果。
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
		taskID := idParam(c)
		// 清退该任务全部定时样本（task_id 归属，只有 source=sched 会打 task_id；手动 biz 为 0 不受影响）
		if err := db.WithContext(ctx).Where("task_id = ?", taskID).Delete(&ProbeResult{}).Error; err != nil {
			log.Printf("[task] DELETE task#%d samples cleanup error: %v", taskID, err)
		}
		res := db.WithContext(ctx).Delete(&ProbeTask{}, taskID)
		if res.Error != nil {
			apiError(c, http.StatusInternalServerError, res.Error.Error())
			return
		}
		// 清内存态：释放该任务重入锁、复位掉线告警计数/已触发标记，避免残留占用或误告警
		taskEndRun(taskID)
		resetTaskAlerts(taskID)
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
	// dns 任务：记录类型规整（缺省 a，非法值拒绝）；非 dns 任务清空避免残留
	if t.APIType == "dns" {
		rt := strings.ToLower(strings.TrimSpace(t.RecordType))
		if rt == "" {
			rt = "a"
		}
		validRT := false
		for _, k := range knownDNSRecordTypes() {
			if rt == k {
				validRT = true
				break
			}
		}
		if !validRT {
			return "recordType must be one of " + strings.Join(knownDNSRecordTypes(), ",")
		}
		t.RecordType = rt
	} else {
		t.RecordType = ""
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
	// 免打扰时段校验（B2）："HH:MM-HH:MM"，支持跨零点（如 23:00-07:00）；空 = 不静默
	if t.QuietHours != "" {
		qh := strings.TrimSpace(t.QuietHours)
		if !validQuietHours(qh) {
			return "quietHours 格式应为 HH:MM-HH:MM（如 23:00-07:00）"
		}
		t.QuietHours = qh
	}
	// 标签规整（C1）：去空白段、逗号分隔
	tagParts := make([]string, 0, 4)
	for _, s := range strings.Split(t.Tags, ",") {
		if s = strings.TrimSpace(s); s != "" {
			tagParts = append(tagParts, s)
		}
	}
	t.Tags = strings.Join(tagParts, ",")
	return ""
}

// validQuietHours 校验 "HH:MM-HH:MM" 格式（各段 00:00~23:59）
func validQuietHours(s string) bool {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return false
	}
	validHM := func(x string) bool {
		if len(x) != 5 || x[2] != ':' {
			return false
		}
		h, err1 := strconv.Atoi(x[:2])
		m, err2 := strconv.Atoi(x[3:])
		return err1 == nil && err2 == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59
	}
	return validHM(parts[0]) && validHM(parts[1])
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

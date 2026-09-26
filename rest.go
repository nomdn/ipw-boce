package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== REST 语法糖层（/api/v1，供个人 Token 等程序化访问） ====================
//
// 与内部 /admin/* 的关系：同一套可见性规则（user 只见自己的任务/明细，admin 全量），
// 但对外形态遵循 REST 规范：资源名词复数、裸资源 + HTTP 状态码（无信封）、
// 错误统一 {"error":{"code","message"}}。
//
// 端点（全部只读）：
//   GET /api/v1/tasks?page=&pageSize=        任务列表 {items,total}（全字段，id 倒序）
//   GET /api/v1/tasks/:id                    任务详情（非本人任务 403，不存在 404）
//   GET /api/v1/tasks/:id/sla?hours=24       顶层汇总 + byNode
//   GET /api/v1/probes?node=&type=&source=&since=&limit=&offset=   拨测明细（精简无 body）
//   GET /api/v1/nodes                        节点简表（enabled，脱敏）
//   GET /api/v1/usage?hours=24               我的用量
//
// 鉴权同 /admin（adminAuthMiddleware）：个人 API Token（ipt_）/ JWT / 静态 admin-token。
// 限流不挂（限流只在 /v1、/middleware 转发口）。错误码：unauthorized/forbidden/not_found/bad_request/internal。

// restError 语法糖层统一错误体：HTTP 状态码 + {"error":{"code","message"}}
func restError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func registerRestRoutes(router *gin.Engine) {
	g := router.Group("/api/v1", adminAuthMiddleware())
	g.GET("/tasks", restTasks)
	g.GET("/tasks/:id", restTask)
	g.GET("/tasks/:id/sla", restTaskSla)
	g.GET("/probes", restProbes)
	g.GET("/nodes", restNodes)
	g.GET("/usage", restUsage)
	g.POST("/probes", restProbeCreate)
}

// restPage 解析分页参数：?page（1 起）与 ?pageSize（别名 ?limit），上限 200
func restPage(c *gin.Context) (page, pageSize int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ = strconv.Atoi(c.DefaultQuery("limit", c.DefaultQuery("pageSize", "50")))
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return
}

// restTaskVisible 取任务并校验可见性：404 不存在 / 403 非本人（user）。通过后返回任务。
func restTaskVisible(c *gin.Context) (*ProbeTask, bool) {
	var t ProbeTask
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).First(&t, idParam(c)).Error; err != nil {
		restError(c, http.StatusNotFound, "not_found", "task not found")
		return nil, false
	}
	if taskOwnedByUserButNot(c, &t) {
		restError(c, http.StatusForbidden, "forbidden", "not your task")
		return nil, false
	}
	return &t, true
}

// restTasks GET /api/v1/tasks
func restTasks(c *gin.Context) {
	page, pageSize := restPage(c)
	uid, role, _ := currentUserFromCtx(c)
	ctx, cancel := dbCtx()
	defer cancel()
	q := db.WithContext(ctx).Model(&ProbeTask{})
	if role == RoleUser {
		q = q.Where("owner_id = ?", uid)
	}
	if v := strings.TrimSpace(c.Query("tag")); v != "" {
		q = q.Where("tags LIKE ?", "%"+v+"%") // 标签子串过滤（标签以逗号分隔存储）
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		restError(c, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	var rows []ProbeTask
	if err := q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		restError(c, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": tasksWithOwner(rows), "total": total, "page": page, "pageSize": pageSize})
}

// restTask GET /api/v1/tasks/:id
func restTask(c *gin.Context) {
	t, ok := restTaskVisible(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, taskWithOwner(t))
}

// restTaskSla GET /api/v1/tasks/:id/sla?hours= | ?start=&end=
// 结构（已与需求确认）：顶层汇总 + byNode，不含 special/judge 等内部渲染字段。
// 窗口：?hours= 相对（缺省 24），或 ?start=/&end= 绝对区间（见 timerange.go）。
func restTaskSla(c *gin.Context) {
	t, ok := restTaskVisible(c)
	if !ok {
		return
	}
	tr := parseTimeRange(c, 24)
	resp, err := computeTaskSlaRange(t.ID, tr.From, tr.To)
	if err != nil {
		restError(c, http.StatusNotFound, "not_found", err.Error())
		return
	}
	c.JSON(http.StatusOK, slaSugar(t.ID, tr.Hours, resp))
}

// slaSugar 把内部 slaTaskResp 重排成语法糖层确认的结构（顶层汇总 + byNode 节点明细）。
// restTaskSla 与公开状态页 JSON（public_status.go）共用。
func slaSugar(taskID uint, hours float64, resp *slaTaskResp) gin.H {
	var up, down, wsum, wlat, maxMs, p95Ms int64
	byNode := make([]gin.H, 0, len(resp.ByNode))
	for _, n := range resp.ByNode {
		up += n.Up
		down += n.Down
		if n.Up > 0 { // 节点平均延迟按其 up 样本数加权出整体均值
			wsum += n.Up
			wlat += n.AvgMs * n.Up
		}
		if n.MaxMs > maxMs {
			maxMs = n.MaxMs
		}
		if n.P95Ms > p95Ms {
			p95Ms = n.P95Ms
		}
		byNode = append(byNode, gin.H{
			"nodeId":       n.NodeID,
			"up":           n.Up,
			"down":         n.Down,
			"availability": n.Availability,
			"avgMs":        n.AvgMs,
			"p95Ms":        n.P95Ms,
			"latestMs":     n.LatestMs,
		})
	}
	availability := 0.0
	if up+down > 0 {
		availability = float64(up) / float64(up+down) * 100
		availability = float64(int(availability*100)) / 100
	}
	avgMs := int64(0)
	if wsum > 0 {
		avgMs = wlat / wsum
	}
	return gin.H{
		"taskId":       taskID,
		"hours":        hours,
		"window":       resp.Window,
		"samples":      resp.Samples,
		"up":           up,
		"down":         down,
		"availability": availability,
		"avgMs":        avgMs,
		"maxMs":        maxMs,
		"p95Ms":        p95Ms,
		"byNode":       byNode,
	}
}

// restProbes GET /api/v1/probes?node=&type=&source=&since=&limit=&offset=
// 结构（已确认）：{items,total}，items 精简无 body。可见性同内部接口。
func restProbes(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit < 1 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}
	source := strings.ToLower(strings.TrimSpace(c.Query("source")))
	if source != "" && source != "sched" && source != "biz" {
		restError(c, http.StatusBadRequest, "bad_request", "source 只能是 sched / biz")
		return
	}
	var since time.Time
	if v := c.Query("since"); v != "" {
		var perr error
		since, perr = time.Parse(time.RFC3339, v)
		if perr != nil {
			restError(c, http.StatusBadRequest, "bad_request", "since 须为 RFC3339 时间")
			return
		}
	}

	uid, role, _ := currentUserFromCtx(c)
	ctx, cancel := dbCtx()
	defer cancel()
	q := db.WithContext(ctx).Model(&ProbeResult{})
	if role == RoleUser {
		ownIDs, err := ownTaskIDs(db.WithContext(ctx), uid)
		if err != nil {
			restError(c, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		if len(ownIDs) > 0 {
			q = q.Where("(source = ? AND task_id IN ?) OR (source = ? AND owner_id = ?)", sourceSched, ownIDs, sourceBiz, uid)
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
	if !since.IsZero() {
		q = q.Where("created_at >= ?", since.UTC())
	}
	switch source {
	case "sched":
		q = q.Where("source = ?", sourceSched)
	case "biz":
		if role != RoleUser {
			q = q.Where("source <> ?", sourceSched)
		} else {
			q = q.Where("source = ? AND owner_id = ?", sourceBiz, uid)
		}
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		restError(c, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	var rows []ProbeResult
	if err := q.Order("id desc").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		restError(c, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		items = append(items, gin.H{
			"nodeId":    r.NodeID,
			"apiType":   r.APIType,
			"raw":       r.Raw,
			"status":    r.Status,
			"latencyMs": r.LatencyMs,
			"source":    r.Source,
			"error":     r.Error,
			"createdAt": r.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}

// restProbeCreate POST /api/v1/probes —— 一键拨测（同步聚合；结构已与需求确认：全量含 body）。
// 拨测类结果落库 source=biz 并归属 Token 主人（与内部一键拨测同一归属口径）。
// nodes 缺省 = 全池，此时跳过当前确证离线的节点（被跳过的在响应 skipped 里返回）；
// 显式给出 nodes 则点名照拨、不跳过（与内部一键拨测同一口径，见 offlineNodeSet）。
func restProbeCreate(c *gin.Context) {
	uid, _, _ := currentUserFromCtx(c)
	var body struct {
		APIType string            `json:"apiType"`
		Raw     string            `json:"raw"`
		Query   map[string]string `json:"query"`
		Nodes   []string          `json:"nodes"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		restError(c, http.StatusBadRequest, "bad_request", "invalid body: "+err.Error())
		return
	}
	body.APIType = strings.TrimSpace(body.APIType)
	body.Raw = strings.TrimPrefix(strings.TrimSpace(body.Raw), "/")
	if body.APIType == "" || body.Raw == "" {
		restError(c, http.StatusBadRequest, "bad_request", "apiType 与 raw 必填")
		return
	}
	query := url.Values{}
	for k, v := range body.Query {
		if v != "" {
			query.Set(k, v)
		}
	}
	results, unknown, skipped, err := batchProbeCore(body.APIType, body.Raw, body.Nodes, query)
	if err != nil {
		restError(c, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	persisted := persistManualProbes(body.APIType, body.Raw, results, uid)
	okCnt, failedCnt := 0, 0
	for _, r := range results {
		if r.Status >= 200 && r.Status < 300 {
			okCnt++
		} else {
			failedCnt++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"apiType":   body.APIType,
		"raw":       body.Raw,
		"targeted":  len(results),
		"ok":        okCnt,
		"failed":    failedCnt,
		"unknown":   unknown,
		"skipped":   skipped,
		"persisted": persisted,
		"results":   results,
	})
}

// restNodes GET /api/v1/nodes — 节点池简表（enabled，脱敏），复用内部 brief 构建
func restNodes(c *gin.Context) {
	items, err := nodeBriefItems()
	if err != nil {
		restError(c, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// restUsage GET /api/v1/usage?hours= — 用量（admin 全站口径；普通用户 = 自己任务 + 自己 biz）
func restUsage(c *gin.Context) {
	uid, role, _ := currentUserFromCtx(c)
	hours := clampFloat(c.Query("hours"), 24, 1, 24*90)
	usage, err := usageForUser(role, uid, hours)
	if err != nil {
		restError(c, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	c.JSON(http.StatusOK, usage)
}

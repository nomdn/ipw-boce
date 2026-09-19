package main

// ==================== 公开状态页数据（B3）====================
//
// 分享模型：令牌即"分享组"——多个任务可绑定同一令牌（多选分享），令牌由
// POST /admin/tasks/share 批量管理（支持自定义令牌，6 位 hex 随机为缺省）。
// 免登录读取：
//   GET /api/public/status/:token?hours=24 — 按令牌：{hours, tasks:[...]}（前端 /s/:token 页面用）
// 暴露面刻意收窄：任务名/类型/目标（可按任务 hideTarget 隐藏）+ 窗口聚合 + 延迟时序，
// 不含 owner/上游地址/内部字段。token 未知 / 已关闭分享 → 404。
//
// 防枚举：随机令牌仅 24-bit 熵（短链接取舍），本端点挂 per-IP 限流（60 次/分）。

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// pubShareLimiter 公开状态 JSON 的防枚举限流（与转发限流相互独立）
var pubShareLimiter = newRateLimiter(60)

func registerPublicStatusRoutes(router *gin.Engine) {
	router.GET("/api/public/status/:token", publicStatusJSON)
}

// taskByShareToken 按分享令牌取任务（未开启分享/令牌无效 → false）
func taskByShareToken(token string) (*ProbeTask, bool) {
	if token == "" {
		return nil, false
	}
	var t ProbeTask
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).Where("share_token = ?", token).First(&t).Error; err != nil {
		return nil, false
	}
	return &t, true
}

// publicStatusJSON GET /api/public/status/:token —— 按令牌返回分享组的全部任务
func publicStatusJSON(c *gin.Context) {
	if !pubShareLimiter.allow(c.ClientIP()) {
		restError(c, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后再试")
		return
	}
	t, ok := taskByShareToken(c.Param("token"))
	if !ok {
		restError(c, http.StatusNotFound, "not_found", "invalid share link")
		return
	}
	hours := clampFloat(c.Query("hours"), 24, 1, 24*90)

	var group []ProbeTask
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).Where("share_token = ?", t.ShareToken).
		Order("id asc").Find(&group).Error; err != nil {
		restError(c, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	tasks := make([]gin.H, 0, len(group))
	for i := range group {
		e, err := sugarTaskEntry(&group[i], hours)
		if err != nil {
			continue // 单任务聚合失败跳过，不拖垮整页
		}
		tasks = append(tasks, e)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"token": t.ShareToken, "hours": hours, "tasks": tasks})
}

// sugarTaskEntry 单任务的公开条目：任务元信息 + 窗口聚合 + 延迟时序。
// HideTarget=true 时不含 target。
func sugarTaskEntry(t *ProbeTask, hours float64) (gin.H, error) {
	resp, err := computeTaskSla(t.ID, time.Duration(hours*float64(time.Hour)))
	if err != nil {
		return nil, err
	}
	e := slaSugar(t.ID, hours, resp)
	e["name"] = t.Name
	e["apiType"] = t.APIType
	if !t.HideTarget {
		e["target"] = t.Target
	}
	// 延迟时序（每轮多节点平均延迟），状态页画折线
	ctx, cancel := dbCtx()
	defer cancel()
	var rows []ProbeResult
	if err := db.WithContext(ctx).
		Where("task_id = ? AND source = ? AND created_at >= ? AND created_at <= ?",
			t.ID, sourceSched, resp.Window.From, resp.Window.To).
		Order("created_at asc, id asc").Find(&rows).Error; err == nil && len(rows) > 0 {
		e["series"] = rowsToRoundSeries(t, rows)

		// 按节点曲线：每个节点一条（多节点对比）。按首轮出现顺序保序，逐节点复用同款分桶聚合
		nodeSeen := map[string]bool{}
		order := make([]string, 0, 8)
		for _, r := range rows {
			if !nodeSeen[r.NodeID] {
				nodeSeen[r.NodeID] = true
				order = append(order, r.NodeID)
			}
		}
		nodeSeries := make([]gin.H, 0, len(order))
		for _, nid := range order {
			nrows := make([]ProbeResult, 0, len(rows)/len(order)+1)
			for _, r := range rows {
				if r.NodeID == nid {
					nrows = append(nrows, r)
				}
			}
			// 曲线图例显示节点名：池 label 优先，UUID 兜底
			name := nid
			if st := findNode(nodePoolForType(t.APIType), nid); st != nil && st.Label != "" {
				name = st.Label
			}
			nodeSeries = append(nodeSeries, gin.H{"nodeId": nid, "label": name, "series": rowsToRoundSeries(t, nrows)})
		}
		if len(nodeSeries) > 1 {
			e["nodeSeries"] = nodeSeries // 单节点任务不给（与自身曲线重复）
		}
	}
	return e, nil
}

package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// ==================== REST 语法糖层 /api/v1（错误体 {"error":{code,message}}） ====================

func registerRestRoutes(r *gin.Engine) {
	g := r.Group("/api/v1", adminAuthMiddleware())
	g.GET("/tasks", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		size, _ := strconv.Atoi(c.DefaultQuery("limit", c.DefaultQuery("pageSize", "50")))
		if page < 1 {
			page = 1
		}
		if size < 1 || size > 200 {
			size = 50
		}
		uid, role, _ := currentUserFromCtx(c)
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&ProbeTask{})
		if role == RoleUser {
			q = q.Where("owner_id = ?", uid)
		}
		if v := strings.TrimSpace(c.Query("tag")); v != "" {
			q = q.Where("tags LIKE ?", "%"+v+"%")
		}
		var total int64
		q.Count(&total)
		var rows []ProbeTask
		if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
			restError(c, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": tasksWithOwner(rows), "total": total, "page": page, "pageSize": size})
	})
	g.GET("/tasks/:id", func(c *gin.Context) {
		if t, ok := loadVisibleTask(c); ok {
			c.JSON(http.StatusOK, tasksWithOwner([]ProbeTask{*t})[0])
		}
	})
	g.GET("/tasks/:id/sla", func(c *gin.Context) {
		t, ok := loadVisibleTask(c)
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
	})
	g.GET("/probes", func(c *gin.Context) {
		limit := clampLimit(c.Query("limit"), 50)
		if limit > 500 {
			limit = 500
		}
		offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
		if offset < 0 {
			offset = 0
		}
		src := c.Query("source")
		if src != "" && src != "sched" && src != "biz" {
			restError(c, http.StatusBadRequest, "bad_request", "source 只能是 sched / biz")
			return
		}
		c.Request.URL.RawQuery = strings.ReplaceAll(c.Request.URL.RawQuery, "source=", "cat=")
		ctx, cancel := dbCtx()
		defer cancel()
		q, ok := probeListQuery(c, db.WithContext(ctx))
		if !ok {
			return
		}
		var total int64
		q.Count(&total)
		var rows []ProbeResult
		q.Order("id desc").Offset(offset).Limit(limit).Find(&rows)
		items := make([]gin.H, 0, len(rows))
		for _, r := range rows {
			items = append(items, gin.H{"nodeId": r.NodeID, "apiType": r.APIType, "raw": r.Raw, "status": r.Status,
				"latencyMs": r.LatencyMs, "source": r.Source, "error": r.Error, "createdAt": r.CreatedAt})
		}
		c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
	})
	g.POST("/probes", func(c *gin.Context) {
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
		body.Raw = strings.TrimPrefix(strings.TrimSpace(body.Raw), "/")
		if strings.TrimSpace(body.APIType) == "" || body.Raw == "" {
			restError(c, http.StatusBadRequest, "bad_request", "apiType 与 raw 必填")
			return
		}
		uid, _, _ := currentUserFromCtx(c)
		cost, msg := chargeProbeCost(uid)
		if msg != "" {
			restError(c, http.StatusPaymentRequired, "insufficient_points", msg)
			return
		}
		q := url.Values{}
		for k, v := range body.Query {
			q.Set(k, v)
		}
		results, unknown, skipped, err := batchProbeCore(body.APIType, body.Raw, body.Nodes, q)
		if err != nil {
			refundProbeCost(uid, cost)
			restError(c, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		c.JSON(http.StatusOK, batchSummary(body.APIType, body.Raw, results, unknown, skipped,
			persistManualProbes(body.APIType, body.Raw, results, uid)))
	})
	g.GET("/nodes", func(c *gin.Context) {
		items := nodeBriefItems()
		c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
	})
	g.GET("/usage", func(c *gin.Context) {
		uid, role, _ := currentUserFromCtx(c)
		u, err := usageForUser(role, uid, clampFloat(c.Query("hours"), 24, 1, 24*90))
		if err != nil {
			restError(c, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		c.JSON(http.StatusOK, u)
	})
	registerPointsRestRoutes(g)
}

// usageForUser admin 全站口径；user = 自己任务 sched + 自己发起的 biz
func usageForUser(role string, uid uint, hours float64) (gin.H, error) {
	since := time.Now().UTC().Add(-time.Duration(hours * float64(time.Hour)))
	ctx, cancel := dbCtx()
	defer cancel()
	var tasks []ProbeTask
	tq := db.WithContext(ctx).Model(&ProbeTask{})
	if role != RoleAdmin {
		tq = tq.Where("owner_id = ?", uid)
	}
	if err := tq.Find(&tasks).Error; err != nil {
		return nil, err
	}
	cfg := map[uint]*ProbeTask{}
	var ids []uint
	for i := range tasks {
		cfg[tasks[i].ID] = &tasks[i]
		ids = append(ids, tasks[i].ID)
	}
	q := db.WithContext(ctx).Model(&ProbeResult{}).Where("created_at >= ?", since)
	switch {
	case role == RoleAdmin:
		q = q.Where("source IN ?", []string{sourceSched, sourceBiz})
	case len(ids) > 0:
		q = q.Where("(source = ? AND task_id IN ?) OR (source = ? AND owner_id = ?)", sourceSched, ids, sourceBiz, uid)
	default:
		q = q.Where("source = ? AND owner_id = ?", sourceBiz, uid)
	}
	var rows []ProbeResult
	if err := q.Select("task_id, api_type, status, body, latency_ms").Find(&rows).Error; err != nil {
		return nil, err
	}
	type agg struct{ total, up, down, invalid, latSum, latCnt int64 }
	byType := map[string]*agg{}
	tot := &agg{}
	for i := range rows {
		r := &rows[i]
		t := cfg[r.TaskID]
		if t == nil {
			t = &ProbeTask{APIType: r.APIType}
		}
		a := byType[r.APIType]
		if a == nil {
			a = &agg{}
			byType[r.APIType] = a
		}
		ev := evalSample(t, r.Status, r.Body, r.LatencyMs)
		for _, x := range []*agg{a, tot} {
			x.total++
			switch {
			case ev.invalid:
				x.invalid++
			case ev.up:
				x.up++
			default:
				x.down++
			}
			if r.LatencyMs > 0 {
				x.latSum += r.LatencyMs
				x.latCnt++
			}
		}
	}
	arr := make([]gin.H, 0, len(byType))
	for k, a := range byType {
		avg := int64(0)
		if a.latCnt > 0 {
			avg = a.latSum / a.latCnt
		}
		arr = append(arr, gin.H{"apiType": k, "total": a.total, "up": a.up, "down": a.down, "invalid": a.invalid, "avgMs": avg})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i]["total"].(int64) > arr[j]["total"].(int64) })
	return gin.H{"hours": hours, "total": tot.total, "up": tot.up, "down": tot.down, "invalid": tot.invalid, "byType": arr}, nil
}

// ==================== 公开状态页 JSON：GET /api/public/status/:token（每 IP 60 次/分） ====================

var pubShareLimiter = newRateLimiter(60)

func registerPublicStatusRoutes(r *gin.Engine) {
	r.GET("/api/public/status/:token", func(c *gin.Context) {
		if !pubShareLimiter.allow(c.ClientIP()) {
			restError(c, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后再试")
			return
		}
		token := c.Param("token")
		ctx, cancel := dbCtx()
		defer cancel()
		var group []ProbeTask
		if token == "" || db.WithContext(ctx).Where("share_token = ?", token).Order("id asc").Find(&group).Error != nil || len(group) == 0 {
			restError(c, http.StatusNotFound, "not_found", "invalid share link")
			return
		}
		hours := clampFloat(c.Query("hours"), 24, 1, 24*90)
		tasks := make([]gin.H, 0, len(group))
		for i := range group {
			t := &group[i]
			resp, err := computeTaskSla(t.ID, time.Duration(hours*float64(time.Hour)))
			if err != nil {
				continue
			}
			e := slaSugar(t.ID, hours, resp)
			e["name"], e["apiType"] = t.Name, t.APIType
			if !t.HideTarget {
				e["target"] = t.Target
			}
			var rows []ProbeResult
			db.WithContext(ctx).Where("task_id = ? AND source = ? AND created_at >= ?", t.ID, sourceSched, resp.Window.From).
				Order("created_at asc").Find(&rows)
			e["series"] = rowsToRoundSeries(t, rows)
			tasks = append(tasks, e)
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"token": token, "hours": hours, "tasks": tasks})
	})
}

// ==================== 浏览器推送 /console/sla（?token= 兼容原生 WS 无法带头；?hours=N 缺省 24） ====================

type consoleSub struct {
	conn  *websocket.Conn
	hours int
	user  ctxUser
	wmu   sync.Mutex
}

type consoleHub struct {
	mu   sync.Mutex
	subs map[*consoleSub]struct{}
}

var hub = &consoleHub{subs: map[*consoleSub]struct{}{}}

func (h *consoleHub) pushSLA(taskID uint) {
	h.mu.Lock()
	subs := make([]*consoleSub, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	if len(subs) == 0 {
		return
	}
	var t ProbeTask
	ctx, cancel := dbCtx()
	err := db.WithContext(ctx).First(&t, taskID).Error
	cancel()
	if err != nil {
		return
	}
	for _, s := range subs {
		if s.user.Role == RoleUser && t.OwnerID != s.user.ID {
			continue
		}
		go func(s *consoleSub) {
			resp, err := computeTaskSla(taskID, time.Duration(s.hours)*time.Hour)
			if err != nil {
				return
			}
			msg := wsMessage{Type: "sla", TS: time.Now().Unix(), Data: mustRaw(gin.H{"taskId": taskID, "sla": resp})}
			s.wmu.Lock()
			defer s.wmu.Unlock()
			wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer wcancel()
			if s.conn.Write(wctx, websocket.MessageText, mustRaw(msg)) != nil {
				h.mu.Lock()
				delete(h.subs, s)
				h.mu.Unlock()
			}
		}(s)
	}
}

func registerConsoleSlaRoute(r *gin.Engine) {
	r.GET("/console/sla", func(c *gin.Context) {
		tok := bearerToken(c)
		if tok == "" {
			tok = c.Query("token")
		}
		u, ok := identityOfAuthToken(tok)
		if !ok {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		hours := 24
		if n, err := strconv.Atoi(c.Query("hours")); err == nil && n >= 1 && n <= 24*90 {
			hours = n
		}
		conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
		if err != nil {
			log.Printf("[ws-sla] accept failed: %v", err)
			return
		}
		sub := &consoleSub{conn: conn, hours: hours, user: u}
		hub.mu.Lock()
		hub.subs[sub] = struct{}{}
		hub.mu.Unlock()
		defer func() {
			hub.mu.Lock()
			delete(hub.subs, sub)
			hub.mu.Unlock()
			conn.Close(websocket.StatusNormalClosure, "bye")
		}()
		for {
			if _, _, err := conn.Read(c.Request.Context()); err != nil {
				return
			}
		}
	})
}

package main

import (
	"math"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 节点可用率（由 node_events 还原离线区间，与原版同口径） ====================
//
//   - 起点状态取"窗口前最后一条事件"；首次 online 之前不计（节点还不存在）
//   - 窗口不超出数据保留期（事件会被清，超出部分截断）
//   - 节点首见时间之后才开始计时
// 捐赠节点的每日在线奖励也用这里的在线秒数（见 donate.go）。

type uptimeInterval struct{ start, end time.Time }

type uptimeDaily struct {
	Date           string  `json:"date"`
	Seconds        int64   `json:"seconds"`
	OfflineSeconds int64   `json:"offlineSeconds"`
	DownCount      int     `json:"downCount"`
	Availability   float64 `json:"availability"`
	start, end     time.Time
}

type nodeUptimeStats struct {
	NodeID         string        `json:"nodeId"`
	Label          string        `json:"label"`
	CurrentOnline  bool          `json:"currentOnline"`
	FirstSeenAt    time.Time     `json:"firstSeenAt,omitempty"`
	From           time.Time     `json:"from"`
	To             time.Time     `json:"to"`
	Seconds        int64         `json:"seconds"`
	OnlineSeconds  int64         `json:"onlineSeconds"`
	OfflineSeconds int64         `json:"offlineSeconds"`
	DownCount      int           `json:"downCount"`
	Availability   float64       `json:"availability"`
	Daily          []uptimeDaily `json:"daily,omitempty"`
}

func clampUptimeRange(days float64) (time.Time, time.Time) {
	to := time.Now().UTC()
	from := to.Add(-time.Duration(days * 24 * float64(time.Hour)))
	if DATA_RETENTION_DAYS > 0 {
		if cut := to.AddDate(0, 0, -DATA_RETENTION_DAYS); cut.After(from) {
			from = cut
		}
	}
	return from, to
}

// loadUptimeEvents prior = 每节点窗口前最后一条事件；in = 窗口内事件（升序）。nodeIDs 为空 = 全部
func loadUptimeEvents(from, to time.Time, nodeIDs []string) (map[string]NodeEvent, map[string][]NodeEvent, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	sub := db.WithContext(ctx).Model(&NodeEvent{}).Select("MAX(id)").Where("created_at < ?", from).Group("node_id")
	if len(nodeIDs) > 0 {
		sub = sub.Where("node_id IN ?", nodeIDs)
	}
	var priors []NodeEvent
	if err := db.WithContext(ctx).Where("id IN (?)", sub).Find(&priors).Error; err != nil {
		return nil, nil, err
	}
	prior := map[string]NodeEvent{}
	for _, e := range priors {
		prior[e.NodeID] = e
	}
	q := db.WithContext(ctx).Where("created_at >= ? AND created_at <= ?", from, to)
	if len(nodeIDs) > 0 {
		q = q.Where("node_id IN ?", nodeIDs)
	}
	var events []NodeEvent
	if err := q.Order("created_at asc, id asc").Find(&events).Error; err != nil {
		return nil, nil, err
	}
	in := map[string][]NodeEvent{}
	for _, e := range events {
		in[e.NodeID] = append(in[e.NodeID], e)
	}
	return prior, in, nil
}

// uptimeForNode 离线区间（含"从未出现"的 none 状态：首个 online 前不计）。
// startedState 返回窗口起点是否已存在（prior 有事件）
func uptimeForNode(nodeID string, from, to time.Time, prior map[string]NodeEvent, in map[string][]NodeEvent) ([]uptimeInterval, time.Time) {
	var out []uptimeInterval
	state := "none"
	var offStart time.Time
	existsFrom := time.Time{} // 节点开始存在的时刻
	if p, ok := prior[nodeID]; ok {
		existsFrom = from
		state = p.Event
		if state == "offline" {
			offStart = from
		}
	}
	for _, e := range in[nodeID] {
		et := e.CreatedAt.UTC()
		switch state {
		case "none":
			if e.Event == "online" {
				state, existsFrom = "online", et
			}
		case "online":
			if e.Event == "offline" {
				state, offStart = "offline", et
			}
		case "offline":
			if e.Event == "online" {
				out = append(out, uptimeInterval{offStart, et})
				state = "online"
			}
		}
	}
	if state == "offline" && to.After(offStart) {
		out = append(out, uptimeInterval{offStart, to})
	}
	return out, existsFrom
}

func overlap(iv uptimeInterval, s, e time.Time) int64 {
	a, b := iv.start, iv.end
	if a.Before(s) {
		a = s
	}
	if b.After(e) {
		b = e
	}
	if d := b.Sub(a); d > 0 {
		return int64(d.Seconds())
	}
	return 0
}

func ratio(off, total int64) float64 {
	if total <= 0 {
		return 1
	}
	return math.Max(0, math.Round((1-float64(off)/float64(total))*10000)/10000)
}

func buildUptimeStats(n *Node, from, to time.Time, iv []uptimeInterval, existsFrom time.Time) nodeUptimeStats {
	if !n.FirstSeenAt.IsZero() && n.FirstSeenAt.After(from) {
		from = n.FirstSeenAt.UTC()
	}
	if !existsFrom.IsZero() && existsFrom.After(from) {
		from = existsFrom
	}
	st := nodeUptimeStats{NodeID: n.NodeID, Label: nodeDisplayName(n.NodeID, n.Label), CurrentOnline: n.Online,
		FirstSeenAt: n.FirstSeenAt, From: from, To: to}
	if existsFrom.IsZero() && !n.Online {
		// 窗口内从未在线、也没有更早事件：不计时长
		st.From, st.Availability = to, 1
		return st
	}
	st.Seconds = int64(to.Sub(from).Seconds())
	if st.Seconds < 0 {
		st.Seconds = 0
	}
	for _, x := range iv {
		st.OfflineSeconds += overlap(x, from, to)
	}
	if st.OfflineSeconds > st.Seconds {
		st.OfflineSeconds = st.Seconds
	}
	st.OnlineSeconds = st.Seconds - st.OfflineSeconds
	st.DownCount = len(iv)
	st.Availability = ratio(st.OfflineSeconds, st.Seconds)
	return st
}

func dayBuckets(from, to time.Time) []uptimeDaily {
	var out []uptimeDaily
	f, t := from.Local(), to.Local()
	for day := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, time.Local); day.Before(t); day = day.AddDate(0, 0, 1) {
		s, e := day, day.AddDate(0, 0, 1)
		if s.Before(f) {
			s = f
		}
		if e.After(t) {
			e = t
		}
		if e.After(s) {
			out = append(out, uptimeDaily{Date: day.Format("2006-01-02"), Seconds: int64(e.Sub(s).Seconds()), start: s, end: e})
		}
	}
	return out
}

func fillUptimeDaily(b []uptimeDaily, iv []uptimeInterval) []uptimeDaily {
	for i := range b {
		for _, x := range iv {
			b[i].OfflineSeconds += overlap(x, b[i].start, b[i].end)
			if !x.start.Before(b[i].start) && x.start.Before(b[i].end) {
				b[i].DownCount++
			}
		}
		if b[i].OfflineSeconds > b[i].Seconds {
			b[i].OfflineSeconds = b[i].Seconds
		}
		b[i].Availability = ratio(b[i].OfflineSeconds, b[i].Seconds)
	}
	return b
}

// nodeOnlineSeconds [from,to) 内某节点在线秒数（捐赠奖励结算用）
func nodeOnlineSeconds(nodeID string, from, to time.Time) int64 {
	prior, in, err := loadUptimeEvents(from, to, []string{nodeID})
	if err != nil {
		return 0
	}
	n, ok := nodeSnapshot(nodeID)
	if !ok {
		return 0
	}
	iv, exists := uptimeForNode(nodeID, from, to, prior, in)
	return buildUptimeStats(&n, from, to, iv, exists).OnlineSeconds
}

func registerUptimeRoutes(g *gin.RouterGroup) {
	g.GET("/nodes/uptime", func(c *gin.Context) {
		from, to := clampUptimeRange(clampFloat(c.Query("days"), 7, 1, 90))
		ctx, cancel := dbCtx()
		defer cancel()
		var nodes []Node
		db.WithContext(ctx).Order("node_id asc").Find(&nodes)
		prior, in, err := loadUptimeEvents(from, to, nil)
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]nodeUptimeStats, 0, len(nodes))
		for i := range nodes {
			iv, ex := uptimeForNode(nodes[i].NodeID, from, to, prior, in)
			out = append(out, buildUptimeStats(&nodes[i], from, to, iv, ex))
		}
		c.JSON(http.StatusOK, gin.H{"from": from, "to": to, "nodes": out})
	})
	g.GET("/nodes/:nodeId/uptime", func(c *gin.Context) {
		id := c.Param("nodeId")
		from, to := clampUptimeRange(clampFloat(c.Query("days"), 7, 1, 90))
		n, ok := nodeSnapshot(id)
		if !ok {
			apiError(c, http.StatusNotFound, "node not found")
			return
		}
		prior, in, err := loadUptimeEvents(from, to, []string{id})
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		iv, ex := uptimeForNode(id, from, to, prior, in)
		st := buildUptimeStats(&n, from, to, iv, ex)
		st.Daily = fillUptimeDaily(dayBuckets(st.From, to), iv)
		c.JSON(http.StatusOK, st)
	})
}

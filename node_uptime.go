package main

import (
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 节点可用率（uptime） ====================
//
// 数据源只有 node_events（online / offline 事件流，见 store.go recordNodeOnline / recordNodeOffline）。
// 不需要新表：把事件流还原成"离线区间"，再用区间与统计窗口求交即可得到可用率。
//
// 口径与容易踩的坑：
//   - 窗口起点状态：不能只看窗口内的事件。必须先取"窗口开始前最后一条事件"决定起点是 online 还是 offline，
//     否则一个从窗口开始前就离线、窗口内才恢复的节点会被算成全程在线。
//   - 从未在线过的区间不算宕机：节点首次注册（FirstSeenAt）之前不存在，那段时间既不算在线也不算离线，
//     所以起点状态用 "none"（未知）表示，只有遇到第一条 online 事件才开始计入。
//   - 事件保留期：node_events 会被 data-retention-days 清掉，统计窗口不能超出保留期，否则老事件缺失
//     会把宕机算成在线（虚高）。超出部分在窗口起点处截断，并在返回值里带上实际统计起点。
//   - 事件保留期内的"沉默但当前在线"的节点（事件被清过 / 老部署没有事件）按全程在线处理，
//     否则会把"没有证据"当成"一直在宕机"，可用率会莫名其妙掉到 0。
//
// 掉线判定本身带 20s 宽限（store.go nodeDownGraceDelay）——那是**告警**去抖，不影响这里：
// 事件流写的是断连的真实时刻，可用率统计的是事实。

// uptimeInterval 一段离线区间（左闭右开）
type uptimeInterval struct {
	start time.Time
	end   time.Time
}

// uptimeDaily 逐日可用率（本地时区按天切分，供趋势图使用）
type uptimeDaily struct {
	Date           string  `json:"date"` // 2026-09-19（服务器本地时区）
	Seconds        int64   `json:"seconds"`
	OfflineSeconds int64   `json:"offlineSeconds"`
	DownCount      int     `json:"downCount"`
	Availability   float64 `json:"availability"` // 0~1

	// start/end 是桶边界（本地时区，左闭右开），只在 fillUptimeDaily 内部用来裁剪离线区间，
	// 不参与 JSON 输出（故不导出）。
	start, end time.Time
}

// nodeUptimeStats 单节点在统计窗口内的可用率
type nodeUptimeStats struct {
	NodeID         string        `json:"nodeId"`
	Label          string        `json:"label"`
	CurrentOnline  bool          `json:"currentOnline"`
	FirstSeenAt    time.Time     `json:"firstSeenAt,omitempty"`
	From           time.Time     `json:"from"` // 实际统计起点（已按首见时间 / 保留期截断）
	To             time.Time     `json:"to"`
	Seconds        int64         `json:"seconds"` // 实际统计秒数；0 = 窗口内该节点还不存在
	OnlineSeconds  int64         `json:"onlineSeconds"`
	OfflineSeconds int64         `json:"offlineSeconds"`
	DownCount      int           `json:"downCount"`    // 窗口内离线区间数（跨窗口起点的算 1 次）
	Availability   float64       `json:"availability"` // 0~1
	Daily          []uptimeDaily `json:"daily,omitempty"`
}

// clampUptimeRange 把统计窗口收敛到"有数据可查"的范围：
// 上限是 now，下限不早于数据保留期（node_events 会被清），也不早于距今 maxDays 天。
func clampUptimeRange(days float64) (from, to time.Time) {
	to = time.Now().UTC()
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	from = to.Add(-time.Duration(days * 24 * float64(time.Hour)))
	if DATA_RETENTION_DAYS > 0 {
		if cutoff := to.AddDate(0, 0, -DATA_RETENTION_DAYS); cutoff.After(from) {
			from = cutoff
		}
	}
	return from, to
}

// loadUptimeEvents 一次性取出统计所需的全部事件：
//   - prior：每个节点在窗口起点之前**最后一条**事件（决定起点状态）
//   - in：窗口内的事件，按时间升序
//
// 用两条查询覆盖全部节点，避免逐节点查库（节点数 × 2 次查询）。
func loadUptimeEvents(from, to time.Time) (map[string]NodeEvent, map[string][]NodeEvent, error) {
	ctx, cancel := dbCtx()
	defer cancel()

	prior := map[string]NodeEvent{}
	// id = MAX(id) 取每节点窗口前最后一条：sqlite / mysql / postgres 都支持
	var priors []NodeEvent
	if err := db.WithContext(ctx).
		Where("id IN (?)", db.WithContext(ctx).Model(&NodeEvent{}).
			Select("MAX(id)").Where("created_at < ?", from).Group("node_id")).
		Find(&priors).Error; err != nil {
		return nil, nil, err
	}
	for _, e := range priors {
		prior[e.NodeID] = e
	}

	var events []NodeEvent
	if err := db.WithContext(ctx).
		Where("created_at >= ? AND created_at <= ?", from, to).
		Order("created_at asc, id asc").Find(&events).Error; err != nil {
		return nil, nil, err
	}
	in := make(map[string][]NodeEvent, len(prior))
	for _, e := range events {
		in[e.NodeID] = append(in[e.NodeID], e)
	}
	return prior, in, nil
}

// uptimeForNode 由事件流还原某节点的离线区间（窗口 [from,to]）。
func uptimeForNode(nodeID string, from, to time.Time, prior map[string]NodeEvent, in map[string][]NodeEvent) []uptimeInterval {
	var out []uptimeInterval
	state := "none" // none | online | offline
	var offlineStart time.Time

	if p, ok := prior[nodeID]; ok {
		switch p.Event {
		case "online":
			state = "online"
		case "offline":
			state = "offline"
			offlineStart = from
		}
	}
	for _, e := range in[nodeID] {
		et := e.CreatedAt.UTC()
		if et.Before(from) || et.After(to) {
			continue
		}
		switch state {
		case "none":
			// 首次出现：只有 online 才代表节点开始存在（注册即写 online 事件）
			if e.Event == "online" {
				state = "online"
			}
		case "online":
			if e.Event == "offline" {
				state = "offline"
				offlineStart = et
			}
		case "offline":
			if e.Event == "online" && !et.Before(offlineStart) {
				out = append(out, uptimeInterval{start: offlineStart, end: et})
				state = "online"
			}
		}
	}
	// 窗口结束时仍处于离线：区间延续到窗口末尾
	if state == "offline" && to.After(offlineStart) {
		out = append(out, uptimeInterval{start: offlineStart, end: to})
	}
	return out
}

// dayBuckets 把 [from,to] 按本地时区的自然日切分成桶（首尾桶按窗口边界裁剪）
func dayBuckets(from, to time.Time) []uptimeDaily {
	out := []uptimeDaily{}
	f, t := from.Local(), to.Local()
	day := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, f.Location())
	for day.Before(t) {
		s, e := day, day.AddDate(0, 0, 1)
		if s.Before(f) {
			s = f
		}
		if e.After(t) {
			e = t
		}
		if e.After(s) {
			out = append(out, uptimeDaily{
				Date:    day.Format("2006-01-02"),
				Seconds: int64(e.Sub(s).Seconds()),
				start:   s,
				end:     e,
			})
		}
		day = day.AddDate(0, 0, 1)
	}
	return out
}

// fillUptimeDaily 把离线区间摊到各日桶上
func fillUptimeDaily(buckets []uptimeDaily, intervals []uptimeInterval) []uptimeDaily {
	for i := range buckets {
		b := &buckets[i]
		var off int64
		for _, iv := range intervals {
			s, e := iv.start.Local(), iv.end.Local()
			if s.Before(b.start) {
				s = b.start
			}
			if e.After(b.end) {
				e = b.end
			}
			if d := e.Sub(s); d > 0 {
				off += int64(d.Seconds())
			}
			// 宕机次数：按"离线区间起点落在本桶内"计数，跨天区间只算在其开始那天
			if !iv.start.Local().Before(b.start) && iv.start.Local().Before(b.end) {
				b.DownCount++
			}
		}
		if off > b.Seconds {
			off = b.Seconds
		}
		b.OfflineSeconds = off
		b.Availability = ratio(off, b.Seconds)
	}
	return buckets
}

// ratio 1 - offline/total，保留 4 位小数；总时长为 0 时按 1（无数据即无宕机）
func ratio(offline, total int64) float64 {
	if total <= 0 {
		return 1
	}
	v := 1 - float64(offline)/float64(total)
	if v < 0 {
		v = 0
	}
	return math.Round(v*10000) / 10000
}

// buildUptimeStats 组装单节点统计（不含 daily）
func buildUptimeStats(n *Node, from, to time.Time, intervals []uptimeInterval) nodeUptimeStats {
	// 统计窗口不早于节点首见时间：之前它还不存在
	if !n.FirstSeenAt.IsZero() && n.FirstSeenAt.After(from) {
		from = n.FirstSeenAt.UTC()
	}
	st := nodeUptimeStats{
		NodeID:        n.NodeID,
		Label:         nodeDisplayName(n.NodeID, n.Label),
		CurrentOnline: n.Online,
		FirstSeenAt:   n.FirstSeenAt,
		From:          from,
		To:            to,
	}
	st.Seconds = int64(to.Sub(from).Seconds())
	if st.Seconds < 0 {
		st.Seconds = 0
	}
	if st.Seconds == 0 {
		st.Availability = 1
		return st
	}
	for _, iv := range intervals {
		s, e := iv.start, iv.end
		if s.Before(from) {
			s = from
		}
		if e.After(to) {
			e = to
		}
		if d := e.Sub(s); d > 0 {
			st.OfflineSeconds += int64(d.Seconds())
		}
	}
	if st.OfflineSeconds > st.Seconds {
		st.OfflineSeconds = st.Seconds
	}
	st.OnlineSeconds = st.Seconds - st.OfflineSeconds
	st.DownCount = len(intervals)
	st.Availability = ratio(st.OfflineSeconds, st.Seconds)
	return st
}

// nodeDisplayName 告警 / 报表里的节点名：池内 label → nodes.label → nodeId（与 monitorNode.display 同口径）
func nodeDisplayName(nodeID, dbLabel string) string {
	if s := poolLabelFor(nodeID); s != "" {
		return s
	}
	if s := strings.TrimSpace(dbLabel); s != "" {
		return s
	}
	return nodeID
}

// registerUptimeRoutes 节点可用率接口（admin only）
//
//	GET /admin/nodes/uptime?days=7        全部节点（不含 daily，供列表展示）
//	GET /admin/nodes/:nodeId/uptime?days=7 单节点（含 daily，供趋势图）
func registerUptimeRoutes(g *gin.RouterGroup) {
	g.GET("/nodes/uptime", func(c *gin.Context) {
		from, to := clampUptimeRange(clampFloat(c.Query("days"), 7, 1, 90))
		ctx, cancel := dbCtx()
		defer cancel()
		var nodes []Node
		if err := db.WithContext(ctx).Order("node_id asc").Find(&nodes).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		prior, in, err := loadUptimeEvents(from, to)
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]nodeUptimeStats, 0, len(nodes))
		for i := range nodes {
			n := &nodes[i]
			iv := uptimeForNode(n.NodeID, from, to, prior, in)
			out = append(out, buildUptimeStats(n, from, to, iv))
		}
		c.JSON(http.StatusOK, gin.H{"days": c.Query("days"), "from": from, "to": to, "nodes": out})
	})

	g.GET("/nodes/:nodeId/uptime", func(c *gin.Context) {
		nodeID := c.Param("nodeId")
		from, to := clampUptimeRange(clampFloat(c.Query("days"), 7, 1, 90))
		n, ok := nodeSnapshot(nodeID)
		if !ok {
			apiError(c, http.StatusNotFound, "node not found")
			return
		}
		prior, in, err := loadUptimeEvents(from, to)
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		iv := uptimeForNode(nodeID, from, to, prior, in)
		st := buildUptimeStats(&n, from, to, iv)
		st.Daily = fillUptimeDaily(dayBuckets(st.From, to), iv)
		log.Printf("[uptime] %s: %.2f%% over %ds (down %dx/%ds)", nodeID, st.Availability*100, st.Seconds, st.DownCount, st.OfflineSeconds)
		c.JSON(http.StatusOK, st)
	})
}

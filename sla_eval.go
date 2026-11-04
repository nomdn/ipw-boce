package main

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== SLA 判定与聚合（只统计 source=sched，口径与原版一致） ====================
//
// up/down 不信任链路 HTTP 状态，而是按任务当前判定配置实时解析存储的 body：
//   detail/ssl：{ipv4:{...}, ipv6:{...}} 双栈；dns：record[]；speed：is_reachable；tcping：ipv4.success
// 改判定配置即可重算历史。

var errTaskNotFound = errors.New("task not found")

type sampleEval struct {
	up, invalid, slow bool
}

func statusIsExpected(code int, expect string) bool {
	if expect == "" || expect == "2xx" {
		return code >= 200 && code < 300
	}
	for _, s := range splitAndTrim(expect, ",") {
		if n, err := strconv.Atoi(s); err == nil && n == code {
			return true
		}
	}
	return false
}

func numOf(o map[string]any, key string) (int, bool) {
	switch n := o[key].(type) {
	case float64:
		return int(n), true
	case string:
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return int(f), true
		}
	}
	return 0, false
}

func evalSample(t *ProbeTask, linkStatus int, body string, latMs int64) sampleEval {
	ev := sampleEval{slow: t.SlowMs > 0 && latMs > int64(t.SlowMs)}
	switch t.APIType {
	case "detail", "ssl":
		ev.up, ev.invalid = evalDualStack(t, body)
	case "dns":
		ev.up, ev.invalid = evalDNS(body, linkStatus)
	default:
		ev.up, ev.invalid = evalFlatBody(t, body, linkStatus)
	}
	return ev
}

func evalFlatBody(t *ProbeTask, body string, linkStatus int) (bool, bool) {
	var root map[string]any
	if body != "" && json.Unmarshal([]byte(body), &root) == nil {
		if reachable, ok := root["is_reachable"].(bool); ok {
			if !reachable {
				return false, false
			}
			if code, ok := numOf(root, "http_status_code"); ok && code > 0 {
				return statusIsExpected(code, t.ExpectStatus), false
			}
			return true, false
		}
		if ipv4, ok := root["ipv4"].(map[string]any); ok {
			succ, ok := numOf(ipv4, "success")
			return ok && succ > 0, false
		}
	}
	if linkStatus == 0 {
		return false, true
	}
	return statusIsExpected(linkStatus, t.ExpectStatus), false
}

func evalDNS(body string, linkStatus int) (bool, bool) {
	var root map[string]any
	if body != "" && json.Unmarshal([]byte(body), &root) == nil {
		if records, ok := root["record"].([]any); ok {
			return len(records) > 0, false
		}
		if _, ok := root["error"].(string); ok {
			return false, false
		}
	}
	if linkStatus == 0 {
		return false, true
	}
	return statusIsExpected(linkStatus, "2xx"), false
}

func evalDualStack(t *ProbeTask, body string) (bool, bool) {
	var root map[string]any
	if body == "" || json.Unmarshal([]byte(body), &root) != nil {
		return false, true
	}
	var ups []bool
	for _, k := range []string{"ipv4", "ipv6"} {
		if o, ok := root[k].(map[string]any); ok {
			ups = append(ups, evalStackUp(t, o))
		}
	}
	if len(ups) == 0 {
		return false, true
	}
	if t.RequireAllStacks {
		for _, u := range ups {
			if !u {
				return false, false
			}
		}
		return true, false
	}
	for _, u := range ups {
		if u {
			return true, false
		}
	}
	return false, false
}

func evalStackUp(t *ProbeTask, o map[string]any) bool {
	if t.CertExpiredDown {
		if v, ok := o["is_expired"].(bool); ok && v {
			return false
		}
	}
	var codes []int
	if v, ok := numOf(o, "https_status_code"); ok {
		codes = append(codes, v)
	}
	if t.APIType == "detail" {
		if v, ok := numOf(o, "http_status_code"); ok {
			codes = append(codes, v)
		}
	}
	if len(codes) == 0 {
		return false
	}
	if t.BothProtocols {
		for _, c := range codes {
			if !statusIsExpected(c, t.ExpectStatus) {
				return false
			}
		}
		return true
	}
	for _, c := range codes {
		if statusIsExpected(c, t.ExpectStatus) {
			return true
		}
	}
	return false
}

// trueLatencyMs 节点实测延迟：detail/ssl 栈 total_time、tcping 栈 avg_rtt、speed total_time、dns duration
func trueLatencyMs(t *ProbeTask, body string) (int64, bool) {
	var root map[string]any
	if body == "" || json.Unmarshal([]byte(body), &root) != nil {
		return 0, false
	}
	switch t.APIType {
	case "detail", "ssl", "tcping":
		field := "total_time"
		if t.APIType == "tcping" {
			field = "avg_rtt"
		}
		var sum, n int
		for _, k := range []string{"ipv4", "ipv6"} {
			if o, ok := root[k].(map[string]any); ok {
				if v, ok := numOf(o, field); ok && v > 0 {
					sum += v
					n++
				}
			}
		}
		if n == 0 {
			return 0, false
		}
		return int64(math.Round(float64(sum) / float64(n))), true
	case "speed":
		if v, ok := numOf(root, "total_time"); ok && v > 0 {
			return int64(v), true
		}
	case "dns":
		if v, ok := numOf(root, "duration"); ok && v > 0 {
			return int64(v), true
		}
	}
	return 0, false
}

func extractSpecial(t *ProbeTask, body string) map[string]any {
	var root map[string]any
	if json.Unmarshal([]byte(body), &root) != nil {
		return nil
	}
	out := map[string]any{}
	switch t.APIType {
	case "dns":
		if v, ok := root["domain"]; ok {
			out["domain"] = v
		}
		if rec, ok := root["record"].([]any); ok {
			out["record_count"] = len(rec)
			if len(rec) > 0 {
				out["first_record"] = rec[0]
			}
		}
		for _, k := range []string{"ttl", "duration"} {
			if v, ok := root[k]; ok {
				out[k] = v
			}
		}
	case "ssl", "detail":
		o, ok := root["ipv4"].(map[string]any)
		if !ok {
			if o, ok = root["ipv6"].(map[string]any); !ok {
				return nil
			}
		}
		keys := []string{"host_record", "download_speed", "page_size", "dns_lookup_time", "tcp_connect_time",
			"http_connect_time", "first_byte_time", "total_time", "http_status_code", "https_status_code"}
		if t.APIType == "ssl" {
			keys = []string{"domain", "subject_common_name", "issuer_common_name", "http_version",
				"cert_start_time", "cert_end_time", "cert_validity_days"}
			if v, ok := o["issuer_organization"].([]any); ok && len(v) > 0 {
				out["issuer_organization"] = v[0]
			}
		}
		for _, k := range keys {
			if v, ok := o[k]; ok {
				out[k] = v
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type slaWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type slaNodeAgg struct {
	NodeID       string         `json:"nodeId"`
	Label        string         `json:"label"`
	Samples      int64          `json:"samples"`
	Up           int64          `json:"up"`
	Down         int64          `json:"down"`
	Invalid      int64          `json:"invalid"`
	Availability float64        `json:"availability"`
	AvgMs        int64          `json:"avgMs"`
	MaxMs        int64          `json:"maxMs"`
	P95Ms        int64          `json:"p95Ms"`
	Slow         int64          `json:"slow"`
	MetRate      float64        `json:"metRate"`
	LatestAt     time.Time      `json:"latestAt"`
	LatestUp     bool           `json:"latestUp"`
	LatestMs     int64          `json:"latestMs"`
	Special      map[string]any `json:"special,omitempty"`
	NodeOnline   *bool          `json:"nodeOnline,omitempty"`
}

type slaTaskResp struct {
	Task    ProbeTask    `json:"task"`
	Window  slaWindow    `json:"window"`
	Samples int64        `json:"samples"`
	ByNode  []slaNodeAgg `json:"byNode"`
	Types   []string     `json:"types"`
}

func computeTaskSlaRange(taskID uint, from, to time.Time) (*slaTaskResp, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var t ProbeTask
	if err := db.WithContext(ctx).First(&t, taskID).Error; err != nil {
		return nil, errTaskNotFound
	}
	var rows []ProbeResult
	if err := db.WithContext(ctx).Where("task_id = ? AND source = ? AND created_at >= ? AND created_at <= ?",
		taskID, sourceSched, from, to).Order("node_id asc, id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	online := onlineNodeSet()
	labels := poolLabelMap()
	agg := map[string]*slaNodeAgg{}
	lat := map[string][]int64{}
	var order []string
	var total int64
	for _, r := range rows {
		a := agg[r.NodeID]
		if a == nil {
			on := online[r.NodeID]
			a = &slaNodeAgg{NodeID: r.NodeID, Label: labels[r.NodeID], NodeOnline: &on}
			agg[r.NodeID] = a
			order = append(order, r.NodeID)
		}
		ev := evalSample(&t, r.Status, r.Body, r.LatencyMs)
		a.Samples++
		total++
		if ev.invalid {
			a.Invalid++
			continue
		}
		if ev.up {
			a.Up++
			lat[r.NodeID] = append(lat[r.NodeID], r.LatencyMs)
		} else {
			a.Down++
		}
		if ev.slow {
			a.Slow++
		}
		if !r.CreatedAt.Before(a.LatestAt) {
			a.LatestAt, a.LatestUp, a.LatestMs = r.CreatedAt, ev.up, r.LatencyMs
			a.Special = extractSpecial(&t, r.Body)
		}
	}
	sort.Strings(order)
	byNode := make([]slaNodeAgg, 0, len(order))
	for _, id := range order {
		a := agg[id]
		if a.Samples > 0 {
			a.Availability = math.Round(float64(a.Up)/float64(a.Samples)*10000) / 100
			a.MetRate = math.Round(float64(a.Up-a.Slow)/float64(a.Samples)*10000) / 100
		}
		if l := lat[id]; len(l) > 0 {
			sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
			var sum int64
			for _, v := range l {
				sum += v
			}
			a.AvgMs = int64(math.Round(float64(sum) / float64(len(l))))
			a.MaxMs = l[len(l)-1]
			a.P95Ms = l[(len(l)-1)*95/100]
		}
		byNode = append(byNode, *a)
	}
	return &slaTaskResp{Task: t, Window: slaWindow{from, to}, Samples: total, ByNode: byNode, Types: knownProbeTaskTypes()}, nil
}

func computeTaskSla(taskID uint, dur time.Duration) (*slaTaskResp, error) {
	to := time.Now().UTC()
	return computeTaskSlaRange(taskID, to.Add(-dur), to)
}

// slaSugar 顶层汇总 + byNode（/api/v1 与公开状态页共用）
func slaSugar(taskID uint, hours float64, resp *slaTaskResp) gin.H {
	var up, down, wsum, wlat, maxMs, p95 int64
	byNode := make([]gin.H, 0, len(resp.ByNode))
	for _, n := range resp.ByNode {
		up += n.Up
		down += n.Down
		if n.Up > 0 {
			wsum += n.Up
			wlat += n.AvgMs * n.Up
		}
		if n.MaxMs > maxMs {
			maxMs = n.MaxMs
		}
		if n.P95Ms > p95 {
			p95 = n.P95Ms
		}
		byNode = append(byNode, gin.H{"nodeId": n.NodeID, "label": n.Label, "up": n.Up, "down": n.Down,
			"availability": n.Availability, "avgMs": n.AvgMs, "p95Ms": n.P95Ms, "latestMs": n.LatestMs})
	}
	avail, avg := 0.0, int64(0)
	if up+down > 0 {
		avail = math.Floor(float64(up)/float64(up+down)*10000) / 100
	}
	if wsum > 0 {
		avg = wlat / wsum
	}
	return gin.H{"taskId": taskID, "hours": hours, "window": resp.Window, "samples": resp.Samples, "up": up,
		"down": down, "availability": avail, "avgMs": avg, "maxMs": maxMs, "p95Ms": p95, "byNode": byNode}
}

// ==================== 曲线 ====================

func stepForWindow(hours float64) int {
	switch {
	case hours <= 1:
		return 1
	case hours <= 6:
		return 5
	case hours <= 24:
		return 30
	case hours <= 72:
		return 120
	}
	return 360
}

type roundAgg struct {
	at                                  time.Time
	samples, valid, up, down, sumMs, nLat int64
}

func (g *roundAgg) add(t *ProbeTask, r *ProbeResult) {
	ev := evalSample(t, r.Status, r.Body, r.LatencyMs)
	g.samples++
	if !ev.invalid {
		g.valid++
	}
	if ev.up {
		g.up++
		g.sumMs += r.LatencyMs
		g.nLat++
	} else if !ev.invalid {
		g.down++
	}
}

func (g *roundAgg) json(rounds int) gin.H {
	avail, avg := 0.0, 0.0
	if g.valid > 0 {
		avail = math.Round(float64(g.up)/float64(g.valid)*10000) / 100
	}
	if g.nLat > 0 {
		avg = math.Round(float64(g.sumMs)/float64(g.nLat)*10) / 10
	}
	h := gin.H{"time": g.at.UTC().Format(time.RFC3339), "minute": g.at.Unix() / 60, "samples": g.samples,
		"up": g.up, "down": g.down, "availability": avail, "avgMs": avg}
	if rounds > 1 {
		h["rounds"] = rounds
	}
	return h
}

const maxRoundPoints = 1500

// rowsToRoundSeries 单任务按采样轮次（同 CreatedAt）出点；超过 1500 点相邻轮合并
func rowsToRoundSeries(t *ProbeTask, rows []ProbeResult) []gin.H {
	agg := map[int64]*roundAgg{}
	var keys []int64
	for i := range rows {
		k := rows[i].CreatedAt.Round(time.Second).Unix()
		g := agg[k]
		if g == nil {
			g = &roundAgg{at: time.Unix(k, 0)}
			agg[k] = g
			keys = append(keys, k)
		}
		g.add(t, &rows[i])
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	merge := 1
	if len(keys) > maxRoundPoints {
		merge = (len(keys) + maxRoundPoints - 1) / maxRoundPoints
	}
	out := make([]gin.H, 0, len(keys)/merge+1)
	for i := 0; i < len(keys); i += merge {
		end := i + merge
		if end > len(keys) {
			end = len(keys)
		}
		acc := &roundAgg{at: agg[keys[i]].at}
		for _, k := range keys[i:end] {
			x := agg[k]
			acc.samples += x.samples
			acc.valid += x.valid
			acc.up += x.up
			acc.down += x.down
			acc.sumMs += x.sumMs
			acc.nLat += x.nLat
		}
		out = append(out, acc.json(end-i))
	}
	return out
}

// rowsToSeries 跨任务按时间桶聚合（我的任务大盘）
func rowsToSeries(taskByID map[uint]*ProbeTask, rows []ProbeResult, stepMin int) []gin.H {
	step := time.Duration(stepMin) * time.Minute
	agg := map[int64]*roundAgg{}
	var keys []int64
	for i := range rows {
		t := taskByID[rows[i].TaskID]
		if t == nil {
			continue
		}
		bk := rows[i].CreatedAt.Truncate(step)
		k := bk.Unix()
		g := agg[k]
		if g == nil {
			g = &roundAgg{at: bk}
			agg[k] = g
			keys = append(keys, k)
		}
		g.add(t, &rows[i])
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]gin.H, 0, len(keys))
	for _, k := range keys {
		out = append(out, agg[k].json(1))
	}
	return out
}

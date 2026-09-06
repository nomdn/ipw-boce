package main

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== SLA 聚合（只统计定时拨测 source=sched） ====================
//
// SLA 看板/接口只认 source=sched 的样本（手动一键拨测不落库、节点自主上报走 ws|http，均不计入）。
// up/down 判定**不信任链路 HTTP 状态**（detail/ssl 的链路恒 200，真实可达性在返回 body 的双栈对象里），
// 而是按任务当前的判定配置（expectStatus / bothProtocols / requireAllStacks / certExpiredDown）
// 实时解析存储的 body 原文——改判定配置即可重算历史，无需重跑拨测。
//
// detail/ssl 节点 body 形如：
//   {"ipv4":{...可达性/状态码/证书...}, "ipv6":{...}}  （detail 样例由用户提供，见需求记录）

// registerTaskSlaRoutes 挂到 /admin 组下（在 sla.go 实现，admin.go 中调用）
func registerTaskSlaRoutes(g *gin.RouterGroup) {
	// 某任务在窗口内的 SLA：按节点聚合（含最新样本特殊字段快照）
	g.GET("/tasks/:id/sla", func(c *gin.Context) {
		hours := clampFloat(c.Query("hours"), 24, 1, 24*90)
		slaForTask(c, idParam(c), time.Duration(hours*float64(time.Hour)))
	})

	// 某任务在窗口内的时序曲线（SLA 卡片的延迟曲线 + 失败时间段红标）
	// 归属同 SLA：user 仅可查自己创建的任务；admin/静态 token 不限。
	g.GET("/tasks/:id/series", func(c *gin.Context) {
		hours := clampFloat(c.Query("hours"), 24, 1, 24*90)
		taskID := idParam(c)
		ctx, cancel := dbCtx()
		defer cancel()
		var t ProbeTask
		if err := db.WithContext(ctx).First(&t, taskID).Error; err != nil {
			apiError(c, http.StatusNotFound, errTaskNotFound.Error())
			return
		}
		if taskOwnedByUserButNot(c, &t) {
			apiError(c, http.StatusForbidden, "not your task")
			return
		}
		to := time.Now().UTC()
		from := to.Add(-time.Duration(hours * float64(time.Hour)))
		var rows []ProbeResult
		if err := db.WithContext(ctx).
			Where("task_id = ? AND source = ? AND created_at >= ? AND created_at <= ?",
				taskID, sourceSched, from, to).
			Order("created_at asc, id asc").Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		stepMin := stepForWindow(hours)
		// 延迟曲线按采样轮次打点（每轮多节点取平均、不拆线不跨轮聚合），见 rowsToRoundSeries
		series := rowsToRoundSeries(&t, rows)
		c.JSON(http.StatusOK, gin.H{"taskId": taskID, "stepMinutes": stepMin, "mode": "round", "series": series})
	})
}

// slaWindow 窗口起点/终点
type slaWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// slaNodeAgg 单节点聚合
type slaNodeAgg struct {
	NodeID string `json:"nodeId"`
	Label  string `json:"label"`
	// 样本与可用性
	Samples      int64   `json:"samples"`
	Up           int64   `json:"up"`
	Down         int64   `json:"down"`
	Invalid      int64   `json:"invalid"`      // body 无法解析/判定不明，不参与成功率
	Availability float64 `json:"availability"` // up/samples，0~1（样本为 0 → 0）
	// 延迟
	AvgMs   int64   `json:"avgMs"`
	MaxMs   int64   `json:"maxMs"`
	P95Ms   int64   `json:"p95Ms"`
	Slow    int64   `json:"slow"`    // 慢次数（LatencyMs>slowMs）
	MetRate float64 `json:"metRate"` // 达标率=(up且不慢)/samples
	// 最新样本快照
	LatestAt   time.Time      `json:"latestAt"`
	LatestUp   bool           `json:"latestUp"`
	LatestMs   int64          `json:"latestMs"`
	Special    map[string]any `json:"special,omitempty"` // 特殊字段（SSL 证书 / Detail 速度等）
	NodeOnline *bool          `json:"nodeOnline,omitempty"`
}

// slaTaskResp 任务级 SLA 响应
type slaTaskResp struct {
	Task    ProbeTask    `json:"task"`
	Window  slaWindow    `json:"window"`
	Samples int64        `json:"samples"`
	ByNode  []slaNodeAgg `json:"byNode"`
	Types   []string     `json:"types"`
}

// sampleEval 单条样本判定结果
type sampleEval struct {
	up      bool
	invalid bool
	slow    bool
	latMs   int64
}

// errTaskNotFound 任务不存在
var errTaskNotFound = errors.New("task not found")

// slaForTask gin handler：查询某任务 source=sched 样本并按节点聚合
// user 仅可查看自己创建的任务；admin/静态 token 不限。
func slaForTask(c *gin.Context, taskID uint, dur time.Duration) {
	ctx, cancel := dbCtx()
	defer cancel()
	var t ProbeTask
	if err := db.WithContext(ctx).First(&t, taskID).Error; err != nil {
		apiError(c, http.StatusNotFound, errTaskNotFound.Error())
		return
	}
	if taskOwnedByUserButNot(c, &t) {
		apiError(c, http.StatusForbidden, "not your task")
		return
	}
	resp, err := computeTaskSla(taskID, dur)
	if err != nil {
		if err == errTaskNotFound {
			apiError(c, http.StatusNotFound, err.Error())
			return
		}
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, resp)
}

// computeTaskSla 核心聚合（HTTP handler 与 WS 实时推送共用）：
// 查询某任务 source=sched 窗口样本，按节点聚合返回完整 SLA 快照。errTaskNotFound 表示任务不存在。
func computeTaskSla(taskID uint, dur time.Duration) (*slaTaskResp, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var t ProbeTask
	if err := db.WithContext(ctx).First(&t, taskID).Error; err != nil {
		return nil, errTaskNotFound
	}
	to := time.Now().UTC()
	from := to.Add(-dur)

	var rows []ProbeResult
	if err := db.WithContext(ctx).
		Where("task_id = ? AND source = ? AND created_at >= ? AND created_at <= ?", taskID, sourceSched, from, to).
		Order("node_id asc, id asc").Find(&rows).Error; err != nil {
		return nil, err
	}

	// 节点在线快照（给卡片一个在线提示，不参与 SLA 计算）
	online := map[string]bool{}
	if onlineRows, err := func() ([]Node, error) {
		var ns []Node
		e := db.WithContext(ctx).Where("online = ?", true).Find(&ns).Error
		return ns, e
	}(); err == nil {
		for _, n := range onlineRows {
			online[n.NodeID] = true
		}
	}

	agg := map[string]*slaNodeAgg{}
	var order []string
	total := int64(0)
	latBuckets := map[string][]int64{}
	for _, r := range rows {
		a := agg[r.NodeID]
		if a == nil {
			a = &slaNodeAgg{NodeID: r.NodeID}
			if st := findNode(nodePoolForType(t.APIType), r.NodeID); st != nil {
				a.Label = st.Label
			}
			if on, ok := online[r.NodeID]; ok {
				a.NodeOnline = &on
			}
			agg[r.NodeID] = a
			order = append(order, r.NodeID)
		}
		ev := evalSample(&t, r.Status, r.Body, r.LatencyMs)
		a.Samples++
		total++
		if ev.invalid {
			a.Invalid++
			continue // invalid 不进 up/down，也不进延迟统计
		}
		if ev.up {
			a.Up++
			// 延迟统计只认可达(up)样本：不可达(down)的延迟无意义（DNS 失败/连不上落库为 0），
			// 若计入平均/p95 会把假数值混进"到目标延迟"，因此排除。
			latBuckets[r.NodeID] = append(latBuckets[r.NodeID], r.LatencyMs)
		} else {
			a.Down++
		}
		if ev.slow {
			a.Slow++
		}
		if r.CreatedAt.After(a.LatestAt) {
			a.LatestAt = r.CreatedAt
			a.LatestUp = ev.up
			a.LatestMs = r.LatencyMs
			if !ev.invalid {
				a.Special = extractSpecial(&t, r.Body)
			}
		}
	}
	// 排序节点 id
	sort.Strings(order)
	byNode := make([]slaNodeAgg, 0, len(order))
	for _, id := range order {
		a := agg[id]
		if a.Samples > 0 {
			a.Availability = math.Round(float64(a.Up)/float64(a.Samples)*10000) / 100
			a.MetRate = math.Round(float64(a.Up-a.Slow)/float64(a.Samples)*10000) / 100
		}
		lat := latBuckets[id]
		if len(lat) > 0 {
			var sum int64
			var max int64
			for _, ms := range lat {
				sum += ms
				if ms > max {
					max = ms
				}
			}
			a.AvgMs = int64(math.Round(float64(sum) / float64(len(lat))))
			a.MaxMs = max
			a.P95Ms = pctOfSortedLat(lat, 95)
		}
		byNode = append(byNode, *a)
	}
	return &slaTaskResp{
		Task:    t,
		Window:  slaWindow{From: from, To: to},
		Samples: total,
		ByNode:  byNode,
		Types:   knownProbeTaskTypes(),
	}, nil
}

// pctOfSortedLat 求延迟列表的 p 分位数（ms）
func pctOfSortedLat(lat []int64, p int) int64 {
	if len(lat) == 0 {
		return 0
	}
	s := append([]int64(nil), lat...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := (len(s) - 1) * p / 100
	return s[idx]
}

// evalSample 判定单条定时拨测样本 up/down
func evalSample(t *ProbeTask, linkStatus int, body string, latMs int64) sampleEval {
	ev := sampleEval{latMs: latMs, slow: t.SlowMs > 0 && latMs > int64(t.SlowMs)}
	switch t.APIType {
	case "detail", "ssl":
		up, invalid := evalDualStack(t, body)
		ev.up = up
		ev.invalid = invalid
	default:
		// tcping / speed：body 扁平（speed 顶层字段、tcping 由 ipv4 包裹）。
		// 优先解析节点 body 的真实可达性（is_reachable / ipv4.success），
		// 链路 HTTP 状态恒为转发层的 200，不可信；仅当 body 拿不到可达性信号时才回退链路命中。
		ev.up, ev.invalid = evalFlatBody(t, body, linkStatus)
	}
	return ev
}

// evalFlatBody 判定 tcping/speed 类样本（无双栈 body）。
// 返回 (up, invalid)。invalid=true 表示 body 无法解析且链路也失败，不进成败。
//
// 各类型 body 可达性信号：
//   - speed：顶层布尔 is_reachable（false=目标不可达/DNS 失败，即使转发链路 200 也应判 down）
//   - tcping：ipv4.success（成功探测次数，>0 视为可达）
//   - 命中可达性信号后，speed 再叠加 http_status_code 是否命中期望（tcping 无状态码，不看）
//
// 回退链路状态仅用于：body 为空 / 非 JSON / 不含可达性信号且结构不可识别的极端情况。
func evalFlatBody(t *ProbeTask, body string, linkStatus int) (up bool, invalid bool) {
	var root map[string]any
	jsonOK := body != "" && json.Unmarshal([]byte(body), &root) == nil
	if jsonOK {
		// speed：顶层 is_reachable 存在 → 以其为最终判定依据
		if reachable, ok := root["is_reachable"].(bool); ok {
			if !reachable {
				return false, false // 明确不可达（DNS 失败等）→ down，计入失败
			}
			// 可达 → 再校验真实状态码命中期望；无状态码则按可达即 up
			if code, ok := numOf(root, "http_status_code"); ok && code > 0 {
				return statusIsExpected(code, t.ExpectStatus), false
			}
			return true, false
		}
		// tcping：解析 ipv4.success（成功探测次数 >0 = 可达）
		if ipv4, ok := root["ipv4"].(map[string]any); ok {
			if succ, ok := numOf(ipv4, "success"); ok && succ > 0 {
				return true, false
			}
			// 有 ipv4 结构但成功数为 0：视为不可达（down）而非 invalid，如实反映失败
			return false, false
		}
		// JSON 可解析但结构不识别的非标准 body → 回落链路判定（不武断判 down）
	}
	// 兜底：body 空/非 JSON/结构未知 → 用链路状态（转发层 200 只在节点正常时出现）
	if linkStatus == 0 {
		return false, true // 链路请求失败且无有效 body，无法判定 → invalid
	}
	return statusIsExpected(linkStatus, t.ExpectStatus), false
}

// evalDualStack 解析 detail/ssl 双栈 body 判定 up。
// 返回 (up, invalid)；invalid=true 表示 body 完全不可解析，不进成败。
func evalDualStack(t *ProbeTask, body string) (up bool, invalid bool) {
	if body == "" {
		return false, true
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return false, true
	}
	stacks := map[string]map[string]any{}
	for _, k := range []string{"ipv4", "ipv6"} {
		if o, ok := root[k].(map[string]any); ok {
			stacks[k] = o
		}
	}
	if len(stacks) == 0 {
		return false, true // 没有可判定的栈
	}
	// 只对"存在的栈"判 up；缺失栈不算失败（IPv4-only 节点在 RequireAllStacks 下不应被误杀）
	stackUp := make([]bool, 0, len(stacks))
	for _, o := range stacks {
		stackUp = append(stackUp, evalStackUp(t, o))
	}
	if t.RequireAllStacks {
		for _, u := range stackUp {
			if !u {
				return false, false
			}
		}
		return true, false
	}
	for _, u := range stackUp {
		if u {
			return true, false
		}
	}
	return false, false
}

// evalStackUp 判定单栈 up
func evalStackUp(t *ProbeTask, o map[string]any) bool {
	// 证书过期：用户可配视为不可用（ssl 特有）
	if t.CertExpiredDown {
		if v, ok := o["is_expired"].(bool); ok && v {
			return false
		}
	}
	// 该栈参与判定的状态码来源：ssl 只报 https；detail 报 http+https 两探针
	// 把 detail 两探针的命中情况收集后按 bothProtocols 合并成"本栈是否命中期望"
	codes := []int{}
	if v, ok := numOf(o, "https_status_code"); ok {
		codes = append(codes, v)
	}
	if t.APIType == "detail" {
		if v, ok := numOf(o, "http_status_code"); ok {
			codes = append(codes, v)
		}
	} else if len(codes) == 0 {
		// ssl 没有 https 码时退回链路无关：无状态码可用则不可判（交给 invalid? 栈级无有效码）
		return false
	}
	if len(codes) == 0 {
		return false
	}
	if t.BothProtocols {
		// detail：http 与 https 都要命中；ssl 仅 https（单个码则等价任一）
		for _, code := range codes {
			if !statusIsExpected(code, t.ExpectStatus) {
				return false
			}
		}
		return true
	}
	// any：任一命中即可
	for _, code := range codes {
		if statusIsExpected(code, t.ExpectStatus) {
			return true
		}
	}
	return false
}

// extractSpecial 从最新样本提取看板展示的特殊字段（SSL 证书 / Detail 速度等）。
// 返回 nil 表示无可展示字段。取 ipv4 栈（无则任一栈）的值。
func extractSpecial(t *ProbeTask, body string) map[string]any {
	if t.APIType != "ssl" && t.APIType != "detail" {
		return nil
	}
	var root map[string]any
	if json.Unmarshal([]byte(body), &root) != nil {
		return nil
	}
	var o map[string]any
	if v, ok := root["ipv4"].(map[string]any); ok {
		o = v
	} else if v, ok := root["ipv6"].(map[string]any); ok {
		o = v
	} else {
		return nil
	}
	out := map[string]any{}
	if t.APIType == "ssl" {
		for _, k := range []string{"domain", "subject_common_name", "issuer_common_name", "http_version",
			"cert_start_time", "cert_end_time", "cert_validity_days"} {
			if v, ok := o[k]; ok {
				out[k] = v
			}
		}
		if v, ok := o["issuer_organization"].([]any); ok && len(v) > 0 {
			out["issuer_organization"] = v[0]
		}
	} else { // detail
		for _, k := range []string{"host_record", "download_speed", "page_size", "dns_lookup_time",
			"tcp_connect_time", "http_connect_time", "first_byte_time", "total_time", "http_status_code", "https_status_code"} {
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

// trueLatencyMs 从节点 body 提取**节点实测**的真实延迟(ms)——不含"控制台→节点"的中间链路往返，
// 与 SLA 曲线/平均延迟想表达的"到目标的真实延迟"一致。按拨测类型取不同字段：
//
//   - detail/ssl：ipv4/ipv6 栈 total_time（节点对目标 HTTP(S) 探测全程耗时）取可达栈平均；
//   - tcping：栈 avg_rtt（节点 ping 往返，可达 >0，DNS 失败为 -1）；
//   - speed：顶层 total_time（节点下载/探测耗时）。
//
// 返回 (ms, ok)。目标不可达/链路失败（无可用延迟字段，如 DNS 失败 avg_rtt=-1、total_time=0/缺失）
// 时返回 (0, false)——由调用方把延迟落 0（不可达无延迟），而非回退成控制台端到端耗时。
func trueLatencyMs(t *ProbeTask, body string) (int64, bool) {
	if body == "" {
		return 0, false
	}
	var root map[string]any
	if json.Unmarshal([]byte(body), &root) != nil {
		return 0, false
	}
	switch t.APIType {
	case "detail", "ssl", "tcping":
		// 双栈/ipv4 包裹：对"存在的栈"取真实延迟字段，仅累加可达栈（字段值>0）。
		// detail/ssl 看 total_time，tcping 看 avg_rtt。
		field := "total_time"
		if t.APIType == "tcping" {
			field = "avg_rtt"
		}
		var sum int64
		var n int
		for _, k := range []string{"ipv4", "ipv6"} {
			o, ok := root[k].(map[string]any)
			if !ok {
				continue
			}
			if v, ok := numOf(o, field); ok && v > 0 {
				sum += int64(v)
				n++
			}
		}
		if n == 0 {
			return 0, false
		}
		return int64(math.Round(float64(sum) / float64(n))), true
	case "speed":
		// speed body 顶层扁平，total_time 为节点真实耗时（不可达时=0/缺失）
		if v, ok := numOf(root, "total_time"); ok && v > 0 {
			return int64(v), true
		}
		return 0, false
	default:
		return 0, false
	}
}

// numOf 从 map 取数值字段（兼容 float64 / json.Number）
func numOf(o map[string]any, key string) (int, bool) {
	v, ok := o[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
	case string:
		var f float64
		if json.Unmarshal([]byte(n), &f) == nil {
			return int(f), true
		}
	}
	return 0, false
}

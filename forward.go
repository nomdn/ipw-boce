package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 数据面转发 /v1/{backendID}/{apiType}/{raw} 与 /middleware/... ====================
//
// HTTP 节点：GET {url}/v1/{apiType}/{raw}，注入 Bearer APIKey；上游状态码与 body 原样透传，
// 仅网络层错误返回 502 Backend unreachable。WS 节点：经 WS 下发 probe，失败 502。

const (
	sourceWS    = "ws"
	sourceHTTP  = "http"
	sourceSched = "sched"
	sourceBiz   = "biz"
)

// isProbeType 拨测明细只收这些类型（其余只进统计聚合）
func isProbeType(t string) bool {
	switch t {
	case "tcping", "speed", "detail", "ssl", "dns", "location":
		return true
	}
	return false
}

func probeTimeout() time.Duration {
	if HTTP_TIMEOUT <= 0 {
		return 30 * time.Second
	}
	return time.Duration(HTTP_TIMEOUT) * time.Second
}

var httpClient = &http.Client{}

func middlewareHandler(c *gin.Context) {
	slugString := strings.TrimPrefix(c.Param("slug"), "/")
	if slugString == "" {
		apiError(c, http.StatusBadRequest, "Missing slug parameter")
		return
	}
	slug := strings.Split(slugString, "/")
	// raw 带协议（https://example.com）时被切成多段，重新拼回
	if len(slug) > 4 {
		protocol := slug[2]
		if protocol != "https:" && protocol != "http:" {
			apiError(c, http.StatusBadRequest, "Invalid slug")
			return
		}
		var rest []string
		for _, p := range slug[3:] {
			if p != "" {
				rest = append(rest, p)
			}
		}
		slug = []string{slug[0], slug[1], protocol + "//" + strings.Join(rest, "/")}
	}
	if len(slug) < 3 {
		apiError(c, http.StatusBadRequest, "Invalid slug")
		return
	}
	backendID, apiType, raw := slug[0], slug[1], strings.Join(slug[2:], "/")
	if backendID == "" || apiType == "" || raw == "" {
		apiError(c, http.StatusBadRequest, "Missing parameters in slug")
		return
	}

	var query url.Values
	queryMap := map[string]string{}
	switch apiType {
	case "whois", "dns", "location", "ssl", "asn", "dnssec", "detail":
	case "tcping", "speed":
		query = url.Values{}
		for k, vals := range c.Request.URL.Query() {
			if len(vals) > 0 && vals[0] != "" {
				query.Set(k, vals[0])
				queryMap[k] = vals[0]
			}
		}
	default:
		apiError(c, http.StatusBadRequest, "Invalid API type")
		return
	}

	node := findNode(nodePoolForType(apiType), backendID)
	if node == nil || (!node.UseWS() && node.URL == "") {
		apiError(c, http.StatusBadRequest, "Invalid backend ID")
		return
	}
	if node.UseWS() {
		if wsSrv == nil {
			c.String(http.StatusBadGateway, "Node %s is WS-only but WS channel is disabled (ws-port=0 or bind failed)", backendID)
			return
		}
		status, body, err := wsSrv.RequestProbe(backendID, apiType, raw, queryMap, probeTimeout(), false)
		if err != nil {
			c.String(http.StatusBadGateway, "WS probe failed: %s", err.Error())
			return
		}
		c.Data(status, "application/json", body)
		return
	}
	target := nodeTarget(node.URL, apiType, raw)
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		apiError(c, http.StatusBadGateway, "Backend unreachable")
		return
	}
	if k := lookupAPIKey(backendID); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("Error fetching from %s: %v", target, err)
		apiError(c, http.StatusBadGateway, "Backend unreachable")
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		apiError(c, http.StatusBadGateway, "Backend unreachable")
		return
	}
	c.Data(resp.StatusCode, "application/json", data)
}

// ==================== 单节点拨测（调度与一键拨测共用） ====================

func probeOneWS(nodeID, apiType, raw string, query map[string]string) (int, []byte, error) {
	if wsSrv == nil {
		return 0, nil, fmt.Errorf("WS channel disabled (ws-port=0)")
	}
	return wsSrv.RequestProbe(nodeID, apiType, raw, query, probeTimeout(), true)
}

// nodeTarget 拼节点请求地址。普通节点：{url}/v1/{apiType}/{raw}。
// 中继节点（url 含 {path} 占位）：把 "{apiType}/{raw}" 代入占位，用于经另一个中间件转发，
// 如 https://1ipw.cn/middleware/hongkong/{path} → /middleware/hongkong/tcping/1.1.1.1
func nodeTarget(nodeURL, apiType, raw string) string {
	if strings.Contains(nodeURL, "{path}") {
		return strings.Replace(nodeURL, "{path}", apiType+"/"+raw, 1)
	}
	return strings.TrimSuffix(nodeURL, "/") + "/v1/" + apiType + "/" + raw
}

// probeOneHTTP 中心主动调度：带 X-Scheduler-Probe:1，节点识别后跳过重复上报
func probeOneHTTP(n apiInfo, apiType, raw string, query url.Values) (int, []byte, error) {
	target := nodeTarget(n.URL, apiType, raw)
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	if k := lookupAPIKey(n.ID); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	req.Header.Set("X-Scheduler-Probe", "1")
	resp, err := (&http.Client{Timeout: probeTimeout()}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

func probeOne(n apiInfo, apiType, raw string, query url.Values) (string, int, []byte, error) {
	if n.UseWS() {
		m := map[string]string{}
		for k := range query {
			m[k] = query.Get(k)
		}
		s, b, err := probeOneWS(n.ID, apiType, raw, m)
		return "ws", s, b, err
	}
	s, b, err := probeOneHTTP(n, apiType, raw, query)
	return "http", s, b, err
}

// ==================== 一键拨测（批量） ====================

type nodeResult struct {
	NodeID    string `json:"nodeId"`
	Label     string `json:"label"`
	Channel   string `json:"channel"`
	Status    int    `json:"status"`
	LatencyMs int64  `json:"latencyMs"`
	Body      any    `json:"body,omitempty"`
	Error     string `json:"error,omitempty"`
}

// probeBodyValue 合法 JSON 原样内联，其余（HTML 错误页等）退化为截断字符串
func probeBodyValue(body []byte) any {
	if len(body) == 0 {
		return nil
	}
	if json.Valid(body) {
		return json.RawMessage(body)
	}
	return truncateStr(string(body), 4096)
}

func probeBodyText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.RawMessage:
		return normalizeReportBody(t)
	}
	return fmt.Sprint(v)
}

// offlineNodeSet 全池一键拨测跳过"确证离线"的节点：WS 看实时连接，HTTP 看 nodes 快照
func offlineNodeSet(pool []apiInfo) map[string]bool {
	off := map[string]bool{}
	var needDB []string
	for _, n := range pool {
		if n.UseWS() && wsSrv != nil {
			if !wsSrv.NodeConnected(n.ID) {
				off[n.ID] = true
			}
			continue
		}
		needDB = append(needDB, n.ID)
	}
	if len(needDB) > 0 {
		ctx, cancel := dbCtx()
		defer cancel()
		var ids []string
		db.WithContext(ctx).Model(&Node{}).Where("node_id IN ? AND online = ?", needDB, false).Pluck("node_id", &ids)
		for _, id := range ids {
			off[id] = true
		}
	}
	return off
}

func batchProbeCore(apiType, raw string, wantNodes []string, query url.Values) ([]nodeResult, []string, []string, error) {
	pool := nodePoolForType(apiType)
	if len(pool) == 0 {
		return nil, nil, nil, fmt.Errorf("No nodes configured for api type %s", apiType)
	}
	wantAll := len(wantNodes) == 0
	selected := map[string]bool{}
	for _, n := range wantNodes {
		selected[n] = true
	}
	var targets []apiInfo
	for _, n := range pool {
		if wantAll || selected[n.ID] {
			targets = append(targets, n)
		}
	}
	unknown := []string{}
	for id := range selected {
		if findNode(pool, id) == nil {
			unknown = append(unknown, id)
		}
	}
	if len(targets) == 0 {
		ids := make([]string, 0, len(pool))
		for _, n := range pool {
			ids = append(ids, n.ID)
		}
		sort.Strings(ids)
		return nil, unknown, nil, fmt.Errorf("No matching nodes (known nodes: %s)", strings.Join(ids, ", "))
	}
	skipped := []string{}
	if wantAll {
		off := offlineNodeSet(pool)
		kept := targets[:0]
		for _, n := range targets {
			if off[n.ID] {
				skipped = append(skipped, n.ID)
			} else {
				kept = append(kept, n)
			}
		}
		targets = kept
		if len(targets) == 0 {
			return nil, unknown, skipped, fmt.Errorf("All nodes are offline (skipped: %s)", strings.Join(skipped, ", "))
		}
	}
	q := url.Values{}
	for k, v := range query {
		if len(v) > 0 && v[0] != "" {
			q.Set(k, v[0])
		}
	}
	results := make([]nodeResult, len(targets))
	var wg sync.WaitGroup
	for i, n := range targets {
		wg.Add(1)
		go func(i int, n apiInfo) {
			defer wg.Done()
			start := time.Now()
			ch, status, body, err := probeOne(n, apiType, raw, q)
			r := nodeResult{NodeID: n.ID, Label: n.Label, Channel: ch, Status: status, LatencyMs: time.Since(start).Milliseconds()}
			if err != nil {
				r.Error = err.Error()
			} else {
				r.Body = probeBodyValue(body)
			}
			results[i] = r
		}(i, n)
	}
	wg.Wait()
	return results, unknown, skipped, nil
}

func persistManualProbes(apiType, raw string, results []nodeResult, ownerID uint) int {
	if !isProbeType(apiType) {
		return 0
	}
	now := time.Now().UTC()
	rows := make([]ProbeResult, 0, len(results))
	for _, r := range results {
		row := ProbeResult{NodeID: r.NodeID, APIType: apiType, Raw: truncateStr(raw, 512), Status: r.Status,
			LatencyMs: r.LatencyMs, Source: sourceBiz, CreatedAt: now, OwnerID: ownerID}
		if r.Error != "" {
			row.Error = truncateStr(r.Error, 512)
		} else {
			row.Body = truncateStr(probeBodyText(r.Body), 64*1024)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return 0
	}
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).CreateInBatches(rows, 100).Error; err != nil {
		log.Printf("[probe] ERROR persist biz samples: %v", err)
		return 0
	}
	return len(rows)
}

func batchSummary(apiType, raw string, results []nodeResult, unknown, skipped []string, persisted int) gin.H {
	ok, failed := 0, 0
	for _, r := range results {
		if r.Status >= 200 && r.Status < 300 {
			ok++
		} else {
			failed++
		}
	}
	return gin.H{"apiType": apiType, "raw": raw, "targeted": len(results), "ok": ok, "failed": failed,
		"unknown": unknown, "skipped": skipped, "persisted": persisted, "results": results}
}

// batchProbeHandler POST /admin/nodes/probe/:apiType/*raw  body 可选 {"nodes":[...]}
func batchProbeHandler(c *gin.Context) {
	apiType := c.Param("apiType")
	raw := strings.TrimPrefix(c.Param("raw"), "/")
	if apiType == "" || raw == "" {
		apiError(c, http.StatusBadRequest, "Invalid slug")
		return
	}
	var body struct {
		Nodes []string `json:"nodes"`
	}
	_ = c.ShouldBindJSON(&body)
	uid, _, _ := currentUserFromCtx(c)
	cost, msg := chargeProbeCost(uid)
	if msg != "" {
		apiError(c, http.StatusPaymentRequired, msg)
		return
	}
	results, unknown, skipped, err := batchProbeCore(apiType, raw, body.Nodes, c.Request.URL.Query())
	if err != nil {
		refundProbeCost(uid, cost)
		apiError(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, batchSummary(apiType, raw, results, unknown, skipped, persistManualProbes(apiType, raw, results, uid)))
}

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

// nodeResult 单节点拨测结果（一键拨测聚合 & 落库共用）
type nodeResult struct {
	NodeID    string `json:"nodeId"`
	Label     string `json:"label"`
	Channel   string `json:"channel"` // ws | http
	Status    int    `json:"status"`
	LatencyMs int64  `json:"latencyMs"`
	Body      any    `json:"body,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ==================== 一键拨测（批量） ====================
//
// 手动运维命令：对节点池中的目标节点批量下发一次拨测，同步聚合各节点结果后一次性返回。
// 路由（/admin 组内，admin-token 鉴权）：
//   POST /admin/nodes/probe/:apiType/*raw
//
//   - :apiType 与 *raw 遵循原转发接口（middlewareHandler）的 slug 语义：
//     location/asn 走 ip-location-api 池，其余走 api-base-url 池；
//     raw 为拨测目标（detail/ssl 为完整 URL，dns 形如 "a/example.com"，tcping 为 host:port 等）。
//   - body 可选 {"nodes":["nodeId",...]}：限定只拨测这些节点；
//     缺省 = 该 apiType 所属节点池中的全部节点（HTTP 节点与 WS 节点一并覆盖）。
//   - query 参数按原接口透传给目标（tcping 的 port/count 等）。
//
// 结果同步聚合：对每个目标节点并发拨测并各自等待（带超时），
// HTTP 节点 GET 上游 v1/{apiType}/{raw}，WS 节点经 WS 通道 RequestProbe。
// 单个节点失败不影响其余节点，全部完成后汇总返回。

// nodePoolForType 按 apiType 返回对应节点池（与 middlewareHandler 的池路由规则一致）
func nodePoolForType(apiType string) []apiInfo {
	if apiType == "location" || apiType == "asn" {
		return IP_LOCATION_APIS
	}
	return flattenStack(API_BASE_URLS)
}

// batchProbeHandler POST /admin/nodes/probe/:apiType/*raw
func batchProbeHandler(c *gin.Context) {
	apiType := c.Param("apiType")
	// *raw 通配参数带前导 /，去掉即还原拨测目标（与 middlewareHandler 的 slug 处理一致）
	raw := strings.TrimPrefix(c.Param("raw"), "/")
	if apiType == "" || raw == "" {
		apiError(c, http.StatusBadRequest, "Invalid slug")
		return
	}

	// 池路由：与 middlewareHandler 一致（location/asn 走 ip-location-api，其余走 api-base-url）
	pool := nodePoolForType(apiType)
	if len(pool) == 0 {
		apiError(c, http.StatusBadRequest, "No nodes configured for api type "+apiType)
		return
	}

	// body 可选 nodes 子集：缺省 = 池中全部节点（HTTP + WS）
	selected := make(map[string]bool)
	wantAll := true
	if c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) > 0 {
		var body struct {
			Nodes []string `json:"nodes"`
		}
		if err := c.ShouldBindJSON(&body); err == nil && len(body.Nodes) > 0 {
			wantAll = false
			for _, n := range body.Nodes {
				selected[n] = true
			}
		}
	}

	// 过滤出目标节点；body 指定了但池里没有的记入 unknown 返回
	targets := make([]apiInfo, 0, len(pool))
	var unknown []string
	for _, n := range pool {
		if wantAll || selected[n.ID] {
			targets = append(targets, n)
		}
	}
	if !wantAll {
		for id := range selected {
			if findNode(pool, id) == nil {
				unknown = append(unknown, id)
			}
		}
	}
	if len(targets) == 0 {
		apiError(c, http.StatusBadRequest, "No matching nodes (known nodes: "+strings.Join(knownNodeIDs(pool), ", ")+")")
		return
	}

	// query 透传（过滤空值，对齐 middlewareHandler）
	query := url.Values{}
	queryMap := make(map[string]string)
	for k, vals := range c.Request.URL.Query() {
		if len(vals) > 0 && vals[0] != "" {
			query.Set(k, vals[0])
			queryMap[k] = vals[0]
		}
	}

	timeout := wsProbeTimeout()

	// 并发对每个目标节点拨测，各自带超时，互不影响
	results := make([]nodeResult, len(targets))
	var wg sync.WaitGroup
	for i, n := range targets {
		wg.Add(1)
		go func(i int, n apiInfo) {
			defer wg.Done()
			start := time.Now()
			if n.UseWS() {
				// WS 节点：经 WS 通道拨测（wsSrv 未启用时直接判失败）
				status, body, err := probeOneWS(n.ID, apiType, raw, queryMap, timeout)
				r := nodeResult{NodeID: n.ID, Label: n.Label, Channel: "ws", Status: status, LatencyMs: time.Since(start).Milliseconds()}
				if err != nil {
					r.Error = err.Error()
				} else {
					r.Body = json.RawMessage(body)
				}
				results[i] = r
				return
			}
			// HTTP 节点：GET 上游 v1/{apiType}/{raw}
			status, body, err := probeOneHTTP(n, apiType, raw, query, timeout)
			r := nodeResult{NodeID: n.ID, Label: n.Label, Channel: "http", Status: status, LatencyMs: time.Since(start).Milliseconds()}
			if err != nil {
				r.Error = err.Error()
			} else {
				r.Body = json.RawMessage(body)
			}
			results[i] = r
		}(i, n)
	}
	wg.Wait()

	// 控制台手动一键拨测落库（source=biz，归明细页"业务拨测"）。
	// 只落拨测类（isProbeType，对齐上报明细语义）；whois/dnssec/location/asn 等诊断类不进明细表。
	persisted := persistManualProbes(apiType, raw, results)

	okCnt, failedCnt := 0, 0
	for _, r := range results {
		if r.Status >= 200 && r.Status < 300 {
			okCnt++
		} else {
			failedCnt++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"apiType":   apiType,
		"raw":       raw,
		"targeted":  len(targets),
		"ok":        okCnt,
		"failed":    failedCnt,
		"unknown":   unknown,
		"persisted": persisted,
		"results":   results,
	})
}

// persistManualProbes 手动一键拨测结果落库（source=biz）。仅拨测类 API 入库，
// 单节点失败不中断；返回实际写入条数。数据库不可用（db==nil）时静默跳过。
func persistManualProbes(apiType, raw string, results []nodeResult) int {
	if !isProbeType(apiType) || db == nil {
		return 0
	}
	now := time.Now().UTC()
	rows := make([]ProbeResult, 0, len(results))
	for _, r := range results {
		if r.NodeID == "" {
			continue
		}
		row := ProbeResult{
			NodeID: r.NodeID, APIType: apiType, Raw: truncateStr(raw, 512),
			Status: r.Status, LatencyMs: r.LatencyMs, Source: sourceBiz, CreatedAt: now,
		}
		if r.Error != "" {
			row.Error = truncateStr(r.Error, 512)
		} else if r.Body != nil {
			row.Body = truncateStr(fmt.Sprint(r.Body), 64*1024)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return 0
	}
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).CreateInBatches(rows, 100).Error; err != nil {
		log.Printf("[probe] ERROR persist manual biz samples: %v", err)
		return 0
	}
	return len(rows)
}

// probeOneWS 经 WS 通道向单个节点拨测（本控制台主动调度，scheduler=true：节点跳过重复上报）
func probeOneWS(nodeID, apiType, raw string, query map[string]string, timeout time.Duration) (int, []byte, error) {
	if wsSrv == nil {
		return 0, nil, fmt.Errorf("WS channel disabled (ws-port=0)")
	}
	return wsSrv.RequestProbe(nodeID, apiType, raw, query, timeout, true)
}

// probeOneHTTP 向单个 HTTP 节点转发拨测（独立 client，per-node 超时，不影响全局 HTTP_CLIENT）
func probeOneHTTP(n apiInfo, apiType, raw string, query url.Values, timeout time.Duration) (int, []byte, error) {
	client := &http.Client{Timeout: timeout}
	base := n.URL
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	target := base + "v1/" + apiType + "/" + raw
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	// 与 middlewareHandler 一致：有 api-keys[节点id] 就注入 Bearer 鉴权
	if key := lookupAPIKey(n.ID); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	// 本控制台主动下发的拨测带调度标记：节点(nodeReportMiddleware)识别后跳过计数/明细上报，
	// 避免与本地落库(source=sched/biz)双算。真实业务转发/用户直连不带此 header，正常上报。
	req.Header.Set("X-Scheduler-Probe", "1")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// knownNodeIDs 汇总节点池全部 ID（错误提示用）
func knownNodeIDs(pool []apiInfo) []string {
	ids := make([]string, 0, len(pool))
	for _, n := range pool {
		ids = append(ids, n.ID)
	}
	sort.Strings(ids)
	return ids
}

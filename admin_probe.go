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
		return locationPoolSnapshot()
	}
	return apiPoolSnapshot()
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
	uid, _, _ := currentUserFromCtx(c) // 一键拨测归属（biz 明细"我的拨测历史"用）

	// body 可选 {"nodes": [...]}：限定只拨测这些节点；缺省 = 池中全部节点（HTTP + WS）
	var wantNodes []string
	if c.Request.ContentLength != 0 || len(c.Request.TransferEncoding) > 0 {
		var body struct {
			Nodes []string `json:"nodes"`
		}
		if err := c.ShouldBindJSON(&body); err == nil && len(body.Nodes) > 0 {
			wantNodes = body.Nodes
		}
	}

	results, unknown, err := batchProbeCore(apiType, raw, wantNodes, c.Request.URL.Query())
	if err != nil {
		apiError(c, http.StatusBadRequest, err.Error())
		return
	}

	// 控制台手动一键拨测落库（source=biz，归明细页"业务拨测"）。
	// 只落拨测类（isProbeType，对齐上报明细语义）；whois/dnssec/location/asn 等诊断类不进明细表。
	// 记录发起者 uid：用户据此在明细页看到"我的拨测历史"（静态 token 为 0，无归属）。
	persisted := persistManualProbes(apiType, raw, results, uid)

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
		"targeted":  len(results),
		"ok":        okCnt,
		"failed":    failedCnt,
		"unknown":   unknown,
		"persisted": persisted,
		"results":   results,
	})
}

// batchProbeCore 一键拨测核心：对节点池（或指定节点子集）并发拨测一轮并同步聚合结果。
// wantNodes 为空 = 全池；query 透传给目标（空值会被过滤）。
// 返回逐节点结果与"指定了但池里没有"的节点 id 列表；err 仅表示请求本身无法执行
// （该类型没有节点池 / 指定节点全部不在池中），单节点失败体现在 results[i].Error。
// 内部路由（/admin/nodes/probe）与 REST 语法糖层（POST /api/v1/probes）共用。
func batchProbeCore(apiType, raw string, wantNodes []string, query url.Values) ([]nodeResult, []string, error) {
	// 池路由：与 middlewareHandler 一致（location/asn 走 ip-location-api，其余走 api-base-url）
	pool := nodePoolForType(apiType)
	if len(pool) == 0 {
		return nil, nil, fmt.Errorf("No nodes configured for api type %s", apiType)
	}

	selected := make(map[string]bool, len(wantNodes))
	wantAll := len(wantNodes) == 0
	for _, n := range wantNodes {
		selected[n] = true
	}
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
		return nil, unknown, fmt.Errorf("No matching nodes (known nodes: %s)", strings.Join(knownNodeIDs(pool), ", "))
	}

	// query 透传（过滤空值，对齐 middlewareHandler）：HTTP 用 Values、WS 用 map 双形态
	queryString := url.Values{}
	queryMap := make(map[string]string)
	for k, vals := range query {
		if len(vals) > 0 && vals[0] != "" {
			queryString.Set(k, vals[0])
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
				status, body, perr := probeOneWS(n.ID, apiType, raw, queryMap, timeout)
				r := nodeResult{NodeID: n.ID, Label: n.Label, Channel: "ws", Status: status, LatencyMs: time.Since(start).Milliseconds()}
				if perr != nil {
					r.Error = perr.Error()
				} else {
					r.Body = json.RawMessage(body)
				}
				results[i] = r
				return
			}
			// HTTP 节点：GET 上游 v1/{apiType}/{raw}
			status, body, perr := probeOneHTTP(n, apiType, raw, queryString, timeout)
			r := nodeResult{NodeID: n.ID, Label: n.Label, Channel: "http", Status: status, LatencyMs: time.Since(start).Milliseconds()}
			if perr != nil {
				r.Error = perr.Error()
			} else {
				r.Body = json.RawMessage(body)
			}
			results[i] = r
		}(i, n)
	}
	wg.Wait()
	return results, unknown, nil
}

// persistManualProbes 手动一键拨测结果落库（source=biz，归属发起者 uid）。仅拨测类 API 入库，
// 单节点失败不中断；返回实际写入条数。数据库不可用（db==nil）时静默跳过。
func persistManualProbes(apiType, raw string, results []nodeResult, ownerID uint) int {
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
			OwnerID: ownerID,
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

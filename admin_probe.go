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
//   - body 可选 {"nodes":["nodeId",...]}：限定只拨测这些节点（点名即照拨，不跳过离线节点）；
//     缺省 = 该 apiType 所属节点池中的全部节点（HTTP 节点与 WS 节点一并覆盖），
//     此时会跳过当前确证离线的节点，被跳过的节点 id 在响应 skipped 字段里返回。
//   - query 参数按原接口透传给目标（tcping 的 port/count 等）。
//
// 结果同步聚合：对每个目标节点并发拨测并各自等待（带超时），
// HTTP 节点 GET 上游 v1/{apiType}/{raw}，WS 节点经 WS 通道 RequestProbe。
// 单个节点失败不影响其余节点，全部完成后汇总返回。

// ==================== 上游响应体的安全序列化 ====================
//
// 上游节点的响应体**未必是 JSON**：CDN / nginx 的 502、520 错误页（HTML）、空体、
// 被截断的 JSON 都会原样出现在这里。而 nodeResult.Body 是 any、值直接取 json.RawMessage，
// 一旦不是合法 JSON，json.Marshal 会报 "invalid character '<' looking for beginning of value"，
// gin 的 c.JSON 序列化失败时只 Abort、**不改已写入的状态码**，客户端于是收到
// HTTP 200 + Content-Type: application/json + Content-Length: 0 的空响应
// （前端表现为点了「执行拨测」毫无反应 / 报 null 相关错误）。
// 所以聚合前必须统一做一次判定：合法 JSON 原样内联透出，其余退化为字符串。

// probeBodyValue 把上游响应体包装成可安全序列化的值（用于接口响应）。
// 空体 → nil（JSON 里省略该字段）；合法 JSON → 原样内联；其余（HTML 错误页等）→ 截断字符串。
func probeBodyValue(body []byte) any {
	if len(body) == 0 {
		return nil
	}
	if json.Valid(body) {
		return json.RawMessage(body)
	}
	return truncateStr(string(body), 4096)
}

// probeBodyText 取响应体的文本形态（用于落库/展示）。
// 注意 json.RawMessage 虽是 []byte 别名，但 fmt 对它有特殊处理、Sprint 得到的是 JSON 原文；
// 这里仍显式分支（而非 fmt.Sprint）以免依赖该隐式行为，并与 report.go 的上报语义保持一致。
func probeBodyText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.RawMessage:
		return normalizeReportBody(t)
	case []byte:
		return string(t)
	default:
		return fmt.Sprint(t)
	}
}

// nodePoolForType 按 apiType 返回对应节点池（与 middlewareHandler 的池路由规则一致）
func nodePoolForType(apiType string) []apiInfo {
	if apiType == "location" || apiType == "asn" {
		return locationPoolSnapshot()
	}
	return apiPoolSnapshot()
}

// ==================== 一键拨测：全池模式跳过离线节点 ====================
//
// 全池一键拨测（不指定 nodes）默认跳过**当前已确证离线**的节点：离线节点不可能返回结果，
// 只会让整轮白等一个超时（WS 未连接干等到 wsProbeTimeout、HTTP 无响应干等到 HTTP 超时；
// 线上实测 1 个 502 节点 + 3 个掉线 WS 节点就能让一轮白等 ~12s）。
// **显式指定 nodes 时不跳过** —— 用户点名要拨的节点一律照拨（典型场景：就是要验证某节点是否恢复）。
//
// "离线"只认**确证离线**，信息不足一律算在线 —— 宁可多拨一次，也不漏掉可用节点：
//   - WS 节点：中心与节点的 WS 连接是实时的（心跳 20s、空闲 >75s 剔除），直接看连接；
//     WS 服务未启用时无从判定，退化为 nodes 表快照。
//   - HTTP 节点：没有常驻连接，只能看 nodes 表的 online 快照（nodeHealth 看门狗探活维护，
//     周期 1 小时、连续 2 次失败才置 down；即快照最多滞后 1 小时）。
//   - nodes 表里没有记录（从未注册 / 从未探活成功，如刚补进池的节点）→ **不算离线**。
//
// 退化原则：读库失败、无库时不跳过任何节点 —— 跳过只是省时间的优化，不该反过来让功能不可用。

// offlineNodeSet 返回池中"当前确证离线"的节点 id 集合（全池一键拨测据此跳过）。
func offlineNodeSet(pool []apiInfo) map[string]bool {
	off := make(map[string]bool)
	// WS 节点按实时连接判定（不查库，快照不如连接准）；其余节点收集起来一次性查库
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
	if len(needDB) == 0 || db == nil {
		return off
	}
	ctx, cancel := dbCtx()
	defer cancel()
	// 只查池内节点；在线的不进集合（默认值即"不算离线"），故只需看 online=false 的行
	var offline []Node
	if err := db.WithContext(ctx).
		Where("node_id IN ? AND online = ?", needDB, false).
		Find(&offline).Error; err != nil {
		log.Printf("[probe] ERROR load node online snapshot, will not skip any node: %v", err)
		return off
	}
	for i := range offline {
		off[offline[i].NodeID] = true
	}
	return off
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

	results, unknown, skipped, err := batchProbeCore(apiType, raw, wantNodes, c.Request.URL.Query())
	if err != nil {
		apiError(c, http.StatusBadRequest, err.Error())
		return
	}

	// 控制台手动一键拨测落库（source=biz，归明细页"业务拨测"）。
	// 只落拨测类（isProbeType，对齐上报明细语义）；whois/dnssec 等诊断类不进明细表，
	// 但 location/asn 已纳入 isProbeType 白名单、会进明细表（以 store.go 白名单为准）。
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
		"skipped":   skipped,
		"persisted": persisted,
		"results":   results,
	})
}

// batchProbeCore 一键拨测核心：对节点池（或指定节点子集）并发拨测一轮并同步聚合结果。
// wantNodes 为空 = 全池（此时跳过确证离线的节点，见 offlineNodeSet）；query 透传给目标（空值会被过滤）。
// 返回逐节点结果、"指定了但池里没有"的节点 id 列表、以及"全池模式下被跳过的离线节点"id 列表；
// err 仅表示请求本身无法执行（该类型没有节点池 / 指定节点全部不在池中 / 全池节点全离线），
// 单节点失败体现在 results[i].Error。
// 内部路由（/admin/nodes/probe）与 REST 语法糖层（POST /api/v1/probes）共用。
func batchProbeCore(apiType, raw string, wantNodes []string, query url.Values) ([]nodeResult, []string, []string, error) {
	// 池路由：与 middlewareHandler 一致（location/asn 走 ip-location-api，其余走 api-base-url）
	pool := nodePoolForType(apiType)
	if len(pool) == 0 {
		return nil, nil, nil, fmt.Errorf("No nodes configured for api type %s", apiType)
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
		return nil, unknown, nil, fmt.Errorf("No matching nodes (known nodes: %s)", strings.Join(knownNodeIDs(pool), ", "))
	}

	// 全池模式跳过确证离线的节点（显式指定 nodes 时不跳，见 offlineNodeSet 注释）。
	// 就地过滤 targets（该切片由本函数独占，可安全复用底层数组）。
	skipped := []string{}
	if wantAll {
		if off := offlineNodeSet(pool); len(off) > 0 {
			kept := targets[:0]
			for _, n := range targets {
				if off[n.ID] {
					skipped = append(skipped, n.ID)
					continue
				}
				kept = append(kept, n)
			}
			targets = kept
		}
	}
	if len(targets) == 0 {
		// 全池皆离线：报错而不是返回空结果，否则界面只剩"什么都没有"，看不出是被跳过了
		return nil, unknown, skipped, fmt.Errorf("All nodes are offline (skipped: %s)", strings.Join(skipped, ", "))
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
					r.Body = probeBodyValue(body)
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
				r.Body = probeBodyValue(body)
			}
			results[i] = r
		}(i, n)
	}
	wg.Wait()
	return results, unknown, skipped, nil
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
		} else if s := probeBodyText(r.Body); s != "" {
			row.Body = truncateStr(s, 64*1024)
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

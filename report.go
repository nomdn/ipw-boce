package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ==================== 数据上报（多入口冗余汇聚） ====================
//
// 同一后端节点可能被多个入口访问（原 Go 中间件、前端内置 TS 中间件、边缘函数），
// 请求不保证经过本中间件。汇聚方式：各入口把"自己观测到的"统计/拨测主动上报到
// 本收集中心，按 (分钟 × 节点 × apiType) 冲突累加，天然合并多入口视角。
//
// 上报协议 v1（HTTP 与 WS 同构）：
//   POST /report   body = {"instance":"上报方标识","stats":[...],"probes":[...]}
//   WS 消息        {"type":"report","data":{... 同 body ...}}（须先 register）
//
// 上报语义（防双算）：
//   - 只上报"第一方观测"：自己转发的请求；不得转播从别处收到的数据
//   - stats 是增量计数（每次上报携带自上次以来的增量），收集器按累加入库
//   - 收集中心自身不要配 report-url 指向自己（会造成本地+上报双份）
//   - 上报为 at-most-once（失败丢弃并记日志），不重试，避免重试导致的重复累加

// reportStat 单条统计增量（一分钟桶）
type reportStat struct {
	Minute       int64  `json:"minute"` // unix 分钟桶；0 = 收集器当前分钟
	NodeID       string `json:"nodeId"`
	APIType      string `json:"apiType"`
	Total        int64  `json:"total"`
	Errors       int64  `json:"errors"`
	LatencySumMs int64  `json:"latencySumMs"`
	LatencyMaxMs int64  `json:"latencyMaxMs"`
}

// reportProbe 单条拨测明细
type reportProbe struct {
	RequestID string          `json:"requestId,omitempty"`
	NodeID    string          `json:"nodeId"`
	APIType   string          `json:"apiType"` // tcping | udping | speed
	Raw       string          `json:"raw"`
	Query     string          `json:"query,omitempty"`
	Status    int             `json:"status"`
	LatencyMs int64           `json:"latencyMs"`
	Error     string          `json:"error,omitempty"`
	Source    string          `json:"source,omitempty"`    // ws | http
	Body      json.RawMessage `json:"body,omitempty"`      // 兼容 string 与 JSON 对象（对齐 WS probe_result 透传语义）
	CreatedAt int64           `json:"createdAt,omitempty"` // unix 秒；0 = 收集器当前时间
}

// reportPayload 上报报文（HTTP body 与 WS data 同构）
type reportPayload struct {
	Instance string        `json:"instance,omitempty"` // 上报方标识（存入 probe_results.origin）
	Stats    []reportStat  `json:"stats,omitempty"`
	Probes   []reportProbe `json:"probes,omitempty"`
}

// 上报单次请求的容量上限（防御性：拒绝超大报文与无限刷库）
const (
	reportMaxStats  = 500
	reportMaxProbes = 500
	reportMaxBody   = 4 << 20 // 4MB
)

// ==================== 入库 ====================

var (
	boceInstanceName string
	boceInstanceOnce sync.Once
)

// boceInstance 本实例标识（hostname，缓存）：转发标记头 X-Boce-Reporter 与上报 instance 共用
func boceInstance() string {
	boceInstanceOnce.Do(func() {
		host, err := os.Hostname()
		if err != nil || host == "" {
			host = "unknown"
		}
		boceInstanceName = host
	})
	return boceInstanceName
}

// ingestReport 校验并入库一份数据上报，返回 (接受统计条数, 接受拨测条数)。
// 非法条目跳过（计入拒绝），不整体失败；结构性错误（超容量）返回 error。
func ingestReport(payload *reportPayload) (int, int, error) {
	if db == nil {
		return 0, 0, fmt.Errorf("store unavailable")
	}
	if len(payload.Stats) > reportMaxStats || len(payload.Probes) > reportMaxProbes {
		return 0, 0, fmt.Errorf("payload too large (max %d stats / %d probes per report)", reportMaxStats, reportMaxProbes)
	}

	ctx, cancel := dbCtx()
	defer cancel()
	nowMinute := time.Now().UTC().Unix() / 60
	acceptedStats, acceptedProbes := 0, 0

	for _, st := range payload.Stats {
		if st.NodeID == "" || st.APIType == "" || st.Total < 0 {
			continue
		}
		minute := st.Minute
		if minute == 0 {
			minute = nowMinute
		}
		// 分钟桶合理性：不允许未来 1 小时以外（时钟漂移容忍），过旧的交给保留期清理
		if minute > nowMinute+60 {
			continue
		}
		err := upsertStatDelta(db.DB, minute, st.NodeID, st.APIType, statVal{
			total: st.Total, errs: st.Errors, latSum: st.LatencySumMs, latMax: st.LatencyMaxMs,
		})
		if err != nil {
			log.Printf("[report] ERROR ingest stat (node=%s api=%s): %v", st.NodeID, st.APIType, err)
			continue
		}
		acceptedStats++
	}

	now := time.Now().UTC()
	rows := make([]ProbeResult, 0, len(payload.Probes))
	for _, p := range payload.Probes {
		if p.NodeID == "" || !isProbeType(p.APIType) {
			continue
		}
		created := now
		if p.CreatedAt > 0 {
			created = time.Unix(p.CreatedAt, 0).UTC()
		}
		source := p.Source
		if source != sourceWS {
			source = sourceHTTP
		}
		body := normalizeReportBody(p.Body)
		rows = append(rows, ProbeResult{
			RequestID: truncateStr(p.RequestID, 64), NodeID: p.NodeID, APIType: p.APIType,
			Raw: truncateStr(p.Raw, 512), Query: truncateStr(p.Query, 512),
			Status: p.Status, LatencyMs: p.LatencyMs, Error: truncateStr(p.Error, 512),
			Source: source, Origin: truncateStr(payload.Instance, 128),
			Body: truncateStr(body, 64*1024), CreatedAt: created,
		})
	}
	if len(rows) > 0 {
		if err := db.WithContext(ctx).CreateInBatches(rows, 200).Error; err != nil {
			log.Printf("[report] ERROR ingest probes: %v", err)
		} else {
			acceptedProbes = len(rows)
		}
	}
	return acceptedStats, acceptedProbes, nil
}

// normalizeReportBody 上报的 body 兼容三种形态：JSON 字符串（按原文解出）、
// JSON 对象/数组（保留原文）、其他（按字符串处理）
func normalizeReportBody(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	return string(raw)
}

// ==================== HTTP 上报接口 ====================

// registerReportRoutes 注册 POST /report。
// 鉴权：report-token，未配置回退 admin-token，两者都空 = 开放（仅限内网/受信环境）。
// 该路由不在限流组内——前端内置中间件是"调一次上报一次"，不能被转发限流误伤。
func registerReportRoutes(router *gin.Engine) {
	router.POST("/report", reportAuth(), func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, reportMaxBody)
		var payload reportPayload
		if err := c.ShouldBindJSON(&payload); err != nil {
			apiError(c, http.StatusBadRequest, "invalid report payload: "+err.Error())
			return
		}
		if len(payload.Stats) == 0 && len(payload.Probes) == 0 {
			apiError(c, http.StatusBadRequest, "empty report (stats/probes both empty)")
			return
		}
		s, p, err := ingestReport(&payload)
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"accepted": gin.H{"stats": s, "probes": p}})
	})
}

// reportAuth 上报鉴权（token 优先级：report-token > admin-token；都空 = 开放）
func reportAuth() gin.HandlerFunc {
	token := REPORT_TOKEN
	if token == "" {
		token = ADMIN_TOKEN
	}
	if token == "" {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer "+token {
			apiError(c, http.StatusUnauthorized, "Unauthorized")
			c.Abort()
			return
		}
		c.Next()
	}
}

// ==================== 上报客户端（report-url 配置后本实例成为上报方） ====================

// reportClient 把本实例第一方观测（统计快照 + 拨测明细）定期批量推给收集中心。
// 通道满/发送失败一律丢弃并计数（at-most-once），绝不重试——统计是累加语义，重试会双算。
type reportClient struct {
	url      string
	token    string
	instance string

	statsCh chan reportStat
	probeCh chan reportProbe

	dropMu     sync.Mutex
	droppedN   int64
	httpC      *http.Client
	stopCh     chan struct{}
	stopWait   sync.WaitGroup
	withProbes bool
}

var reporter *reportClient // 非 nil 表示本实例开启了上报

func newReportClient() *reportClient {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return &reportClient{
		url:        REPORT_URL,
		token:      REPORT_TOKEN,
		instance:   host,
		statsCh:    make(chan reportStat, 4096),
		probeCh:    make(chan reportProbe, 2048),
		httpC:      &http.Client{Timeout: 10 * time.Second},
		stopCh:     make(chan struct{}),
		withProbes: REPORT_PROBES,
	}
}

func (r *reportClient) Start() {
	r.stopWait.Add(1)
	go r.loop()
	log.Printf("[report] client enabled -> %s (instance=%s, interval=%ds, probes=%v)",
		r.url, r.instance, REPORT_INTERVAL, r.withProbes)
}

// outStats 把一次 Flush 的统计快照转成上报条目（本实例"自己统计上报"的统计侧出口）
func (r *reportClient) outStats(snap map[statKey]*statVal, minute int64) {
	for k, v := range snap {
		r.send(reportStat{
			Minute: minute, NodeID: k.Node, APIType: k.API,
			Total: v.total, Errors: v.errs, LatencySumMs: v.latSum, LatencyMaxMs: v.latMax,
		})
	}
}

// outProbe 拨测明细出口（recordProbeResult 钩子）
func (r *reportClient) outProbe(pr probeResult) {
	if !r.withProbes {
		return
	}
	// body 以 JSON 字符串传输（收集端 normalizeReportBody 解回原文），保证任意文本 round-trip
	body, err := json.Marshal(pr.Body)
	if err != nil {
		body = nil
	}
	r.sendProbe(reportProbe{
		RequestID: pr.RequestID, NodeID: pr.NodeID, APIType: pr.APIType,
		Raw: pr.Raw, Query: pr.Query, Status: pr.Status, LatencyMs: pr.LatencyMs,
		Error: pr.Error, Source: pr.Source, Body: body,
		CreatedAt: time.Now().Unix(),
	})
}

func (r *reportClient) send(st reportStat) {
	select {
	case r.statsCh <- st:
	default:
		r.markDropped()
	}
}

func (r *reportClient) sendProbe(p reportProbe) {
	select {
	case r.probeCh <- p:
	default:
		r.markDropped()
	}
}

func (r *reportClient) markDropped() {
	r.dropMu.Lock()
	r.droppedN++
	n := r.droppedN
	r.dropMu.Unlock()
	if n%100 == 1 {
		log.Printf("[report] WARN upstream queue full, dropped %d records so far", n)
	}
}

// loop 定期把积累的统计/拨测批量 POST 给收集中心
func (r *reportClient) loop() {
	defer r.stopWait.Done()
	ticker := time.NewTicker(time.Duration(REPORT_INTERVAL) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.flushAll()
		case <-r.stopCh:
			r.flushAll()
			return
		}
	}
}

func (r *reportClient) flushAll() {
	for {
		payload := reportPayload{Instance: r.instance}
		statsN, probesN := 0, 0
		for len(r.statsCh) > 0 && statsN < reportMaxStats {
			payload.Stats = append(payload.Stats, <-r.statsCh)
			statsN++
		}
		if r.withProbes {
			for len(r.probeCh) > 0 && probesN < reportMaxProbes {
				payload.Probes = append(payload.Probes, <-r.probeCh)
				probesN++
			}
		}
		if statsN == 0 && probesN == 0 {
			return
		}
		if err := r.post(&payload); err != nil {
			// at-most-once：失败丢弃（数据仍在本地库），不重试避免统计双算
			log.Printf("[report] ERROR post to collector: %v (dropped stats=%d probes=%d)", err, statsN, probesN)
		}
		if statsN < reportMaxStats && probesN < reportMaxProbes {
			return
		}
	}
}

func (r *reportClient) post(payload *reportPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.httpC.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("collector returned status %d", resp.StatusCode)
	}
	return nil
}

// ==================== WS 上报（ws.go 的 report 消息走这里） ====================

// handleWSReport 处理已注册节点经 WS 通道发来的 report 消息
func handleWSReport(nodeID string, data json.RawMessage) {
	if store == nil {
		return
	}
	var payload reportPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		log.Printf("[ws] bad report from %s: %v", nodeID, err)
		return
	}
	if payload.Instance == "" {
		payload.Instance = "ws:" + nodeID
	}
	s, p, err := ingestReport(&payload)
	if err != nil {
		log.Printf("[ws] report rejected from %s: %v", nodeID, err)
		return
	}
	if s+p > 0 {
		log.Printf("[ws] report from %s: stats=%d probes=%d", nodeID, s, p)
	}
}

// upsertStatDelta 单条统计增量 upsert（Flush 与 ingestReport 共用）。
// latency_max 取历史与新值较大者；SQLite 无 GREATEST，用标量 max(a,b)。
func upsertStatDelta(g *gorm.DB, minute int64, nodeID, apiType string, v statVal) error {
	row := RequestStat{
		Minute: minute, NodeID: nodeID, APIType: apiType,
		Total: v.total, Errors: v.errs, LatencySumMs: v.latSum, LatencyMaxMs: v.latMax,
		UpdatedAt: time.Now().UTC(),
	}
	maxExpr := "GREATEST(request_stats.latency_max_ms, ?)"
	if g.Dialector.Name() == "sqlite" {
		maxExpr = "max(request_stats.latency_max_ms, ?)"
	}
	ctx, cancel := dbCtx()
	defer cancel()
	return g.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "minute"}, {Name: "node_id"}, {Name: "api_type"}},
		DoUpdates: clause.Assignments(map[string]any{
			"total":          gorm.Expr("request_stats.total + ?", v.total),
			"errors":         gorm.Expr("request_stats.errors + ?", v.errs),
			"latency_sum_ms": gorm.Expr("request_stats.latency_sum_ms + ?", v.latSum),
			"latency_max_ms": gorm.Expr(maxExpr, v.latMax),
			"updated_at":     time.Now().UTC(),
		}),
	}).Create(&row).Error
}

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ==================== 节点上报（收集端） ====================
//
// 统计与拨测明细由拨测节点自己上报，中间件不在转发路径上采集：
//   - 节点连着 WS 通道 → 经 WS 发 {"type":"report","data":{...}}（须先 register）
//   - 节点没有 WS（纯 HTTP 节点）→ POST /report
//
// 汇聚口径：按 (分钟 × 节点 × apiType) 冲突累加，多份上报自然合并。
//
// 上报协议 v1（两条通道报文同构）：
//   POST /report   body = {"instance":"上报方标识","stats":[...],"probes":[...]}
//   WS 消息        {"type":"report","data":{... 同 body ...}}
//
// 语义约束：
//   - 上报方只报"自己第一方观测"的（自己处理的请求），不得转播从别处收到的数据
//   - stats 是增量计数，收集端按累加入库；上报为 at-most-once（失败丢弃并记日志），
//     不重试——统计是累加语义，重试会导致重复计算

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
	APIType   string          `json:"apiType"` // 拨测类：detail | ssl | dns | tcping | speed
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

// statVal 统计增量（写入 request_stats 时的一组聚合值）
type statVal struct {
	total, errs, latSum, latMax int64
}

// ==================== 入库 ====================

// ingestReport 校验并入库一份上报，返回 (接受统计条数, 接受拨测条数)。
// 非法条目跳过（不计入接受数），不整体失败；结构性错误（超容量）返回 error。
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

// registerReportRoutes 注册 POST /report（无 WS 通道的节点走这里）。
// 鉴权：report-token，未配置回退 admin-token，两者都空 = 开放（仅限内网/受信环境）。
// 该路由不在限流组内——节点是"处理一次上报一次"，不能被转发限流误伤。
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

// upsertStatDelta 单条统计增量 upsert（节点上报的唯一落库口）。
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

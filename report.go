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

// ==================== 节点上报协议 v1（HTTP POST /report 与 WS report 同构） ====================
//
// body = {"instance":"上报方标识","stats":[{minute,nodeId,apiType,total,errors,latencySumMs,latencyMaxMs}],
//         "probes":[{requestId,nodeId,apiType,raw,query,status,latencyMs,error,source,body,createdAt}]}
// stats 为增量，按 (分钟 × 节点 × apiType) 冲突累加；at-most-once，不重试。

type reportStat struct {
	Minute       int64  `json:"minute"`
	NodeID       string `json:"nodeId"`
	APIType      string `json:"apiType"`
	Total        int64  `json:"total"`
	Errors       int64  `json:"errors"`
	LatencySumMs int64  `json:"latencySumMs"`
	LatencyMaxMs int64  `json:"latencyMaxMs"`
}

type reportProbe struct {
	RequestID string          `json:"requestId,omitempty"`
	NodeID    string          `json:"nodeId"`
	APIType   string          `json:"apiType"`
	Raw       string          `json:"raw"`
	Query     string          `json:"query,omitempty"`
	Status    int             `json:"status"`
	LatencyMs int64           `json:"latencyMs"`
	Error     string          `json:"error,omitempty"`
	Source    string          `json:"source,omitempty"`
	Body      json.RawMessage `json:"body,omitempty"`
	CreatedAt int64           `json:"createdAt,omitempty"`
}

type reportPayload struct {
	Instance string        `json:"instance,omitempty"`
	Stats    []reportStat  `json:"stats,omitempty"`
	Probes   []reportProbe `json:"probes,omitempty"`
}

const (
	reportMaxStats  = 500
	reportMaxProbes = 500
	reportMaxBody   = 4 << 20
)

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

func ingestReport(p *reportPayload) (int, int, error) {
	if len(p.Stats) > reportMaxStats || len(p.Probes) > reportMaxProbes {
		return 0, 0, fmt.Errorf("payload too large (max %d stats / %d probes per report)", reportMaxStats, reportMaxProbes)
	}
	nowMinute := time.Now().UTC().Unix() / 60
	accStats := 0
	for _, st := range p.Stats {
		if st.NodeID == "" || st.APIType == "" || st.Total < 0 {
			continue
		}
		if st.Minute == 0 {
			st.Minute = nowMinute
		}
		if st.Minute > nowMinute+60 {
			continue
		}
		if err := upsertStatDelta(st); err != nil {
			log.Printf("[report] ERROR ingest stat: %v", err)
			continue
		}
		accStats++
	}
	now := time.Now().UTC()
	rows := make([]ProbeResult, 0, len(p.Probes))
	for _, pr := range p.Probes {
		if pr.NodeID == "" || !isProbeType(pr.APIType) {
			continue
		}
		created := now
		if pr.CreatedAt > 0 {
			created = time.Unix(pr.CreatedAt, 0).UTC()
		}
		src := sourceHTTP
		if pr.Source == sourceWS {
			src = sourceWS
		}
		rows = append(rows, ProbeResult{
			RequestID: truncateStr(pr.RequestID, 64), NodeID: pr.NodeID, APIType: pr.APIType,
			Raw: truncateStr(pr.Raw, 512), Query: truncateStr(pr.Query, 512), Status: pr.Status,
			LatencyMs: pr.LatencyMs, Error: truncateStr(pr.Error, 512), Source: src,
			Origin: truncateStr(p.Instance, 128), Body: truncateStr(normalizeReportBody(pr.Body), 64*1024), CreatedAt: created,
		})
	}
	accProbes := 0
	if len(rows) > 0 {
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).CreateInBatches(rows, 200).Error; err != nil {
			log.Printf("[report] ERROR ingest probes: %v", err)
		} else {
			accProbes = len(rows)
		}
	}
	return accStats, accProbes, nil
}

func upsertStatDelta(st reportStat) error {
	maxExpr := "GREATEST(request_stats.latency_max_ms, ?)"
	if db.driver == "sqlite" {
		maxExpr = "max(request_stats.latency_max_ms, ?)"
	}
	ctx, cancel := dbCtx()
	defer cancel()
	row := RequestStat{Minute: st.Minute, NodeID: st.NodeID, APIType: st.APIType, Total: st.Total,
		Errors: st.Errors, LatencySumMs: st.LatencySumMs, LatencyMaxMs: st.LatencyMaxMs}
	return db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "minute"}, {Name: "node_id"}, {Name: "api_type"}},
		DoUpdates: clause.Assignments(map[string]any{
			"total":          gorm.Expr("request_stats.total + ?", st.Total),
			"errors":         gorm.Expr("request_stats.errors + ?", st.Errors),
			"latency_sum_ms": gorm.Expr("request_stats.latency_sum_ms + ?", st.LatencySumMs),
			"latency_max_ms": gorm.Expr(maxExpr, st.LatencyMaxMs),
			"updated_at":     time.Now().UTC(),
		}),
	}).Create(&row).Error
}

// reportAuth report-token > admin-token；都空 = 开放
func reportAuth() gin.HandlerFunc {
	token := REPORT_TOKEN
	if token == "" {
		token = ADMIN_TOKEN
	}
	return func(c *gin.Context) {
		if token != "" && c.GetHeader("Authorization") != "Bearer "+token {
			apiError(c, http.StatusUnauthorized, "Unauthorized")
			c.Abort()
			return
		}
		c.Next()
	}
}

func registerReportRoutes(r *gin.Engine) {
	r.POST("/report", reportAuth(), func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, reportMaxBody)
		var p reportPayload
		if err := c.ShouldBindJSON(&p); err != nil {
			apiError(c, http.StatusBadRequest, "invalid report payload: "+err.Error())
			return
		}
		if len(p.Stats) == 0 && len(p.Probes) == 0 {
			apiError(c, http.StatusBadRequest, "empty report (stats/probes both empty)")
			return
		}
		s, pr, err := ingestReport(&p)
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"accepted": gin.H{"stats": s, "probes": pr}})
	})
}

func handleWSReport(nodeID string, data json.RawMessage) {
	var p reportPayload
	if err := json.Unmarshal(data, &p); err != nil {
		log.Printf("[ws] bad report from %s: %v", nodeID, err)
		return
	}
	if p.Instance == "" {
		p.Instance = "ws:" + nodeID
	}
	if _, _, err := ingestReport(&p); err != nil {
		log.Printf("[ws] report rejected from %s: %v", nodeID, err)
	}
}

// retentionLoop 每小时清理超过保留期的拨测/统计/事件（0 = 永久）
func retentionLoop() {
	if DATA_RETENTION_DAYS <= 0 {
		return
	}
	for {
		cutoff := time.Now().UTC().AddDate(0, 0, -DATA_RETENTION_DAYS)
		ctx, cancel := dbCtx()
		db.WithContext(ctx).Where("created_at < ?", cutoff).Delete(&ProbeResult{})
		db.WithContext(ctx).Where("created_at < ?", cutoff).Delete(&NodeEvent{})
		db.WithContext(ctx).Where("minute < ?", cutoff.Unix()/60).Delete(&RequestStat{})
		cancel()
		cleanupMaintenanceWindows()
		time.Sleep(time.Hour)
	}
}

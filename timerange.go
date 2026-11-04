package main

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 统一时间窗口：?hours=N 或 ?start=&end=（RFC3339 / Unix 秒 / 毫秒），上限 90 天 ====================

const maxRangeHours = 24 * 90

type timeRange struct {
	From, To time.Time
	Hours    float64
	Abs      bool
}

func parseTimeArg(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		if n > 1e12 {
			return time.UnixMilli(n).UTC(), true
		}
		return time.Unix(n, 0).UTC(), true
	}
	return time.Time{}, false
}

func parseTimeRange(c *gin.Context, defHours float64) timeRange {
	now := time.Now().UTC()
	startRaw, endRaw := strings.TrimSpace(c.Query("start")), strings.TrimSpace(c.Query("end"))
	if startRaw != "" || endRaw != "" {
		to := now
		if t, ok := parseTimeArg(endRaw); ok {
			to = t
		}
		from := to.Add(-time.Duration(defHours * float64(time.Hour)))
		if t, ok := parseTimeArg(startRaw); ok {
			from = t
		}
		if !from.Before(to) {
			from = to.Add(-time.Minute)
		}
		if to.Sub(from) > maxRangeHours*time.Hour {
			from = to.Add(-maxRangeHours * time.Hour)
		}
		return timeRange{From: from, To: to, Hours: to.Sub(from).Hours(), Abs: true}
	}
	h := clampFloat(c.Query("hours"), defHours, 1, maxRangeHours)
	return timeRange{From: now.Add(-time.Duration(h * float64(time.Hour))), To: now, Hours: h}
}

func registerTimeRangeRoutes(g *gin.RouterGroup) {
	g.GET("/stats/range", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var statMin int64
		db.WithContext(ctx).Model(&RequestStat{}).Select("COALESCE(MIN(minute), 0)").Scan(&statMin)
		var probeMin sql.NullTime
		db.WithContext(ctx).Model(&ProbeResult{}).Select("MIN(created_at)").Scan(&probeMin)
		earliest := statMin * 60
		if probeMin.Valid && (earliest == 0 || probeMin.Time.Unix() < earliest) {
			earliest = probeMin.Time.Unix()
		}
		maxDays := maxRangeHours / 24
		if DATA_RETENTION_DAYS > 0 && DATA_RETENTION_DAYS < maxDays {
			maxDays = DATA_RETENTION_DAYS
		}
		var e time.Time
		if earliest > 0 {
			e = time.Unix(earliest, 0).UTC()
		}
		c.JSON(http.StatusOK, gin.H{"now": time.Now().UTC(), "earliest": e, "maxDays": maxDays, "retention": DATA_RETENTION_DAYS})
	})
}

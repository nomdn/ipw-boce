package main

// ==================== 统一时间窗口解析 ====================
//
// 控制台的时间范围选择器（预设清单 + ◀▶ 平移 + 自定义区间）会产生两种窗口：
//   - 相对窗口：?hours=24 —— “最近 24 小时”，终点恒为 now（向后兼容既有调用方）
//   - 绝对区间：?start=&end= —— “今天 / 本周 / 某个更早的历史区间”，终点可以是过去
//
// 二者在查询层只是 (from, to) 两个边界，因此统一收敛到 parseTimeRange：
// 有 start/end 就走绝对区间，否则回落 hours。调用方拿到 timeRange 后只需把
// Where(...) 的边界换成 From/To —— 分桶、SLA 判定等计算逻辑完全不用改。
//
// 上限 90 天（maxRangeHours）与 data-retention-days（缺省 30 天）共同决定
// 预设清单能放多远：超出保留期的预设查出来必然是空图，所以前端会按
// GET /admin/stats/range 返回的可用范围把这类预设直接隐藏。

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// maxRangeHours 单次查询的最大跨度（小时）：与既有 hours clamp 上限一致（90 天）。
// 再放宽之前需要先给 request_stats（分钟粒度）和 probe_results（采样轮次粒度）做
// 服务端降采样，否则一次查询就会拉出几十万行、JSON 也撑不住。
const maxRangeHours = 24 * 90

// minRangeSpan 最小跨度：避免 start/end 传成同一时刻（或首尾颠倒被夹平）后出现空窗口。
const minRangeSpan = time.Minute

// timeRange 一次窗口解析的结果。
type timeRange struct {
	From  time.Time // 含
	To    time.Time // 含
	Hours float64   // 实际跨度（小时），供分桶粒度与前端回显
	Abs   bool      // true = 显式指定了 start/end（可能是历史区间，不再“跟随当前”）
}

// parseTimeRange 解析统一的时间窗口参数（首参为缺省相对窗口的小时数）。
//
// 规则：
//   - 带 ?start= 或 ?end=（RFC3339 / Unix 秒 / Unix 毫秒）→ 绝对区间；
//     end 缺省 = now（于是“今天”只需传 start）；start 缺省按 defHours 反推。
//   - 否则 ?hours=N（缺省 defHours，clamp 到 1 ~ maxRangeHours）。
//   - 非法值不报错：当作未提供该参数处理，避免前端一个笔误整页 400。
//     （真要看“为什么窗口不对”，日志里有；控制台不该因为 URL 手改而白屏。）
func parseTimeRange(c *gin.Context, defHours float64) timeRange {
	now := time.Now().UTC()
	startRaw := strings.TrimSpace(c.Query("start"))
	endRaw := strings.TrimSpace(c.Query("end"))

	if startRaw != "" || endRaw != "" {
		to := now
		if endRaw != "" {
			if t, ok := parseTimeArg(endRaw); ok {
				to = t
			}
		}
		from := to.Add(-time.Duration(clampFloat("", defHours, 1, maxRangeHours) * float64(time.Hour)))
		if startRaw != "" {
			if t, ok := parseTimeArg(startRaw); ok {
				from = t
			}
		}
		// 首尾颠倒 / 同一时刻 → 夹成最小跨度，保住“有一个合法窗口”的语义
		if !from.Before(to) {
			from = to.Add(-minRangeSpan)
		}
		// 超宽 → 只截取靠后的部分（保留离 now 更近的一侧，排查场景更有用）
		if span := to.Sub(from); span > time.Duration(maxRangeHours)*time.Hour {
			from = to.Add(-time.Duration(maxRangeHours) * time.Hour)
		}
		return timeRange{From: from, To: to, Hours: to.Sub(from).Hours(), Abs: true}
	}

	hours := clampFloat(c.Query("hours"), defHours, 1, maxRangeHours)
	return timeRange{From: now.Add(-time.Duration(hours * float64(time.Hour))), To: now, Hours: hours}
}

// parseTimeArg 接受 RFC3339（含毫秒，JS toISOString 的产物）、Unix 秒、Unix 毫秒。
func parseTimeArg(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		if n > 1e12 { // 毫秒（> 2001 年的秒级时间戳不可能这么大）
			return time.UnixMilli(n).UTC(), true
		}
		return time.Unix(n, 0).UTC(), true
	}
	return time.Time{}, false
}

// ==================== 数据可用范围（预设清单裁剪依据） ====================

// registerTimeRangeRoutes 挂 GET /admin/stats/range（admin 组内调用）。
//
// 返回“最早一条统计数据 / 拨测明细的时间”，前端据此决定预设清单能放多远：
// 保留期只有 30 天的部署不该显示“最近 90 天”之外的项，否则用户点进去看到的是空图，
// 会误以为是功能坏了而不是数据被清了。
func registerTimeRangeRoutes(g *gin.RouterGroup) {
	g.GET("/stats/range", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()

		// 分钟粒度的聚合表覆盖全部 apiType，是判断“最早有数据”最稳的依据；
		// probe_results 只收白名单类型，两者取较早的一个。
		// 注意两表时间列类型不同：request_stats.minute 是 Unix 分钟（int64），
		// probe_results.created_at 是 datetime —— 后者必须按 sql.NullTime 扫，
		// 按整数扫会报 "converting driver.Value type string to a int64"。
		var statMin int64
		db.WithContext(ctx).Model(&RequestStat{}).Select("COALESCE(MIN(minute), 0)").Scan(&statMin)
		var probeMin sql.NullTime
		db.WithContext(ctx).Model(&ProbeResult{}).Select("MIN(created_at)").Scan(&probeMin)

		earliest := int64(0)
		if statMin > 0 {
			earliest = statMin * 60
		}
		if probeMin.Valid {
			if pm := probeMin.Time.Unix(); earliest == 0 || pm < earliest {
				earliest = pm
			}
		}

		now := time.Now().UTC()
		// 生效上限 = min(接口上限, 保留期)。保留期 0 = 永久，此时只看接口上限。
		maxDays := maxRangeHours / 24
		if DATA_RETENTION_DAYS > 0 && DATA_RETENTION_DAYS < maxDays {
			maxDays = DATA_RETENTION_DAYS
		}

		c.JSON(http.StatusOK, gin.H{
			"now":       now,
			"earliest":  timeOrZero(earliest),
			"maxDays":   maxDays,
			"retention": DATA_RETENTION_DAYS, // 0 = 永久保留
		})
	})
}

// timeOrZero Unix 秒 → UTC 时间；0 → 零值（前端据此显示“暂无历史数据”）。
func timeOrZero(unixSec int64) time.Time {
	if unixSec <= 0 {
		return time.Time{}
	}
	return time.Unix(unixSec, 0).UTC()
}

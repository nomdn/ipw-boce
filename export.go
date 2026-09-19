package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== CSV 导出 ====================
//
// 三处导出共用同一套写出逻辑（UTF-8 BOM + CRLF，Excel 直接双击打开中文不乱码）：
//   - 拨测明细  GET /admin/probes/export?...      与列表同参数、同权限范围（user 只看自己的）
//   - 节点事件  GET /admin/nodes/:id/events/export?days=30
//   - SLA 曲线  GET /admin/tasks/:id/series/export?hours=24
//
// 一律服务端生成、不落盘；行数上限见 maxExportRows（防止一次拉爆内存与浏览器）。

// maxExportRows 单次导出行数上限
const maxExportRows = 20000

// exportRowLimit 导出专用 limit 解析（列表接口的 clampLimit 上限 1000，对导出太小）
func exportRowLimit(c *gin.Context) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
		return maxExportRows
	}
	if n > maxExportRows {
		return maxExportRows
	}
	return n
}

// writeCSV 输出 CSV 下载。BOM 放在最前面，Excel 才不会把 UTF-8 中文读成乱码。
func writeCSV(c *gin.Context, filename string, header []string, rows [][]string) {
	var buf bytes.Buffer
	buf.WriteString("\ufeff")
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	_ = w.Write(header)
	for _, r := range rows {
		_ = w.Write(r)
	}
	w.Flush()
	c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buf.Bytes())
}

// exportStamp 文件名时间戳（本地时区，便于同一天多次导出区分）
func exportStamp() string { return time.Now().Format("20060102-150405") }

// csvTime 时间列统一成本地时区的可读格式（报表里直接看，不做时区换算）
func csvTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// safeFileToken 文件名里的动态片段（nodeId / 任务名）只保留安全字符。
// 任务名多为中文（如「示例 · TCPing 1.1.1.1:443」），逐字剔除后会粘成一串读不出的字符，
// 所以把「连续被剔除的字符」压成一个 "-"，并保留 "."（文件名合法），最终去掉首尾分隔符。
func safeFileToken(s string) string {
	var b strings.Builder
	gap := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
			gap = false
		default:
			if !gap && b.Len() > 0 {
				b.WriteByte('-')
				gap = true
			}
		}
		if b.Len() >= 48 {
			break
		}
	}
	out := strings.Trim(b.String(), "-_.")
	if out == "" {
		return "x"
	}
	return out
}

// sourceLabelOf 导出报表里的来源列用中文（与界面口径一致）
func sourceLabelOf(s string) string {
	switch s {
	case sourceSched:
		return "定时拨测"
	case sourceBiz:
		return "一键拨测"
	case sourceWS, sourceHTTP:
		return "节点上报"
	case "":
		return ""
	}
	return s
}

// ==================== 拨测明细导出 ====================

// probeListQuery 构造 /admin/probes 的查询条件（权限范围 + 全部筛选参数）。
// 列表与导出共用这一份，避免两边筛选口径漂移。ctx 由调用方负责释放。
// 返回 (查询, 是否正常)；false 表示已经写出错误响应，调用方直接 return。
func probeListQuery(c *gin.Context, ctx context.Context) (*gorm.DB, bool) {
	uid, role, _ := currentUserFromCtx(c)
	q := db.WithContext(ctx).Model(&ProbeResult{})
	// 权限范围：user 只能看"自己任务的定时拨测" + "自己发起的一键拨测"
	if role == RoleUser {
		ownIDs, err := ownTaskIDs(db.WithContext(ctx), uid)
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return nil, false
		}
		if len(ownIDs) > 0 {
			q = q.Where("(source = ? AND task_id IN ?) OR (source = ? AND owner_id = ?)", sourceSched, ownIDs, sourceBiz, uid)
		} else {
			q = q.Where("source = ? AND owner_id = ?", sourceBiz, uid)
		}
	}
	if v := c.Query("node"); v != "" {
		q = q.Where("node_id = ?", v)
	}
	if v := c.Query("type"); v != "" {
		q = q.Where("api_type = ?", v)
	}
	// target：按目标模糊匹配（对 raw 做 LIKE）。通配符转义后再拼，避免用户输入的 % _ 变成任意匹配。
	if v := strings.TrimSpace(c.Query("target")); v != "" {
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
		q = q.Where(`raw LIKE ? ESCAPE '\'`, "%"+esc+"%")
	}
	if v := c.Query("cat"); v != "" {
		switch v {
		case "sched":
			q = q.Where("source = ?", sourceSched)
		case "biz":
			if role != RoleUser {
				q = q.Where("source <> ?", sourceSched)
			} else {
				q = q.Where("source = ? AND owner_id = ?", sourceBiz, uid)
			}
		default:
			apiError(c, http.StatusBadRequest, "invalid cat (sched|biz)")
			return nil, false
		}
	}
	if v := c.Query("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q = q.Where("created_at >= ?", t.UTC())
		}
	}
	return q, true
}

// exportProbesHandler 拨测明细 CSV
func exportProbesHandler(c *gin.Context) {
	ctx, cancel := dbCtx()
	defer cancel()
	q, ok := probeListQuery(c, ctx)
	if !ok {
		return
	}
	var rows []ProbeResult
	if err := q.Order("id desc").Limit(exportRowLimit(c)).Find(&rows).Error; err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([][]string, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		out = append(out, []string{
			csvTime(r.CreatedAt), r.NodeID, r.APIType, r.Raw, r.Query,
			strconv.Itoa(r.Status), strconv.FormatInt(r.LatencyMs, 10),
			r.Error, sourceLabelOf(r.Source), r.RequestID,
		})
	}
	writeCSV(c, "probes-"+exportStamp()+".csv",
		[]string{"时间", "节点", "拨测方案", "目标", "参数", "状态码", "延迟(ms)", "错误", "来源", "请求ID"}, out)
}

// ==================== 节点事件导出 ====================

// exportNodeEventsHandler 某节点的上线/下线事件 CSV（admin only）
func exportNodeEventsHandler(c *gin.Context) {
	nodeID := c.Param("nodeId")
	days := clampFloat(c.Query("days"), 30, 1, 90)
	from := time.Now().UTC().Add(-time.Duration(days * 24 * float64(time.Hour)))
	ctx, cancel := dbCtx()
	defer cancel()
	var events []NodeEvent
	if err := db.WithContext(ctx).
		Where("node_id = ? AND created_at >= ?", nodeID, from).
		Order("id asc").Limit(exportRowLimit(c)).Find(&events).Error; err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([][]string, 0, len(events))
	for _, e := range events {
		ev := "上线"
		if e.Event != "online" {
			ev = "下线"
		}
		out = append(out, []string{csvTime(e.CreatedAt), ev, e.Event, e.Reason})
	}
	writeCSV(c, "node-events-"+safeFileToken(nodeID)+"-"+exportStamp()+".csv",
		[]string{"时间", "事件", "event", "原因"}, out)
}

// ==================== SLA 曲线导出 ====================

// exportTaskSeriesHandler 某任务窗口内的时序曲线 CSV（与 /series 同参数、同归属校验）
func exportTaskSeriesHandler(c *gin.Context) {
	tr := parseTimeRange(c, 24)
	node := strings.TrimSpace(c.Query("node"))
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
	// 窗口由 parseTimeRange 统一解析（?hours= 或 ?start=&end=），见 timerange.go
	q := db.WithContext(ctx).
		Where("task_id = ? AND source = ? AND created_at >= ? AND created_at <= ?",
			taskID, sourceSched, tr.From, tr.To)
	if node != "" {
		q = q.Where("node_id = ?", node)
	}
	var rows []ProbeResult
	if err := q.Order("created_at asc, id asc").Find(&rows).Error; err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	series := rowsToRoundSeries(&t, rows)
	out := make([][]string, 0, len(series))
	for _, s := range series {
		ts := ""
		if v, ok := s["time"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339, v); err == nil {
				ts = csvTime(parsed)
			} else {
				ts = v
			}
		}
		out = append(out, []string{
			ts,
			ci(s["samples"]), ci(s["up"]), ci(s["down"]),
			cf(s["availability"]), cf(s["avgMs"]),
		})
	}
	name := safeFileToken(t.Name)
	// 节点对比视图逐节点导出时文件名带上节点，避免同任务多次导出互相覆盖
	if node != "" {
		name += "-" + safeFileToken(node)
	}
	writeCSV(c, "sla-"+name+"-"+exportStamp()+".csv",
		[]string{"时间", "样本数", "成功", "失败", "可用率(%)", "平均延迟(ms)"}, out)
}

// ci / cf 导出时把 gin.H 里的数值安全转成字符串
func ci(v any) string {
	switch n := v.(type) {
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func cf(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(n, 10)
	case int:
		return strconv.Itoa(n)
	}
	return fmt.Sprint(v)
}

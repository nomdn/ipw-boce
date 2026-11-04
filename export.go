package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== CSV 导出（UTF-8 BOM + CRLF，Excel 直接打开不乱码；上限 20000 行） ====================
//
//   GET /admin/probes/export?...              与列表同参数、同权限范围
//   GET /admin/nodes/:nodeId/events/export    admin
//   GET /admin/tasks/:id/series/export        与 /series 同参数、同归属
//   GET /admin/nodes/uptime/export?days=      admin
//   GET /admin/points/admin/ledger/export     admin
//   GET /admin/shop/admin/orders/export       admin

const maxExportRows = 20000

func exportLimit(c *gin.Context) int {
	n, err := strconv.Atoi(c.Query("limit"))
	if err != nil || n <= 0 || n > maxExportRows {
		return maxExportRows
	}
	return n
}

func writeCSV(c *gin.Context, name string, header []string, rows [][]string) {
	var buf bytes.Buffer
	buf.WriteString("\ufeff")
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	_ = w.Write(header)
	_ = w.WriteAll(rows)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.csv"`, safeFileToken(name), time.Now().Format("20060102-150405")))
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buf.Bytes())
}

func csvTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func safeFileToken(s string) string {
	var b strings.Builder
	gap := false
	for _, r := range s {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			b.WriteRune(r)
			gap = false
		} else if !gap && b.Len() > 0 {
			b.WriteByte('-')
			gap = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	if out := strings.Trim(b.String(), "-_."); out != "" {
		return out
	}
	return "export"
}

func sourceLabel(s string) string {
	switch s {
	case sourceSched:
		return "定时拨测"
	case sourceBiz:
		return "一键拨测"
	case sourceWS, sourceHTTP:
		return "节点上报"
	}
	return s
}

func i64(v int64) string { return strconv.FormatInt(v, 10) }

func registerExportRoutes(admin, restricted *gin.RouterGroup) {
	admin.GET("/probes/export", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		q, ok := probeListQuery(c, db.WithContext(ctx))
		if !ok {
			return
		}
		var rows []ProbeResult
		q.Order("id desc").Limit(exportLimit(c)).Find(&rows)
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{csvTime(r.CreatedAt), r.NodeID, r.APIType, r.Raw, r.Query, strconv.Itoa(r.Status),
				i64(r.LatencyMs), r.Error, sourceLabel(r.Source), r.RequestID})
		}
		writeCSV(c, "probes", []string{"时间", "节点", "拨测方案", "目标", "参数", "状态码", "延迟(ms)", "错误", "来源", "请求ID"}, out)
	})

	admin.GET("/tasks/:id/series/export", func(c *gin.Context) {
		t, ok := loadVisibleTask(c)
		if !ok {
			return
		}
		tr := parseTimeRange(c, 24)
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Where("task_id = ? AND source = ? AND created_at >= ? AND created_at <= ?", t.ID, sourceSched, tr.From, tr.To)
		if n := c.Query("node"); n != "" {
			q = q.Where("node_id = ?", n)
		}
		var rows []ProbeResult
		q.Order("created_at asc").Find(&rows)
		out := [][]string{}
		for _, s := range rowsToRoundSeries(t, rows) {
			ts, _ := time.Parse(time.RFC3339, fmt.Sprint(s["time"]))
			out = append(out, []string{csvTime(ts), fmt.Sprint(s["samples"]), fmt.Sprint(s["up"]), fmt.Sprint(s["down"]),
				fmt.Sprint(s["availability"]), fmt.Sprint(s["avgMs"])})
		}
		writeCSV(c, "sla-"+t.Name, []string{"时间", "样本数", "成功", "失败", "可用率(%)", "平均延迟(ms)"}, out)
	})

	restricted.GET("/nodes/:nodeId/events/export", func(c *gin.Context) {
		id := c.Param("nodeId")
		days := clampFloat(c.Query("days"), 30, 1, 90)
		ctx, cancel := dbCtx()
		defer cancel()
		var ev []NodeEvent
		db.WithContext(ctx).Where("node_id = ? AND created_at >= ?", id, time.Now().UTC().Add(-time.Duration(days*24)*time.Hour)).
			Order("id asc").Limit(exportLimit(c)).Find(&ev)
		out := make([][]string, 0, len(ev))
		for _, e := range ev {
			zh := "上线"
			if e.Event != "online" {
				zh = "下线"
			}
			out = append(out, []string{csvTime(e.CreatedAt), zh, e.Event, e.Reason})
		}
		writeCSV(c, "node-events-"+id, []string{"时间", "事件", "event", "原因"}, out)
	})

	restricted.GET("/nodes/uptime/export", func(c *gin.Context) {
		from, to := clampUptimeRange(clampFloat(c.Query("days"), 7, 1, 90))
		ctx, cancel := dbCtx()
		defer cancel()
		var nodes []Node
		db.WithContext(ctx).Order("node_id asc").Find(&nodes)
		prior, in, _ := loadUptimeEvents(from, to, nil)
		out := [][]string{}
		for i := range nodes {
			iv, ex := uptimeForNode(nodes[i].NodeID, from, to, prior, in)
			st := buildUptimeStats(&nodes[i], from, to, iv, ex)
			out = append(out, []string{st.NodeID, st.Label, strconv.FormatBool(st.CurrentOnline), csvTime(st.From), csvTime(st.To),
				i64(st.OnlineSeconds), i64(st.OfflineSeconds), strconv.Itoa(st.DownCount), fmt.Sprintf("%.2f", st.Availability*100)})
		}
		writeCSV(c, "node-uptime", []string{"节点", "名称", "当前在线", "统计起点", "统计终点", "在线秒", "离线秒", "掉线次数", "可用率(%)"}, out)
	})

	restricted.GET("/points/admin/ledger/export", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&PointLedger{})
		if v := c.Query("userId"); v != "" {
			q = q.Where("user_id = ?", v)
		}
		if v := c.Query("kind"); v != "" {
			q = q.Where("kind = ?", v)
		}
		var rows []PointLedger
		q.Order("id desc").Limit(exportLimit(c)).Find(&rows)
		out := make([][]string, 0, len(rows))
		for _, l := range rows {
			out = append(out, []string{csvTime(l.CreatedAt), fmt.Sprint(l.UserID), l.Kind, i64(l.Delta), i64(l.BalanceAfter), l.RefType, l.RefID, l.Remark, l.Operator})
		}
		writeCSV(c, "points-ledger", []string{"时间", "用户ID", "类型", "变动", "变动后余额", "关联类型", "关联ID", "备注", "操作人"}, out)
	})

	restricted.GET("/shop/admin/orders/export", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&ShopOrder{})
		if v := c.Query("status"); v != "" {
			q = q.Where("status = ?", v)
		}
		var rows []ShopOrder
		q.Order("id desc").Limit(exportLimit(c)).Find(&rows)
		out := make([][]string, 0, len(rows))
		for _, o := range rows {
			del := ""
			if o.DeliveredAt != nil {
				del = csvTime(*o.DeliveredAt)
			}
			out = append(out, []string{o.OrderNo, csvTime(o.CreatedAt), o.Username, o.ProductName, o.Delivery, strconv.Itoa(o.Quantity),
				i64(o.TotalPoints), o.Status, o.UserRemark, del, o.LastError})
		}
		writeCSV(c, "shop-orders", []string{"订单号", "时间", "用户", "商品", "发货方式", "数量", "积分", "状态", "用户备注", "发货时间", "错误"}, out)
	})
}

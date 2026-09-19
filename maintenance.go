package main

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 计划维护窗口（节点告警免打扰） ====================
//
// 用途：割接 / 发布 / 计划重启这类"自己知道要断"的场景，节点必然短暂掉线，此时不该刷告警。
//
// 语义与任务级 QuietHours（见 alert.go inQuietHours）保持一致：**只屏蔽通知，不屏蔽事实**——
// offline/online 事件照写、nodes.online 快照照改，所以节点状态页、事件历史、可用率统计都不受影响，
// 事后仍能看出"这段时间确实断过"。
//
// 配对规则（与 OTA 豁免、掉线宽限窗口同一套思路）：只有"掉线通知被吞掉"的那次故障，
// 它随后的"恢复上线"才一并吞掉（markMaintSuppressed → takeMaintSuppressed）。
// 掉线发生在窗口外、恢复发生在窗口内时，恢复通知照发 —— 否则群里会留下一条没有配对的孤立掉线。
//
// 范围（scope）：global = 全部节点；其余取值 = 该 nodeId。命中判定取并集。
// 两种形态：
//   - 一次性窗口：StartAt ~ EndAt 的绝对时间段（割接、发布）。
//   - 每日重复窗口：只比 StartAt / EndAt 的**时钟时刻**（服务器本地时区），支持跨零点（如 03:00-04:00
//     或 23:30-00:30 的夜间例行重启）。

// maintenanceScopeGlobal 全局窗口的 scope 取值；空串一律按 global 处理
const maintenanceScopeGlobal = "global"

// maintenanceScopesFor 某节点要匹配的 scope 集合（全局 + 该节点自身）
func maintenanceScopesFor(nodeID string) []string {
	id := strings.TrimSpace(nodeID)
	if id == "" {
		return []string{maintenanceScopeGlobal}
	}
	return []string{maintenanceScopeGlobal, id}
}

// maintenanceHit 该节点此刻是否落在某个维护窗口内；命中则返回该窗口（供日志与提示文案）。
// 查询失败按"未命中"处理：宁可多报一次告警，也不要因为库抖动把真故障吞掉。
func maintenanceHit(nodeID string, now time.Time) (bool, *MaintenanceWindow) {
	if db == nil {
		return false, nil
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var rows []MaintenanceWindow
	if err := db.WithContext(ctx).Where("scope IN ?", maintenanceScopesFor(nodeID)).Find(&rows).Error; err != nil {
		log.Printf("[maint] ERROR query windows for %s: %v", nodeID, err)
		return false, nil
	}
	for i := range rows {
		if maintenanceWindowCovers(&rows[i], now) {
			return true, &rows[i]
		}
	}
	return false, nil
}

// maintenanceWindowCovers 单个窗口是否覆盖 now。
// 每日重复只比时钟（跨零点按"或"处理）；一次性窗口按时段（左闭右开）。
func maintenanceWindowCovers(w *MaintenanceWindow, now time.Time) bool {
	if w.RepeatDaily {
		start, end := clockMinutes(w.StartAt.Local()), clockMinutes(w.EndAt.Local())
		if start == end {
			return false // 起止时刻相同 = 空窗口，避免整天命中
		}
		cur := now.Hour()*60 + now.Minute()
		if start < end {
			return cur >= start && cur < end
		}
		return cur >= start || cur < end // 跨零点
	}
	return !now.Before(w.StartAt) && now.Before(w.EndAt)
}

// clockMinutes 取本地时刻的"当日第几分钟"（0~1439）
func clockMinutes(t time.Time) int { return t.Hour()*60 + t.Minute() }

// describe 窗口的可读描述（日志与告警文案用）
func (w *MaintenanceWindow) describe() string {
	scope := w.Scope
	if scope == "" || scope == maintenanceScopeGlobal {
		scope = "全部节点"
	}
	reason := strings.TrimSpace(w.Reason)
	if reason == "" {
		reason = "计划维护"
	}
	if w.RepeatDaily {
		return "每日 " + w.StartAt.Local().Format("15:04") + "-" + w.EndAt.Local().Format("15:04") + "（" + scope + "，" + reason + "）"
	}
	f := func(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }
	return f(w.StartAt) + " ~ " + f(w.EndAt) + "（" + scope + "，" + reason + "）"
}

// ==================== 掉线/上线的维护窗口抑制配对 ====================

var (
	maintSuppressMu sync.Mutex
	maintSuppressed = map[string]bool{}
)

// markMaintSuppressed 记下"这次掉线通知被维护窗口吞掉了"，供随后的恢复上线一并吞掉
func markMaintSuppressed(nodeID string) {
	maintSuppressMu.Lock()
	maintSuppressed[nodeID] = true
	maintSuppressMu.Unlock()
}

// takeMaintSuppressed 取出并清除标记（一次性消费），返回此前是否存在
func takeMaintSuppressed(nodeID string) bool {
	maintSuppressMu.Lock()
	defer maintSuppressMu.Unlock()
	found := maintSuppressed[nodeID]
	delete(maintSuppressed, nodeID)
	return found
}

// ==================== 管理接口 ====================

// parseWindowTime 兼容多种时间写法；不带时区的按服务器本地时区解释，统一存 UTC。
// 前端一次性窗口的 datetime-local 控件产出 "2006-01-02T15:04"（本地时间，无时区后缀）；
// 每日重复窗口的 time 控件只产出 "15:04"（无日期），锚到当天 —— 该形态只比时钟，日期无意义。
func parseWindowTime(raw string) (time.Time, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339, time.RFC3339Nano,
		"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02",
		"2006-01-02 15:04:05", "2006-01-02 15:04",
	} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UTC(), true
		}
	}
	for _, layout := range []string{"15:04:05", "15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			now := time.Now()
			return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.Local).UTC(), true
		}
	}
	return time.Time{}, false
}

// maintenanceView 列表出参：窗口本体 + 运行期算出的状态位
type maintenanceView struct {
	MaintenanceWindow
	Active  bool `json:"active"`  // 此刻是否命中
	Expired bool `json:"expired"` // 一次性窗口且已结束
}

// registerMaintenanceRoutes 维护窗口 CRUD（admin only，挂在 restricted 组下）
func registerMaintenanceRoutes(g *gin.RouterGroup) {
	// 列表：最近创建在前
	g.GET("/maintenance", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var rows []MaintenanceWindow
		if err := db.WithContext(ctx).Order("id desc").Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		now := time.Now()
		out := make([]maintenanceView, 0, len(rows))
		for i := range rows {
			w := &rows[i]
			out = append(out, maintenanceView{
				MaintenanceWindow: *w,
				Active:            maintenanceWindowCovers(w, now),
				Expired:           !w.RepeatDaily && now.After(w.EndAt),
			})
		}
		c.JSON(http.StatusOK, out)
	})

	// 新建：{scope?, startAt, endAt, repeatDaily?, reason?}
	g.POST("/maintenance", func(c *gin.Context) {
		var body struct {
			Scope       string `json:"scope"`
			StartAt     string `json:"startAt"`
			EndAt       string `json:"endAt"`
			RepeatDaily bool   `json:"repeatDaily"`
			Reason      string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "请求体不是合法 JSON："+err.Error())
			return
		}
		start, okStart := parseWindowTime(body.StartAt)
		end, okEnd := parseWindowTime(body.EndAt)
		if !okStart || !okEnd {
			apiError(c, http.StatusBadRequest, "开始/结束时间格式不正确")
			return
		}
		if body.RepeatDaily {
			// 每日重复只关心时钟：允许 end <= start（跨零点），但起止时刻不能相同（否则是空窗口）
			if clockMinutes(start.Local()) == clockMinutes(end.Local()) {
				apiError(c, http.StatusBadRequest, "每日重复窗口的开始与结束时刻不能相同")
				return
			}
		} else if !end.After(start) {
			apiError(c, http.StatusBadRequest, "结束时间必须晚于开始时间")
			return
		}
		scope := strings.TrimSpace(body.Scope)
		if scope == "" {
			scope = maintenanceScopeGlobal
		}
		if len(scope) > 128 {
			scope = scope[:128]
		}
		reason := strings.TrimSpace(body.Reason)
		if len(reason) > 256 {
			reason = reason[:256]
		}
		_, _, username := currentUserFromCtx(c)
		row := MaintenanceWindow{
			Scope: scope, StartAt: start, EndAt: end,
			RepeatDaily: body.RepeatDaily, Reason: reason,
			CreatedBy: username, CreatedAt: time.Now().UTC(),
		}
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Create(&row).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[maint] window #%d created by %s: %s", row.ID, username, row.describe())
		c.JSON(http.StatusOK, row)
	})

	// 删除
	g.DELETE("/maintenance/:id", func(c *gin.Context) {
		id := idParam(c)
		if id == 0 {
			apiError(c, http.StatusBadRequest, "invalid id")
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		res := db.WithContext(ctx).Delete(&MaintenanceWindow{}, id)
		if res.Error != nil {
			apiError(c, http.StatusInternalServerError, res.Error.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": id, "deleted": res.RowsAffected > 0})
	})
}

// cleanupMaintenanceWindows 清理早已结束的一次性窗口（每日重复窗口保留）。
// 由 store.go retentionLoop 每小时调用一次；窗口只在建/删时写库，长期堆积没有意义。
func cleanupMaintenanceWindows() {
	if db == nil {
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -30)
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).
		Where("repeat_daily = ? AND end_at < ?", false, cutoff).
		Delete(&MaintenanceWindow{}).Error; err != nil {
		log.Printf("[maint] ERROR cleanup expired windows: %v", err)
	}
}

package main

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 计划维护窗口（只屏蔽节点上下线通知，不改事实） ====================
//
// scope：global = 全部节点，其余 = 该 nodeId。
// 一次性窗口按绝对时段；每日重复窗口只比时钟（服务器本地时区，支持跨零点）。
// 被吞掉的掉线，对应的恢复也一并吞掉；窗口外掉线、窗口内恢复则照常通知恢复。
// 在维护窗口内的离线时长不计入「捐赠节点在线奖励」的扣减（见 donate.go）。

type MaintenanceWindow struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Scope       string    `gorm:"size:128;index" json:"scope"`
	StartAt     time.Time `json:"startAt"`
	EndAt       time.Time `json:"endAt"`
	RepeatDaily bool      `json:"repeatDaily"`
	Reason      string    `gorm:"size:256" json:"reason"`
	CreatedBy   string    `gorm:"size:64" json:"createdBy,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

func clockMinutes(t time.Time) int { return t.Hour()*60 + t.Minute() }

func (w *MaintenanceWindow) covers(now time.Time) bool {
	if w.RepeatDaily {
		s, e := clockMinutes(w.StartAt.Local()), clockMinutes(w.EndAt.Local())
		if s == e {
			return false
		}
		cur := clockMinutes(now.Local())
		if s < e {
			return cur >= s && cur < e
		}
		return cur >= s || cur < e
	}
	return !now.Before(w.StartAt) && now.Before(w.EndAt)
}

func maintenanceHit(nodeID string, now time.Time) bool {
	ctx, cancel := dbCtx()
	defer cancel()
	var rows []MaintenanceWindow
	if db.WithContext(ctx).Where("scope IN ?", []string{"global", nodeID}).Find(&rows).Error != nil {
		return false
	}
	for i := range rows {
		if rows[i].covers(now) {
			return true
		}
	}
	return false
}

var maintSuppressed sync.Map

func parseWindowTime(raw string) (time.Time, bool) {
	s := strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UTC(), true
		}
	}
	for _, layout := range []string{"15:04:05", "15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			n := time.Now()
			return time.Date(n.Year(), n.Month(), n.Day(), t.Hour(), t.Minute(), 0, 0, time.Local).UTC(), true
		}
	}
	return time.Time{}, false
}

func registerMaintenanceRoutes(g *gin.RouterGroup) {
	g.GET("/maintenance", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var rows []MaintenanceWindow
		db.WithContext(ctx).Order("id desc").Find(&rows)
		now := time.Now()
		out := make([]gin.H, 0, len(rows))
		for i := range rows {
			w := &rows[i]
			out = append(out, gin.H{"id": w.ID, "scope": w.Scope, "startAt": w.StartAt, "endAt": w.EndAt,
				"repeatDaily": w.RepeatDaily, "reason": w.Reason, "createdBy": w.CreatedBy, "createdAt": w.CreatedAt,
				"active": w.covers(now), "expired": !w.RepeatDaily && now.After(w.EndAt)})
		}
		c.JSON(http.StatusOK, out)
	})
	g.POST("/maintenance", func(c *gin.Context) {
		var b struct {
			Scope       string `json:"scope"`
			StartAt     string `json:"startAt"`
			EndAt       string `json:"endAt"`
			RepeatDaily bool   `json:"repeatDaily"`
			Reason      string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&b); err != nil {
			apiError(c, http.StatusBadRequest, "请求体不是合法 JSON："+err.Error())
			return
		}
		start, ok1 := parseWindowTime(b.StartAt)
		end, ok2 := parseWindowTime(b.EndAt)
		if !ok1 || !ok2 {
			apiError(c, http.StatusBadRequest, "开始/结束时间格式不正确")
			return
		}
		if b.RepeatDaily && clockMinutes(start.Local()) == clockMinutes(end.Local()) {
			apiError(c, http.StatusBadRequest, "每日重复窗口的开始与结束时刻不能相同")
			return
		}
		if !b.RepeatDaily && !end.After(start) {
			apiError(c, http.StatusBadRequest, "结束时间必须晚于开始时间")
			return
		}
		scope := strings.TrimSpace(b.Scope)
		if scope == "" {
			scope = "global"
		}
		row := MaintenanceWindow{Scope: truncateStr(scope, 128), StartAt: start, EndAt: end, RepeatDaily: b.RepeatDaily,
			Reason: truncateStr(strings.TrimSpace(b.Reason), 256), CreatedBy: currentUsername(c)}
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Create(&row).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, row)
	})
	g.DELETE("/maintenance/:id", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		res := db.WithContext(ctx).Delete(&MaintenanceWindow{}, idParam(c))
		c.JSON(http.StatusOK, gin.H{"id": idParam(c), "deleted": res.RowsAffected > 0})
	})
}

func cleanupMaintenanceWindows() {
	ctx, cancel := dbCtx()
	defer cancel()
	db.WithContext(ctx).Where("repeat_daily = ? AND end_at < ?", false, time.Now().UTC().AddDate(0, 0, -30)).Delete(&MaintenanceWindow{})
}

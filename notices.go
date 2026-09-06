package main

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 站内信（AppNotice） ====================
//
// 任务掉线告警在"所有者无邮箱"（或邮件路径不可用）时，落到站内信，所有者登录控制台后
// 在顶栏铃铛查看（见 web ConsoleLayout 通知下拉）。内容为纯文本告警，可标记已读。
//
// 写入口：alert.go deliverDownAlert → createNotice；
// 读入口：/admin/notices（列表/未读数/标记已读），见 registerNoticeRoutes。

// AppNotice 一条站内通知
type AppNotice struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"userId"`   // 收件用户 users.id
	Kind      string    `gorm:"size:24" json:"kind"`   // sla_down（SLA 任务掉线）| node_down（服务节点掉线）
	TaskID    uint      `json:"taskId,omitempty"`      // 关联任务 id（kind=sla_down 时）；节点告警为 0
	Title     string    `gorm:"size:256" json:"title"` // 主题（与邮件 subject 一致）
	Body      string    `gorm:"type:text" json:"body"` // 正文
	Read      bool      `gorm:"index" json:"read"`     // 是否已读
	CreatedAt time.Time `json:"createdAt"`
}

// createNotice 为某用户落一条站内信。kind 用 NoticeKind* 常量（见 alert.go/nodeHealth.go）。
func createNotice(userID uint, kind string, taskID uint, title, body string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	n := &AppNotice{UserID: userID, Kind: kind, TaskID: taskID, Title: title, Body: body, CreatedAt: time.Now().UTC()}
	return db.WithContext(ctx).Create(n).Error
}

// registerNoticeRoutes /admin/notices（JWT 用户本人可查自己的通知；静态 token 无可视用户返回空）
func registerNoticeRoutes(admin *gin.RouterGroup) {
	g := admin.Group("/notices")

	// 未读数（铃铛角标轮询，轻量）
	g.GET("/unread", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		var unread int64
		if uid != 0 {
			ctx, cancel := dbCtx()
			defer cancel()
			db.WithContext(ctx).Model(&AppNotice{}).Where("user_id = ? AND read = ?", uid, false).Count(&unread)
		}
		c.JSON(http.StatusOK, gin.H{"unread": unread})
	})

	// 列表：仅当前登录用户自己的通知；?unread=1 只看未读
	g.GET("", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			c.JSON(http.StatusOK, gin.H{"list": []any{}, "unread": 0})
			return
		}
		limit := clampLimit(c.Query("limit"), 50)
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&AppNotice{}).Where("user_id = ?", uid)
		if c.Query("unread") == "1" {
			q = q.Where("read = ?", false)
		}
		var rows []AppNotice
		if err := q.Order("id desc").Limit(limit).Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		var unread int64
		if err := db.WithContext(ctx).Model(&AppNotice{}).Where("user_id = ? AND read = ?", uid, false).Count(&unread).Error; err != nil {
			unread = 0
		}
		c.JSON(http.StatusOK, gin.H{"list": rows, "unread": unread})
	})

	// 标记已读：body {ids?: [..]}，缺省全已读；返回最新未读数
	g.POST("/read", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			apiError(c, http.StatusForbidden, "no user context")
			return
		}
		var body struct {
			IDs []uint `json:"ids"`
		}
		_ = c.ShouldBindJSON(&body)
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&AppNotice{}).Where("user_id = ? AND read = ?", uid, false)
		if len(body.IDs) > 0 {
			q = q.Where("id IN ?", body.IDs)
		}
		if err := q.Update("read", true).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		var unread int64
		db.WithContext(ctx).Model(&AppNotice{}).Where("user_id = ? AND read = ?", uid, false).Count(&unread)
		c.JSON(http.StatusOK, gin.H{"unread": unread})
	})

	// 清理：删除指定或全部本人已读通知
	g.DELETE("", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			apiError(c, http.StatusForbidden, "no user context")
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Where("user_id = ?", uid).Delete(&AppNotice{}).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[notices] user#%d cleared all notices", uid)
		c.JSON(http.StatusOK, gin.H{"cleared": true})
	})
}

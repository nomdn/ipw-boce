package main

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 个人资料 /admin/me（本人，任意登录角色） ====================

// meUser 取当前账号；静态 token(uid=0) 无账号时写错误并返回 nil
func meUser(c *gin.Context) *User {
	uid, _, _ := currentUserFromCtx(c)
	if uid == 0 {
		apiError(c, http.StatusBadRequest, "static token has no account")
		return nil
	}
	u, err := userByID(uid)
	if err != nil || u == nil {
		apiError(c, http.StatusNotFound, "account not found")
		return nil
	}
	return u
}

func registerProfileRoutes(admin *gin.RouterGroup) {
	p := admin.Group("/me")

	p.GET("", func(c *gin.Context) {
		uid, _, name := currentUserFromCtx(c)
		if uid == 0 {
			c.JSON(http.StatusOK, gin.H{"id": 0, "username": name, "role": RoleAdmin, "staticToken": true})
			return
		}
		u := meUser(c)
		if u == nil {
			return
		}
		h := u.userPublic()
		h["points"] = balanceOf(u.ID)
		c.JSON(http.StatusOK, h)
	})

	p.PATCH("", func(c *gin.Context) {
		var body struct {
			Email       *string `json:"email"`
			WebhookURL  *string `json:"webhookUrl"`
			WebhookType *string `json:"webhookType"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body")
			return
		}
		if body.Email == nil && body.WebhookURL == nil && body.WebhookType == nil {
			apiError(c, http.StatusBadRequest, "body must include at least one of email/webhookUrl/webhookType")
			return
		}
		u := meUser(c)
		if u == nil {
			return
		}
		updates := map[string]any{}
		reset := false
		if body.Email != nil {
			email := strings.TrimSpace(*body.Email)
			if email != "" && !validEmail(email) {
				apiError(c, http.StatusBadRequest, "邮箱格式不正确")
				return
			}
			updates["email"] = email
			reset = email != "" && email != strings.TrimSpace(u.Email)
			if reset {
				updates["email_verified"] = false
			}
		}
		if body.WebhookURL != nil {
			wh := strings.TrimSpace(*body.WebhookURL)
			if wh != "" && !strings.HasPrefix(wh, "http://") && !strings.HasPrefix(wh, "https://") {
				apiError(c, http.StatusBadRequest, "webhookUrl 必须是 http(s) 地址")
				return
			}
			updates["webhook_url"] = wh
		}
		if body.WebhookType != nil {
			wt := strings.ToLower(strings.TrimSpace(*body.WebhookType))
			switch wt {
			case "", "generic", "wecom", "feishu":
			default:
				apiError(c, http.StatusBadRequest, "webhookType 只能是 generic / wecom / feishu")
				return
			}
			updates["webhook_type"] = wt
		}
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Model(&User{}).Where("id = ?", u.ID).Updates(updates).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		resp := gin.H{"updated": true}
		if v, ok := updates["email"]; ok {
			resp["email"] = v
			resp["emailVerified"] = !reset && u.EmailVerified
		}
		c.JSON(http.StatusOK, resp)
	})

	p.POST("/webhook/test", func(c *gin.Context) {
		u := meUser(c)
		if u == nil {
			return
		}
		if strings.TrimSpace(u.WebhookURL) == "" {
			apiError(c, http.StatusBadRequest, "请先保存 webhookUrl")
			return
		}
		if err := webhookDeliver(u, "测试通知：1IPW-BOCE webhook 通道已打通", "webhook_test", 0, ""); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	p.GET("/usage", func(c *gin.Context) {
		uid, role, _ := currentUserFromCtx(c)
		usage, err := usageForUser(role, uid, clampFloat(c.Query("hours"), 24, 1, 24*90))
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, usage)
	})

	p.POST("/verify/send", func(c *gin.Context) {
		u := meUser(c)
		if u == nil {
			return
		}
		email := strings.ToLower(strings.TrimSpace(u.Email))
		if !validEmail(email) {
			apiError(c, http.StatusBadRequest, "请先在个人资料里设置有效邮箱")
			return
		}
		if !regSmtpReady() {
			apiError(c, http.StatusBadRequest, "验证邮件暂不可用：SMTP 邮件未配置，请联系管理员")
			return
		}
		if err := grantCode(email, "verify"); err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"sent": true, "email": email})
	})

	p.POST("/verify", func(c *gin.Context) {
		u := meUser(c)
		if u == nil {
			return
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body")
			return
		}
		if err := verifyEmailCode(u.Email, body.Code); err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		db.WithContext(ctx).Model(&User{}).Where("id = ?", u.ID).Update("email_verified", true)
		c.JSON(http.StatusOK, gin.H{"emailVerified": true})
	})

	// 个人 API Token：明文仅本次返回；重复生成即重置
	p.POST("/token", func(c *gin.Context) {
		u := meUser(c)
		if u == nil {
			return
		}
		raw := "ipt_" + randHex(20)
		hash, err := hashPassword(raw)
		if err != nil {
			apiError(c, http.StatusInternalServerError, "hash: "+err.Error())
			return
		}
		now := time.Now().UTC()
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Model(&User{}).Where("id = ?", u.ID).Updates(map[string]any{
			"api_token_hash": hash, "api_token_hint": raw[len(raw)-4:], "api_token_created_at": now,
		}).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[users] #%d issued personal API token", u.ID)
		c.JSON(http.StatusOK, gin.H{"token": raw, "hint": raw[len(raw)-4:], "createdAt": now})
	})

	p.DELETE("/token", func(c *gin.Context) {
		u := meUser(c)
		if u == nil {
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		db.WithContext(ctx).Model(&User{}).Where("id = ?", u.ID).
			Updates(map[string]any{"api_token_hash": "", "api_token_hint": ""})
		c.JSON(http.StatusOK, gin.H{"revoked": true})
	})

	p.PATCH("/password", func(c *gin.Context) {
		var body struct {
			OldPassword string `json:"oldPassword"`
			NewPassword string `json:"newPassword"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body")
			return
		}
		if len(body.NewPassword) < 6 {
			apiError(c, http.StatusBadRequest, "new password must be at least 6 chars")
			return
		}
		u := meUser(c)
		if u == nil {
			return
		}
		if !verifyPassword(u.PasswordHash, body.OldPassword) {
			apiError(c, http.StatusUnauthorized, "old password incorrect")
			return
		}
		hash, err := hashPassword(body.NewPassword)
		if err != nil {
			apiError(c, http.StatusInternalServerError, "hash: "+err.Error())
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		db.WithContext(ctx).Model(&User{}).Where("id = ?", u.ID).Update("password_hash", hash)
		c.JSON(http.StatusOK, gin.H{"ok": true, "passwordChanged": true})
	})
}

// registerNoticeRoutes /admin/notices（本人站内信）
func registerNoticeRoutes(admin *gin.RouterGroup) {
	g := admin.Group("/notices")
	unreadOf := func(uid uint) int64 {
		var n int64
		ctx, cancel := dbCtx()
		defer cancel()
		db.WithContext(ctx).Model(&AppNotice{}).Where("user_id = ? AND is_read = ?", uid, false).Count(&n)
		return n
	}
	g.GET("/unread", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		var n int64
		if uid != 0 {
			n = unreadOf(uid)
		}
		c.JSON(http.StatusOK, gin.H{"unread": n})
	})
	g.GET("", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			c.JSON(http.StatusOK, gin.H{"list": []any{}, "unread": 0})
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Where("user_id = ?", uid)
		if c.Query("unread") == "1" {
			q = q.Where("is_read = ?", false)
		}
		var rows []AppNotice
		if err := q.Order("id desc").Limit(clampLimit(c.Query("limit"), 50)).Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"list": rows, "unread": unreadOf(uid)})
	})
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
		q := db.WithContext(ctx).Model(&AppNotice{}).Where("user_id = ? AND is_read = ?", uid, false)
		if len(body.IDs) > 0 {
			q = q.Where("id IN ?", body.IDs)
		}
		q.Update("is_read", true)
		c.JSON(http.StatusOK, gin.H{"unread": unreadOf(uid)})
	})
	g.DELETE("", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			apiError(c, http.StatusForbidden, "no user context")
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		db.WithContext(ctx).Where("user_id = ?", uid).Delete(&AppNotice{})
		c.JSON(http.StatusOK, gin.H{"cleared": true})
	})
}

package main

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ==================== 个人资料（本人，任意登录角色） ====================
//
// 每个登录用户管理自己的邮箱与口令。与 users.go 的"管理员代管"不同：
// 这里只能操作自己（uid 取自 JWT），admin 也适用；静态 token(uid=0) 无账号可改。

// registerProfileRoutes 挂到 /admin 组下（已过 adminAuthMiddleware，登录即可）。
func registerProfileRoutes(admin *gin.RouterGroup) {
	p := admin.Group("/me")

	// 本人资料（uid=0 的静态 token 无账号，返回该标记供前端提示）
	p.GET("", func(c *gin.Context) {
		uid, _, name := currentUserFromCtx(c)
		if uid == 0 {
			c.JSON(http.StatusOK, gin.H{"id": 0, "username": name, "role": RoleAdmin, "staticToken": true})
			return
		}
		u, err := userByID(uid)
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		if u == nil {
			apiError(c, http.StatusNotFound, "account not found")
			return
		}
		c.JSON(http.StatusOK, u.userPublic())
	})

	// 改本人邮箱（body: {email}）
	p.PATCH("", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			apiError(c, http.StatusBadRequest, "static token has no account; log in with an account to edit profile")
			return
		}
		var body struct {
			Email *string `json:"email"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Email == nil {
			apiError(c, http.StatusBadRequest, "body must include email")
			return
		}
		email := strings.TrimSpace(*body.Email)
		ctx, cancel := dbCtx()
		defer cancel()
		// 邮箱变更后需重新验证（新地址未验证），否则限制 SLA
		updates := map[string]any{"email": email, "email_verified": false}
		if err := db.WithContext(ctx).Model(&User{}).Where("id = ?", uid).Updates(updates).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"email": email, "updated": true, "emailVerified": false})
	})

	// 发邮箱验证码（登录态）：向本人邮箱发码，用于补验证（admin 直建未验证的账号 / 换邮箱后）。
	p.POST("/verify/send", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			apiError(c, http.StatusBadRequest, "static token has no account")
			return
		}
		u, err := userByID(uid)
		if err != nil || u == nil {
			apiError(c, http.StatusNotFound, "account not found")
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
		if err := grantCode(email, u.Username, "verify"); err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"sent": true, "email": email})
	})

	// 提交邮箱验证码（登录态）：body {code}，成功置 email_verified=true
	p.POST("/verify", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			apiError(c, http.StatusBadRequest, "static token has no account")
			return
		}
		u, err := userByID(uid)
		if err != nil || u == nil {
			apiError(c, http.StatusNotFound, "account not found")
			return
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body")
			return
		}
		email := strings.ToLower(strings.TrimSpace(u.Email))
		if !validEmail(email) {
			apiError(c, http.StatusBadRequest, "请先在个人资料里设置有效邮箱")
			return
		}
		if err := verifyEmailCode(email, body.Code); err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Model(&User{}).Where("id = ?", uid).
			Update("email_verified", true).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[users] #%d verified email %s", uid, email)
		c.JSON(http.StatusOK, gin.H{"emailVerified": true})
	})

	// 改本人口令（body: {oldPassword, newPassword}；需校验旧口令）
	p.PATCH("/password", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			apiError(c, http.StatusBadRequest, "static token has no account; log in with an account to change password")
			return
		}
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
		u, err := userByID(uid)
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		if u == nil {
			apiError(c, http.StatusNotFound, "account not found")
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
		if err := db.WithContext(ctx).Model(&User{}).Where("id = ?", uid).
			Update("password_hash", hash).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "passwordChanged": true})
	})
}

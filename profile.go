package main

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 个人资料（本人，任意登录角色） ====================
//
// 每个登录用户管理自己的邮箱与口令。与 users.go 的"管理员代管"不同：
// 这里只能操作自己（uid 取自 JWT），admin 也适用；静态 token(uid=0) 无账号可改。

// usageForUser 用量聚合。可见性：admin（含静态 token，uid=0）= 全站口径——全部任务的
// sched 样本 + 全部 biz 拨测；普通用户 = 自己任务的 sched 样本 + 自己发起的 biz 拨测。
// /admin/me/usage 与 /api/v1/usage 共用（见 rest.go）。
//
// up/down 判定与 SLA **同口径**（evalSample：解析 body 按任务判定配置判，status=200 但
// body 不可达的样本计入 down）；无法判定的样本计 invalid，不进成败。无归属任务的 sched
// 样本按其任务配置判；biz 样本用该类型的默认判定（expectStatus=2xx / 任一栈 / 任一协议）。
func usageForUser(role string, uid uint, hours float64) (gin.H, error) {
	since := time.Now().UTC().Add(-time.Duration(hours * float64(time.Hour)))
	ctx, cancel := dbCtx()
	defer cancel()

	isAdmin := role == RoleAdmin
	var ownIDs []uint
	cfgByID := map[uint]*ProbeTask{}
	{
		var tasks []ProbeTask
		tq := db.WithContext(ctx).Model(&ProbeTask{})
		if !isAdmin {
			tq = tq.Where("owner_id = ?", uid)
		}
		if err := tq.Find(&tasks).Error; err != nil {
			return nil, err
		}
		for i := range tasks {
			ownIDs = append(ownIDs, tasks[i].ID)
			cfgByID[tasks[i].ID] = &tasks[i]
		}
	}
	q := db.WithContext(ctx).Model(&ProbeResult{}).Where("created_at >= ?", since)
	if isAdmin {
		q = q.Where("source IN ?", []string{sourceSched, sourceBiz})
	} else if len(ownIDs) > 0 {
		q = q.Where("(source = ? AND task_id IN ?) OR (source = ? AND owner_id = ?)", sourceSched, ownIDs, sourceBiz, uid)
	} else {
		q = q.Where("source = ? AND owner_id = ?", sourceBiz, uid)
	}
	var rows []ProbeResult
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}

	type agg struct {
		total, up, down, invalid, latSum, latCnt int64
	}
	byType := map[string]*agg{}
	tot := &agg{}
	defaultCfg := map[string]*ProbeTask{}
	cfgFor := func(r *ProbeResult) *ProbeTask {
		if c := cfgByID[r.TaskID]; c != nil {
			return c
		}
		if c := defaultCfg[r.APIType]; c != nil {
			return c
		}
		c := &ProbeTask{APIType: r.APIType}
		defaultCfg[r.APIType] = c
		return c
	}
	for i := range rows {
		r := &rows[i]
		a := byType[r.APIType]
		if a == nil {
			a = &agg{}
			byType[r.APIType] = a
		}
		tot.total++
		a.total++
		ev := evalSample(cfgFor(r), r.Status, r.Body, r.LatencyMs)
		if ev.invalid {
			tot.invalid++
			a.invalid++
		} else if ev.up {
			tot.up++
			a.up++
		} else {
			tot.down++
			a.down++
		}
		if r.LatencyMs > 0 {
			tot.latSum += r.LatencyMs
			tot.latCnt++
			a.latSum += r.LatencyMs
			a.latCnt++
		}
	}
	byTypeArr := make([]gin.H, 0, len(byType))
	for k, a := range byType {
		avg := int64(0)
		if a.latCnt > 0 {
			avg = a.latSum / a.latCnt
		}
		byTypeArr = append(byTypeArr, gin.H{"apiType": k, "total": a.total, "up": a.up, "down": a.down, "invalid": a.invalid, "avgMs": avg})
	}
	sort.Slice(byTypeArr, func(i, j int) bool { return byTypeArr[i]["total"].(int64) > byTypeArr[j]["total"].(int64) })
	return gin.H{"hours": hours, "total": tot.total, "up": tot.up, "down": tot.down, "invalid": tot.invalid, "byType": byTypeArr}, nil
}

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

	// 改本人资料（body: {email?, webhookUrl?, webhookType?}，字段均可选，只更新传了的）。
	// 邮箱变更后需重新验证（新地址未验证），否则限制 SLA；webhook 见 alert.go pushUserWebhook。
	p.PATCH("", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			apiError(c, http.StatusBadRequest, "static token has no account; log in with an account to edit profile")
			return
		}
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
		u, err := userByID(uid)
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		if u == nil {
			apiError(c, http.StatusNotFound, "account not found")
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		updates := map[string]any{}
		emailVerifiedReset := false
		if body.Email != nil {
			email := strings.TrimSpace(*body.Email)
			if email != "" && !validEmail(email) {
				apiError(c, http.StatusBadRequest, "邮箱格式不正确")
				return
			}
			updates["email"] = email
			// 仅邮箱真的变化时才重置验证态（原值提交不折腾；清空邮箱不标记未验证）
			emailVerifiedReset = email != "" && email != strings.TrimSpace(u.Email)
			if emailVerifiedReset {
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
		if err := db.WithContext(ctx).Model(&User{}).Where("id = ?", uid).Updates(updates).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		resp := gin.H{"updated": true}
		if v, ok := updates["email"]; ok {
			resp["email"] = v
			resp["emailVerified"] = !emailVerifiedReset && u.EmailVerified
		}
		c.JSON(http.StatusOK, resp)
	})

	// 测试本人 Webhook（登录态）：向自配地址推一条测试消息，返回投递结果
	p.POST("/webhook/test", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			apiError(c, http.StatusBadRequest, "static token has no account")
			return
		}
		u, err := userByID(uid)
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		if u == nil || strings.TrimSpace(u.WebhookURL) == "" {
			apiError(c, http.StatusBadRequest, "请先保存 webhookUrl")
			return
		}
		// pushUserWebhook 失败只记日志；测试端点需要把结果回给用户，这里复用报文构造手动发一次
		if err := webhookDeliver(u, "测试通知：IPW-BOCE webhook 通道已打通", "webhook_test", 0, ""); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// 我的用量（登录态）：admin（含静态 token）= 全站口径；普通用户 = 自己任务 + 自己的 biz 拨测。
	// 构建逻辑与 /api/v1/usage 共用（见 usageForUser）。
	p.GET("/usage", func(c *gin.Context) {
		uid, role, _ := currentUserFromCtx(c)
		hours := clampFloat(c.Query("hours"), 24, 1, 24*90)
		usage, err := usageForUser(role, uid, hours)
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, usage)
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

	// 生成/重发个人 API Token（ipt_ 前缀）：明文仅本次响应返回一次，库里只存 bcrypt 哈希；
	// 重复生成即重置（旧 token 立即失效）。Token 等价本人登录身份（权限随角色），见 auth.go。
	p.POST("/token", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			apiError(c, http.StatusBadRequest, "static token has no account")
			return
		}
		b := make([]byte, 20)
		if _, err := rand.Read(b); err != nil {
			apiError(c, http.StatusInternalServerError, "rand: "+err.Error())
			return
		}
		raw := "ipt_" + hex.EncodeToString(b)
		hash, err := hashPassword(raw)
		if err != nil {
			apiError(c, http.StatusInternalServerError, "hash: "+err.Error())
			return
		}
		now := time.Now().UTC()
		ctx, cancel := dbCtx()
		defer cancel()
		updates := map[string]any{
			"api_token_hash":       hash,
			"api_token_hint":       raw[len(raw)-4:],
			"api_token_created_at": now,
		}
		if err := db.WithContext(ctx).Model(&User{}).Where("id = ?", uid).Updates(updates).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[users] #%d issued personal API token", uid)
		c.JSON(http.StatusOK, gin.H{"token": raw, "hint": raw[len(raw)-4:], "createdAt": now})
	})

	// 吊销个人 API Token（清哈希即可；旧 token 立即失效）
	p.DELETE("/token", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			apiError(c, http.StatusBadRequest, "static token has no account")
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Model(&User{}).Where("id = ?", uid).
			Updates(map[string]any{"api_token_hash": "", "api_token_hint": ""}).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[users] #%d revoked personal API token", uid)
		c.JSON(http.StatusOK, gin.H{"revoked": true})
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

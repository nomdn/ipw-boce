package main

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 公开自助注册（邮箱验证码） ====================
//
// 注册流程（任何人可自助注册为 role=user，要求先验证邮箱）：
//   1) POST /admin/register/send-code {email, username?}  → 校验邮箱/用户名未被占用、SMTP 可用，
//      生成 6 位验证码并发信（有效期 5 分钟、同邮箱 60s 冷却）。SMTP 未配置 → 400，直接拒绝注册。
//   2) POST /admin/register {username, password, email, code} → 校验码正确 → 建账号
//      role=user、email_verified=true（注册即已验证）。
//
// 另为"admin 直建但未验证邮箱"的存量账号提供登录后的补验证：
//   - POST /admin/me/verify/send（登录态，向自己邮箱发码）
//   - POST /admin/me/verify      {code}（登录态，校验后置 email_verified=true）
// 未验证(Email 非空且 email_verified=false)的账号可登录但 SLA 受限（建/改 SLA 任务被拒，见 probe 任务守卫）。
//
// 安全：验证码纯内存存储（重启即失效，可重发）、单码覆盖、60s 冷却、5 次尝试后作废。

// verifyCode 内存中的一个邮箱验证码
type verifyCode struct {
	Code     string    // 6 位数字
	Username string    // send-code 时若带用户名则记下（注册预检）
	Exp      time.Time // 过期
	Attempt  int       // 已尝试次数（>5 作废）
	LastSent time.Time // 上次发送时刻（冷却用）
}

var (
	vcodeMu    sync.Mutex
	vcodeStore = map[string]*verifyCode{} // key = 小写邮箱
)

const (
	verifyTTL     = 5 * time.Minute
	verifyResend  = 60 * time.Second // 同邮箱重发冷却
	verifyMaxTry  = 5
	verifyCodeLen = 6
)

var emailRe = regexp.MustCompile(`^[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}$`)

// validEmail 简单邮箱格式校验
func validEmail(s string) bool { return emailRe.MatchString(s) }

// regSmtpReady 注册/验证邮件路径是否可用：仅需 smtp host+user 齐备（from 缺省回退 user）。
// 与 smtpReady()（还要求 alert.enabled）不同——注册不依赖"掉线告警"开关。
func regSmtpReady() bool {
	c := SMTP_CONF
	return strings.TrimSpace(c.Host) != "" && strings.TrimSpace(c.User) != ""
}

// newVerifyCode 生成 verifyCodeLen 位随机数字码
func newVerifyCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000)) // 0..999999
	return fmt.Sprintf("%06d", n.Int64())
}

// sendVerifyEmail 发一封验证码邮件到 to，正文含 code；失败返回 error
func sendVerifyEmail(to, purpose, code string) error {
	subject := "[IPW-BOCE] 邮箱验证码"
	var b strings.Builder
	b.WriteString("你好，\n\n")
	if purpose == "register" {
		b.WriteString("你在 IPW-BOCE 控制台发起账号注册，邮箱验证码如下：\n")
	} else {
		b.WriteString("你在 IPW-BOCE 控制台请求验证邮箱，验证码如下：\n")
	}
	b.WriteString("\n")
	b.WriteString("    " + code + "\n\n")
	b.WriteString(fmt.Sprintf("验证码 %d 分钟内有效，请勿泄露给他人。如非本人操作请忽略本邮件。\n", int(verifyTTL.Minutes())))
	return mailSender([]string{to}, subject, b.String()) // 走 mailSender 以便测试替换
}

// grantCode 生成/覆盖某邮箱的验证码并发信；返回 error（含冷却/发信失败）
func grantCode(email, username, purpose string) error {
	vcodeMu.Lock()
	now := time.Now()
	old := vcodeStore[email]
	if old != nil && now.Sub(old.LastSent) < verifyResend {
		left := int((verifyResend - now.Sub(old.LastSent)).Seconds())
		vcodeMu.Unlock()
		return fmt.Errorf("发送过于频繁，请 %d 秒后再试", left)
	}
	code := newVerifyCode()
	vcodeStore[email] = &verifyCode{
		Code: code, Username: username, Exp: now.Add(verifyTTL),
		LastSent: now,
	}
	vcodeMu.Unlock()

	if err := sendVerifyEmail(email, purpose, code); err != nil {
		// 发信失败：作废刚生成的码，便于用户重试
		vcodeMu.Lock()
		if c, ok := vcodeStore[email]; ok && c.Code == code {
			delete(vcodeStore, email)
		}
		vcodeMu.Unlock()
		return fmt.Errorf("邮件发送失败: %w", err)
	}
	return nil
}

// verifyEmailCode 校验邮箱验证码；成功返回 nil（并作废该码）。错误文案面向用户。
func verifyEmailCode(email, code string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	code = strings.TrimSpace(code)
	if code == "" {
		return fmt.Errorf("请输入验证码")
	}
	vcodeMu.Lock()
	defer vcodeMu.Unlock()
	v := vcodeStore[email]
	if v == nil {
		return fmt.Errorf("验证码不存在或已过期，请重新获取")
	}
	if time.Now().After(v.Exp) {
		delete(vcodeStore, email)
		return fmt.Errorf("验证码已过期，请重新获取")
	}
	v.Attempt++
	if v.Attempt > verifyMaxTry {
		delete(vcodeStore, email)
		return fmt.Errorf("尝试次数过多，验证码已作废，请重新获取")
	}
	if v.Code != code {
		return fmt.Errorf("验证码不正确（还可尝试 %d 次）", verifyMaxTry-v.Attempt)
	}
	delete(vcodeStore, email) // 一次性，用完即焚
	return nil
}

// userEmailOrNameTaken 判断 username / email 是否已被占用。返回 (taken bool, what string)
func userEmailOrNameTaken(username, email string) (bool, string) {
	if db == nil {
		return false, ""
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var u User
	if username != "" {
		if err := db.WithContext(ctx).Where("username = ?", username).Limit(1).Find(&u).Error; err == nil && u.ID != 0 {
			return true, "用户名已被占用"
		}
		u = User{}
	}
	if email != "" {
		if err := db.WithContext(ctx).Where("email = ?", email).Limit(1).Find(&u).Error; err == nil && u.ID != 0 {
			return true, "该邮箱已被注册"
		}
	}
	return false, ""
}

// registerPublicRoutes 公开路由：注册发码 / 注册提交（挂在 router 上，免鉴权）。
// 依赖 JWT 登录启用（否则注册出的账号无法登录）与 db 就绪。
func registerPublicRoutes(router *gin.Engine) {
	// 发验证码：body {email, username?}
	router.POST("/admin/register/send-code", func(c *gin.Context) {
		var body struct {
			Email    string `json:"email"`
			Username string `json:"username"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		email := strings.ToLower(strings.TrimSpace(body.Email))
		if !validEmail(email) {
			apiError(c, http.StatusBadRequest, "邮箱格式不正确")
			return
		}
		if !regSmtpReady() {
			apiError(c, http.StatusBadRequest, "注册暂不可用：SMTP 邮件未配置，请联系管理员")
			return
		}
		if taken, what := userEmailOrNameTaken(strings.TrimSpace(body.Username), email); taken {
			apiError(c, http.StatusConflict, what)
			return
		}
		if err := grantCode(email, strings.TrimSpace(body.Username), "register"); err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"sent": true, "email": email, "expiresIn": int(verifyTTL.Seconds())})
	})

	// 提交注册：body {username, password, email, code}
	router.POST("/admin/register", func(c *gin.Context) {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Email    string `json:"email"`
			Code     string `json:"code"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		username := strings.TrimSpace(body.Username)
		email := strings.ToLower(strings.TrimSpace(body.Email))
		if username == "" || len(username) > 64 {
			apiError(c, http.StatusBadRequest, "用户名必填且不超过 64 字符")
			return
		}
		if len(body.Password) < 6 {
			apiError(c, http.StatusBadRequest, "密码至少 6 位")
			return
		}
		if !validEmail(email) {
			apiError(c, http.StatusBadRequest, "邮箱格式不正确")
			return
		}
		if taken, what := userEmailOrNameTaken(username, email); taken {
			apiError(c, http.StatusConflict, what)
			return
		}
		// 校验邮箱验证码
		if err := verifyEmailCode(email, body.Code); err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		hash, err := hashPassword(body.Password)
		if err != nil {
			apiError(c, http.StatusInternalServerError, "hash: "+err.Error())
			return
		}
		u := &User{
			Username:      username,
			PasswordHash:  hash,
			Email:         email,
			Role:          RoleUser,
			Enabled:       true,
			EmailVerified: true, // 注册需先验证邮箱，故创建即已验证
		}
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Create(u).Error; err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				apiError(c, http.StatusConflict, "用户名或邮箱已被占用")
				return
			}
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[register] new user %q (id=%d) role=user email verified", u.Username, u.ID)
		c.JSON(http.StatusCreated, u.userPublic())
	})
}

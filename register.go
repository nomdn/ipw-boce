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

// ==================== 公开自助注册（邮箱验证码，与原版一致） ====================
//
//   POST /admin/register/send-code {email, username?} → 6 位码，5 分钟有效，60s 冷却，5 次作废
//   POST /admin/register {username, password, email, code} → role=user、email_verified=true
// 登录后补验证：POST /admin/me/verify/send、POST /admin/me/verify {code}

type verifyCode struct {
	Code     string
	Exp      time.Time
	Attempt  int
	LastSent time.Time
}

var (
	vcodeMu    sync.Mutex
	vcodeStore = map[string]*verifyCode{}
)

const (
	verifyTTL    = 5 * time.Minute
	verifyResend = 60 * time.Second
	verifyMaxTry = 5
)

var emailRe = regexp.MustCompile(`^[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}$`)

func validEmail(s string) bool { return emailRe.MatchString(s) }

func regSmtpReady() bool {
	c := currentSMTP()
	return strings.TrimSpace(c.Host) != "" && strings.TrimSpace(c.User) != ""
}

func grantCode(email, purpose string) error {
	vcodeMu.Lock()
	now := time.Now()
	if old := vcodeStore[email]; old != nil && now.Sub(old.LastSent) < verifyResend {
		left := int((verifyResend - now.Sub(old.LastSent)).Seconds())
		vcodeMu.Unlock()
		return fmt.Errorf("发送过于频繁，请 %d 秒后再试", left)
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	code := fmt.Sprintf("%06d", n.Int64())
	vcodeStore[email] = &verifyCode{Code: code, Exp: now.Add(verifyTTL), LastSent: now}
	vcodeMu.Unlock()

	intro := "你在 1IPW-BOCE 控制台请求验证邮箱，验证码如下："
	if purpose == "register" {
		intro = "你在 1IPW-BOCE 控制台发起账号注册，邮箱验证码如下："
	}
	body := fmt.Sprintf("你好，\n\n%s\n\n    %s\n\n验证码 %d 分钟内有效，请勿泄露给他人。如非本人操作请忽略本邮件。\n",
		intro, code, int(verifyTTL.Minutes()))
	if err := sendMail([]string{email}, "[1IPW-BOCE] 邮箱验证码", body); err != nil {
		vcodeMu.Lock()
		if c, ok := vcodeStore[email]; ok && c.Code == code {
			delete(vcodeStore, email)
		}
		vcodeMu.Unlock()
		return fmt.Errorf("邮件发送失败: %w", err)
	}
	return nil
}

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
	delete(vcodeStore, email)
	return nil
}

func userEmailOrNameTaken(username, email string) (bool, string) {
	ctx, cancel := dbCtx()
	defer cancel()
	var n int64
	if username != "" {
		db.WithContext(ctx).Model(&User{}).Where("username = ?", username).Count(&n)
		if n > 0 {
			return true, "用户名已被占用"
		}
	}
	if email != "" {
		db.WithContext(ctx).Model(&User{}).Where("email = ?", email).Count(&n)
		if n > 0 {
			return true, "该邮箱已被注册"
		}
	}
	return false, ""
}

func registerPublicRoutes(router *gin.Engine) {
	registerCaptchaRoutes(router)
	router.POST("/admin/register/send-code", func(c *gin.Context) {
		if !registrationOpen() {
			apiError(c, http.StatusForbidden, "注册已关闭，请联系管理员创建账号")
			return
		}
		if !getSiteSettings().EmailVerifyRequired {
			apiError(c, http.StatusBadRequest, "当前注册不需要邮箱验证码")
			return
		}
		if !authLimiter.allow(c.ClientIP()) {
			apiError(c, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
			return
		}
		var body struct {
			Email    string `json:"email"`
			Username string `json:"username"`
			captchaInput
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
		// 发信是最贵的一步：需要邮箱验证时，人机验证放在这里
		if err := checkCaptcha(c, "register", body.captchaInput); err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		if taken, what := userEmailOrNameTaken(strings.TrimSpace(body.Username), email); taken {
			apiError(c, http.StatusConflict, what)
			return
		}
		if err := grantCode(email, "register"); err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"sent": true, "email": email, "expiresIn": int(verifyTTL.Seconds())})
	})

	router.POST("/admin/register", func(c *gin.Context) {
		if !registrationOpen() {
			apiError(c, http.StatusForbidden, "注册已关闭，请联系管理员创建账号")
			return
		}
		if !authLimiter.allow(c.ClientIP()) {
			apiError(c, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
			return
		}
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Email    string `json:"email"`
			Code     string `json:"code"`
			captchaInput
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
		verified := getSiteSettings().EmailVerifyRequired
		if verified {
			if err := verifyEmailCode(email, body.Code); err != nil {
				apiError(c, http.StatusBadRequest, err.Error())
				return
			}
		} else if err := checkCaptcha(c, "register", body.captchaInput); err != nil {
			// 不需要邮箱验证码时，人机验证放在提交注册这一步
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		hash, err := hashPassword(body.Password)
		if err != nil {
			apiError(c, http.StatusInternalServerError, "hash: "+err.Error())
			return
		}
		u := &User{Username: username, PasswordHash: hash, Email: email, Role: RoleUser, Enabled: true, EmailVerified: verified}
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Create(u).Error; err != nil {
			if isUniqueErr(err) {
				apiError(c, http.StatusConflict, "用户名或邮箱已被占用")
				return
			}
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		// 积分体系：新用户注册奖励（规则见积分设置，0 = 不发）
		grantSignupBonus(u.ID)
		log.Printf("[register] new user %q (id=%d)", u.Username, u.ID)
		c.JSON(http.StatusCreated, u.userPublic())
	})
}

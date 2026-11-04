package main

// ==================== 系统设置（后台可改，存 point_settings key=site，保存即生效） ====================
//
// 注册开关、注册是否需要邮箱验证码、人机验证（图形验证码 / Cloudflare Turnstile）、SMTP 发信。
// setting.json 里的 registration-enabled / smtp 只作为首次启动的缺省值；后台保存过之后以库里为准。
//
//   GET  /admin/public/site                 公开：登录页需要的开关（不含任何密钥）
//   GET  /admin/site-settings               管理员：完整设置（密钥只回 has*）
//   PUT  /admin/site-settings               管理员：保存（密钥字段留空 = 不修改）
//   POST /admin/site-settings/test-mail     管理员：{to} 用当前 SMTP 发测试邮件

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

const (
	CaptchaOff       = "off"
	CaptchaImage     = "image"
	CaptchaTurnstile = "turnstile"
)

type siteSettings struct {
	RegistrationEnabled bool   `json:"registrationEnabled"`
	EmailVerifyRequired bool   `json:"emailVerifyRequired"` // 注册是否需要邮箱验证码（关闭后邮箱只做格式校验）
	CaptchaProvider     string `json:"captchaProvider"`     // off | image | turnstile
	CaptchaOnLogin      bool   `json:"captchaOnLogin"`
	CaptchaOnRegister   bool   `json:"captchaOnRegister"`
	TurnstileSiteKey    string `json:"turnstileSiteKey"`
	TurnstileSecret     string `json:"turnstileSecret,omitempty"`

	Popup popupSettings `json:"popup"`
	SMTP  smtpConfig    `json:"smtp"`
}

var (
	siteMu   sync.RWMutex
	siteConf siteSettings
)

// defaultSiteSettings 首次启动：沿用 setting.json 的注册开关与 SMTP
func defaultSiteSettings() siteSettings {
	return siteSettings{RegistrationEnabled: REGISTRATION_ENABLED, EmailVerifyRequired: true,
		CaptchaProvider: CaptchaOff, CaptchaOnLogin: true, CaptchaOnRegister: true, Popup: defaultPopup(), SMTP: SMTP_CONF}
}

func loadSiteSettings() {
	ctx, cancel := dbCtx()
	defer cancel()
	var row PointSetting
	db.WithContext(ctx).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: "site"}).Limit(1).Find(&row)
	cfg := defaultSiteSettings()
	if row.Value != "" {
		_ = json.Unmarshal([]byte(row.Value), &cfg)
	}
	applySiteSettings(cfg)
}

func applySiteSettings(cfg siteSettings) {
	if cfg.SMTP.TimeoutSec <= 0 {
		cfg.SMTP.TimeoutSec = 10
	}
	siteMu.Lock()
	siteConf = cfg
	siteMu.Unlock()
}

func getSiteSettings() siteSettings {
	siteMu.RLock()
	defer siteMu.RUnlock()
	return siteConf
}

func saveSiteSettings(cfg siteSettings) error {
	ctx, cancel := dbCtx()
	defer cancel()
	row := PointSetting{Key: "site", Value: string(mustRaw(cfg)), UpdatedAt: time.Now().UTC()}
	if err := db.WithContext(ctx).Save(&row).Error; err != nil {
		return err
	}
	applySiteSettings(cfg)
	return nil
}

func registrationOpen() bool { return getSiteSettings().RegistrationEnabled }

// currentSMTP 发信用的 SMTP（后台设置优先）
func currentSMTP() smtpConfig { return getSiteSettings().SMTP }

// captchaFor 某场景需要的人机验证方式（off = 不需要）
func captchaFor(scene string) string {
	s := getSiteSettings()
	p := s.CaptchaProvider
	if p == CaptchaTurnstile && (s.TurnstileSiteKey == "" || s.TurnstileSecret == "") {
		p = CaptchaOff // 没配齐 Turnstile 时不拦人，避免把所有人锁在外面
	}
	if p != CaptchaImage && p != CaptchaTurnstile {
		return CaptchaOff
	}
	if (scene == "login" && s.CaptchaOnLogin) || (scene == "register" && s.CaptchaOnRegister) {
		return p
	}
	return CaptchaOff
}

func publicSiteView() gin.H {
	s := getSiteSettings()
	h := gin.H{"registrationEnabled": s.RegistrationEnabled, "emailVerifyRequired": s.EmailVerifyRequired,
		"captcha": gin.H{"login": captchaFor("login"), "register": captchaFor("register")}}
	if captchaFor("login") == CaptchaTurnstile || captchaFor("register") == CaptchaTurnstile {
		h["turnstileSiteKey"] = s.TurnstileSiteKey
	}
	if s.Popup.Enabled && s.Popup.ShowBoce {
		h["popup"] = s.Popup.view("")
	}
	return h
}

func adminSiteView() gin.H {
	s := getSiteSettings()
	smtpView := s.SMTP
	smtpView.Password = ""
	return gin.H{"registrationEnabled": s.RegistrationEnabled, "emailVerifyRequired": s.EmailVerifyRequired,
		"captchaProvider": s.CaptchaProvider, "captchaOnLogin": s.CaptchaOnLogin, "captchaOnRegister": s.CaptchaOnRegister,
		"turnstileSiteKey": s.TurnstileSiteKey, "hasTurnstileSecret": s.TurnstileSecret != "", "popup": s.Popup,
		"smtp": smtpView, "hasSmtpPassword": s.SMTP.Password != "", "smtpReady": regSmtpReady()}
}

type siteInput struct {
	RegistrationEnabled *bool   `json:"registrationEnabled"`
	EmailVerifyRequired *bool   `json:"emailVerifyRequired"`
	CaptchaProvider     *string `json:"captchaProvider"`
	CaptchaOnLogin      *bool   `json:"captchaOnLogin"`
	CaptchaOnRegister   *bool   `json:"captchaOnRegister"`
	TurnstileSiteKey    *string `json:"turnstileSiteKey"`
	TurnstileSecret     string  `json:"turnstileSecret"` // 留空 = 不修改
	ClearTurnstile      bool    `json:"clearTurnstileSecret"`
	Popup               *popupSettings `json:"popup"`
	SMTP                *struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		User     string `json:"user"`
		Password string `json:"password"` // 留空 = 不修改
		From     string `json:"from"`
		FromName string `json:"fromName"`
		Ssl      bool   `json:"ssl"`
		StartTLS bool   `json:"startTLS"`
		Insecure bool   `json:"insecure"`
	} `json:"smtp"`
}

// merge 把输入合进现有设置；返回错误文案（空 = 通过）
func (in *siteInput) merge(cur siteSettings) (siteSettings, string) {
	if in.RegistrationEnabled != nil {
		cur.RegistrationEnabled = *in.RegistrationEnabled
	}
	if in.EmailVerifyRequired != nil {
		cur.EmailVerifyRequired = *in.EmailVerifyRequired
	}
	if in.CaptchaProvider != nil {
		p := strings.TrimSpace(*in.CaptchaProvider)
		if p != CaptchaOff && p != CaptchaImage && p != CaptchaTurnstile {
			return cur, "人机验证方式只能是 off / image / turnstile"
		}
		cur.CaptchaProvider = p
	}
	if in.CaptchaOnLogin != nil {
		cur.CaptchaOnLogin = *in.CaptchaOnLogin
	}
	if in.CaptchaOnRegister != nil {
		cur.CaptchaOnRegister = *in.CaptchaOnRegister
	}
	if in.TurnstileSiteKey != nil {
		cur.TurnstileSiteKey = strings.TrimSpace(*in.TurnstileSiteKey)
	}
	if in.Popup != nil {
		np := *in.Popup
		if msg := np.sanitize(); msg != "" {
			return cur, msg
		}
		np.Rev = cur.Popup.Rev
		if !np.sameContent(cur.Popup) || np.Rev == 0 {
			np.Rev = time.Now().UnixMilli() // 内容变了：换一个 rev，已读过的用户会再看到一次
		}
		cur.Popup = np
	}
	if s := strings.TrimSpace(in.TurnstileSecret); s != "" {
		cur.TurnstileSecret = s
	} else if in.ClearTurnstile {
		cur.TurnstileSecret = ""
	}
	if cur.CaptchaProvider == CaptchaTurnstile && (cur.TurnstileSiteKey == "" || cur.TurnstileSecret == "") {
		return cur, "使用 Turnstile 需要同时填写 Site Key 和 Secret Key"
	}
	if m := in.SMTP; m != nil {
		pw := cur.SMTP.Password
		if m.Password != "" {
			pw = m.Password
		}
		if strings.TrimSpace(m.Host) == "" {
			pw = "" // 清空主机 = 关闭 SMTP
		}
		if m.Port < 0 || m.Port > 65535 {
			return cur, "SMTP 端口不正确"
		}
		cur.SMTP = smtpConfig{Host: strings.TrimSpace(m.Host), Port: m.Port, User: strings.TrimSpace(m.User), Password: pw,
			From: strings.TrimSpace(m.From), FromName: strings.TrimSpace(m.FromName), Ssl: m.Ssl, StartTLS: m.StartTLS,
			Insecure: m.Insecure, TimeoutSec: cur.SMTP.TimeoutSec}
	}
	if cur.RegistrationEnabled && cur.EmailVerifyRequired && strings.TrimSpace(cur.SMTP.Host) == "" {
		return cur, "开放注册且要求邮箱验证时必须先配置 SMTP（或关闭「注册需邮箱验证」）"
	}
	return cur, ""
}

func registerSiteRoutes(r *gin.Engine, restricted *gin.RouterGroup) {
	registerPopupRoutes(r, restricted)
	r.GET("/admin/public/site", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, publicSiteView())
	})
	restricted.GET("/site-settings", func(c *gin.Context) { c.JSON(http.StatusOK, adminSiteView()) })
	restricted.PUT("/site-settings", func(c *gin.Context) {
		var in siteInput
		if err := c.ShouldBindJSON(&in); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body")
			return
		}
		next, msg := in.merge(getSiteSettings())
		if msg != "" {
			apiError(c, http.StatusBadRequest, msg)
			return
		}
		if err := saveSiteSettings(next); err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, adminSiteView())
	})
	restricted.POST("/site-settings/test-mail", func(c *gin.Context) {
		var b struct {
			To string `json:"to"`
		}
		_ = c.ShouldBindJSON(&b)
		to := strings.TrimSpace(b.To)
		if !validEmail(to) {
			apiError(c, http.StatusBadRequest, "邮箱格式不正确")
			return
		}
		if !regSmtpReady() {
			apiError(c, http.StatusBadRequest, "SMTP 未配置")
			return
		}
		if err := sendMail([]string{to}, "[1IPW-BOCE] 测试邮件", "这是一封测试邮件，收到说明 SMTP 配置可用。\n"); err != nil {
			apiError(c, http.StatusBadGateway, "发送失败："+err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"sent": true})
	})
}

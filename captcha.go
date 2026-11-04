package main

// ==================== 人机验证 ====================
//
// image：GET /admin/captcha → {id, image(data:image/svg+xml)}，4 位字符，5 分钟有效，一次性，错一次即作废。
// turnstile：前端拿到 cf-turnstile-response，后端向 Cloudflare siteverify 校验。
// 请求体统一带 captchaId / captchaCode（图形）或 turnstileToken（Turnstile）。

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	captchaTTL   = 5 * time.Minute
	captchaChars = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // 去掉易混的 I/L/O/0/1
)

type captchaEntry struct {
	answer string
	exp    time.Time
}

var (
	captchaMu    sync.Mutex
	captchaStore = map[string]captchaEntry{}
	// captchaLimiter 拉图片的单 IP 限流
	captchaLimiter = newRateLimiter(40)
	// turnstileVerifyURL 测试可替换
	turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
)

type captchaInput struct {
	CaptchaID      string `json:"captchaId"`
	CaptchaCode    string `json:"captchaCode"`
	TurnstileToken string `json:"turnstileToken"`
}

func rnd(n int) int {
	v, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
	return int(v.Int64())
}

func newImageCaptcha() (id, answer, svg string) {
	b := make([]byte, 4)
	for i := range b {
		b[i] = captchaChars[rnd(len(captchaChars))]
	}
	answer = string(b)
	id = randHex(12)
	captchaMu.Lock()
	now := time.Now()
	if len(captchaStore) > 5000 {
		for k, v := range captchaStore {
			if now.After(v.exp) {
				delete(captchaStore, k)
			}
		}
	}
	captchaStore[id] = captchaEntry{answer: answer, exp: now.Add(captchaTTL)}
	captchaMu.Unlock()
	return id, answer, captchaSVG(answer)
}

// captchaSVG 黑白灰 SVG：字符随机旋转/偏移 + 干扰线 + 噪点（配合控制台黑白风格）
func captchaSVG(text string) string {
	const w, h = 120, 44
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, w, h, w, h)
	sb.WriteString(`<rect width="100%" height="100%" fill="#f4f4f4"/>`)
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&sb, `<path d="M%d %d Q%d %d %d %d" stroke="#%s" stroke-width="1.%d" fill="none"/>`,
			rnd(20), rnd(h), 30+rnd(60), rnd(h), 100+rnd(20), rnd(h), grey(), rnd(6))
	}
	for i, ch := range text {
		x := 14 + i*26 + rnd(6)
		y := 30 + rnd(8)
		fmt.Fprintf(&sb, `<text x="%d" y="%d" font-family="monospace" font-size="%d" font-weight="700" fill="#%s" transform="rotate(%d %d %d)">%c</text>`,
			x, y, 24+rnd(6), dark(), rnd(50)-25, x, y, ch)
	}
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&sb, `<circle cx="%d" cy="%d" r="1" fill="#%s"/>`, rnd(w), rnd(h), grey())
	}
	sb.WriteString(`</svg>`)
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(sb.String()))
}

func grey() string { v := 120 + rnd(90); return fmt.Sprintf("%02x%02x%02x", v, v, v) }
func dark() string { v := 10 + rnd(70); return fmt.Sprintf("%02x%02x%02x", v, v, v) }

// verifyImageCaptcha 一次性：无论对错都作废，防止对同一张图反复猜
func verifyImageCaptcha(id, code string) error {
	captchaMu.Lock()
	e, ok := captchaStore[id]
	delete(captchaStore, id)
	captchaMu.Unlock()
	if strings.TrimSpace(code) == "" {
		return errors.New("请输入图形验证码")
	}
	if !ok || time.Now().After(e.exp) {
		return errors.New("图形验证码已过期，请刷新")
	}
	if !strings.EqualFold(strings.TrimSpace(code), e.answer) {
		return errors.New("图形验证码不正确")
	}
	return nil
}

func verifyTurnstile(token, ip string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("请先完成人机验证")
	}
	form := url.Values{"secret": {getSiteSettings().TurnstileSecret}, "response": {token}}
	if ip != "" {
		form.Set("remoteip", ip)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).PostForm(turnstileVerifyURL, form)
	if err != nil {
		return errors.New("人机验证服务暂不可用，请稍后再试")
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || !out.Success {
		return errors.New("人机验证未通过，请重试")
	}
	return nil
}

// checkCaptcha 按场景校验；场景未开启时直接通过
func checkCaptcha(c *gin.Context, scene string, in captchaInput) error {
	switch captchaFor(scene) {
	case CaptchaImage:
		return verifyImageCaptcha(in.CaptchaID, in.CaptchaCode)
	case CaptchaTurnstile:
		return verifyTurnstile(in.TurnstileToken, c.ClientIP())
	}
	return nil
}

func registerCaptchaRoutes(r *gin.Engine) {
	r.GET("/admin/captcha", func(c *gin.Context) {
		if !captchaLimiter.allow(c.ClientIP()) {
			apiError(c, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
			return
		}
		id, _, img := newImageCaptcha()
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"id": id, "image": img, "expiresIn": int(captchaTTL.Seconds())})
	})
}

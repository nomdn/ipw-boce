package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSiteSettingsAdminAndPublic(t *testing.T) {
	r := setupTest(t)
	code, body := call(r, "PUT", "/admin/site-settings", "static-admin", map[string]any{"captchaProvider": "turnstile", "turnstileSiteKey": "0xSITE"})
	if code != 400 || !strings.Contains(body["statusMessage"].(string), "Secret") {
		t.Fatalf("turnstile without secret: %d %v", code, body)
	}
	code, body = call(r, "PUT", "/admin/site-settings", "static-admin", map[string]any{"registrationEnabled": true, "emailVerifyRequired": true})
	if code != 400 || !strings.Contains(body["statusMessage"].(string), "SMTP") {
		t.Fatalf("email verify without smtp: %d %v", code, body)
	}
	code, body = call(r, "PUT", "/admin/site-settings", "static-admin", map[string]any{
		"registrationEnabled": true, "emailVerifyRequired": false, "captchaProvider": "turnstile",
		"turnstileSiteKey": "0xSITE", "turnstileSecret": "0xSECRET", "captchaOnLogin": false, "captchaOnRegister": true,
		"smtp": map[string]any{"host": "smtp.test", "port": 465, "user": "u@test", "password": "pw", "ssl": true}})
	if code != 200 || body["hasTurnstileSecret"] != true || body["hasSmtpPassword"] != true || body["turnstileSecret"] != nil {
		t.Fatalf("save: %d %v", code, body)
	}
	if body["smtp"].(map[string]any)["password"] != "" {
		t.Fatal("smtp password leaked")
	}
	// 密钥留空 = 不修改
	call(r, "PUT", "/admin/site-settings", "static-admin", map[string]any{"turnstileSecret": "", "smtp": map[string]any{"host": "smtp.test", "user": "u@test"}})
	if s := getSiteSettings(); s.TurnstileSecret != "0xSECRET" || s.SMTP.Password != "pw" {
		t.Fatalf("secrets overwritten: %+v", s)
	}
	// 普通用户不能改
	u := mkUser(t, "plain")
	tok, _, _ := issueToken(u.ID, u.Username, u.Role)
	if code, _ := call(r, "GET", "/admin/site-settings", tok, nil); code != 403 {
		t.Fatalf("user read settings code=%d", code)
	}
	// 公开接口：不含 secret
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/admin/public/site", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "0xSECRET") || strings.Contains(w.Body.String(), `"pw"`) ||
		!strings.Contains(w.Body.String(), `"register":"turnstile"`) || !strings.Contains(w.Body.String(), `"login":"off"`) {
		t.Fatalf("public site: %s", w.Body.String())
	}
	// 重启后从库恢复
	applySiteSettings(defaultSiteSettings())
	loadSiteSettings()
	if s := getSiteSettings(); !s.RegistrationEnabled || s.CaptchaProvider != CaptchaTurnstile || s.SMTP.Host != "smtp.test" {
		t.Fatalf("reload: %+v", s)
	}
	if currentSMTP().Host != "smtp.test" {
		t.Fatal("sendMail would not use console SMTP")
	}
}

func TestImageCaptchaOnLogin(t *testing.T) {
	r := setupTest(t)
	s := getSiteSettings()
	s.CaptchaProvider, s.CaptchaOnLogin = CaptchaImage, true
	applySiteSettings(s)
	creds := map[string]string{"username": "admin", "password": "admin123"}
	if code, _ := call(r, "POST", "/admin/login", "", creds); code != 400 {
		t.Fatalf("no captcha code=%d", code)
	}
	id, ans, img := newImageCaptcha()
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(img, "data:image/svg+xml;base64,"))
	if !strings.HasPrefix(string(raw), "<svg") {
		t.Fatal("not svg")
	}
	creds["captchaId"], creds["captchaCode"] = id, "ZZZZ"
	if ans == "ZZZZ" {
		creds["captchaCode"] = "YYYY"
	}
	if code, _ := call(r, "POST", "/admin/login", "", creds); code != 400 {
		t.Fatalf("wrong captcha code=%d", code)
	}
	creds["captchaCode"] = ans // 同一张图错过一次即作废
	if code, _ := call(r, "POST", "/admin/login", "", creds); code != 400 {
		t.Fatalf("reused captcha code=%d", code)
	}
	id, ans, _ = newImageCaptcha()
	creds["captchaId"], creds["captchaCode"] = id, strings.ToLower(ans)
	if code, body := call(r, "POST", "/admin/login", "", creds); code != 200 {
		t.Fatalf("right captcha code=%d %v", code, body)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/admin/captcha", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "data:image/svg+xml;base64,") {
		t.Fatalf("captcha endpoint %d", w.Code)
	}
}

func TestRegisterWithoutEmailVerifyAndTurnstile(t *testing.T) {
	r := setupTest(t)
	ok := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		if req.Form.Get("secret") != "sec" || req.Form.Get("response") == "" {
			w.Write([]byte(`{"success":false}`))
			return
		}
		if ok {
			w.Write([]byte(`{"success":true}`))
		} else {
			w.Write([]byte(`{"success":false}`))
		}
	}))
	defer ts.Close()
	old := turnstileVerifyURL
	turnstileVerifyURL = ts.URL
	defer func() { turnstileVerifyURL = old }()

	s := getSiteSettings()
	s.RegistrationEnabled, s.EmailVerifyRequired = true, false
	s.CaptchaProvider, s.CaptchaOnRegister, s.TurnstileSiteKey, s.TurnstileSecret = CaptchaTurnstile, true, "site", "sec"
	applySiteSettings(s)

	if code, _ := call(r, "POST", "/admin/register/send-code", "", map[string]string{"email": "a@b.co"}); code != 400 {
		t.Fatalf("send-code should be refused when email verify off: %d", code)
	}
	reg := map[string]string{"username": "newbie", "password": "secret1", "email": "n@b.co"}
	if code, _ := call(r, "POST", "/admin/register", "", reg); code != 400 {
		t.Fatalf("no turnstile token code=%d", code)
	}
	ok = false
	reg["turnstileToken"] = "tok"
	if code, _ := call(r, "POST", "/admin/register", "", reg); code != 400 {
		t.Fatalf("turnstile failed code=%d", code)
	}
	ok = true
	code, body := call(r, "POST", "/admin/register", "", reg)
	if code != 201 || body["emailVerified"] != false {
		t.Fatalf("register: %d %v", code, body)
	}
	// Turnstile 没配齐时不拦人
	s.TurnstileSecret = ""
	applySiteSettings(s)
	if captchaFor("register") != CaptchaOff {
		t.Fatal("half-configured turnstile must not block")
	}
}

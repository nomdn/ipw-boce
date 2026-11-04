package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 1x1 透明 PNG
var tinyPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

func upload(r http.Handler, token, filename string, data []byte) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", filename)
	fw.Write(data)
	w.Close()
	req := httptest.NewRequest("POST", "/admin/popup/image", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestPopupSettingsRevAndVisibility(t *testing.T) {
	r := setupTest(t)
	SYNC_TOKEN = "sync-secret"
	defer func() { SYNC_TOKEN = "" }()
	s := getSiteSettings()
	s.EmailVerifyRequired = false // 与线上一致：不要求邮箱验证码，保存弹窗不受 SMTP 校验影响
	applySiteSettings(s)

	// 没内容不能开启；非法链接被拒
	if code, _ := call(r, "PUT", "/admin/site-settings", "static-admin", map[string]any{"popup": map[string]any{"enabled": true}}); code != 400 {
		t.Fatalf("empty popup enabled code=%d", code)
	}
	if code, _ := call(r, "PUT", "/admin/site-settings", "static-admin", map[string]any{"popup": map[string]any{"enabled": true, "title": "x", "link": "javascript:alert(1)"}}); code != 400 {
		t.Fatalf("js link code=%d", code)
	}
	if code, _ := call(r, "PUT", "/admin/site-settings", "static-admin", map[string]any{"popup": map[string]any{"enabled": true, "title": "x", "imageUrl": "data:image/png;base64,AAAA"}}); code != 400 {
		t.Fatalf("data url code=%d", code)
	}
	if code, _ := call(r, "PUT", "/admin/site-settings", "static-admin", map[string]any{"popup": map[string]any{"enabled": true, "title": "x", "frequency": "hourly"}}); code != 400 {
		t.Fatalf("bad frequency code=%d", code)
	}

	pop := map[string]any{"enabled": true, "showBoce": true, "showIpw": false, "title": "公告", "body": "第一行\n第二行",
		"imageUrl": "/popup-img/0123456789abcdef0123456789abcdef.png", "link": "https://example.com/a", "frequency": "daily"}
	code, body := call(r, "PUT", "/admin/site-settings", "static-admin", map[string]any{"popup": pop})
	if code != 200 {
		t.Fatalf("save %d %v", code, body)
	}
	rev1 := body["popup"].(map[string]any)["rev"].(float64)
	if rev1 == 0 {
		t.Fatal("rev not set")
	}
	// 内容没变再保存：rev 不变；改了标题：rev 变
	_, body = call(r, "PUT", "/admin/site-settings", "static-admin", map[string]any{"popup": pop})
	if body["popup"].(map[string]any)["rev"].(float64) != rev1 {
		t.Fatal("rev changed without content change")
	}
	pop["title"] = "新公告"
	_, body = call(r, "PUT", "/admin/site-settings", "static-admin", map[string]any{"popup": pop})
	if body["popup"].(map[string]any)["rev"].(float64) == rev1 {
		t.Fatal("rev not bumped after content change")
	}

	// 公开接口：只在勾选了 boce 时出现；1IPW 没勾选 → null
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/admin/public/site", nil))
	if !strings.Contains(w.Body.String(), `"popup":{`) || !strings.Contains(w.Body.String(), `"imageUrl":"/popup-img/`) {
		t.Fatalf("public site popup: %s", w.Body.String())
	}
	// 1IPW 没勾选 → 同步接口返回 null
	if code, body := call(r, "GET", "/api/sync/popup", "sync-secret", nil); code != 200 || body["popup"] != nil {
		t.Fatalf("sync should be null when showIpw=false: %d %v", code, body)
	}
}

func TestPopupSyncAndImageUpload(t *testing.T) {
	r := setupTest(t)
	s := getSiteSettings()
	s.EmailVerifyRequired = false
	applySiteSettings(s)
	DB_CONF = dbConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")}
	SYNC_TOKEN = "sync-secret"
	defer func() { SYNC_TOKEN = "" }()
	PUBLIC_BASE_URL = "https://boce.test"
	defer func() { PUBLIC_BASE_URL = "" }()

	// 未配 token / 错误 token
	if code, _ := call(r, "GET", "/api/sync/popup", "wrong", nil); code != 401 {
		t.Fatalf("wrong token %d", code)
	}
	// 关闭状态：popup=null
	if code, body := call(r, "GET", "/api/sync/popup", "sync-secret", nil); code != 200 || body["popup"] != nil {
		t.Fatalf("disabled: %d %v", code, body)
	}

	// 上传：非图片/SVG/过大被拒，PNG 通过
	if rec := upload(r, "static-admin", "a.png", []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>")); rec.Code != 400 {
		t.Fatalf("svg upload %d", rec.Code)
	}
	if rec := upload(r, "static-admin", "a.png", bytes.Repeat([]byte("x"), 4<<20)); rec.Code != 413 && rec.Code != 400 {
		t.Fatalf("big upload %d", rec.Code)
	}
	u := mkUser(t, "plain")
	tok, _, _ := issueToken(u.ID, u.Username, u.Role)
	if rec := upload(r, tok, "a.png", tinyPNG); rec.Code != 403 {
		t.Fatalf("non-admin upload %d", rec.Code)
	}
	rec := upload(r, "static-admin", "evil.html", tinyPNG) // 扩展名不可信，按内容识别
	var up struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &up)
	if rec.Code != 200 || !strings.HasPrefix(up.URL, "/popup-img/") || !strings.HasSuffix(up.URL, ".png") {
		t.Fatalf("png upload %d %s", rec.Code, rec.Body.String())
	}
	url := up.URL

	// 公开读取：正确类型、可缓存；路径穿越/乱名 404
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("serve %d %v", w.Code, w.Header())
	}
	for _, p := range []string{"/popup-img/..%2Ft.db", "/popup-img/x.png", "/popup-img/0123456789abcdef0123456789abcdef.svg"} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 404 {
			t.Fatalf("%s code=%d", p, w.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(uploadDir(), strings.TrimPrefix(url, "/popup-img/"))); err != nil {
		t.Fatal(err)
	}

	// 开启并只勾 1IPW：同步接口返回绝对图片地址；boce 公开接口不带 popup
	code, _ := call(r, "PUT", "/admin/site-settings", "static-admin", map[string]any{"popup": map[string]any{
		"enabled": true, "showBoce": false, "showIpw": true, "title": "T", "body": "B", "imageUrl": url, "frequency": "session"}})
	if code != 200 {
		t.Fatalf("save %d", code)
	}
	code, body := call(r, "GET", "/api/sync/popup", "sync-secret", nil)
	p, _ := body["popup"].(map[string]any)
	if code != 200 || p == nil || p["imageUrl"] != "https://boce.test"+url || p["frequency"] != "session" || p["title"] != "T" || p["rev"].(float64) == 0 {
		t.Fatalf("sync popup: %d %v", code, body)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/admin/public/site", nil))
	if strings.Contains(w.Body.String(), `"popup"`) {
		t.Fatalf("boce public leaked popup while showBoce=false: %s", w.Body.String())
	}
}

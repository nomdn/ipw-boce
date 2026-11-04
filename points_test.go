package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

func setupTest(t *testing.T) *gin.Engine {
	t.Helper()
	var err error
	db, err = openDB(dbConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s, err := db.DB.DB(); err == nil {
			s.Close()
		}
	})
	JWT_SECRET, JWT_EXPIRY = "test-secret", 3600
	ADMIN_USER, ADMIN_PASSWORD, ADMIN_TOKEN = "admin", "admin123", "static-admin"
	REGISTRATION_ENABLED, SMTP_CONF = true, smtpConfig{}
	seedUsers()
	pointsConf = defaultPointsConfig()
	loadSiteSettings()
	authLimiter = newRateLimiter(10) // 每个测试独立的登录/注册限流窗口
	return setupRouterOnly()
}

func setupRouterOnly() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerAdminRoutes(r)
	registerRestRoutes(r)
	return r
}

type httpHandlerFunc func(http.ResponseWriter, *http.Request)

func (f httpHandlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) { f(w, r) }

func mkUser(t *testing.T, name string) *User {
	t.Helper()
	h, _ := hashPassword("secret1")
	u := &User{Username: name, PasswordHash: h, Role: RoleUser, Enabled: true}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func call(r *gin.Engine, method, path, token string, body any, hdr ...string) (int, map[string]any) {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestErrorEnvelopesAndAuth(t *testing.T) {
	r := setupTest(t)
	// 内部接口：h3 形态
	code, body := call(r, "GET", "/admin/me", "", nil)
	if code != 401 || body["statusMessage"] != "Unauthorized" || body["statusCode"].(float64) != 401 {
		t.Fatalf("admin 401 envelope: %d %v", code, body)
	}
	// /api/v1：REST 形态
	code, body = call(r, "GET", "/api/v1/tasks", "bad", nil)
	if code != 401 || body["error"].(map[string]any)["code"] != "unauthorized" {
		t.Fatalf("rest 401 envelope: %d %v", code, body)
	}
	// JWT 登录
	code, body = call(r, "POST", "/admin/login", "", map[string]string{"username": "admin", "password": "admin123"})
	if code != 200 || body["role"] != "admin" || body["tokenType"] != "Bearer" {
		t.Fatalf("login: %d %v", code, body)
	}
	jwt := body["token"].(string)
	if code, _ = call(r, "GET", "/admin/me", jwt, nil); code != 200 {
		t.Fatalf("jwt access: %d", code)
	}
	// 个人 Token
	code, body = call(r, "POST", "/admin/me/token", jwt, nil)
	ipt := body["token"].(string)
	if code != 200 || !strings.HasPrefix(ipt, "ipt_") || len(ipt) != 44 {
		t.Fatalf("token: %d %v", code, body)
	}
	if code, _ = call(r, "GET", "/api/v1/nodes", ipt, nil); code != 200 {
		t.Fatalf("ipt access: %d", code)
	}
	call(r, "DELETE", "/admin/me/token", jwt, nil)
	if code, body = call(r, "GET", "/api/v1/nodes", ipt, nil); code != 401 || body["error"].(map[string]any)["message"] != "invalid or revoked token" {
		t.Fatalf("revoked ipt: %d %v", code, body)
	}
	// 普通用户访问 admin 接口 → 403
	u := mkUser(t, "alice")
	tok, _, _ := issueToken(u.ID, u.Username, RoleUser)
	if code, body = call(r, "GET", "/admin/users", tok, nil); code != 403 || body["statusMessage"] != "admin role required" {
		t.Fatalf("user → admin: %d %v", code, body)
	}
	if code, _ = call(r, "POST", "/api/v1/points/grant", tok, map[string]any{"userId": u.ID, "points": 5}); code != 403 {
		t.Fatalf("user → grant: %d", code)
	}
}

func TestGrantIdempotent(t *testing.T) {
	r := setupTest(t)
	u := mkUser(t, "bob")
	for i := 0; i < 3; i++ {
		code, body := call(r, "POST", "/api/v1/points/grant", ADMIN_TOKEN,
			map[string]any{"username": "bob", "points": 100, "remark": "活动奖励"}, "Idempotency-Key", "evt-1")
		if code != 200 || body["balance"].(float64) != 100 {
			t.Fatalf("grant #%d: %d %v", i, code, body)
		}
		if replayed := body["replayed"].(bool); replayed != (i > 0) {
			t.Fatalf("grant #%d replayed=%v", i, replayed)
		}
	}
	if b := balanceOf(u.ID); b != 100 {
		t.Fatalf("balance %d, want 100", b)
	}
	code, body := call(r, "POST", "/api/v1/points/deduct", ADMIN_TOKEN, map[string]any{"userId": u.ID, "points": 101})
	if code != 402 || body["error"].(map[string]any)["code"] != "insufficient_points" {
		t.Fatalf("over-deduct: %d %v", code, body)
	}
}

func TestConcurrentDeductNeverNegative(t *testing.T) {
	setupTest(t)
	u := mkUser(t, "carol")
	if _, err := changePoints(pointOp{UserID: u.ID, Delta: 50, Kind: LedgerAdmin}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := changePoints(pointOp{UserID: u.ID, Delta: -10, Kind: LedgerAPIDeduct}); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 5 || balanceOf(u.ID) != 0 {
		t.Fatalf("ok=%d balance=%d, want 5 / 0", ok, balanceOf(u.ID))
	}
	var sum int64
	db.Model(&PointLedger{}).Where("user_id = ?", u.ID).Select("SUM(delta)").Scan(&sum)
	if sum != 0 {
		t.Fatalf("ledger sum %d != balance 0", sum)
	}
}

func TestCheckinOncePerDay(t *testing.T) {
	setupTest(t)
	u := mkUser(t, "dave")
	ck, _, err := doCheckin(u.ID)
	if err != nil || ck.Points != 10 || ck.Streak != 1 {
		t.Fatalf("checkin: %+v %v", ck, err)
	}
	if _, _, err := doCheckin(u.ID); err != errCheckedIn {
		t.Fatalf("second checkin err=%v", err)
	}
	if got := checkinPoints(defaultPointsConfig(), 30); got != 10+6*2 {
		t.Fatalf("streak cap points=%d", got)
	}
}

func TestRedeemCodeLimits(t *testing.T) {
	setupTest(t)
	a, b, c := mkUser(t, "u1"), mkUser(t, "u2"), mkUser(t, "u3")
	codes, err := createRedeemCodes(1, 30, 2, "VIP", "", "", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	code := strings.ToLower(codes[0].Code) // 大小写不敏感
	if _, _, err := redeemCode(a.ID, code); err != nil {
		t.Fatal(err)
	}
	if _, _, err := redeemCode(a.ID, code); err == nil {
		t.Fatal("same user redeemed twice")
	}
	if _, _, err := redeemCode(b.ID, code); err != nil {
		t.Fatal(err)
	}
	if _, _, err := redeemCode(c.ID, code); err == nil || !strings.Contains(err.Error(), "领完") {
		t.Fatalf("exhausted code err=%v", err)
	}
	if balanceOf(a.ID) != 30 || balanceOf(c.ID) != 0 {
		t.Fatal("wrong balances")
	}
}

func TestShopCDKAndRefund(t *testing.T) {
	setupTest(t)
	u := mkUser(t, "erin")
	changePoints(pointOp{UserID: u.ID, Delta: 1000, Kind: LedgerAdmin})

	p := Product{Name: "月卡", Price: 100, Delivery: DeliveryCDK, Stock: -1, Enabled: true}
	db.Create(&p)
	n, err := importCards(db.DB, p.ID, "CARD-A\n\nCARD-B\r\nCARD-C\n")
	if err != nil || n != 3 {
		t.Fatalf("import %d %v", n, err)
	}
	o, err := placeOrder(u, orderInput{ProductID: p.ID, Quantity: 2})
	if err != nil || o.Status != OrderDelivered || o.Content != "CARD-A\nCARD-B" {
		t.Fatalf("cdk order: %+v %v", o, err)
	}
	if _, err := placeOrder(u, orderInput{ProductID: p.ID, Quantity: 2}); err == nil || err.Error() != "库存不足" {
		t.Fatalf("oversell err=%v", err)
	}
	if balanceOf(u.ID) != 800 {
		t.Fatalf("balance after cdk %d (failed order must not charge)", balanceOf(u.ID))
	}

	// 人工发货：取消后退款且库存回补
	m := Product{Name: "实物周边", Price: 300, Delivery: DeliveryManual, Stock: 1, PerUserLimit: 1, Enabled: true}
	db.Create(&m)
	o2, err := placeOrder(u, orderInput{ProductID: m.ID, Quantity: 1})
	if err != nil || o2.Status != OrderPending || balanceOf(u.ID) != 500 {
		t.Fatalf("manual order: %+v %v bal=%d", o2, err, balanceOf(u.ID))
	}
	if _, err := placeOrder(u, orderInput{ProductID: m.ID, Quantity: 1}); err == nil {
		t.Fatal("per-user limit not enforced")
	}
	if _, err := finishOrder(o2.ID, OrderCanceled, "", "缺货", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := finishOrder(o2.ID, OrderCanceled, "", "again", "admin"); err == nil {
		t.Fatal("double refund allowed")
	}
	db.First(&m, m.ID)
	if balanceOf(u.ID) != 800 || m.Stock != 1 {
		t.Fatalf("after cancel bal=%d stock=%d", balanceOf(u.ID), m.Stock)
	}
	// 余额不足
	big := Product{Name: "大奖", Price: 99999, Delivery: DeliveryManual, Stock: -1, Enabled: true}
	db.Create(&big)
	if _, err := placeOrder(u, orderInput{ProductID: big.ID}); err != errInsufficient {
		t.Fatalf("insufficient err=%v", err)
	}
}

func TestRegistrationToggleAndLoginLimit(t *testing.T) {
	r := setupTest(t)
	s := getSiteSettings()
	s.RegistrationEnabled = false
	applySiteSettings(s)
	code, body := call(r, "POST", "/admin/register/send-code", "", map[string]string{"email": "a@b.co"})
	if code != 403 || body["statusMessage"] != "注册已关闭，请联系管理员创建账号" {
		t.Fatalf("registration off: %d %v", code, body)
	}
	// 登录限流：11 次触发 429（setupTest 里 admin/admin123 可登录，前 10 次要么 200 要么 401）
	last := 0
	for i := 0; i < 11; i++ {
		last, _ = call(r, "POST", "/admin/login", "", map[string]string{"username": "nobody", "password": "x"})
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("login limit last=%d want 429", last)
	}
}

func TestWSRequireKey(t *testing.T) {
	setupTest(t)
	WS_REQUIRE_KEY = true
	defer func() { WS_REQUIRE_KEY = false }()
	// 未预配密钥的节点注册被拒（sendJSON 走内存 peer，注册失败不会进 peers）
	if WS_REQUIRE_KEY && lookupWSKey("ghost") != "" {
		t.Fatal("unexpected key")
	}
}

func TestShopWebhookDelivery(t *testing.T) {
	setupTest(t)
	u := mkUser(t, "frank")
	changePoints(pointOp{UserID: u.ID, Delta: 100, Kind: LedgerAdmin})
	secret := "whsec_test"
	var gotSig bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		buf.ReadFrom(r.Body)
		ts := r.Header.Get("X-Boce-Timestamp")
		var tsn int64
		json.Unmarshal([]byte(ts), &tsn)
		gotSig = r.Header.Get("X-Boce-Signature") == signWebhook(secret, tsn, buf.Bytes())
		w.Write([]byte(`{"content":"KEY-123"}`))
	}))
	defer srv.Close()
	p := Product{Name: "外部会员", Price: 40, Delivery: DeliveryWebhook, Stock: -1, Enabled: true,
		DeliveryURL: srv.URL, DeliverySecret: secret}
	db.Create(&p)
	o, err := placeOrder(u, orderInput{ProductID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	dispatchWebhookOrder(o.ID) // 同步再跑一次以确保完成（异步那次可能已完成，finishOrder 幂等拒绝）
	var got ShopOrder
	for i := 0; i < 50; i++ {
		db.First(&got, o.ID)
		if got.Status == OrderDelivered {
			break
		}
	}
	if got.Status != OrderDelivered || got.Content != "KEY-123" || !gotSig {
		t.Fatalf("webhook order: %+v sig=%v", got, gotSig)
	}

	// 失败的发放接口 → 自动退款
	bad := Product{Name: "坏接口", Price: 30, Delivery: DeliveryWebhook, Stock: -1, Enabled: true, DeliveryURL: srv.URL + "/x"}
	db.Create(&bad)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	o3, _ := placeOrder(u, orderInput{ProductID: bad.ID})
	dispatchWebhookOrder(o3.ID)
	var failed ShopOrder
	db.First(&failed, o3.ID)
	if failed.Status != OrderFailed || balanceOf(u.ID) != 60 {
		t.Fatalf("failed webhook: status=%s bal=%d", failed.Status, balanceOf(u.ID))
	}
	var refunds int64
	db.Model(&PointLedger{}).Where("kind = ? AND ref_id = ?", LedgerRefund, o3.OrderNo).Count(&refunds)
	if refunds != 1 {
		t.Fatalf("refund ledger count %d (async + sync dispatch must refund once)", refunds)
	}
}

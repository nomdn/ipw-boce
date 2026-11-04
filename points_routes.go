package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 积分接口 ====================
//
// 用户（/admin 组，登录即可）：
//   GET  /admin/points/me                 余额 + 签到状态 + 规则摘要
//   GET  /admin/points/ledger             我的流水
//   POST /admin/points/checkin            每日签到
//   POST /admin/points/redeem {code}      使用积分兑换码
//   GET  /admin/shop/products             上架商品
//   POST /admin/shop/orders {productId,quantity,remark}
//   GET  /admin/shop/orders               我的订单
// 管理员（admin only）：/admin/points/admin/*、/admin/shop/admin/*（见 registerPointsAdminRoutes）
// 开放接口（/api/v1，admin 身份：静态 admin-token 或 admin 账号的 ipt_ Token）：见 registerPointsRestRoutes

func registerPointsRoutes(admin, restricted *gin.RouterGroup) {
	admin.GET("/points/me", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		cfg := getPointsConfig()
		ctx, cancel := dbCtx()
		defer cancel()
		var acc PointAccount
		db.WithContext(ctx).Where("user_id = ?", uid).Limit(1).Find(&acc)
		checked, streak, next := checkinState(uid)
		c.JSON(http.StatusOK, gin.H{"userId": uid, "balance": acc.Balance, "totalEarned": acc.TotalEarned,
			"totalSpent": acc.TotalSpent, "checkedInToday": checked, "streak": streak, "nextCheckinPoints": next,
			"rules": gin.H{"enabled": cfg.Enabled, "currencyName": cfg.CurrencyName, "checkinEnabled": cfg.CheckinEnabled,
				"checkinBase": cfg.CheckinBase, "checkinStreakBonus": cfg.CheckinStreakBonus,
				"checkinStreakMax": cfg.CheckinStreakMax, "probeCost": cfg.ProbeCost}})
	})

	admin.GET("/points/ledger", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		listLedger(c, uid)
	})

	admin.POST("/points/checkin", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			apiError(c, http.StatusBadRequest, "static token has no account")
			return
		}
		ck, led, err := doCheckin(uid)
		if err != nil {
			status := http.StatusBadRequest
			if err == errCheckedIn {
				status = http.StatusConflict
			}
			apiError(c, status, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"points": ck.Points, "streak": ck.Streak, "balance": led.BalanceAfter, "day": ck.Day})
	})

	admin.POST("/points/redeem", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		if uid == 0 {
			apiError(c, http.StatusBadRequest, "static token has no account")
			return
		}
		var body struct {
			Code string `json:"code"`
		}
		_ = c.ShouldBindJSON(&body)
		if !redeemLimiter.allow(fmt.Sprintf("u%d", uid)) {
			apiError(c, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
			return
		}
		rc, led, err := redeemCode(uid, body.Code)
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"points": rc.Points, "balance": led.BalanceAfter})
	})

	admin.GET("/shop/products", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var list []Product
		db.WithContext(ctx).Where("enabled = ?", true).Order("sort_order asc, id desc").Find(&list)
		fillAvailability(list)
		out := make([]gin.H, 0, len(list))
		for _, p := range list {
			out = append(out, gin.H{"id": p.ID, "name": p.Name, "description": p.Description, "imageUrl": p.ImageURL,
				"price": p.Price, "delivery": p.Delivery, "available": p.Available, "perUserLimit": p.PerUserLimit, "sold": p.Sold})
		}
		c.JSON(http.StatusOK, out)
	})

	admin.POST("/shop/orders", func(c *gin.Context) {
		u := meUser(c)
		if u == nil {
			return
		}
		var in orderInput
		if err := c.ShouldBindJSON(&in); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body")
			return
		}
		o, err := placeOrder(u, in)
		if err != nil {
			status := http.StatusBadRequest
			if err == errInsufficient {
				status = http.StatusPaymentRequired
			}
			apiError(c, status, err.Error())
			return
		}
		c.JSON(http.StatusOK, o)
	})

	admin.GET("/shop/orders", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		ctx, cancel := dbCtx()
		defer cancel()
		var list []ShopOrder
		db.WithContext(ctx).Where("user_id = ?", uid).Order("id desc").Limit(clampLimit(c.Query("limit"), 100)).Find(&list)
		c.JSON(http.StatusOK, list)
	})

	registerPointsAdminRoutes(restricted)
}

var redeemLimiter = newRateLimiter(20)

func listLedger(c *gin.Context, uid uint) {
	limit := clampLimit(c.Query("limit"), 50)
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	ctx, cancel := dbCtx()
	defer cancel()
	q := db.WithContext(ctx).Model(&PointLedger{})
	if uid > 0 {
		q = q.Where("user_id = ?", uid)
	} else if v := c.Query("userId"); v != "" {
		q = q.Where("user_id = ?", v)
	}
	if v := c.Query("kind"); v != "" {
		q = q.Where("kind = ?", v)
	}
	var total int64
	q.Count(&total)
	var rows []PointLedger
	q.Order("id desc").Offset(offset).Limit(limit).Find(&rows)
	c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "limit": limit, "offset": offset})
}

// ==================== 管理员 ====================

func registerPointsAdminRoutes(g *gin.RouterGroup) {
	pa := g.Group("/points/admin")

	pa.GET("/settings", func(c *gin.Context) { c.JSON(http.StatusOK, getPointsConfig()) })
	pa.PUT("/settings", func(c *gin.Context) {
		cfg := getPointsConfig()
		if err := c.ShouldBindJSON(&cfg); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if cfg.CheckinBase < 0 || cfg.CheckinStreakBonus < 0 || cfg.SignupBonus < 0 || cfg.ProbeCost < 0 || cfg.CheckinStreakMax < 0 {
			apiError(c, http.StatusBadRequest, "积分规则不能为负数")
			return
		}
		if strings.TrimSpace(cfg.CurrencyName) == "" {
			cfg.CurrencyName = "积分"
		}
		if err := savePointsConfig(cfg); err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, cfg)
	})

	pa.GET("/overview", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var s struct {
			Balance, Earned, Spent int64
		}
		db.WithContext(ctx).Model(&PointAccount{}).
			Select("COALESCE(SUM(balance),0) as balance, COALESCE(SUM(total_earned),0) as earned, COALESCE(SUM(total_spent),0) as spent").Scan(&s)
		var pending, processing, products int64
		db.WithContext(ctx).Model(&ShopOrder{}).Where("status = ?", OrderPending).Count(&pending)
		db.WithContext(ctx).Model(&ShopOrder{}).Where("status = ?", OrderProcessing).Count(&processing)
		db.WithContext(ctx).Model(&Product{}).Where("enabled = ?", true).Count(&products)
		var today int64
		db.WithContext(ctx).Model(&PointCheckin{}).Where("day = ?", todayStr(time.Now())).Count(&today)
		c.JSON(http.StatusOK, gin.H{"circulating": s.Balance, "issued": s.Earned, "spent": s.Spent,
			"pendingOrders": pending, "processingOrders": processing, "productsOnShelf": products, "checkinsToday": today})
	})

	// 手动增减：{userId|username, delta, remark}
	pa.POST("/adjust", func(c *gin.Context) {
		var body grantInput
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body")
			return
		}
		u, err := resolveUser(body.UserID, body.Username, body.Email)
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		if body.Delta == 0 || body.Delta > 1e9 || body.Delta < -1e9 {
			apiError(c, http.StatusBadRequest, "delta 须为非 0 整数且绝对值不超过 10 亿")
			return
		}
		led, err := changePoints(pointOp{UserID: u.ID, Delta: body.Delta, Kind: LedgerAdmin, Remark: body.Remark,
			Operator: currentUsername(c)})
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		log.Printf("[points] admin %s adjust user#%d delta=%d", currentUsername(c), u.ID, body.Delta)
		c.JSON(http.StatusOK, led)
	})

	pa.GET("/ledger", func(c *gin.Context) { listLedger(c, 0) })

	pa.GET("/accounts", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var users []User
		q := db.WithContext(ctx).Model(&User{})
		if kw := strings.TrimSpace(c.Query("q")); kw != "" {
			q = q.Where("username LIKE ? OR email LIKE ?", "%"+kw+"%", "%"+kw+"%")
		}
		q.Order("id asc").Limit(500).Find(&users)
		bal := balancesOf(users)
		out := make([]gin.H, 0, len(users))
		for _, u := range users {
			out = append(out, gin.H{"userId": u.ID, "username": u.Username, "email": u.Email, "role": u.Role, "balance": bal[u.ID]})
		}
		c.JSON(http.StatusOK, out)
	})

	// ---- 积分兑换码 ----
	pa.GET("/codes", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&RedeemCode{})
		if v := c.Query("batch"); v != "" {
			q = q.Where("batch = ?", v)
		}
		var list []RedeemCode
		q.Order("id desc").Limit(clampLimit(c.Query("limit"), 500)).Find(&list)
		c.JSON(http.StatusOK, list)
	})
	pa.POST("/codes", func(c *gin.Context) {
		var body struct {
			Count     int    `json:"count"`
			Points    int64  `json:"points"`
			MaxUses   int    `json:"maxUses"`
			Prefix    string `json:"prefix"`
			Code      string `json:"code"` // 自定义单个兑换码（count 忽略）
			ExpiresAt string `json:"expiresAt"`
			Remark    string `json:"remark"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body")
			return
		}
		codes, err := createRedeemCodes(body.Count, body.Points, body.MaxUses, body.Prefix, body.Code, body.ExpiresAt, body.Remark, currentUsername(c))
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"batch": codes[0].Batch, "count": len(codes), "codes": codes})
	})
	pa.PATCH("/codes/:id", func(c *gin.Context) {
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.Enabled == nil {
			apiError(c, http.StatusBadRequest, "enabled 必填")
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		db.WithContext(ctx).Model(&RedeemCode{}).Where("id = ?", idParam(c)).Update("enabled", *body.Enabled)
		c.JSON(http.StatusOK, gin.H{"id": idParam(c), "enabled": *body.Enabled})
	})
	pa.DELETE("/codes/:id", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		res := db.WithContext(ctx).Delete(&RedeemCode{}, idParam(c))
		c.JSON(http.StatusOK, gin.H{"deleted": res.RowsAffected > 0})
	})

	registerShopAdminRoutes(g)
}

type grantInput struct {
	UserID   uint   `json:"userId"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Delta    int64  `json:"delta"`
	Points   int64  `json:"points"`
	Remark   string `json:"remark"`
}

func createRedeemCodes(count int, points int64, maxUses int, prefix, custom, expires, remark, by string) ([]RedeemCode, error) {
	if points <= 0 || points > 1e9 {
		return nil, fmt.Errorf("points 须为 1 ~ 10 亿")
	}
	if maxUses <= 0 {
		maxUses = 1
	}
	var exp *time.Time
	if strings.TrimSpace(expires) != "" {
		t, ok := parseTimeArg(strings.TrimSpace(expires))
		if !ok {
			return nil, fmt.Errorf("expiresAt 须为 RFC3339 时间或 Unix 时间戳")
		}
		exp = &t
	}
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	if len(prefix) > 16 {
		return nil, fmt.Errorf("前缀最长 16 位")
	}
	batch := time.Now().Format("20060102-150405") + "-" + randHex(2)
	var list []RedeemCode
	if c := normalizeCode(custom); c != "" {
		if len(c) < 4 || len(c) > 64 {
			return nil, fmt.Errorf("自定义兑换码长度须为 4~64")
		}
		list = append(list, RedeemCode{Code: c})
	} else {
		if count <= 0 {
			count = 1
		}
		if count > 1000 {
			return nil, fmt.Errorf("单次最多生成 1000 个")
		}
		for i := 0; i < count; i++ {
			list = append(list, RedeemCode{Code: newCodeString(prefix)})
		}
	}
	for i := range list {
		list[i].Points, list[i].MaxUses, list[i].ExpiresAt = points, maxUses, exp
		list[i].Enabled, list[i].Batch, list[i].Remark, list[i].CreatedBy = true, batch, truncateStr(remark, 256), by
	}
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).CreateInBatches(list, 200).Error; err != nil {
		if isUniqueErr(err) {
			return nil, fmt.Errorf("兑换码已存在")
		}
		return nil, err
	}
	return list, nil
}

// ==================== 商城管理 ====================

type productInput struct {
	Name           *string `json:"name"`
	Description    *string `json:"description"`
	ImageURL       *string `json:"imageUrl"`
	Price          *int64  `json:"price"`
	Delivery       *string `json:"delivery"`
	Stock          *int    `json:"stock"`
	PerUserLimit   *int    `json:"perUserLimit"`
	Enabled        *bool   `json:"enabled"`
	SortOrder      *int    `json:"sortOrder"`
	DeliveryURL    *string `json:"deliveryUrl"`
	DeliverySecret *string `json:"deliverySecret"` // nil 不改；空串清除
	Cards          string  `json:"cards"`          // 新建时可顺带导入卡密（每行一条）
}

func (in *productInput) apply(p *Product) string {
	if in.Name != nil {
		p.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		p.Description = *in.Description
	}
	if in.ImageURL != nil {
		p.ImageURL = strings.TrimSpace(*in.ImageURL)
	}
	if in.Price != nil {
		p.Price = *in.Price
	}
	if in.Delivery != nil {
		p.Delivery = strings.TrimSpace(*in.Delivery)
	}
	if in.Stock != nil {
		p.Stock = *in.Stock
	}
	if in.PerUserLimit != nil {
		p.PerUserLimit = *in.PerUserLimit
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	if in.SortOrder != nil {
		p.SortOrder = *in.SortOrder
	}
	if in.DeliveryURL != nil {
		p.DeliveryURL = strings.TrimSpace(*in.DeliveryURL)
	}
	if in.DeliverySecret != nil {
		p.DeliverySecret = strings.TrimSpace(*in.DeliverySecret)
	}
	switch {
	case p.Name == "":
		return "商品名称必填"
	case p.Price < 0:
		return "价格不能为负"
	case p.Delivery != DeliveryCDK && p.Delivery != DeliveryWebhook && p.Delivery != DeliveryManual:
		return "delivery 只能是 cdk / webhook / manual"
	case p.Delivery == DeliveryWebhook && !strings.HasPrefix(p.DeliveryURL, "http://") && !strings.HasPrefix(p.DeliveryURL, "https://"):
		return "webhook 发货须填写 http(s) 发放接口地址"
	case p.Stock < -1:
		return "库存须 ≥ 0，或 -1 表示不限"
	case p.PerUserLimit < 0:
		return "每人限兑不能为负"
	}
	return ""
}

func splitCards(raw string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, truncateStr(line, 4096))
		}
	}
	return out
}

func importCards(tx *gorm.DB, productID uint, raw string) (int, error) {
	cards := splitCards(raw)
	if len(cards) == 0 {
		return 0, nil
	}
	if len(cards) > 5000 {
		return 0, fmt.Errorf("单次最多导入 5000 条卡密")
	}
	rows := make([]ProductCard, 0, len(cards))
	for _, cd := range cards {
		rows = append(rows, ProductCard{ProductID: productID, Content: cd})
	}
	return len(rows), tx.CreateInBatches(rows, 500).Error
}

func registerShopAdminRoutes(g *gin.RouterGroup) {
	sa := g.Group("/shop/admin")

	sa.GET("/products", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var list []Product
		db.WithContext(ctx).Order("sort_order asc, id desc").Find(&list)
		fillAvailability(list)
		c.JSON(http.StatusOK, list)
	})

	sa.POST("/products", func(c *gin.Context) {
		var in productInput
		if err := c.ShouldBindJSON(&in); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		p := Product{Delivery: DeliveryCDK, Stock: -1, Enabled: true}
		if msg := in.apply(&p); msg != "" {
			apiError(c, http.StatusBadRequest, msg)
			return
		}
		if p.Delivery == DeliveryWebhook && p.DeliverySecret == "" {
			p.DeliverySecret = "whsec_" + randHex(16) // 未填即自动生成签名密钥
		}
		imported := 0
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&p).Error; err != nil {
				return err
			}
			var err error
			if p.Delivery == DeliveryCDK {
				imported, err = importCards(tx, p.ID, in.Cards)
			}
			return err
		})
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		list := []Product{p}
		fillAvailability(list)
		c.JSON(http.StatusOK, gin.H{"product": list[0], "imported": imported, "deliverySecret": p.DeliverySecret})
	})

	sa.PUT("/products/:id", func(c *gin.Context) {
		var in productInput
		if err := c.ShouldBindJSON(&in); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		var p Product
		if err := db.WithContext(ctx).First(&p, idParam(c)).Error; err != nil {
			apiError(c, http.StatusNotFound, "商品不存在")
			return
		}
		if msg := in.apply(&p); msg != "" {
			apiError(c, http.StatusBadRequest, msg)
			return
		}
		if err := db.WithContext(ctx).Save(&p).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		list := []Product{p}
		fillAvailability(list)
		c.JSON(http.StatusOK, list[0])
	})

	sa.DELETE("/products/:id", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var n int64
		db.WithContext(ctx).Model(&ShopOrder{}).Where("product_id = ?", idParam(c)).Count(&n)
		if n > 0 {
			// 有订单的商品只下架，保留订单关联
			db.WithContext(ctx).Model(&Product{}).Where("id = ?", idParam(c)).Update("enabled", false)
			c.JSON(http.StatusOK, gin.H{"deleted": false, "disabled": true})
			return
		}
		db.WithContext(ctx).Where("product_id = ?", idParam(c)).Delete(&ProductCard{})
		res := db.WithContext(ctx).Delete(&Product{}, idParam(c))
		c.JSON(http.StatusOK, gin.H{"deleted": res.RowsAffected > 0})
	})

	// 重置签名密钥（明文只在本次返回）
	sa.POST("/products/:id/secret", func(c *gin.Context) {
		secret := "whsec_" + randHex(16)
		ctx, cancel := dbCtx()
		defer cancel()
		res := db.WithContext(ctx).Model(&Product{}).Where("id = ?", idParam(c)).Update("delivery_secret", secret)
		if res.RowsAffected == 0 {
			apiError(c, http.StatusNotFound, "商品不存在")
			return
		}
		c.JSON(http.StatusOK, gin.H{"deliverySecret": secret})
	})

	// 测试发放接口：发送 event=ping，不产生订单
	sa.POST("/products/:id/test", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var p Product
		if err := db.WithContext(ctx).First(&p, idParam(c)).Error; err != nil {
			apiError(c, http.StatusNotFound, "商品不存在")
			return
		}
		reply, err := postDeliveryWebhook(&p, map[string]any{"event": "ping", "productId": p.ID, "productName": p.Name, "ts": time.Now().Unix()})
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "reply": reply})
	})

	// 卡密
	sa.GET("/products/:id/cards", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Where("product_id = ?", idParam(c))
		switch c.Query("state") {
		case "unused":
			q = q.Where("order_id = 0")
		case "used":
			q = q.Where("order_id <> 0")
		}
		var cards []ProductCard
		q.Order("id desc").Limit(clampLimit(c.Query("limit"), 500)).Find(&cards)
		c.JSON(http.StatusOK, cards)
	})
	sa.POST("/products/:id/cards", func(c *gin.Context) {
		var body struct {
			Cards string `json:"cards"`
		}
		_ = c.ShouldBindJSON(&body)
		ctx, cancel := dbCtx()
		defer cancel()
		var p Product
		if err := db.WithContext(ctx).First(&p, idParam(c)).Error; err != nil || p.Delivery != DeliveryCDK {
			apiError(c, http.StatusBadRequest, "仅卡密类商品可导入卡密")
			return
		}
		n, err := importCards(db.WithContext(ctx), p.ID, body.Cards)
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"imported": n, "available": cardStock([]uint{p.ID})[p.ID]})
	})
	sa.DELETE("/cards/:id", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		res := db.WithContext(ctx).Where("id = ? AND order_id = 0", idParam(c)).Delete(&ProductCard{})
		if res.RowsAffected == 0 {
			apiError(c, http.StatusBadRequest, "卡密不存在或已发出")
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": true})
	})

	// 订单
	sa.GET("/orders", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&ShopOrder{})
		if v := c.Query("status"); v != "" {
			q = q.Where("status = ?", v)
		}
		if v := c.Query("productId"); v != "" {
			q = q.Where("product_id = ?", v)
		}
		if v := strings.TrimSpace(c.Query("q")); v != "" {
			q = q.Where("order_no LIKE ? OR username LIKE ?", "%"+v+"%", "%"+v+"%")
		}
		var list []ShopOrder
		q.Order("id desc").Limit(clampLimit(c.Query("limit"), 200)).Find(&list)
		c.JSON(http.StatusOK, list)
	})
	sa.POST("/orders/:id/deliver", func(c *gin.Context) {
		var body struct {
			Content string `json:"content"`
		}
		_ = c.ShouldBindJSON(&body)
		if strings.TrimSpace(body.Content) == "" {
			apiError(c, http.StatusBadRequest, "请填写发货内容")
			return
		}
		o, err := finishOrder(idParam(c), OrderDelivered, body.Content, "", currentUsername(c))
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, o)
	})
	sa.POST("/orders/:id/cancel", func(c *gin.Context) {
		var body struct {
			Reason string `json:"reason"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.Reason == "" {
			body.Reason = "管理员取消"
		}
		o, err := finishOrder(idParam(c), OrderCanceled, "", body.Reason, currentUsername(c))
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.JSON(http.StatusOK, o)
	})
	// 重新推送发放接口（processing 订单）
	sa.POST("/orders/:id/retry", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var o ShopOrder
		if err := db.WithContext(ctx).First(&o, idParam(c)).Error; err != nil || o.Status != OrderProcessing {
			apiError(c, http.StatusBadRequest, "只有处理中的 webhook 订单可以重新推送")
			return
		}
		dispatchWebhookOrder(o.ID)
		db.WithContext(ctx).First(&o, o.ID)
		c.JSON(http.StatusOK, o)
	})
}

// ==================== 开放接口 /api/v1（需 admin 身份） ====================
//
//   POST /api/v1/points/grant   {userId|username|email, points, remark}  Header Idempotency-Key 可选
//   POST /api/v1/points/deduct  同上（余额不足 402 insufficient_points）
//   GET  /api/v1/points/balance?userId=|username=|email=
//   GET  /api/v1/points/ledger?userId=&kind=&limit=&offset=
//   POST /api/v1/points/codes   {count, points, maxUses, prefix, code, expiresAt, remark} 生成积分兑换码
//   GET  /api/v1/shop/orders/:orderNo
//   POST /api/v1/shop/orders/:orderNo/deliver {content}   外部发放系统回调：发货完成
//   POST /api/v1/shop/orders/:orderNo/fail    {reason}    外部发放系统回调：失败并退款

func registerPointsRestRoutes(g *gin.RouterGroup) {
	open := g.Group("", adminOnly())

	pointsChange := func(sign int64, kind string) gin.HandlerFunc {
		return func(c *gin.Context) {
			var body grantInput
			if err := c.ShouldBindJSON(&body); err != nil {
				restError(c, http.StatusBadRequest, "bad_request", "invalid body: "+err.Error())
				return
			}
			amount := body.Points
			if amount == 0 {
				amount = body.Delta
			}
			if amount <= 0 || amount > 1e9 {
				restError(c, http.StatusBadRequest, "bad_request", "points 须为 1 ~ 10 亿的正整数")
				return
			}
			u, err := resolveUser(body.UserID, body.Username, body.Email)
			if err != nil {
				restError(c, http.StatusNotFound, "not_found", err.Error())
				return
			}
			key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
			if key != "" {
				key = kind + ":" + key
			}
			led, err := changePoints(pointOp{UserID: u.ID, Delta: sign * amount, Kind: kind, RefType: "api",
				Remark: body.Remark, Operator: currentUsername(c), IdemKey: key})
			switch err {
			case nil:
				c.JSON(http.StatusOK, gin.H{"replayed": false, "ledger": led, "balance": led.BalanceAfter})
			case errDuplicate:
				prev := ledgerByIdemKey(key)
				if prev == nil {
					restError(c, http.StatusConflict, "conflict", "duplicate idempotency key")
					return
				}
				c.JSON(http.StatusOK, gin.H{"replayed": true, "ledger": prev, "balance": balanceOf(prev.UserID)})
			case errInsufficient:
				restError(c, http.StatusPaymentRequired, "insufficient_points", err.Error())
			default:
				restError(c, http.StatusInternalServerError, "internal", err.Error())
			}
		}
	}
	open.POST("/points/grant", pointsChange(1, LedgerAPIGrant))
	open.POST("/points/deduct", pointsChange(-1, LedgerAPIDeduct))

	open.GET("/points/balance", func(c *gin.Context) {
		id, _ := strconv.ParseUint(c.Query("userId"), 10, 64)
		u, err := resolveUser(uint(id), c.Query("username"), c.Query("email"))
		if err != nil {
			restError(c, http.StatusNotFound, "not_found", err.Error())
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		var acc PointAccount
		db.WithContext(ctx).Where("user_id = ?", u.ID).Limit(1).Find(&acc)
		c.JSON(http.StatusOK, gin.H{"userId": u.ID, "username": u.Username, "balance": acc.Balance,
			"totalEarned": acc.TotalEarned, "totalSpent": acc.TotalSpent})
	})
	open.GET("/points/ledger", func(c *gin.Context) { listLedger(c, 0) })

	open.POST("/points/codes", func(c *gin.Context) {
		var body struct {
			Count     int    `json:"count"`
			Points    int64  `json:"points"`
			MaxUses   int    `json:"maxUses"`
			Prefix    string `json:"prefix"`
			Code      string `json:"code"`
			ExpiresAt string `json:"expiresAt"`
			Remark    string `json:"remark"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			restError(c, http.StatusBadRequest, "bad_request", "invalid body")
			return
		}
		codes, err := createRedeemCodes(body.Count, body.Points, body.MaxUses, body.Prefix, body.Code, body.ExpiresAt, body.Remark, currentUsername(c))
		if err != nil {
			restError(c, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		out := make([]string, 0, len(codes))
		for _, rc := range codes {
			out = append(out, rc.Code)
		}
		c.JSON(http.StatusOK, gin.H{"batch": codes[0].Batch, "points": body.Points, "maxUses": codes[0].MaxUses, "codes": out})
	})

	open.GET("/shop/orders/:orderNo", func(c *gin.Context) {
		o, err := orderByNo(c.Param("orderNo"))
		if err != nil {
			restError(c, http.StatusNotFound, "not_found", err.Error())
			return
		}
		c.JSON(http.StatusOK, o)
	})
	open.POST("/shop/orders/:orderNo/deliver", func(c *gin.Context) {
		var body struct {
			Content string `json:"content"`
		}
		_ = c.ShouldBindJSON(&body)
		o, err := orderByNo(c.Param("orderNo"))
		if err != nil {
			restError(c, http.StatusNotFound, "not_found", err.Error())
			return
		}
		if o.Status == OrderDelivered {
			c.JSON(http.StatusOK, o) // 幂等：重复回调直接返回
			return
		}
		if strings.TrimSpace(body.Content) == "" {
			body.Content = "已由发放接口完成发货"
		}
		o, err = finishOrder(o.ID, OrderDelivered, body.Content, "", currentUsername(c))
		if err != nil {
			restError(c, http.StatusConflict, "conflict", err.Error())
			return
		}
		c.JSON(http.StatusOK, o)
	})
	open.POST("/shop/orders/:orderNo/fail", func(c *gin.Context) {
		var body struct {
			Reason string `json:"reason"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.Reason == "" {
			body.Reason = "发放接口回报失败"
		}
		o, err := orderByNo(c.Param("orderNo"))
		if err != nil {
			restError(c, http.StatusNotFound, "not_found", err.Error())
			return
		}
		o, err = finishOrder(o.ID, OrderFailed, "", body.Reason, currentUsername(c))
		if err != nil {
			restError(c, http.StatusConflict, "conflict", err.Error())
			return
		}
		c.JSON(http.StatusOK, o)
	})
}

package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ==================== 兑换码（积分 CDK）与商城 ====================
//
// 兑换码：管理员批量生成，用户输入后领取积分；每码可设总次数，同一用户同码只能用一次。
// 商品三种发货方式：
//   - cdk     卡密库存：管理员导入卡密，兑换时原子分配，立即展示给用户；
//   - webhook 发放接口：兑换后带 HMAC 签名 POST 到管理员配置的 URL，由外部系统发货；
//             可同步返回 {"content":"..."} 直接完成，或返回 {"status":"pending"} 之后回调
//             POST /api/v1/shop/orders/:orderNo/deliver 完成（/fail 失败并退款）；
//   - manual  人工发货：生成订单，管理员在后台填写发货内容。
// 失败/取消的订单自动退还积分（流水 kind=refund）。

type RedeemCode struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	Code      string     `gorm:"uniqueIndex;size:64" json:"code"`
	Points    int64      `json:"points"`
	MaxUses   int        `json:"maxUses"`
	UsedCount int        `json:"usedCount"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Enabled   bool       `json:"enabled"`
	Batch     string     `gorm:"size:64;index" json:"batch"`
	Remark    string     `gorm:"size:256" json:"remark,omitempty"`
	CreatedBy string     `gorm:"size:64" json:"createdBy,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

type RedeemCodeUse struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CodeID    uint      `gorm:"uniqueIndex:idx_code_user,priority:1" json:"codeId"`
	UserID    uint      `gorm:"uniqueIndex:idx_code_user,priority:2;index" json:"userId"`
	Points    int64     `json:"points"`
	CreatedAt time.Time `json:"createdAt"`
}

const (
	DeliveryCDK     = "cdk"
	DeliveryWebhook = "webhook"
	DeliveryManual  = "manual"
)

type Product struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	Name           string    `gorm:"size:128" json:"name"`
	Description    string    `gorm:"type:text" json:"description"`
	ImageURL       string    `gorm:"size:512" json:"imageUrl"`
	Price          int64     `json:"price"`
	Delivery       string    `gorm:"size:16" json:"delivery"`
	Stock          int       `json:"stock"` // webhook/manual：-1 = 不限；cdk 以未用卡密数为准
	PerUserLimit   int       `json:"perUserLimit"`
	Sold           int       `json:"sold"`
	Enabled        bool      `gorm:"index" json:"enabled"`
	SortOrder      int       `json:"sortOrder"`
	DeliveryURL    string    `gorm:"size:512" json:"deliveryUrl,omitempty"`
	DeliverySecret string    `gorm:"size:128" json:"-"`
	HasSecret      bool      `gorm:"-" json:"hasSecret"`
	Available      int       `gorm:"-" json:"available"` // 当前可兑数量（-1 = 不限）
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type ProductCard struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	ProductID uint       `gorm:"index:idx_card_avail,priority:1" json:"productId"`
	OrderID   uint       `gorm:"index:idx_card_avail,priority:2;default:0" json:"orderId"`
	Content   string     `gorm:"type:text" json:"content"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

const (
	OrderPending    = "pending"    // 待人工发货
	OrderProcessing = "processing" // 已推送外部发放接口，等待回调
	OrderDelivered  = "delivered"
	OrderFailed     = "failed"   // 发放失败，已退款
	OrderCanceled   = "canceled" // 管理员取消，已退款
)

type ShopOrder struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	OrderNo     string     `gorm:"uniqueIndex;size:32" json:"orderNo"`
	UserID      uint       `gorm:"index" json:"userId"`
	Username    string     `gorm:"size:64" json:"username"`
	ProductID   uint       `gorm:"index" json:"productId"`
	ProductName string     `gorm:"size:128" json:"productName"`
	Delivery    string     `gorm:"size:16" json:"delivery"`
	Quantity    int        `json:"quantity"`
	UnitPrice   int64      `json:"unitPrice"`
	TotalPoints int64      `json:"totalPoints"`
	Status      string     `gorm:"size:16;index" json:"status"`
	Content     string     `gorm:"type:text" json:"content,omitempty"` // 发货内容（卡密 / 外部返回 / 人工填写）
	UserRemark  string     `gorm:"size:512" json:"userRemark,omitempty"`
	AdminRemark string     `gorm:"size:512" json:"adminRemark,omitempty"`
	LastError   string     `gorm:"size:512" json:"lastError,omitempty"`
	DeliveredAt *time.Time `json:"deliveredAt,omitempty"`
	CreatedAt   time.Time  `gorm:"index" json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// ==================== 兑换码 ====================

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // 去掉易混淆的 0/O/1/I

// newCodeString 16 位（XXXX-XXXX-XXXX-XXXX），字母表 32 个字符，256 % 32 == 0 无取模偏差
func newCodeString(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	out := make([]byte, 0, 19)
	for i, v := range b {
		out = append(out, codeAlphabet[int(v)%len(codeAlphabet)])
		if i%4 == 3 && i != 15 {
			out = append(out, '-')
		}
	}
	if prefix != "" {
		return strings.ToUpper(prefix) + "-" + string(out)
	}
	return string(out)
}

func normalizeCode(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

func redeemCode(uid uint, raw string) (*RedeemCode, *PointLedger, error) {
	cfg := getPointsConfig()
	if !cfg.Enabled {
		return nil, nil, errors.New("积分功能未开启")
	}
	code := normalizeCode(raw)
	if code == "" {
		return nil, nil, errors.New("请输入兑换码")
	}
	var rc RedeemCode
	var led *PointLedger
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("code = ?", code).Limit(1).Find(&rc).Error; err != nil {
			return err
		}
		if rc.ID == 0 || !rc.Enabled {
			return errors.New("兑换码无效")
		}
		if rc.ExpiresAt != nil && time.Now().After(*rc.ExpiresAt) {
			return errors.New("兑换码已过期")
		}
		var used int64
		tx.Model(&RedeemCodeUse{}).Where("code_id = ? AND user_id = ?", rc.ID, uid).Count(&used)
		if used > 0 {
			return errors.New("你已使用过该兑换码")
		}
		res := tx.Model(&RedeemCode{}).Where("id = ? AND used_count < max_uses", rc.ID).
			Update("used_count", gorm.Expr("used_count + 1"))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errors.New("兑换码已被领完")
		}
		if err := tx.Create(&RedeemCodeUse{CodeID: rc.ID, UserID: uid, Points: rc.Points}).Error; err != nil {
			if isUniqueErr(err) {
				return errors.New("你已使用过该兑换码")
			}
			return err
		}
		var err error
		led, err = applyPoints(tx, pointOp{UserID: uid, Delta: rc.Points, Kind: LedgerRedeemCode, RefType: "redeem_code",
			RefID: strconv.FormatUint(uint64(rc.ID), 10), Remark: "兑换码 " + maskCode(rc.Code)})
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return &rc, led, nil
}

func maskCode(c string) string {
	if len(c) <= 8 {
		return c
	}
	return c[:4] + "****" + c[len(c)-4:]
}

// ==================== 商品库存 ====================

func cardStock(productIDs []uint) map[uint]int {
	out := map[uint]int{}
	if len(productIDs) == 0 {
		return out
	}
	var rows []struct {
		ProductID uint
		N         int
	}
	ctx, cancel := dbCtx()
	defer cancel()
	db.WithContext(ctx).Model(&ProductCard{}).Select("product_id, COUNT(*) as n").
		Where("product_id IN ? AND order_id = 0", productIDs).Group("product_id").Scan(&rows)
	for _, r := range rows {
		out[r.ProductID] = r.N
	}
	return out
}

func fillAvailability(list []Product) {
	var ids []uint
	for _, p := range list {
		if p.Delivery == DeliveryCDK {
			ids = append(ids, p.ID)
		}
	}
	stock := cardStock(ids)
	for i := range list {
		p := &list[i]
		p.HasSecret = p.DeliverySecret != ""
		if p.Delivery == DeliveryCDK {
			p.Available = stock[p.ID]
		} else {
			p.Available = p.Stock
		}
	}
}

// ==================== 下单兑换 ====================

func newOrderNo() string {
	return time.Now().Format("20060102150405") + strings.ToUpper(randHex(4))
}

type orderInput struct {
	ProductID uint   `json:"productId"`
	Quantity  int    `json:"quantity"`
	Remark    string `json:"remark"`
}

func placeOrder(u *User, in orderInput) (*ShopOrder, error) {
	cfg := getPointsConfig()
	if !cfg.Enabled {
		return nil, errors.New("积分功能未开启")
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	if in.Quantity > 20 {
		return nil, errors.New("单次最多兑换 20 件")
	}
	var order ShopOrder
	err := db.Transaction(func(tx *gorm.DB) error {
		var p Product
		if err := tx.First(&p, in.ProductID).Error; err != nil || !p.Enabled {
			return errors.New("商品不存在或已下架")
		}
		if p.PerUserLimit > 0 {
			var bought int64
			tx.Model(&ShopOrder{}).Where("user_id = ? AND product_id = ? AND status NOT IN ?", u.ID, p.ID,
				[]string{OrderFailed, OrderCanceled}).Select("COALESCE(SUM(quantity),0)").Scan(&bought)
			if int(bought)+in.Quantity > p.PerUserLimit {
				return fmt.Errorf("该商品每人限兑 %d 件（已兑 %d 件）", p.PerUserLimit, bought)
			}
		}
		total := p.Price * int64(in.Quantity)
		order = ShopOrder{OrderNo: newOrderNo(), UserID: u.ID, Username: u.Username, ProductID: p.ID,
			ProductName: p.Name, Delivery: p.Delivery, Quantity: in.Quantity, UnitPrice: p.Price,
			TotalPoints: total, Status: OrderPending, UserRemark: truncateStr(strings.TrimSpace(in.Remark), 512)}
		switch p.Delivery {
		case DeliveryWebhook:
			order.Status = OrderProcessing
		case DeliveryCDK:
			order.Status = OrderDelivered
		}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		// 库存：cdk 原子分配卡密；其余守卫式扣 stock（-1 不限）
		if p.Delivery == DeliveryCDK {
			var cards []ProductCard
			tx.Where("product_id = ? AND order_id = 0", p.ID).Order("id asc").Limit(in.Quantity * 3).Find(&cards)
			now := time.Now().UTC()
			var picked []string
			for _, cd := range cards {
				if len(picked) == in.Quantity {
					break
				}
				res := tx.Model(&ProductCard{}).Where("id = ? AND order_id = 0", cd.ID).
					Updates(map[string]any{"order_id": order.ID, "used_at": now})
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected == 1 {
					picked = append(picked, cd.Content)
				}
			}
			if len(picked) < in.Quantity {
				return errors.New("库存不足")
			}
			order.Content = strings.Join(picked, "\n")
			order.DeliveredAt = &now
		} else if p.Stock >= 0 {
			res := tx.Model(&Product{}).Where("id = ? AND stock >= ?", p.ID, in.Quantity).
				Update("stock", gorm.Expr("stock - ?", in.Quantity))
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return errors.New("库存不足")
			}
		}
		tx.Model(&Product{}).Where("id = ?", p.ID).Update("sold", gorm.Expr("sold + ?", in.Quantity))
		if _, err := applyPoints(tx, pointOp{UserID: u.ID, Delta: -total, Kind: LedgerOrder, RefType: "order",
			RefID: order.OrderNo, Remark: fmt.Sprintf("兑换 %s ×%d", p.Name, in.Quantity)}); err != nil {
			return err
		}
		return tx.Model(&ShopOrder{}).Where("id = ?", order.ID).
			Updates(map[string]any{"content": order.Content, "delivered_at": order.DeliveredAt}).Error
	})
	if err != nil {
		return nil, err
	}
	if order.Delivery == DeliveryWebhook {
		go dispatchWebhookOrder(order.ID)
	}
	if order.Delivery == DeliveryManual {
		go notifyAdmins("shop_order", "[1IPW-BOCE] 新的待发货订单 "+order.OrderNo,
			fmt.Sprintf("用户 %s 兑换了 %s ×%d（%d 积分），请在后台「积分管理 → 订单」发货。",
				order.Username, order.ProductName, order.Quantity, order.TotalPoints))
	}
	return &order, nil
}

// finishOrder 订单终结（delivered / failed / canceled）；失败与取消退还积分并回补库存。
// 只允许从 pending / processing 迁移，返回是否实际迁移。
func finishOrder(orderID uint, status, content, reason, operator string) (*ShopOrder, error) {
	var o ShopOrder
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&o, orderID).Error; err != nil {
			return errors.New("订单不存在")
		}
		if o.Status != OrderPending && o.Status != OrderProcessing {
			return fmt.Errorf("订单当前状态为 %s，不能再变更", o.Status)
		}
		updates := map[string]any{"status": status, "updated_at": time.Now().UTC()}
		res := tx.Model(&ShopOrder{}).Where("id = ? AND status IN ?", o.ID, []string{OrderPending, OrderProcessing})
		switch status {
		case OrderDelivered:
			now := time.Now().UTC()
			updates["content"] = content
			updates["delivered_at"] = now
			o.Content, o.DeliveredAt = content, &now
		case OrderFailed, OrderCanceled:
			updates["last_error"] = truncateStr(reason, 512)
			if operator != "" {
				updates["admin_remark"] = truncateStr(reason, 512)
			}
		}
		if r := res.Updates(updates); r.Error != nil || r.RowsAffected == 0 {
			return errors.New("订单状态已变化，请刷新")
		}
		if status == OrderFailed || status == OrderCanceled {
			var p Product
			if tx.First(&p, o.ProductID).Error == nil && p.Delivery != DeliveryCDK && p.Stock >= 0 {
				tx.Model(&Product{}).Where("id = ?", p.ID).Update("stock", gorm.Expr("stock + ?", o.Quantity))
			}
			tx.Model(&Product{}).Where("id = ? AND sold >= ?", o.ProductID, o.Quantity).Update("sold", gorm.Expr("sold - ?", o.Quantity))
			if _, err := applyPoints(tx, pointOp{UserID: o.UserID, Delta: o.TotalPoints, Kind: LedgerRefund, RefType: "order",
				RefID: o.OrderNo, Remark: "订单退款：" + truncateStr(reason, 200), Operator: operator,
				IdemKey: "refund:" + o.OrderNo}); err != nil && err != errDuplicate {
				return err
			}
		}
		o.Status = status
		return nil
	})
	if err != nil {
		return nil, err
	}
	if u, _ := userByID(o.UserID); u != nil {
		switch status {
		case OrderDelivered:
			createNotice(u.ID, "shop_delivered", 0, "订单已发货："+o.ProductName, "订单 "+o.OrderNo+" 已发货，可在「积分商城 → 我的订单」查看发货内容。")
		default:
			createNotice(u.ID, "shop_refund", 0, "订单已退款："+o.ProductName,
				fmt.Sprintf("订单 %s 未能完成（%s），%d 积分已退回。", o.OrderNo, reason, o.TotalPoints))
		}
	}
	return &o, nil
}

func orderByNo(no string) (*ShopOrder, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var o ShopOrder
	if err := db.WithContext(ctx).Where("order_no = ?", no).Limit(1).Find(&o).Error; err != nil || o.ID == 0 {
		return nil, errors.New("订单不存在")
	}
	return &o, nil
}

// ==================== Webhook 发放接口 ====================
//
// 请求：POST {deliveryUrl}，Content-Type: application/json
//   X-Boce-Event: order.created
//   X-Boce-Timestamp: <unix 秒>
//   X-Boce-Signature: sha256=<hex(HMAC-SHA256(secret, timestamp + "." + body))>
// 响应：2xx + {"content":"发货内容"} → 已发货；2xx + {"status":"pending"} → 等待回调；
//       2xx + {"status":"failed","message":"..."} 或非 2xx / 网络错误 → 失败并退款。

func signWebhook(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10) + "."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type webhookReply struct {
	Status  string `json:"status"`
	Content string `json:"content"`
	Message string `json:"message"`
}

func postDeliveryWebhook(p *Product, payload map[string]any) (*webhookReply, error) {
	if strings.TrimSpace(p.DeliveryURL) == "" {
		return nil, errors.New("商品未配置发放接口 URL")
	}
	body := mustRaw(payload)
	ts := time.Now().Unix()
	req, err := http.NewRequest(http.MethodPost, p.DeliveryURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Boce-Event", fmt.Sprint(payload["event"]))
	req.Header.Set("X-Boce-Timestamp", strconv.FormatInt(ts, 10))
	if p.DeliverySecret != "" {
		req.Header.Set("X-Boce-Signature", signWebhook(p.DeliverySecret, ts, body))
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("发放接口返回 HTTP %d: %s", resp.StatusCode, truncateStr(string(b), 200))
	}
	var r webhookReply
	if len(bytes.TrimSpace(b)) > 0 && json.Unmarshal(b, &r) != nil {
		r.Content = string(b) // 非 JSON 文本视为发货内容
	}
	return &r, nil
}

func orderPayload(o *ShopOrder, u *User) map[string]any {
	email := ""
	if u != nil {
		email = u.Email
	}
	return map[string]any{"event": "order.created", "orderNo": o.OrderNo, "productId": o.ProductID,
		"productName": o.ProductName, "quantity": o.Quantity, "totalPoints": o.TotalPoints,
		"userId": o.UserID, "username": o.Username, "email": email, "remark": o.UserRemark,
		"createdAt": o.CreatedAt.Unix(), "callback": "/api/v1/shop/orders/" + o.OrderNo + "/deliver"}
}

func dispatchWebhookOrder(orderID uint) {
	ctx, cancel := dbCtx()
	var o ShopOrder
	var p Product
	err1 := db.WithContext(ctx).First(&o, orderID).Error
	err2 := db.WithContext(ctx).First(&p, o.ProductID).Error
	cancel()
	if err1 != nil || err2 != nil || o.Status != OrderProcessing {
		return
	}
	u, _ := userByID(o.UserID)
	reply, err := postDeliveryWebhook(&p, orderPayload(&o, u))
	switch {
	case err != nil:
		log.Printf("[shop] webhook order %s failed: %v", o.OrderNo, err)
		finishOrder(o.ID, OrderFailed, "", err.Error(), "")
	case reply.Status == "failed":
		finishOrder(o.ID, OrderFailed, "", "发放接口拒绝："+reply.Message, "")
	case reply.Status == "pending":
		ctx, cancel := dbCtx()
		db.WithContext(ctx).Model(&ShopOrder{}).Where("id = ?", o.ID).Update("last_error", "")
		cancel()
	default:
		content := reply.Content
		if content == "" {
			content = "已由发放接口完成发货"
		}
		finishOrder(o.ID, OrderDelivered, content, "", "")
	}
}

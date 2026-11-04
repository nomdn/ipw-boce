package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== 用户捐赠节点 ====================
//
// 流程：用户提交申请（名称 / 地区 / 运营商 / 网络类型 idc|home / 栈 / 接入方式）
//   → 系统生成 nodeId 与 WS Key，用户把节点程序配好连到中心（WS 节点无需公网端口）
//   → 管理员审核：通过 = 写入节点池（node_defs）+ 发放一次性捐赠奖励；拒绝 = 记原因
//   → 每天（服务器本地时区）00:05 结算前一天：在线时长 ≥ donateMinOnlineHours 的已通过节点，
//     按网络类型发放每日在线奖励（流水幂等键 donate_day:<捐赠ID>:<日期>，重复结算不会重复发）
// 维护窗口内的离线不扣在线时长（计划停机不该罚捐赠者）。
//
// 状态：pending（待审核）/ approved（已上线入池）/ rejected / disabled（管理员停用）/ withdrawn（用户撤回）

const (
	NetIDC  = "idc"
	NetHome = "home"

	DonPending   = "pending"
	DonApproved  = "approved"
	DonRejected  = "rejected"
	DonDisabled  = "disabled"
	DonWithdrawn = "withdrawn"
)

type NodeDonation struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	UserID       uint       `gorm:"index" json:"userId"`
	Username     string     `gorm:"size:64" json:"username"`
	NodeID       string     `gorm:"uniqueIndex;size:128" json:"nodeId"`
	Label        string     `gorm:"size:256" json:"label"`
	Region       string     `gorm:"size:128" json:"region"`
	ISP          string     `gorm:"size:64" json:"isp"`
	NetType      string     `gorm:"size:8;index" json:"netType"` // idc | home
	Stack        string     `gorm:"size:16" json:"stack"`        // DualStack | IPv4 | IPv6
	Channel      string     `gorm:"size:8" json:"channel"`       // ws | http
	URL          string     `gorm:"size:512" json:"url,omitempty"`
	WSKey        string     `gorm:"size:128" json:"-"`
	APIKey       string     `gorm:"size:256" json:"-"`
	Bandwidth    string     `gorm:"size:64" json:"bandwidth,omitempty"`
	Contact      string     `gorm:"size:128" json:"contact,omitempty"`
	Remark       string     `gorm:"size:512" json:"remark,omitempty"`
	Status       string     `gorm:"size:16;index" json:"status"`
	ReviewNote   string     `gorm:"size:512" json:"reviewNote,omitempty"`
	ReviewedBy   string     `gorm:"size:64" json:"reviewedBy,omitempty"`
	ReviewedAt   *time.Time `json:"reviewedAt,omitempty"`
	BonusPaid    int64      `json:"bonusPaid"`
	DailyEarned  int64      `json:"dailyEarned"`
	RewardDays   int        `json:"rewardDays"`
	LastSettled  string     `gorm:"size:10" json:"lastSettled,omitempty"` // 最近结算日期 YYYY-MM-DD
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

func netTypeLabel(t string) string {
	if t == NetHome {
		return "家庭宽带"
	}
	return "机房"
}

func donateRates(cfg pointsConfig, netType string) (bonus, daily int64) {
	if netType == NetHome {
		return cfg.DonateBonusHome, cfg.DonateDailyHome
	}
	return cfg.DonateBonusIDC, cfg.DonateDailyIDC
}

var nodeIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,46}[a-z0-9]$`)

type donationInput struct {
	NodeID    string `json:"nodeId"`
	Label     string `json:"label"`
	Region    string `json:"region"`
	ISP       string `json:"isp"`
	NetType   string `json:"netType"`
	Stack     string `json:"stack"`
	Channel   string `json:"channel"`
	URL       string `json:"url"`
	APIKey    string `json:"apiKey"`
	Bandwidth string `json:"bandwidth"`
	Contact   string `json:"contact"`
	Remark    string `json:"remark"`
}

func (in *donationInput) normalize() error {
	in.Label, in.Region, in.ISP = strings.TrimSpace(in.Label), strings.TrimSpace(in.Region), strings.TrimSpace(in.ISP)
	in.NetType = strings.ToLower(strings.TrimSpace(in.NetType))
	in.Channel = strings.ToLower(strings.TrimSpace(in.Channel))
	in.URL = strings.TrimSpace(in.URL)
	if in.Region == "" || in.ISP == "" {
		return errors.New("请填写地区与运营商")
	}
	if in.NetType != NetIDC && in.NetType != NetHome {
		return errors.New("网络类型只能是 idc（机房）或 home（家庭宽带）")
	}
	switch in.Stack {
	case "", stackDualStack:
		in.Stack = stackDualStack
	case stackIPv4, stackIPv6:
	default:
		return errors.New("stack 只能是 DualStack / IPv4 / IPv6")
	}
	if in.Channel == "" {
		in.Channel = "ws"
	}
	if in.Channel != "ws" && in.Channel != "http" {
		return errors.New("接入方式只能是 ws 或 http")
	}
	if in.Channel == "http" && !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		return errors.New("HTTP 接入需填写节点的 http(s) 地址")
	}
	if in.Label == "" {
		in.Label = in.Region + " " + in.ISP
		if in.NetType == NetHome {
			in.Label += "（家宽）"
		}
	}
	return nil
}

func donationVisible(c *gin.Context) (*NodeDonation, bool) {
	ctx, cancel := dbCtx()
	defer cancel()
	var d NodeDonation
	if err := db.WithContext(ctx).First(&d, idParam(c)).Error; err != nil {
		apiError(c, http.StatusNotFound, "捐赠记录不存在")
		return nil, false
	}
	if uid, role, _ := currentUserFromCtx(c); role == RoleUser && d.UserID != uid {
		apiError(c, http.StatusForbidden, "not your donation")
		return nil, false
	}
	return &d, true
}

// donationView 出参：附接入信息与当前在线状态；本人/管理员才带 wsKey
func donationView(d *NodeDonation, withKey bool) gin.H {
	h := gin.H{}
	_ = jsonCopy(d, &h)
	h["netTypeLabel"] = netTypeLabel(d.NetType)
	n, ok := nodeSnapshot(d.NodeID)
	h["online"] = ok && n.Online
	h["version"] = n.Version
	h["lastSeenAt"] = n.LastSeenAt
	if withKey {
		h["wsKey"] = d.WSKey
		h["hasApiKey"] = d.APIKey != ""
	}
	return h
}

func publicWSURL(c *gin.Context) string {
	if PUBLIC_WS_URL != "" {
		return PUBLIC_WS_URL
	}
	host := c.Request.Host
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	return fmt.Sprintf("ws://%s:%d/ws", host, WS_PORT)
}

// approveDonation 审核通过：写节点池（含 WS Key / API Key）+ 发一次性奖励（幂等键 donate:<ID>）
func approveDonation(d *NodeDonation, operator, note string) error {
	cfg := getPointsConfig()
	bonus, _ := donateRates(cfg, d.NetType)
	now := time.Now().UTC()
	err := db.Transaction(func(tx *gorm.DB) error {
		var def NodeDef
		tx.Where("node_id = ?", d.NodeID).Limit(1).Find(&def)
		if def.ID != 0 && def.DonationID != d.ID {
			return fmt.Errorf("节点池里已存在同名节点 %s", d.NodeID)
		}
		def.NodeID, def.Label, def.WS, def.URL = d.NodeID, d.Label, d.Channel == "ws", d.URL
		def.Pool, def.Stack, def.Enabled = poolAPI, d.Stack, true
		def.WSKey, def.APIKey, def.DonationID, def.OwnerID, def.NetType = d.WSKey, d.APIKey, d.ID, d.UserID, d.NetType
		if err := tx.Save(&def).Error; err != nil {
			return err
		}
		up := map[string]any{"status": DonApproved, "review_note": truncateStr(note, 512), "reviewed_by": operator, "reviewed_at": &now}
		if d.BonusPaid == 0 && bonus > 0 && cfg.Enabled {
			_, err := applyPoints(tx, pointOp{UserID: d.UserID, Delta: bonus, Kind: LedgerDonate, RefType: "donation",
				RefID: strconv.FormatUint(uint64(d.ID), 10), Remark: fmt.Sprintf("捐赠%s节点 %s", netTypeLabel(d.NetType), d.Label),
				Operator: operator, IdemKey: fmt.Sprintf("donate:%d", d.ID)})
			if err != nil && err != errDuplicate {
				return err
			}
			if err == nil {
				up["bonus_paid"] = bonus
				d.BonusPaid = bonus
			}
		}
		return tx.Model(&NodeDonation{}).Where("id = ?", d.ID).Updates(up).Error
	})
	if err != nil {
		return err
	}
	_ = reloadNodePools()
	d.Status = DonApproved
	msg := fmt.Sprintf("你捐赠的节点「%s」已通过审核并加入节点池。", d.Label)
	if d.BonusPaid > 0 {
		msg += fmt.Sprintf("已发放捐赠奖励 %d %s。", d.BonusPaid, cfg.CurrencyName)
	}
	_, daily := donateRates(cfg, d.NetType)
	if daily > 0 {
		msg += fmt.Sprintf("此后每天在线满 %.0f 小时可获得 %d %s。", cfg.DonateMinOnlineHours, daily, cfg.CurrencyName)
	}
	createNotice(d.UserID, "donate", 0, "节点捐赠已通过："+d.Label, msg)
	return nil
}

// setDonationOffPool 拒绝 / 停用 / 撤回：从节点池移除（已发奖励不追回）
func setDonationOffPool(d *NodeDonation, status, operator, note string) error {
	now := time.Now().UTC()
	err := db.Transaction(func(tx *gorm.DB) error {
		tx.Where("donation_id = ?", d.ID).Delete(&NodeDef{})
		up := map[string]any{"status": status}
		if operator != "" {
			up["review_note"], up["reviewed_by"], up["reviewed_at"] = truncateStr(note, 512), operator, &now
		}
		return tx.Model(&NodeDonation{}).Where("id = ?", d.ID).Updates(up).Error
	})
	if err != nil {
		return err
	}
	_ = reloadNodePools()
	if wsSrv != nil {
		wsSrv.Kick(d.NodeID, "donation "+status)
	}
	return nil
}

// ==================== 每日在线奖励结算 ====================

// settleDonationDay 结算某天（本地时区）全部已通过节点；返回发放笔数。幂等。
func settleDonationDay(day time.Time) int {
	cfg := getPointsConfig()
	if !cfg.Enabled || !cfg.DonateEnabled {
		return 0
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 0, 1)
	dayStr := start.Format("2006-01-02")
	need := int64(cfg.DonateMinOnlineHours * 3600)
	if need <= 0 || need > 86400 {
		need = 86400
	}
	ctx, cancel := dbCtx()
	var list []NodeDonation
	db.WithContext(ctx).Where("status = ?", DonApproved).Find(&list)
	cancel()
	paid := 0
	for i := range list {
		d := &list[i]
		if d.ReviewedAt != nil && d.ReviewedAt.After(end) {
			continue // 当天之后才通过的不结算
		}
		_, daily := donateRates(cfg, d.NetType)
		online := nodeOnlineSeconds(d.NodeID, start.UTC(), end.UTC()) + maintenanceCredit(d.NodeID, start, end)
		if daily <= 0 || online < need {
			continue
		}
		err := db.Transaction(func(tx *gorm.DB) error {
			_, err := applyPoints(tx, pointOp{UserID: d.UserID, Delta: daily, Kind: LedgerDonateDay, RefType: "donation",
				RefID: strconv.FormatUint(uint64(d.ID), 10),
				Remark: fmt.Sprintf("%s 节点 %s 在线 %.1f 小时", dayStr, d.Label, float64(online)/3600),
				IdemKey: fmt.Sprintf("donate_day:%d:%s", d.ID, dayStr)})
			if err != nil {
				return err
			}
			return tx.Model(&NodeDonation{}).Where("id = ?", d.ID).Updates(map[string]any{
				"daily_earned": gorm.Expr("daily_earned + ?", daily), "reward_days": gorm.Expr("reward_days + 1"), "last_settled": dayStr,
			}).Error
		})
		if err == nil {
			paid++
		} else if err != errDuplicate {
			log.Printf("[donate] settle %s donation#%d: %v", dayStr, d.ID, err)
		}
	}
	if paid > 0 {
		log.Printf("[donate] settled %s: %d node(s) rewarded", dayStr, paid)
	}
	return paid
}

// maintenanceCredit 维护窗口内且节点离线的时长，按在线补回（粗粒度：按 5 分钟采样）
func maintenanceCredit(nodeID string, start, end time.Time) int64 {
	ctx, cancel := dbCtx()
	defer cancel()
	var ws []MaintenanceWindow
	db.WithContext(ctx).Where("scope IN ?", []string{"global", nodeID}).Find(&ws)
	if len(ws) == 0 {
		return 0
	}
	prior, in, err := loadUptimeEvents(start.UTC(), end.UTC(), []string{nodeID})
	if err != nil {
		return 0
	}
	iv, _ := uptimeForNode(nodeID, start.UTC(), end.UTC(), prior, in)
	var credit int64
	const step = 5 * time.Minute
	for t := start; t.Before(end); t = t.Add(step) {
		inMaint := false
		for i := range ws {
			if ws[i].covers(t) {
				inMaint = true
				break
			}
		}
		if !inMaint {
			continue
		}
		for _, x := range iv {
			if !t.Before(x.start) && t.Before(x.end) {
				credit += int64(step.Seconds())
				break
			}
		}
	}
	return credit
}

// donationSettleLoop 启动时补结算最近 3 天（幂等），之后每天 00:05 结算前一天
func donationSettleLoop() {
	time.Sleep(10 * time.Second)
	for i := 3; i >= 1; i-- {
		settleDonationDay(time.Now().AddDate(0, 0, -i))
	}
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), 0, 5, 0, 0, time.Local)
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		time.Sleep(time.Until(next))
		settleDonationDay(time.Now().AddDate(0, 0, -1))
	}
}

// ==================== 路由 ====================

func registerDonateRoutes(admin, restricted *gin.RouterGroup) {
	// 用户：规则 + 我的捐赠
	admin.GET("/donations/meta", func(c *gin.Context) {
		cfg := getPointsConfig()
		c.JSON(http.StatusOK, gin.H{"enabled": cfg.Enabled && cfg.DonateEnabled, "currencyName": cfg.CurrencyName,
			"bonus": gin.H{NetIDC: cfg.DonateBonusIDC, NetHome: cfg.DonateBonusHome},
			"daily": gin.H{NetIDC: cfg.DonateDailyIDC, NetHome: cfg.DonateDailyHome},
			"minOnlineHours": cfg.DonateMinOnlineHours, "maxPerUser": cfg.DonateMaxPerUser, "wsUrl": publicWSURL(c)})
	})
	admin.GET("/donations/mine", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		ctx, cancel := dbCtx()
		defer cancel()
		var list []NodeDonation
		db.WithContext(ctx).Where("user_id = ?", uid).Order("id desc").Find(&list)
		tok, base := userAgentToken(uid), publicBaseURL(c)
		out := make([]gin.H, 0, len(list))
		for i := range list {
			v := donationView(&list[i], true)
			// 一键安装命令只在审核通过后给出（WS 接入）
			if list[i].Status == DonApproved && list[i].Channel == "ws" {
				v["installCmd"] = agentInstallCmd(base, tok, list[i].NodeID)
			}
			out = append(out, v)
		}
		c.JSON(http.StatusOK, out)
	})
	admin.POST("/donations", func(c *gin.Context) {
		u := meUser(c)
		if u == nil {
			return
		}
		cfg := getPointsConfig()
		if !cfg.Enabled || !cfg.DonateEnabled {
			apiError(c, http.StatusForbidden, "节点捐赠暂未开放")
			return
		}
		var in donationInput
		if err := c.ShouldBindJSON(&in); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body")
			return
		}
		if err := in.normalize(); err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		if cfg.DonateMaxPerUser > 0 {
			var n int64
			db.WithContext(ctx).Model(&NodeDonation{}).Where("user_id = ? AND status IN ?", u.ID,
				[]string{DonPending, DonApproved, DonDisabled}).Count(&n)
			if n >= int64(cfg.DonateMaxPerUser) {
				apiError(c, http.StatusForbidden, fmt.Sprintf("每人最多捐赠 %d 个节点", cfg.DonateMaxPerUser))
				return
			}
		}
		nodeID := strings.ToLower(strings.TrimSpace(in.NodeID))
		if nodeID == "" {
			nodeID = fmt.Sprintf("d%d-%s", u.ID, randHex(3))
		}
		if !nodeIDRe.MatchString(nodeID) || nodeID == "global" {
			apiError(c, http.StatusBadRequest, "节点 ID 只能用 3~48 位小写字母、数字、连字符")
			return
		}
		var dup int64
		db.WithContext(ctx).Model(&NodeDef{}).Where("node_id = ?", nodeID).Count(&dup)
		if dup > 0 {
			apiError(c, http.StatusConflict, "节点 ID 已被占用")
			return
		}
		d := NodeDonation{UserID: u.ID, Username: u.Username, NodeID: nodeID, Label: truncateStr(in.Label, 256),
			Region: truncateStr(in.Region, 128), ISP: truncateStr(in.ISP, 64), NetType: in.NetType, Stack: in.Stack,
			Channel: in.Channel, URL: in.URL, WSKey: "nk_" + randHex(16), APIKey: strings.TrimSpace(in.APIKey),
			Bandwidth: truncateStr(in.Bandwidth, 64), Contact: truncateStr(in.Contact, 128), Remark: truncateStr(in.Remark, 512),
			Status: DonPending}
		if err := db.WithContext(ctx).Create(&d).Error; err != nil {
			if isUniqueErr(err) {
				apiError(c, http.StatusConflict, "节点 ID 已被占用")
				return
			}
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		// WS Key 先登记到凭据索引：节点在审核前即可连上来（只是不进节点池），方便管理员看到它在线
		_ = reloadNodePools()
		go notifyAdmins("donate_pending", "[1IPW-BOCE] 新的节点捐赠待审核",
			fmt.Sprintf("%s 捐赠了%s节点「%s」（%s %s，%s），请在「捐赠节点」审核。", u.Username, netTypeLabel(d.NetType), d.Label, d.Region, d.ISP, d.Stack))
		c.JSON(http.StatusOK, donationView(&d, true))
	})
	admin.PATCH("/donations/:id", func(c *gin.Context) {
		d, ok := donationVisible(c)
		if !ok {
			return
		}
		var in donationInput
		if err := c.ShouldBindJSON(&in); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body")
			return
		}
		if d.Status != DonPending && d.Status != DonRejected {
			apiError(c, http.StatusBadRequest, "只有待审核或被拒绝的申请可以修改")
			return
		}
		if err := in.normalize(); err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		up := map[string]any{"label": in.Label, "region": in.Region, "isp": in.ISP, "net_type": in.NetType, "stack": in.Stack,
			"channel": in.Channel, "url": in.URL, "bandwidth": in.Bandwidth, "contact": in.Contact, "remark": in.Remark, "status": DonPending}
		if strings.TrimSpace(in.APIKey) != "" {
			up["api_key"] = strings.TrimSpace(in.APIKey)
		}
		ctx, cancel := dbCtx()
		defer cancel()
		db.WithContext(ctx).Model(&NodeDonation{}).Where("id = ?", d.ID).Updates(up)
		db.WithContext(ctx).First(d, d.ID)
		c.JSON(http.StatusOK, donationView(d, true))
	})
	admin.POST("/donations/:id/rotate-key", func(c *gin.Context) {
		d, ok := donationVisible(c)
		if !ok {
			return
		}
		key := "nk_" + randHex(16)
		ctx, cancel := dbCtx()
		defer cancel()
		db.WithContext(ctx).Model(&NodeDonation{}).Where("id = ?", d.ID).Update("ws_key", key)
		db.WithContext(ctx).Model(&NodeDef{}).Where("donation_id = ?", d.ID).Update("ws_key", key)
		_ = reloadNodePools()
		if wsSrv != nil {
			wsSrv.Kick(d.NodeID, "key rotated")
		}
		c.JSON(http.StatusOK, gin.H{"wsKey": key})
	})
	admin.POST("/donations/:id/withdraw", func(c *gin.Context) {
		d, ok := donationVisible(c)
		if !ok {
			return
		}
		if d.Status == DonWithdrawn {
			c.JSON(http.StatusOK, gin.H{"status": DonWithdrawn})
			return
		}
		if err := setDonationOffPool(d, DonWithdrawn, "", ""); err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": DonWithdrawn})
	})

	// 管理员
	ra := restricted.Group("/donations/admin")
	ra.GET("", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&NodeDonation{})
		if v := c.Query("status"); v != "" {
			q = q.Where("status = ?", v)
		}
		if v := c.Query("netType"); v != "" {
			q = q.Where("net_type = ?", v)
		}
		var list []NodeDonation
		q.Order("id desc").Limit(500).Find(&list)
		out := make([]gin.H, 0, len(list))
		for i := range list {
			out = append(out, donationView(&list[i], true))
		}
		c.JSON(http.StatusOK, out)
	})
	ra.POST("/:id/review", func(c *gin.Context) {
		var b struct {
			Action string `json:"action"` // approve | reject | disable
			Note   string `json:"note"`
		}
		_ = c.ShouldBindJSON(&b)
		ctx, cancel := dbCtx()
		var d NodeDonation
		err := db.WithContext(ctx).First(&d, idParam(c)).Error
		cancel()
		if err != nil {
			apiError(c, http.StatusNotFound, "捐赠记录不存在")
			return
		}
		op := currentUsername(c)
		switch b.Action {
		case "approve":
			if d.Status == DonWithdrawn {
				apiError(c, http.StatusBadRequest, "用户已撤回该捐赠")
				return
			}
			err = approveDonation(&d, op, b.Note)
		case "reject", "disable":
			st := DonRejected
			if b.Action == "disable" {
				st = DonDisabled
			}
			err = setDonationOffPool(&d, st, op, b.Note)
			if err == nil {
				title := "节点捐赠未通过：" + d.Label
				if st == DonDisabled {
					title = "捐赠节点已被停用：" + d.Label
				}
				createNotice(d.UserID, "donate", 0, title, "原因："+strings.TrimSpace(b.Note+" "))
			}
		default:
			apiError(c, http.StatusBadRequest, "action 只能是 approve / reject / disable")
			return
		}
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		ctx, cancel = dbCtx()
		defer cancel()
		db.WithContext(ctx).First(&d, d.ID)
		c.JSON(http.StatusOK, donationView(&d, true))
	})
	// 手动结算某天（幂等，重复点击不会重复发）
	ra.POST("/settle", func(c *gin.Context) {
		var b struct {
			Date string `json:"date"`
		}
		_ = c.ShouldBindJSON(&b)
		day := time.Now().AddDate(0, 0, -1)
		if b.Date != "" {
			t, err := time.ParseInLocation("2006-01-02", b.Date, time.Local)
			if err != nil {
				apiError(c, http.StatusBadRequest, "date 格式为 YYYY-MM-DD")
				return
			}
			if todayStr(t) >= todayStr(time.Now()) {
				apiError(c, http.StatusBadRequest, "只能结算已经过去的日期")
				return
			}
			day = t
		}
		c.JSON(http.StatusOK, gin.H{"date": todayStr(day), "rewarded": settleDonationDay(day)})
	})
}

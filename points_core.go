package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ==================== 积分体系：账户 / 流水 / 设置 ====================
//
// 所有余额变动都走 applyPoints（同一事务内：守卫式扣减 + 写流水），保证：
//   - 余额永不为负：扣减用 UPDATE ... WHERE balance >= ?，RowsAffected=0 即余额不足；
//   - 每笔变动都有流水，balance_after 为变动后的余额快照；
//   - 外部发放可带幂等键（idem_key 唯一索引），同一键重复调用只入账一次。

type PointAccount struct {
	UserID      uint      `gorm:"primaryKey;autoIncrement:false" json:"userId"`
	Balance     int64     `json:"balance"`
	TotalEarned int64     `json:"totalEarned"`
	TotalSpent  int64     `json:"totalSpent"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// 流水类型
const (
	LedgerCheckin    = "checkin"     // 每日签到
	LedgerSignup     = "signup"      // 注册奖励
	LedgerRedeemCode = "redeem_code" // 兑换码
	LedgerAdmin      = "admin"       // 管理员手动增减
	LedgerAPIGrant   = "api_grant"   // 外部接口发放
	LedgerAPIDeduct  = "api_deduct"  // 外部接口扣减
	LedgerOrder      = "order"       // 商城兑换
	LedgerRefund     = "refund"      // 订单退款
	LedgerProbe      = "probe"       // 一键拨测消耗
	LedgerProbeBack  = "probe_back"  // 拨测失败退还
	LedgerDonate     = "donate"      // 捐赠节点审核通过奖励
	LedgerDonateDay  = "donate_day"  // 捐赠节点每日在线奖励
)

type PointLedger struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	UserID       uint      `gorm:"index" json:"userId"`
	Delta        int64     `json:"delta"`
	BalanceAfter int64     `json:"balanceAfter"`
	Kind         string    `gorm:"size:24;index" json:"kind"`
	RefType      string    `gorm:"size:24" json:"refType,omitempty"`
	RefID        string    `gorm:"size:64" json:"refId,omitempty"`
	Remark       string    `gorm:"size:256" json:"remark,omitempty"`
	Operator     string    `gorm:"size:64" json:"operator,omitempty"`
	IdemKey      *string   `gorm:"size:128;uniqueIndex" json:"idempotencyKey,omitempty"`
	CreatedAt    time.Time `gorm:"index" json:"createdAt"`
}

type PointCheckin struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"uniqueIndex:idx_checkin_day,priority:1" json:"userId"`
	Day       string    `gorm:"uniqueIndex:idx_checkin_day,priority:2;size:10" json:"day"` // 服务器本地时区 YYYY-MM-DD
	Streak    int       `json:"streak"`
	Points    int64     `json:"points"`
	CreatedAt time.Time `json:"createdAt"`
}

type PointSetting struct {
	Key       string    `gorm:"primaryKey;size:64" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// pointsConfig 积分规则（存 point_settings key=rules，JSON）
type pointsConfig struct {
	Enabled            bool   `json:"enabled"`            // 总开关：关闭后签到/兑换/商城不可用（发放接口与流水仍可查）
	CurrencyName       string `json:"currencyName"`       // 展示名，缺省"积分"
	CheckinEnabled     bool   `json:"checkinEnabled"`     // 签到开关
	CheckinBase        int64  `json:"checkinBase"`        // 签到基础积分
	CheckinStreakBonus int64  `json:"checkinStreakBonus"` // 连续签到每天额外加成
	CheckinStreakMax   int    `json:"checkinStreakMax"`   // 加成封顶天数
	SignupBonus        int64  `json:"signupBonus"`        // 自助注册奖励（0 = 不发）
	ProbeCost          int64  `json:"probeCost"`          // 普通用户一键拨测每次消耗（0 = 免费；admin 不扣）

	// ---- 捐赠节点（见 donate.go）：机房 idc / 家庭宽带 home 分开定价 ----
	DonateEnabled        bool    `json:"donateEnabled"`        // 是否开放用户捐赠节点
	DonateBonusIDC       int64   `json:"donateBonusIdc"`       // 机房节点审核通过一次性奖励
	DonateBonusHome      int64   `json:"donateBonusHome"`      // 家庭宽带节点审核通过一次性奖励
	DonateDailyIDC       int64   `json:"donateDailyIdc"`       // 机房节点每在线满一天奖励
	DonateDailyHome      int64   `json:"donateDailyHome"`      // 家庭宽带节点每在线满一天奖励
	DonateMinOnlineHours float64 `json:"donateMinOnlineHours"` // 当天在线满多少小时才算"在线一天"
	DonateMaxPerUser     int     `json:"donateMaxPerUser"`     // 每人最多捐赠节点数（0 = 不限）
}

func defaultPointsConfig() pointsConfig {
	return pointsConfig{Enabled: true, CurrencyName: "积分", CheckinEnabled: true, CheckinBase: 10,
		CheckinStreakBonus: 2, CheckinStreakMax: 7, SignupBonus: 50, ProbeCost: 0,
		DonateEnabled: true, DonateBonusIDC: 500, DonateBonusHome: 200, DonateDailyIDC: 50, DonateDailyHome: 20,
		DonateMinOnlineHours: 20, DonateMaxPerUser: 5}
}

var (
	pointsConfMu sync.RWMutex
	pointsConf   = defaultPointsConfig()
)

func loadPointsConfig() {
	ctx, cancel := dbCtx()
	defer cancel()
	var row PointSetting
	// key 在 MySQL 是保留字：用 clause.Eq 让 GORM 按方言加引号
	db.WithContext(ctx).Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: "rules"}).Limit(1).Find(&row)
	cfg := defaultPointsConfig()
	if row.Value != "" {
		_ = json.Unmarshal([]byte(row.Value), &cfg)
	}
	pointsConfMu.Lock()
	pointsConf = cfg
	pointsConfMu.Unlock()
}

func getPointsConfig() pointsConfig {
	pointsConfMu.RLock()
	defer pointsConfMu.RUnlock()
	return pointsConf
}

func savePointsConfig(cfg pointsConfig) error {
	ctx, cancel := dbCtx()
	defer cancel()
	row := PointSetting{Key: "rules", Value: string(mustRaw(cfg)), UpdatedAt: time.Now().UTC()}
	if err := db.WithContext(ctx).Save(&row).Error; err != nil {
		return err
	}
	pointsConfMu.Lock()
	pointsConf = cfg
	pointsConfMu.Unlock()
	return nil
}

var (
	errInsufficient = errors.New("积分余额不足")
	errDuplicate    = errors.New("duplicate idempotency key")
)

// pointOp 一次余额变动
type pointOp struct {
	UserID   uint
	Delta    int64
	Kind     string
	RefType  string
	RefID    string
	Remark   string
	Operator string
	IdemKey  string
}

// applyPoints 在 tx 内完成一次余额变动并写流水；返回流水（含变动后余额）。
// 扣减时余额不足返回 errInsufficient；幂等键冲突返回 errDuplicate（调用方回放已有流水）。
func applyPoints(tx *gorm.DB, op pointOp) (*PointLedger, error) {
	if op.UserID == 0 {
		return nil, errors.New("invalid user")
	}
	if op.Delta == 0 {
		return nil, errors.New("delta must not be 0")
	}
	if op.IdemKey != "" {
		var n int64
		tx.Model(&PointLedger{}).Where("idem_key = ?", op.IdemKey).Count(&n)
		if n > 0 {
			return nil, errDuplicate
		}
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&PointAccount{UserID: op.UserID}).Error; err != nil {
		return nil, err
	}
	var res *gorm.DB
	if op.Delta > 0 {
		res = tx.Model(&PointAccount{}).Where("user_id = ?", op.UserID).Updates(map[string]any{
			"balance":      gorm.Expr("balance + ?", op.Delta),
			"total_earned": gorm.Expr("total_earned + ?", op.Delta),
			"updated_at":   time.Now().UTC(),
		})
	} else {
		res = tx.Model(&PointAccount{}).Where("user_id = ? AND balance >= ?", op.UserID, -op.Delta).Updates(map[string]any{
			"balance":     gorm.Expr("balance + ?", op.Delta),
			"total_spent": gorm.Expr("total_spent + ?", -op.Delta),
			"updated_at":  time.Now().UTC(),
		})
	}
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, errInsufficient
	}
	var acc PointAccount
	if err := tx.First(&acc, "user_id = ?", op.UserID).Error; err != nil {
		return nil, err
	}
	l := &PointLedger{UserID: op.UserID, Delta: op.Delta, BalanceAfter: acc.Balance, Kind: op.Kind,
		RefType: op.RefType, RefID: truncateStr(op.RefID, 64), Remark: truncateStr(op.Remark, 256),
		Operator: truncateStr(op.Operator, 64)}
	if op.IdemKey != "" {
		k := truncateStr(op.IdemKey, 128)
		l.IdemKey = &k
	}
	if err := tx.Create(l).Error; err != nil {
		if isUniqueErr(err) {
			return nil, errDuplicate
		}
		return nil, err
	}
	return l, nil
}

// changePoints 独立事务包装
func changePoints(op pointOp) (*PointLedger, error) {
	var out *PointLedger
	err := db.Transaction(func(tx *gorm.DB) error {
		l, err := applyPoints(tx, op)
		out = l
		return err
	})
	return out, err
}

func ledgerByIdemKey(key string) *PointLedger {
	ctx, cancel := dbCtx()
	defer cancel()
	var l PointLedger
	if db.WithContext(ctx).Where("idem_key = ?", key).Limit(1).Find(&l).Error != nil || l.ID == 0 {
		return nil
	}
	return &l
}

func balanceOf(uid uint) int64 {
	ctx, cancel := dbCtx()
	defer cancel()
	var acc PointAccount
	db.WithContext(ctx).Where("user_id = ?", uid).Limit(1).Find(&acc)
	return acc.Balance
}

func balancesOf(users []User) map[uint]int64 {
	out := map[uint]int64{}
	if len(users) == 0 {
		return out
	}
	ids := make([]uint, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var accs []PointAccount
	db.WithContext(ctx).Where("user_id IN ?", ids).Find(&accs)
	for _, a := range accs {
		out[a.UserID] = a.Balance
	}
	return out
}

func grantSignupBonus(uid uint) {
	cfg := getPointsConfig()
	if !cfg.Enabled || cfg.SignupBonus <= 0 {
		return
	}
	if _, err := changePoints(pointOp{UserID: uid, Delta: cfg.SignupBonus, Kind: LedgerSignup, Remark: "注册奖励",
		IdemKey: fmt.Sprintf("signup:%d", uid)}); err != nil && err != errDuplicate {
		log.Printf("[points] signup bonus user#%d failed: %v", uid, err)
	}
}

// ==================== 签到 ====================

func todayStr(t time.Time) string { return t.Local().Format("2006-01-02") }

func checkinPoints(cfg pointsConfig, streak int) int64 {
	extra := streak - 1
	if cfg.CheckinStreakMax > 0 && extra > cfg.CheckinStreakMax-1 {
		extra = cfg.CheckinStreakMax - 1
	}
	if extra < 0 {
		extra = 0
	}
	return cfg.CheckinBase + int64(extra)*cfg.CheckinStreakBonus
}

// checkinState 今日是否已签、当前连续天数、下次签到可得积分
func checkinState(uid uint) (bool, int, int64) {
	cfg := getPointsConfig()
	now := time.Now()
	ctx, cancel := dbCtx()
	defer cancel()
	var last PointCheckin
	db.WithContext(ctx).Where("user_id = ?", uid).Order("day desc").Limit(1).Find(&last)
	today, yesterday := todayStr(now), todayStr(now.AddDate(0, 0, -1))
	switch last.Day {
	case today:
		return true, last.Streak, checkinPoints(cfg, last.Streak+1)
	case yesterday:
		return false, last.Streak, checkinPoints(cfg, last.Streak+1)
	}
	return false, 0, checkinPoints(cfg, 1)
}

var errCheckedIn = errors.New("今天已经签到过了")

func doCheckin(uid uint) (*PointCheckin, *PointLedger, error) {
	cfg := getPointsConfig()
	if !cfg.Enabled || !cfg.CheckinEnabled {
		return nil, nil, errors.New("签到功能未开启")
	}
	now := time.Now()
	today := todayStr(now)
	var ck PointCheckin
	var led *PointLedger
	err := db.Transaction(func(tx *gorm.DB) error {
		var last PointCheckin
		tx.Where("user_id = ?", uid).Order("day desc").Limit(1).Find(&last)
		if last.Day == today {
			return errCheckedIn
		}
		streak := 1
		if last.Day == todayStr(now.AddDate(0, 0, -1)) {
			streak = last.Streak + 1
		}
		ck = PointCheckin{UserID: uid, Day: today, Streak: streak, Points: checkinPoints(cfg, streak)}
		if err := tx.Create(&ck).Error; err != nil {
			if isUniqueErr(err) {
				return errCheckedIn
			}
			return err
		}
		var err error
		led, err = applyPoints(tx, pointOp{UserID: uid, Delta: ck.Points, Kind: LedgerCheckin, RefType: "checkin",
			RefID: today, Remark: fmt.Sprintf("每日签到（连续 %d 天）", streak), IdemKey: fmt.Sprintf("checkin:%d:%s", uid, today)})
		if err == errDuplicate {
			return errCheckedIn
		}
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return &ck, led, nil
}

// ==================== 一键拨测消耗（普通用户，失败退还） ====================

// chargeProbeCost 返回 (实际扣除额, 拒绝原因)；原因为空 = 放行。admin / 静态 token 不扣
func chargeProbeCost(uid uint) (int64, string) {
	cfg := getPointsConfig()
	if !cfg.Enabled || cfg.ProbeCost <= 0 || uid == 0 {
		return 0, ""
	}
	u, err := userByID(uid)
	if err != nil || u == nil || u.Role == RoleAdmin {
		return 0, ""
	}
	if _, err := changePoints(pointOp{UserID: uid, Delta: -cfg.ProbeCost, Kind: LedgerProbe, Remark: "一键拨测"}); err != nil {
		if err == errInsufficient {
			return 0, fmt.Sprintf("%s不足：一键拨测每次消耗 %d", cfg.CurrencyName, cfg.ProbeCost)
		}
		return 0, err.Error()
	}
	return cfg.ProbeCost, ""
}

// refundProbeCost 拨测请求本身无法执行（无节点/全离线）时退还本次消耗
func refundProbeCost(uid uint, cost int64) {
	if cost <= 0 {
		return
	}
	if _, err := changePoints(pointOp{UserID: uid, Delta: cost, Kind: LedgerProbeBack, Remark: "拨测未执行，退还"}); err != nil {
		log.Printf("[points] refund probe cost user#%d: %v", uid, err)
	}
}

// resolveUser 按 userId / username / email 定位用户（开放接口用）
func resolveUser(id uint, username, email string) (*User, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var u User
	q := db.WithContext(ctx)
	switch {
	case id > 0:
		q = q.Where("id = ?", id)
	case strings.TrimSpace(username) != "":
		q = q.Where("username = ?", strings.TrimSpace(username))
	case strings.TrimSpace(email) != "":
		q = q.Where("email = ?", strings.ToLower(strings.TrimSpace(email)))
	default:
		return nil, errors.New("需提供 userId / username / email 之一")
	}
	if err := q.Limit(1).Find(&u).Error; err != nil {
		return nil, err
	}
	if u.ID == 0 {
		return nil, errors.New("用户不存在")
	}
	return &u, nil
}

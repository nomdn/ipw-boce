package main

import (
	"testing"
	"time"
)

func addEvent(nodeID, ev string, at time.Time) {
	db.Create(&NodeEvent{NodeID: nodeID, Event: ev, Reason: "test", CreatedAt: at.UTC()})
}

func TestUptimeIntervals(t *testing.T) {
	setupTest(t)
	day := time.Date(2025, 3, 10, 0, 0, 0, 0, time.Local)
	db.Create(&Node{NodeID: "n1", Online: true, FirstSeenAt: day.Add(-48 * time.Hour).UTC()})
	addEvent("n1", "online", day.Add(-48*time.Hour))
	addEvent("n1", "offline", day.Add(2*time.Hour))
	addEvent("n1", "online", day.Add(5*time.Hour))
	got := nodeOnlineSeconds("n1", day.UTC(), day.AddDate(0, 0, 1).UTC())
	if got != 21*3600 {
		t.Fatalf("online=%d want %d", got, 21*3600)
	}
	// 窗口开始前就离线、窗口内恢复：起点状态必须取窗口前最后一条事件
	db.Create(&Node{NodeID: "n2", Online: true, FirstSeenAt: day.Add(-72 * time.Hour).UTC()})
	addEvent("n2", "online", day.Add(-72*time.Hour))
	addEvent("n2", "offline", day.Add(-1*time.Hour))
	addEvent("n2", "online", day.Add(3*time.Hour))
	if got := nodeOnlineSeconds("n2", day.UTC(), day.AddDate(0, 0, 1).UTC()); got != 21*3600 {
		t.Fatalf("n2 online=%d", got)
	}
	// 当天中午才首次上线：之前不计
	db.Create(&Node{NodeID: "n3", Online: true, FirstSeenAt: day.Add(12 * time.Hour).UTC()})
	addEvent("n3", "online", day.Add(12*time.Hour))
	if got := nodeOnlineSeconds("n3", day.UTC(), day.AddDate(0, 0, 1).UTC()); got != 12*3600 {
		t.Fatalf("n3 online=%d", got)
	}
}

func TestDonationApproveAndDailyReward(t *testing.T) {
	setupTest(t)
	cfg := defaultPointsConfig()
	cfg.DonateBonusIDC, cfg.DonateBonusHome, cfg.DonateDailyIDC, cfg.DonateDailyHome, cfg.DonateMinOnlineHours = 500, 200, 50, 20, 20
	pointsConf = cfg
	DATA_RETENTION_DAYS = 0
	u := mkUser(t, "donor")
	idc := NodeDonation{UserID: u.ID, Username: u.Username, NodeID: "d-idc", Label: "上海 电信", NetType: NetIDC, Stack: stackDualStack, Channel: "ws", WSKey: "k1", Status: DonPending}
	home := NodeDonation{UserID: u.ID, Username: u.Username, NodeID: "d-home", Label: "杭州 移动 家宽", NetType: NetHome, Stack: stackIPv4, Channel: "ws", WSKey: "k2", Status: DonPending}
	db.Create(&idc)
	db.Create(&home)
	if lookupWSKey("d-idc") != "" {
		reloadNodePools()
	}
	reloadNodePools()
	if lookupWSKey("d-idc") != "k1" {
		t.Fatal("pending donation key not registered")
	}
	if findNode(apiPoolSnapshot(), "d-idc") != nil {
		t.Fatal("pending donation must not enter pool")
	}
	for _, d := range []*NodeDonation{&idc, &home} {
		if err := approveDonation(d, "admin", "ok"); err != nil {
			t.Fatal(err)
		}
	}
	if err := approveDonation(&idc, "admin", "again"); err != nil {
		t.Fatal(err)
	}
	if b := balanceOf(u.ID); b != 700 {
		t.Fatalf("bonus balance %d want 700 (idc 500 + home 200, re-approve no double pay)", b)
	}
	n := findNode(apiPoolSnapshot(), "d-home")
	if n == nil || !n.UseWS() || n.Stack != stackIPv4 {
		t.Fatalf("donated node not in pool: %+v", n)
	}

	// 昨天：idc 全天在线，home 只在线 10 小时（不达标）
	y := time.Now().AddDate(0, 0, -1)
	start := time.Date(y.Year(), y.Month(), y.Day(), 0, 0, 0, 0, time.Local)
	past := start.Add(-24 * time.Hour)
	db.Model(&NodeDonation{}).Where("id IN ?", []uint{idc.ID, home.ID}).Update("reviewed_at", past.UTC())
	db.Create(&Node{NodeID: "d-idc", Online: true, FirstSeenAt: past.UTC()})
	db.Create(&Node{NodeID: "d-home", Online: true, FirstSeenAt: past.UTC()})
	addEvent("d-idc", "online", past)
	addEvent("d-home", "online", past)
	addEvent("d-home", "offline", start.Add(10*time.Hour))
	addEvent("d-home", "online", start.Add(24*time.Hour).Add(time.Minute))

	if paid := settleDonationDay(y); paid != 1 {
		t.Fatalf("paid=%d want 1", paid)
	}
	if paid := settleDonationDay(y); paid != 0 {
		t.Fatalf("re-settle paid=%d want 0 (idempotent)", paid)
	}
	if b := balanceOf(u.ID); b != 750 {
		t.Fatalf("after daily balance %d want 750", b)
	}

	// 维护窗口覆盖 home 的离线时段 → 补回在线时长 → 达标
	db.Create(&MaintenanceWindow{Scope: "d-home", StartAt: start.Add(10 * time.Hour).UTC(), EndAt: start.Add(24 * time.Hour).UTC()})
	if paid := settleDonationDay(y); paid != 1 {
		t.Fatalf("with maintenance credit paid=%d want 1", paid)
	}
	if b := balanceOf(u.ID); b != 770 {
		t.Fatalf("final balance %d want 770", b)
	}

	// 停用：移出节点池
	if err := setDonationOffPool(&idc, DonDisabled, "admin", "test"); err != nil {
		t.Fatal(err)
	}
	if findNode(apiPoolSnapshot(), "d-idc") != nil {
		t.Fatal("disabled donation still in pool")
	}
}

func TestMaintenanceCovers(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.Local)
	w := MaintenanceWindow{RepeatDaily: true, StartAt: base.Add(23 * time.Hour), EndAt: base.Add(30 * time.Minute)}
	at := func(h, m int) time.Time { return time.Date(2025, 6, 6, h, m, 0, 0, time.Local) }
	if !w.covers(at(23, 30)) || !w.covers(at(0, 10)) || w.covers(at(1, 0)) || w.covers(at(12, 0)) {
		t.Fatal("cross-midnight daily window wrong")
	}
	once := MaintenanceWindow{StartAt: at(10, 0), EndAt: at(11, 0)}
	if !once.covers(at(10, 30)) || once.covers(at(11, 0)) {
		t.Fatal("one-off window wrong")
	}
}

func TestCheckinToggle(t *testing.T) {
	setupTest(t)
	u := mkUser(t, "nocheck")
	cfg := defaultPointsConfig()
	cfg.CheckinEnabled = false
	pointsConf = cfg
	if _, _, err := doCheckin(u.ID); err == nil {
		t.Fatal("checkin should be disabled")
	}
}

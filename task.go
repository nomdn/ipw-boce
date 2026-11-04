package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ==================== 定时拨测任务（SLA 数据源，source=sched） ====================

type ProbeTask struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	Name             string    `gorm:"size:128" json:"name"`
	OwnerID          uint      `gorm:"index" json:"ownerId"`
	Enabled          bool      `gorm:"index" json:"enabled"`
	APIType          string    `gorm:"size:16;index" json:"apiType"`
	Target           string    `gorm:"size:512" json:"target"`
	Stack            string    `gorm:"size:8" json:"stack"`
	RecordType       string    `gorm:"size:8" json:"recordType"`
	NodeScope        string    `gorm:"size:8;default:all" json:"nodeScope"`
	NodeIDs          string    `gorm:"size:512" json:"nodeIds"`
	Interval         int       `json:"intervalSec"`
	SlowMs           int       `json:"slowMs"`
	ExpectStatus     string    `gorm:"size:32;default:2xx" json:"expectStatus"`
	BothProtocols    bool      `json:"bothProtocols"`
	RequireAllStacks bool      `json:"requireAllStacks"`
	CertExpiredDown  bool      `json:"certExpiredDown"`
	NotifyRecover    bool      `json:"notifyRecover"`
	QuietHours       string    `gorm:"size:16" json:"quietHours"`
	ShareToken       string    `gorm:"index;size:32" json:"-"`
	HideTarget       bool      `json:"hideTarget"`
	Tags             string    `json:"tags"`
	CreatedAt        time.Time `json:"-"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

func knownProbeTaskTypes() []string { return []string{"tcping", "speed", "ssl", "detail", "dns"} }

func knownDNSRecordTypes() []string {
	return []string{"a", "aaaa", "cname", "mx", "ns", "ptr", "srv", "txt", "caa"}
}

func (t *ProbeTask) toMap() map[string]any {
	b, _ := json.Marshal(t)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

func validateTask(t *ProbeTask) string {
	if strings.TrimSpace(t.Name) == "" {
		return "name is required"
	}
	if !contains(knownProbeTaskTypes(), t.APIType) {
		return "apiType must be one of " + strings.Join(knownProbeTaskTypes(), ",")
	}
	if strings.TrimSpace(t.Target) == "" {
		return "target is required"
	}
	if t.APIType == "dns" {
		rt := strings.ToLower(strings.TrimSpace(t.RecordType))
		if rt == "" {
			rt = "a"
		}
		if !contains(knownDNSRecordTypes(), rt) {
			return "recordType must be one of " + strings.Join(knownDNSRecordTypes(), ",")
		}
		t.RecordType = rt
	} else {
		t.RecordType = ""
	}
	if t.Interval <= 0 {
		return "intervalSec is required"
	}
	if t.Interval < 10 {
		t.Interval = 10
	}
	if t.NodeScope != "custom" {
		t.NodeScope = "all"
	}
	if t.ExpectStatus == "" {
		t.ExpectStatus = "2xx"
	}
	if t.SlowMs < 0 {
		t.SlowMs = 0
	}
	if qh := strings.TrimSpace(t.QuietHours); qh != "" {
		if !validQuietHours(qh) {
			return "quietHours 格式应为 HH:MM-HH:MM（如 23:00-07:00）"
		}
		t.QuietHours = qh
	}
	t.Tags = strings.Join(splitAndTrim(t.Tags, ","), ",")
	return ""
}

func parseHM(x string) (int, bool) {
	if len(x) != 5 || x[2] != ':' {
		return 0, false
	}
	h, e1 := strconv.Atoi(x[:2])
	m, e2 := strconv.Atoi(x[3:])
	if e1 != nil || e2 != nil || h > 23 || m > 59 || h < 0 || m < 0 {
		return 0, false
	}
	return h*60 + m, true
}

func validQuietHours(s string) bool {
	p := strings.SplitN(s, "-", 2)
	if len(p) != 2 {
		return false
	}
	_, a := parseHM(p[0])
	_, b := parseHM(p[1])
	return a && b
}

func inQuietHours(t *ProbeTask) bool {
	p := strings.SplitN(t.QuietHours, "-", 2)
	if len(p) != 2 {
		return false
	}
	s, _ := parseHM(p[0])
	e, _ := parseHM(p[1])
	now := time.Now()
	cur := now.Hour()*60 + now.Minute()
	if s <= e {
		return cur >= s && cur < e
	}
	return cur >= s || cur < e
}

// normalizeTaskRaw speed 补 v4/ v6/ 前缀；dns 拼 "<类型>/<域名>"
func normalizeTaskRaw(t *ProbeTask) string {
	if t.APIType == "speed" && !strings.HasPrefix(t.Target, "v4/") && !strings.HasPrefix(t.Target, "v6/") {
		st := t.Stack
		if st != "v4" && st != "v6" {
			st = "v4"
		}
		return st + "/" + t.Target
	}
	if t.APIType == "dns" {
		rt := t.RecordType
		if rt == "" {
			rt = "a"
		}
		return rt + "/" + strings.TrimPrefix(strings.TrimSpace(t.Target), "/")
	}
	return t.Target
}

// filterStackEligible 双栈判定只派 DualStack；speed 单栈派 DualStack+对应栈
func filterStackEligible(t *ProbeTask, targets []apiInfo) []apiInfo {
	var want map[string]bool
	switch {
	case t.RequireAllStacks:
		want = map[string]bool{stackDualStack: true}
	case t.APIType == "speed":
		st := stackIPv4
		if t.Stack == "v6" {
			st = stackIPv6
		}
		want = map[string]bool{stackDualStack: true, st: true}
	default:
		return targets
	}
	var kept []apiInfo
	for _, n := range targets {
		st := n.Stack
		if st != stackIPv4 && st != stackIPv6 {
			st = stackDualStack
		}
		if want[st] {
			kept = append(kept, n)
		}
	}
	return kept
}

func onlineNodeSet() map[string]bool {
	ctx, cancel := dbCtx()
	defer cancel()
	var ids []string
	db.WithContext(ctx).Model(&Node{}).Where("online = ?", true).Pluck("node_id", &ids)
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	if wsSrv != nil {
		for _, id := range wsSrv.PeerIDs() {
			set[id] = true
		}
	}
	return set
}

// ==================== 调度器 ====================

var taskRun sync.Map // 任务级串行：上一轮未完成跳过本轮

func schedulerLoop() {
	next := map[uint]time.Time{}
	for range time.Tick(time.Second) {
		ctx, cancel := dbCtx()
		var tasks []ProbeTask
		err := db.WithContext(ctx).Where("enabled = ?", true).Find(&tasks).Error
		cancel()
		if err != nil {
			continue
		}
		now := time.Now()
		alive := map[uint]bool{}
		for i := range tasks {
			t := tasks[i]
			alive[t.ID] = true
			if t.Interval < 10 {
				t.Interval = 10
			}
			if nxt, ok := next[t.ID]; ok && now.Before(nxt) {
				continue
			}
			if _, running := taskRun.LoadOrStore(t.ID, true); running {
				continue
			}
			next[t.ID] = now.Add(time.Duration(t.Interval) * time.Second)
			go runTaskSamples(&t)
		}
		for id := range next {
			if !alive[id] {
				delete(next, id)
			}
		}
	}
}

func runTaskSamples(t *ProbeTask) {
	defer taskRun.Delete(t.ID)
	targets := nodePoolForType(t.APIType)
	if t.NodeScope == "custom" && strings.TrimSpace(t.NodeIDs) != "" {
		want := splitAndTrim(t.NodeIDs, ",")
		var picked []apiInfo
		for _, n := range targets {
			if contains(want, n.ID) {
				picked = append(picked, n)
			}
		}
		targets = picked
	}
	targets = filterStackEligible(t, targets)
	on := onlineNodeSet()
	var live []apiInfo
	for _, n := range targets {
		if on[n.ID] {
			live = append(live, n)
		}
	}
	if len(live) == 0 {
		return
	}
	raw := normalizeTaskRaw(t)
	now := time.Now().UTC() // 同一轮共用 CreatedAt，曲线按轮聚合
	rows := make([]ProbeResult, len(live))
	var wg sync.WaitGroup
	for i, n := range live {
		wg.Add(1)
		go func(i int, n apiInfo) {
			defer wg.Done()
			_, status, body, err := probeOne(n, t.APIType, raw, url.Values{})
			row := ProbeResult{NodeID: n.ID, APIType: t.APIType, Raw: raw, Status: status,
				Source: sourceSched, TaskID: t.ID, CreatedAt: now}
			if err != nil {
				row.Error = truncateStr(err.Error(), 512)
			} else {
				row.Body = truncateStr(string(body), 64*1024)
				if lat, ok := trueLatencyMs(t, row.Body); ok {
					row.LatencyMs = lat
				}
			}
			rows[i] = row
		}(i, n)
	}
	wg.Wait()
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).CreateInBatches(rows, 100).Error; err != nil {
		log.Printf("[sched] ERROR persist task#%d: %v", t.ID, err)
		return
	}
	hub.pushSLA(t.ID)
	noteRoundOutcome(t, rows)
}

// ==================== 掉线告警（连续 N 轮整组 down → 通知任务所有者） ====================

var (
	alertMu     sync.Mutex
	alertStreak = map[uint]int{}
	alertFired  = map[uint]bool{}
)

func resetTaskAlerts(id uint) {
	alertMu.Lock()
	delete(alertStreak, id)
	delete(alertFired, id)
	alertMu.Unlock()
}

func noteRoundOutcome(t *ProbeTask, rows []ProbeResult) {
	valid, anyUp := 0, false
	for _, r := range rows {
		ev := evalSample(t, r.Status, r.Body, r.LatencyMs)
		if ev.invalid {
			continue
		}
		valid++
		if ev.up {
			anyUp = true
		}
	}
	if valid == 0 {
		return
	}
	alertMu.Lock()
	var fireDown, fireUp bool
	streak := 0
	if anyUp {
		fireUp = alertFired[t.ID] && t.NotifyRecover
		delete(alertStreak, t.ID)
		delete(alertFired, t.ID)
	} else {
		alertStreak[t.ID]++
		streak = alertStreak[t.ID]
		if streak >= ALERT_CONF.DownThreshold && !alertFired[t.ID] {
			alertFired[t.ID] = true
			fireDown = true
		}
	}
	alertMu.Unlock()
	if !ALERT_CONF.Enabled || t.OwnerID == 0 || (!fireDown && !fireUp) {
		return
	}
	owner, err := userByID(t.OwnerID)
	if err != nil || owner == nil {
		return
	}
	title := fmt.Sprintf("[1IPW-BOCE] 任务恢复：%s", t.Name)
	body := fmt.Sprintf("任务 %s（%s %s）已恢复可用。", t.Name, t.APIType, t.Target)
	kind := "sla_up"
	if fireDown {
		title = fmt.Sprintf("[1IPW-BOCE] 任务不可用：%s", t.Name)
		body = fmt.Sprintf("任务 %s（%s %s）连续 %d 轮全部节点判定为不可用。\n时间：%s",
			t.Name, t.APIType, t.Target, streak, time.Now().Format("2006-01-02 15:04:05"))
		kind = "sla_down"
	}
	if inQuietHours(t) {
		createNotice(owner.ID, kind, t.ID, title, body) // 免打扰：只落站内信
		return
	}
	notifyUser(owner, kind, t.ID, title, body)
}

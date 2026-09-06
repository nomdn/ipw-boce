package main

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ==================== 定时拨测任务（SLA 数据源） ====================
//
// 用户在前台维护一组"拨测任务"，后端调度器按各自 interval 定时对目标节点池批量拨测，
// 结果落 ProbeResult 并打 source=sched。SLA 看板只统计 source=sched 的样本，
// 手动一键拨测（source 为空 / 未落库）与节点自主上报（source=ws|http）不计入。
//
// 任务判定字段是可配置的（用户要求"状态码让用户自己填 / 判定口径创建任务时指定"）：
//   - detail/ssl 节点返回双栈 body（{ipv4:{...}, ipv6:{...}}），真实可达性在各栈的
//     http_status_code / https_status_code 里（链路 HTTP 恒 200，不能当判定依据）。
//   - 因此 SLA 的 up/down 由聚合层按"任务当前判定配置 + 存储的 body"实时解析，
//     改判定配置即可重算历史，无需重跑拨测。

// ProbeTask 一条定时拨测任务
type ProbeTask struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:128" json:"name"`
	// OwnerID 创建该任务的用户（users.id）。0 = 无归属（静态 admin-token 建的旧任务）。
	// 掉线告警只发给所有者；无归属任务不发告警；删用户时级联删其任务（见 users.go DELETE）。
	OwnerID   uint   `gorm:"index" json:"ownerId"`
	Enabled   bool   `gorm:"index" json:"enabled"`                // 注意：勿加 gorm default:true，否则 false 会被默认值吞掉无法建 disabled 任务
	APIType   string `gorm:"size:16;index" json:"apiType"`        // tcping | speed | ssl | detail
	Target    string `gorm:"size:512" json:"target"`              // detail/ssl=域名；tcping=host[:port]；speed=v4/或v6/+URL
	Stack     string `gorm:"size:8" json:"stack"`                 // ""(按类型) | v4 | v6：speed 想固定单栈时用，可覆盖前缀
	NodeScope string `gorm:"size:8;default:all" json:"nodeScope"` // all(全池) | custom(指定)
	NodeIDs   string `gorm:"size:512" json:"nodeIds"`             // custom 时的逗号分隔节点 id；all 忽略
	Interval  int    `json:"intervalSec"`                         // 调度间隔（秒），最小 10
	SlowMs    int    `json:"slowMs"`                              // 慢阈值（ms）；>0 时超过算一次"慢/未达标"，0=不启用
	// ---- 判定配置（前端表单总显式传 bool；不加 gorm default，避免 false 被吞）----
	ExpectStatus     string    `gorm:"size:32;default:2xx" json:"expectStatus"` // "2xx"(默认 2xx 区间) 或 "200" / "200,301,302"
	BothProtocols    bool      `json:"bothProtocols"`                           // detail：http 与 https 都要命中才算成功；false=任一命中
	RequireAllStacks bool      `json:"requireAllStacks"`                        // 双栈(ipv4+ipv6)全通才算该节点可用；false=任一栈通即可
	CertExpiredDown  bool      `json:"certExpiredDown"`                         // ssl：body 里 is_expired=true 视为不可用
	CreatedAt        time.Time `json:"-"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// 任务里 target 为域名/裸 host 时，detail/ssl 节点的实际下发 raw 仍走域名（节点自动双栈）。
// speed 若 Target 未带 v4/v6 前缀且 Stack 非空，下发改成前缀方式，保证节点能识别单栈。

// knownProbeTaskTypes 任务可选拨测类型（用户划入 SLA 拨测的选项）
func knownProbeTaskTypes() []string {
	return []string{"tcping", "speed", "ssl", "detail"}
}

// toGinH 把任务结构转成通用 JSON map（便于附加 ownerUsername 等派生字段；json 往返保全部字段）
func (t *ProbeTask) toGinH() map[string]any {
	b, err := json.Marshal(t)
	if err != nil {
		return map[string]any{"id": t.ID}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{"id": t.ID}
	}
	return m
}

// ==================== 定时调度器 ====================

// 调度器按任务各自 interval 触发；一个任务同一时刻只允许一次在跑（防止上一轮未完成、
// 下一轮又触发造成叠加）。running 用 map + mutex 记录，任务级串行。
var (
	taskRunMu sync.Mutex
	taskRun   = map[uint]bool{}
)

func taskStartRun(id uint) bool {
	taskRunMu.Lock()
	defer taskRunMu.Unlock()
	if taskRun[id] {
		return false // 上一轮仍在跑，跳过本轮
	}
	taskRun[id] = true
	return true
}

func taskEndRun(id uint) {
	taskRunMu.Lock()
	delete(taskRun, id)
	taskRunMu.Unlock()
}

// schedulerLoop 常驻：每 1s 扫描一次 enabled 任务，到期且空闲则触发一轮拨测
func (s *dataStore) schedulerLoop() {
	defer s.stopWait.Done()
	if db == nil {
		log.Printf("[sched] DB unavailable, scheduler disabled")
		return
	}
	// 首次扫描前先同步一次"启用任务"表，避免启动即全部打满
	next := map[uint]time.Time{}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			s.schedTick(next)
		case <-s.stopCh:
			return
		}
	}
}

// schedTick 扫描任务并触发到期项
func (s *dataStore) schedTick(next map[uint]time.Time) {
	ctx, cancel := dbCtx()
	defer cancel()
	var tasks []ProbeTask
	if err := s.gdb.WithContext(ctx).Where("enabled = ?", true).Find(&tasks).Error; err != nil {
		log.Printf("[sched] ERROR list tasks: %v", err)
		return
	}
	now := time.Now()
	for i := range tasks {
		t := &tasks[i]
		if t.Interval < 10 {
			t.Interval = 10 // 最小 10s，防御误配刷爆
		}
		nxt, ok := next[t.ID]
		if !ok {
			// 启动后尽快跑首轮；之后严格按 interval
			next[t.ID] = now
			continue
		}
		if now.Before(nxt) {
			continue
		}
		if !taskStartRun(t.ID) {
			continue // 上一轮还在跑
		}
		next[t.ID] = now.Add(time.Duration(t.Interval) * time.Second)
		go s.runTaskSamples(t) // 异步拨测，不阻塞调度扫描
	}
}

// runTaskSamples 对任务的目标节点池拨测一轮并把结果落库（source=sched）
func (s *dataStore) runTaskSamples(t *ProbeTask) {
	defer taskEndRun(t.ID)
	start := time.Now()
	pool := nodePoolForType(t.APIType)
	if len(pool) == 0 {
		log.Printf("[sched] task#%d %s: no nodes", t.ID, t.Name)
		return
	}
	// 目标节点过滤：custom 只拨指定节点；否则全池
	var targets []apiInfo
	if t.NodeScope == "custom" && strings.TrimSpace(t.NodeIDs) != "" {
		want := map[string]bool{}
		for _, id := range splitAndTrim(t.NodeIDs, ",") {
			want[id] = true
		}
		for _, n := range pool {
			if want[n.ID] {
				targets = append(targets, n)
			}
		}
		if len(targets) == 0 {
			log.Printf("[sched] task#%d %s: none of custom nodes in pool", t.ID, t.Name)
			return
		}
	} else {
		targets = pool
	}

	// 掉线节点不参与本轮拨测：只保留 nodes 表里在线(online=true)的节点。
	// 在线状态来源与节点状态页一致（WS 版随注册/断开即时更新，HTTP 版由看门狗探活维护），
	// 避免对已掉线节点每轮干等超时并产出无意义的 down 样本。
	targets = filterOnline(targets)
	if len(targets) == 0 {
		log.Printf("[sched] task#%d %s: all target nodes offline, skip this round", t.ID, t.Name)
		return
	}

	raw := normalizeTaskRaw(t)
	timeout := wsProbeTimeout()
	query := map[string]string{} // 定时拨测暂无 extra query

	type res struct {
		id     string
		ch     string
		status int
		body   []byte
		err    error
		latMs  int64
	}
	results := make([]res, len(targets))
	var wg sync.WaitGroup
	for i, n := range targets {
		wg.Add(1)
		go func(i int, n apiInfo) {
			defer wg.Done()
			st := time.Now()
			if n.UseWS() {
				status, body, err := probeOneWS(n.ID, t.APIType, raw, query, timeout)
				results[i] = res{n.ID, "ws", status, body, err, time.Since(st).Milliseconds()}
				return
			}
			status, body, err := probeOneHTTP(n, t.APIType, raw, nil, timeout)
			results[i] = res{n.ID, "http", status, body, err, time.Since(st).Milliseconds()}
		}(i, n)
	}
	wg.Wait()

	// 落库：只存原始结果，up/down 判定留到 SLA 读取时按任务当前判定配置算
	rows := make([]ProbeResult, 0, len(results))
	now := time.Now().UTC()
	for _, r := range results {
		body := ""
		if len(r.body) > 0 {
			body = string(r.body)
		}
		// 延迟口径：detail/ssl 从 body 提取节点实测的 total_time 作为真延迟（不含控制台→节点链路），
		// 提取不到(如链路失败无 body、tcping/speed)才回退为控制台端到端耗时 r.latMs。
		lat := r.latMs
		if real, ok := trueLatencyMs(t, body); ok {
			lat = real
		}
		row := ProbeResult{
			NodeID: r.id, APIType: t.APIType, Raw: raw,
			Status: r.status, LatencyMs: lat, Source: sourceSched,
			TaskID:    t.ID, // 归属该任务；SLA 按 task_id 精确聚合
			CreatedAt: now,
		}
		if r.err != nil {
			row.Error = truncateStr(r.err.Error(), 512)
		} else {
			row.Body = truncateStr(body, 64*1024)
		}
		rows = append(rows, row)
	}
	ctx, cancel := dbCtx()
	defer cancel()
	persistErr := s.gdb.WithContext(ctx).CreateInBatches(rows, 100).Error
	if persistErr != nil {
		log.Printf("[sched] ERROR persist task#%d samples: %v", t.ID, persistErr)
	}
	up, down := 0, 0
	for _, r := range results {
		if judgeTaskUp(t, &r.status) {
			up++
		} else {
			down++
		}
	}
	// 实时推送：落库成功后通知浏览器控制台刷新该任务 SLA（无订阅者时自动跳过，见 sla_ws.go）
	if persistErr == nil {
		hub.pushSLA(t.ID)
	}
	// 掉线告警检测：按本轮实际拨测结果累计连续 down，达阈值发邮件（无 smtp 配置时内部空转，见 alert.go）
	noteRoundOutcome(t, rows)
	log.Printf("[sched] task#%d %s %s/%s nodes=%d ok=%d fail=%d took=%s",
		t.ID, t.Name, t.APIType, raw, len(results), up, down, time.Since(start).Round(time.Millisecond))
}

// normalizeTaskRaw 把任务 target 规范成节点下发 raw：
//   - detail/ssl：域名/URL 原样（节点自动双栈）
//   - tcping：host[:port] 原样
//   - speed：若没带 v4//v6/ 前缀，按 task.Stack（缺省 v4）补上前缀，节点据此单栈测速
func normalizeTaskRaw(t *ProbeTask) string {
	if t.APIType == "speed" && !strings.HasPrefix(t.Target, "v4/") && !strings.HasPrefix(t.Target, "v6/") {
		stack := strings.TrimPrefix(t.Stack, "")
		if stack != "v4" && stack != "v6" {
			stack = "v4"
		}
		return stack + "/" + t.Target
	}
	return t.Target
}

// onlineNodeSet 查询 nodes 表当前在线的节点 id 集合（与节点状态页同一数据源）。
// 查询失败/无库时返回空集（保守：宁可本轮跳过也不对状态不明的节点下发）。
func onlineNodeSet() map[string]bool {
	set := map[string]bool{}
	if db == nil {
		return set
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var ids []string
	if err := db.WithContext(ctx).Model(&Node{}).
		Where("online = ?", true).Pluck("node_id", &ids).Error; err != nil {
		log.Printf("[sched] ERROR query online nodes: %v", err)
		return set
	}
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// filterOnline 从候选节点里筛掉已掉线的节点，返回仍参与本轮的在线节点。
// 通过本轮落一条日志记录被跳过的节点（便于排障），在线集合为空时直接返回空。
func filterOnline(targets []apiInfo) []apiInfo {
	if len(targets) == 0 {
		return targets
	}
	on := onlineNodeSet()
	if len(on) == 0 {
		return nil // 无可用的在线集合依据，本轮不下发
	}
	kept := make([]apiInfo, 0, len(targets))
	var dropped []string
	for _, n := range targets {
		if on[n.ID] {
			kept = append(kept, n)
		} else {
			dropped = append(dropped, n.ID)
		}
	}
	if len(dropped) > 0 {
		log.Printf("[sched] skip offline nodes: %v", dropped)
	}
	return kept
}

// judgeTaskUp 调度日志侧对"链路层面"是否成功做粗略判定（仅日志用，不落库，不影响 SLA）。
// SLA 精确判定在聚合层按任务判定配置 + body 解析（见 sla.go）。
func judgeTaskUp(t *ProbeTask, status *int) bool {
	if status == nil || *status == 0 {
		return false
	}
	return statusIsExpected(*status, t.ExpectStatus)
}

// statusIsExpected 判定链路 HTTP 状态是否命中期望：ExpectStatus="2xx" 时按 2xx 区间，
// 否则解析逗号分隔的具体码（如 "200" / "200,301,302"），任一命中即 true。
func statusIsExpected(code int, expect string) bool {
	if expect == "" || expect == "2xx" {
		return code >= 200 && code < 300
	}
	for _, s := range splitAndTrim(expect, ",") {
		if n, err := strconv.Atoi(s); err == nil && n == code {
			return true
		}
	}
	return false
}

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ==================== 服务节点(拨测节点)掉线监控 ====================
//
// 监控对象：setting.json 节点池（api-base-url 三栈 + ip-location-api）里配置的全部服务节点。
// 两种连路各自判活 —— 但**本模块只管 HTTP 版**：
//   - WS 版节点（节点条目 "ws": true）：不用本模块判定。判活靠 ws.go 心跳（maintenanceLoop 每 20s
//     ping+status、空闲 >75s 剔除），翻转与通报收敛在 store.go 的 recordNodeOffline /
//     recordNodeOnline 单点——断连即置离线，通报则先过 nodeDownGraceDelay 宽限窗口再确认发送。
//   - HTTP 版节点（缺省，非 ws）：middleware 每 1 小时 GET 该节点 url（health 接口就在 url 根路径，
//     无任何追加路径）探活；返回 2xx/3xx 视为 up，网络错或 >=4xx 视为 down；连续失败才判 down。
//     版本号与能力清单**不在健康检查里**，探活通过后再单独取 `GET {url}info`（见 httpUp/nodeInfo）。
//
// 掉线判定原则（与用户确认）：
//   - 仅对"曾经在线"的节点告警：新启动即未连上/从未探活成功的节点(如停用、池中占位)不上报，
//     避免冷启动误报。
//   - down 翻转(up→down) 时通知一次(每事件一封)，up 复位后可再次告警——即"每事件一次"。
//   - WS 版的掉线通报另带 20s 宽限窗口：窗口内节点恢复注册则**整条不报**（掉线与随后的"恢复上线"
//     都不报），避免秒级闪断刷屏；窗口过后仍离线才发。判定与通报见 store.go recordNodeOffline。
//
// 三道"计划内静默"叠加在通知层（采集与事件流一律如实记录，只影响"怎么告诉人"）：
//   - 掉线宽限（store.go nodeDownGraceDelay，20s）：秒级闪断整条不报
//   - 计划维护窗口（maintenance.go）：窗口内的掉线/恢复都不报，且成对抑制（不会孤立上线）
//   - 批次汇总（alert_batch.go，alert.batchSeconds 缺省 10s）：同批多节点合并成一条，避免刷屏
//
// 通知投递（与用户确认）：节点掉线/恢复 → 发给**所有启用的 admin 账号**（role=admin 且 enabled），
//   每个 admin **同时**走三路（互不回退）：SMTP 邮件(有邮箱且 SMTP 可用) + 站内信(铃铛可见) +
//   Webhook(个人资料自配才推)。节点告警是系统级事件，Webhook 同样只覆盖 admin 角色。
//   down → kind=node_down / event=node_down；up → kind=node_up / event=node_up。
//   "上线"只在**掉线后恢复**时通报（冷启动首次上线、OTA 计划内重启复联、中心重启后重连都不报），
//   见 notifyNodeUp 与 store.go recordNodeOnline。

// 探活与防抖参数（固定值，无配置项）
const (
	nodeWatchTick     = 60 * time.Second // 看门狗判定轮询间隔
	nodeHTTPInterval  = time.Hour        // HTTP 版探活周期：每 1 小时
	nodeHTTPTimeout   = 10 * time.Second // 单次 HTTP 探活超时
	nodeHTTPDownFails = 2                // HTTP 版连续探活失败达此数才判 down（约 2 小时）
)

// monitorNode 一个被监控节点（来自配置池，id 去重）
type monitorNode struct {
	id    string // backendID / nodeID
	label string
	url   string
	ws    bool // true=WS 版节点；false=HTTP 版节点
}

// nodeWatchState 单节点掉线状态（内存）
type nodeWatchState struct {
	node monitorNode
	// 通用
	everOnline bool // 本进程内是否见过其在线（未见过不告警 down）
	down       bool // 当前是否判 down
	upWritten  bool // 是否已把在线状态写入 nodes 表（HTTP 版用：首次上线入库一次）
	// HTTP 版
	httpLast  time.Time // 上次探活时间
	httpFails int       // 连续失败次数
}

// nodeWatcher 全局看门狗状态
type nodeWatcher struct {
	mu      sync.Mutex
	nodes   map[string]*nodeWatchState
	client  *http.Client // 探活专用（带超时）
	started bool         // run 协程是否已启动（节点池热更新时据此补启动，避免重复）
}

var nodeWatch = &nodeWatcher{}

// startNodeWatcher 初始化看门狗：采集配置池节点并启动后台协程。进程启动时调用一次。
func startNodeWatcher() {
	nodeWatch.mu.Lock()
	nodeWatch.nodes = collectMonitorNodes()
	nodeWatch.mu.Unlock()
	if nodeWatch.client == nil {
		nodeWatch.client = &http.Client{Timeout: nodeHTTPTimeout}
	}
	if len(nodeWatch.nodes) == 0 {
		log.Printf("[node] no configured probe nodes, node watchdog idle")
		return
	}
	nodeWatch.mu.Lock()
	nodeWatch.started = true
	nodeWatch.mu.Unlock()
	log.Printf("[node] node watchdog watching %d nodes (ws=%d http=%d)",
		len(nodeWatch.nodes), countWS(), countHTTP())
	go nodeWatch.run()
}

// refreshWatchedNodes 节点池热更新后重新采集监控清单（见 node_defs.go 的 applyNodeDefs）。
// 已监控节点保留连续失败计数与探活状态，仅刷新静态属性；新增的纳入，已删除的移出。
func refreshWatchedNodes() {
	fresh := collectMonitorNodes()

	nodeWatch.mu.Lock()
	defer nodeWatch.mu.Unlock()
	if nodeWatch.nodes == nil {
		nodeWatch.nodes = fresh
	} else {
		for id, state := range fresh {
			if old, ok := nodeWatch.nodes[id]; ok {
				old.node = state.node // 只刷新 label/url/ws
				continue
			}
			nodeWatch.nodes[id] = state
		}
		for id := range nodeWatch.nodes {
			if _, ok := fresh[id]; !ok {
				delete(nodeWatch.nodes, id)
			}
		}
	}
	// 启动时无节点而协程未起来，后续补录了节点则在此补启动
	if !nodeWatch.started && len(nodeWatch.nodes) > 0 {
		nodeWatch.started = true
		if nodeWatch.client == nil {
			nodeWatch.client = &http.Client{Timeout: nodeHTTPTimeout}
		}
		log.Printf("[node] node watchdog started after pool refresh, watching %d nodes", len(nodeWatch.nodes))
		go nodeWatch.run()
	}
}

func countWS() int {
	n := 0
	for _, s := range nodeWatch.nodes {
		if s.node.ws {
			n++
		}
	}
	return n
}

func countHTTP() int { return len(nodeWatch.nodes) - countWS() }

// collectMonitorNodes 汇总配置池全部节点并去重（同 id 视为同一节点；任一 ws:true 即按 WS 版）。
func collectMonitorNodes() map[string]*nodeWatchState {
	// 走快照函数读取（与管理端热更新互斥）；另建切片承接，避免 append 复用前一个池的底层数组
	entries := make([]apiInfo, 0, 16)
	entries = append(entries, apiPoolSnapshot()...)
	entries = append(entries, locationPoolSnapshot()...)
	out := map[string]*nodeWatchState{}
	for _, e := range entries {
		id := strings.TrimSpace(e.ID)
		if id == "" {
			continue
		}
		st, ok := out[id]
		if !ok {
			st = &nodeWatchState{node: monitorNode{id: id, label: e.Label, url: e.URL, ws: e.UseWS()}}
			out[id] = st
		} else {
			if e.UseWS() {
				st.node.ws = true
			}
			if st.node.label == "" {
				st.node.label = e.Label
			}
			if st.node.url == "" {
				st.node.url = e.URL
			}
		}
	}
	return out
}

// run 看门狗主循环：每 nodeWatchTick 判定一次全部节点。启动先跑一轮 HTTP 探活拿基线。
func (w *nodeWatcher) run() {
	w.tick(true)
	ticker := time.NewTicker(nodeWatchTick)
	defer ticker.Stop()
	for range ticker.C {
		w.tick(false)
	}
}

// tick 判定一轮全部节点。
func (w *nodeWatcher) tick(first bool) {
	w.mu.Lock()
	states := make([]*nodeWatchState, 0, len(w.nodes))
	for _, st := range w.nodes {
		states = append(states, st)
	}
	w.mu.Unlock()

	for _, st := range states {
		// WS 版节点不归本模块管（见文件头注释）：判活与通报在 ws.go 心跳 + store.go 单点驱动
		if !st.node.ws {
			w.checkHTTP(st, first)
		}
	}
}

// httpUp 单次探活是否成功（health 接口 = 节点 url 根路径，GET url，2xx/3xx = up）。
//
// 判活只看根路径；版本号与能力清单已从健康检查里分出去（节点 `GET /info`），
// 探活通过后再由 nodeInfo 单独取回 —— 健康检查是免鉴权的对外端点，不该回报版本。
func (w *nodeWatcher) httpUp(st *nodeWatchState) (bool, string, string, []string) {
	base := strings.TrimSpace(st.node.url)
	if base == "" {
		return false, "empty url", "", nil
	}
	resp, err := w.client.Get(base)
	if err != nil {
		return false, err.Error(), "", nil
	}
	drain(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return false, fmt.Sprintf("http %d", resp.StatusCode), "", nil
	}
	version, caps := w.nodeInfo(base)
	return true, "", version, caps
}

// nodeInfo 取节点的版本号与能力清单（GET {url}info，节点信息接口：
// {"version":"...","capabilities":["probe",...]}）。
//
// 只用于状态页展示与"能否理解某类管理指令"的判定，**失败不影响判活**：
// 老版本节点没有这个接口（404），边缘函数版节点也不回报版本，
// 取不到就留空 —— markNodeUp 对空值不覆盖库里已有值，不会把已知信息抹掉。
func (w *nodeWatcher) nodeInfo(base string) (string, []string) {
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	resp, err := w.client.Get(base + "info")
	if err != nil {
		return "", nil
	}
	defer drain(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", nil
	}
	var info struct {
		Version      string   `json:"version"`
		Capabilities []string `json:"capabilities"`
	}
	// 限制读取体积：只需要一个小 JSON，防止对端返回超大响应体把内存吃掉
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	_ = json.Unmarshal(body, &info)
	return info.Version, info.Capabilities
}

// drain 读完并关闭响应体（复用连接），探活/取信息都不需要正文以外的内容时用。
// 读取同样限长，避免对端返回超大响应体把内存吃掉。
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}

// checkHTTP 探活并更新状态。探活仅当到达周期（或 first 强制立即一次）。
func (w *nodeWatcher) checkHTTP(st *nodeWatchState, first bool) {
	if !(first || time.Since(st.httpLast) >= nodeHTTPInterval) {
		return
	}
	ok, reason, version, caps := w.httpUp(st)

	w.mu.Lock()
	st.httpLast = time.Now()
	if ok {
		wasDown := st.down
		st.everOnline = true
		st.httpFails = 0
		recovered := false
		if wasDown {
			// 从离线恢复：翻回在线并写库（markNodeUp 内部做事件去重）
			st.down = false
			recovered = true
			log.Printf("[node] %s(%s) recovered (http)", st.node.id, st.node.label)
			markNodeUp(st.node, "http probe ok", version, caps)
		} else if !st.upWritten {
			// 首次探活成功且此前从未入库：把它写进 nodes 表，节点状态页才显示该 HTTP 节点。
			// 事件去重交给 markNodeUp(nodeWasOffline)；之后持续在线不再重复写库。
			st.upWritten = true
			log.Printf("[node] %s(%s) http up, now tracked in node list", st.node.id, st.node.label)
			markNodeUp(st.node, "http probe ok", version, caps)
		}
		// 出锁后再用节点信息（refreshWatchedNodes 会在锁内改写 st.node），先复制一份避免竞态
		node := st.node
		w.mu.Unlock()
		// 只有"判过 down 又探活成功"才算恢复上线：首次入库的那次不发通知
		// （cold start / 池中新补的节点都不该报"恢复"），与 WS 版的判定口径一致。
		if recovered {
			log.Printf("[node] %s(%s) http recovered, notify admins", node.id, node.label)
			go notifyNodeUp(node, "HTTP 版", "探活恢复")
		}
		return
	}
	// 探活失败：累计，达到阈值且从未告警过本次 down 才翻转
	st.httpFails++
	if !st.everOnline {
		w.mu.Unlock()
		log.Printf("[node] %s(%s) probe failed (%s), never seen online, no alert", st.node.id, st.node.label, reason)
		return
	}
	fire := st.httpFails >= nodeHTTPDownFails && !st.down
	if fire {
		st.down = true // 置 down，防止 tick 重复触发（up 复位）
		markNodeDown(st.node, "http probe fail: "+reason)
	}
	w.mu.Unlock()

	if fire {
		log.Printf("[node] %s(%s) HTTP DOWN x%d (%s)", st.node.id, st.node.label, st.httpFails, reason)
		go notifyNodeDown(st.node, "HTTP 版", reason, st.httpFails)
	}
}

// notifyNodeDown 通知所有启用 admin：邮件(有邮箱且 SMTP 可用) + 站内信 + Webhook 三路。
// 每 down 事件一次由调用方状态机保证（见 checkHTTP 的 down 翻转、store.go recordNodeOffline 的锁存）。
//
// 这里还叠了两道**计划内静默**（顺序有意义）：
//  1. 计划维护窗口（maintenance.go）：窗口内的掉线不推送，并打标记让随后的恢复一并静默——保证配对，
//     不会出现"掉线被吞、恢复照发"的孤立上线通知。
//  2. 批次汇总（alert_batch.go）：同一时间窗内多个节点掉线合并成一条；只有 1 个节点时文案与原来完全一致。
func notifyNodeDown(n monitorNode, mode, reason string, streak int) {
	if hit, w := maintenanceHit(n.id, time.Now()); hit {
		markMaintSuppressed(n.id)
		log.Printf("[node] %s down but inside maintenance window (%s), down/up alert suppressed", n.id, w.describe())
		return
	}
	enqueueNodeAlert(nodeAlertDown, nodeAlertItem{n: n, mode: mode, reason: reason, streak: streak})
}

// notifyNodeUp 通知所有启用 admin：某节点"掉线后恢复上线"。
//
// 只在**真的掉过线**时才会被调用（判定见 store.go recordNodeOnline / nodeHealth.go checkHTTP）：
// 冷启动首次上线、从未探活成功的节点、OTA 计划内重启的复联、中心重启后的重连都不发，
// 保证群里的"上线"总能对上先前那条"掉线"，不会出现孤立的恢复通知。
func notifyNodeUp(n monitorNode, mode, reason string) {
	// 掉线被维护窗口吞掉的那次：恢复也不报，否则会留下一条没有前置掉线的孤立上线通知
	if takeMaintSuppressed(n.id) {
		log.Printf("[node] %s back online but its down was suppressed by maintenance window, up alert suppressed", n.id)
		return
	}
	enqueueNodeAlert(nodeAlertUp, nodeAlertItem{n: n, mode: mode, reason: reason})
}

// display 告警文案里的节点名。优先级：节点池 label（管理员配的中文名，最可读）
// → nodes 表 label（HTTP 版节点由探活写入）→ nodeId 兜底。
// 必须兜底：WS 版节点注册只写 nodes(online/version/…)，不带 label，
// 直接取库内 label 会渲染成"节点:  (mock-cn-sh)"这种空白名。
func (n monitorNode) display() string {
	if s := strings.TrimSpace(n.label); s != "" {
		return s
	}
	if s := poolLabelFor(n.id); s != "" {
		return s
	}
	return n.id
}

// poolLabelFor 在配置池快照（api + location，读锁）里找该节点的 label；未入池/未配名返回空串。
func poolLabelFor(nodeID string) string {
	for _, item := range apiPoolSnapshot() {
		if item.ID == nodeID {
			return strings.TrimSpace(item.Label)
		}
	}
	for _, item := range locationPoolSnapshot() {
		if item.ID == nodeID {
			return strings.TrimSpace(item.Label)
		}
	}
	return ""
}

// nodeLine 告警正文的节点行：有可读节点名时写「名称 (id)」，否则只写 id（不出现"空名 (id)"）
func nodeLine(n monitorNode) string {
	d := n.display()
	if d == n.id {
		return fmt.Sprintf("节点: %s\n", n.id)
	}
	return fmt.Sprintf("节点: %s (%s)\n", d, n.id)
}

// notifyAdmins 单节点告警的三路投递，收件人 = **所有启用 admin**（role=admin 且 enabled）。
// 多节点汇总通知走 notifyAdminsForNodeIDs（见 alert_batch.go）。
func notifyAdmins(n monitorNode, kind, event, subject, body string) {
	notifyAdminsForNodeIDs([]string{n.id}, kind, event, subject, body)
}

// notifyAdminsForNodeIDs 三路投递（互不回退）的公共实现；nodeIDs 用于日志与 generic webhook 的
// nodeId 字段（汇总通知时是逗号串，单节点时就是单个 id，对既有接收端向后兼容）：
//   - 邮件：有邮箱且 SMTP 可用才发；缺邮箱/不可用/失败仅记日志
//   - 站内信：admin 启用即落（kind 区分 node_down / node_up）
//   - Webhook：admin 在个人资料配了接收端就推。节点告警是系统级事件、收件人就是管理员组，
//     所以 Webhook 也只覆盖 admin 角色——普通用户即便配了 Webhook 也收不到节点告警
//     （与邮件/站内信的收件范围保持一致，见 users.go enabledAdmins）。
func notifyAdminsForNodeIDs(nodeIDs []string, kind, event, subject, body string) {
	admins := enabledAdmins()
	ids := strings.Join(nodeIDs, ",")
	if len(admins) == 0 {
		log.Printf("[node] %s %s but no enabled admin to notify", ids, event)
		return
	}
	for i := range admins {
		a := &admins[i]
		// 1) 邮件：有邮箱且 SMTP 可用才发；缺邮箱/不可用/失败仅记日志，不影响站内信
		if e := strings.TrimSpace(a.Email); e != "" {
			if !smtpReady() {
				log.Printf("[node] %s %s: admin#%d email set but smtp not ready, email skipped", ids, event, a.ID)
			} else if err := mailSender([]string{e}, subject, body); err != nil {
				log.Printf("[node] ERROR send node-%s mail to %s: %v", event, e, err)
			} else {
				log.Printf("[node] sent node-%s mail -> admin#%d <%s>", event, a.ID, e)
			}
		} else {
			log.Printf("[node] %s %s: admin#%d has no email, email skipped", ids, event, a.ID)
		}
		// 2) 站内信：admin 启用即落（与邮件并存）
		if err := createNotice(a.ID, kind, 0, subject, body); err != nil {
			log.Printf("[node] ERROR create node-%s notice admin#%d: %v", event, a.ID, err)
		} else {
			log.Printf("[node] in-app node-%s notice -> admin#%d", event, a.ID)
		}
		// 3) Webhook：配了接收端就推（未配置 = no-op，失败仅记日志）
		pushUserWebhook(a, subject, body, event, 0, ids)
	}
}

func buildNodeDownBody(n monitorNode, mode, reason string, streak int) string {
	var b strings.Builder
	b.WriteString("拨测服务节点疑似掉线，请及时处理。\n\n")
	b.WriteString(nodeLine(n))
	fmt.Fprintf(&b, "连接方式: %s\n", mode)
	fmt.Fprintf(&b, "判定依据: %s\n", reason)
	fmt.Fprintf(&b, "持续判离线次数: %d\n", streak)
	if mode == "HTTP 版" {
		fmt.Fprintf(&b, "探活地址: %s\n", n.url)
	}
	fmt.Fprintf(&b, "时间: %s\n\n", time.Now().UTC().Format(time.RFC3339))
	b.WriteString("—— ipw-boce 自动告警")
	return b.String()
}

// buildNodeUpBody 上线通知正文。与掉线正文对称，便于接收端对照（同一节点的掉线/恢复配对）。
func buildNodeUpBody(n monitorNode, mode, reason string) string {
	var b strings.Builder
	b.WriteString("拨测服务节点已恢复上线。\n\n")
	b.WriteString(nodeLine(n))
	fmt.Fprintf(&b, "连接方式: %s\n", mode)
	fmt.Fprintf(&b, "恢复依据: %s\n", reason)
	if mode == "HTTP 版" {
		fmt.Fprintf(&b, "探活地址: %s\n", n.url)
	}
	fmt.Fprintf(&b, "时间: %s\n\n", time.Now().UTC().Format(time.RFC3339))
	b.WriteString("—— ipw-boce 自动通知")
	return b.String()
}

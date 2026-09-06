package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ==================== 服务节点(拨测节点)掉线监控 ====================
//
// 监控对象：setting.json 节点池（api-base-url 三栈 + ip-location-api）里配置的全部服务节点。
// 两种连路各自判活：
//   - WS 版节点（节点条目 "ws": true）：靠心跳——节点维持 WS 长连接，middleware maintenanceLoop
//     每 20s ping、空闲 >75s 剔除。本模块据此(peer 是否在线)判 up/down。
//   - HTTP 版节点（缺省，非 ws）：middleware 每 1 小时 GET 该节点 url（health 接口就在 url 根路径，
//     无任何追加路径）探活；返回 2xx/3xx 视为 up，网络错或 >=4xx 视为 down；连续失败才判 down。
//
// 掉线判定原则（与用户确认）：
//   - 仅对"曾经在线"的节点告警：新启动即未连上/从未探活成功的节点(如停用、池中占位)不上报，
//     避免冷启动误报。
//   - down 翻转(up→down) 时通知一次(每事件一封)，up 复位后可再次告警——即"每事件一次"。
//
// 通知投递（与用户确认）：节点掉线 → 发给**所有启用的 admin 账号**，
//   每个 admin **同时**发 SMTP 邮件(有邮箱且 SMTP 可用) + 落一条站内信(铃铛可见)。

// 探活与防抖参数（固定值，无配置项）
const (
	nodeWatchTick     = 60 * time.Second // 看门狗判定轮询间隔
	nodeHTTPInterval  = time.Hour        // HTTP 版探活周期：每 1 小时
	nodeHTTPTimeout   = 10 * time.Second // 单次 HTTP 探活超时
	nodeHTTPDownFails = 2                // HTTP 版连续探活失败达此数才判 down（约 2 小时）
	nodeWSDownTicks   = 3                // WS 版连续多少轮(60s)不在线判 down（约 3 分钟，容忍重连）
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
	// WS 版
	wsDownTicks int // 连续判不在线的轮数
}

// nodeWatcher 全局看门狗状态
type nodeWatcher struct {
	mu     sync.Mutex
	nodes  map[string]*nodeWatchState
	client *http.Client // 探活专用（带超时）
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
	log.Printf("[node] node watchdog watching %d nodes (ws=%d http=%d)",
		len(nodeWatch.nodes), countWS(), countHTTP())
	go nodeWatch.run()
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
	entries := append(flattenStack(API_BASE_URLS), IP_LOCATION_APIS...)
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
		if st.node.ws {
			w.checkWS(st)
		} else {
			w.checkHTTP(st, first)
		}
	}
}

// httpUp 单次探活是否成功（health 接口 = 节点 url 根路径，GET url，2xx/3xx = up）
func (w *nodeWatcher) httpUp(st *nodeWatchState) (bool, string) {
	if strings.TrimSpace(st.node.url) == "" {
		return false, "empty url"
	}
	resp, err := w.client.Get(st.node.url)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return true, ""
	}
	return false, fmt.Sprintf("http %d", resp.StatusCode)
}

// checkHTTP 探活并更新状态。探活仅当到达周期（或 first 强制立即一次）。
func (w *nodeWatcher) checkHTTP(st *nodeWatchState, first bool) {
	if !(first || time.Since(st.httpLast) >= nodeHTTPInterval) {
		return
	}
	ok, reason := w.httpUp(st)

	w.mu.Lock()
	st.httpLast = time.Now()
	if ok {
		wasDown := st.down
		st.everOnline = true
		st.httpFails = 0
		if wasDown {
			// 从离线恢复：翻回在线并写库（markNodeUp 内部做事件去重）
			st.down = false
			log.Printf("[node] %s(%s) recovered (http)", st.node.id, st.node.label)
			markNodeUp(st.node, "http probe ok")
		} else if !st.upWritten {
			// 首次探活成功且此前从未入库：把它写进 nodes 表，节点状态页才显示该 HTTP 节点。
			// 事件去重交给 markNodeUp(nodeWasOffline)；之后持续在线不再重复写库。
			st.upWritten = true
			log.Printf("[node] %s(%s) http up, now tracked in node list", st.node.id, st.node.label)
			markNodeUp(st.node, "http probe ok")
		}
		w.mu.Unlock()
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

// checkWS 根据 WS peer 是否在线判定（DB online 快照由 ws.go 心跳维护）。
func (w *nodeWatcher) checkWS(st *nodeWatchState) {
	online := false
	if wsSrv != nil {
		wsSrv.mu.Lock()
		_, online = wsSrv.peers[st.node.id]
		wsSrv.mu.Unlock()
	}
	w.mu.Lock()
	if online {
		if !st.everOnline {
			st.everOnline = true
		}
		if st.down {
			st.down = false
			st.wsDownTicks = 0
			log.Printf("[node] %s(%s) ws recovered", st.node.id, st.node.label)
		}
		w.mu.Unlock()
		return
	}
	if !st.everOnline {
		w.mu.Unlock()
		return // 启动后从未连上：不告警
	}
	st.wsDownTicks++
	if st.wsDownTicks >= nodeWSDownTicks && !st.down {
		st.down = true
	}
	w.mu.Unlock()

	// WS 节点掉线通知改由 registry 单点驱动（store.go recordNodeOffline 在 real online→offline
	// 翻转时发），与节点状态页严格一致、且覆盖未入配置池的临时 WS 节点。
	// 此处仅维护 down 状态，不再重复发 notifyNodeDown，避免对配置池节点双重告警。
}

// notifyNodeDown 通知所有启用 admin：每个 admin 同时发邮件(有邮箱且 SMTP 可用) + 站内信。
// 每 down 事件一次由调用方状态机保证（见 checkHTTP/checkWS 的 down 翻转）。
func notifyNodeDown(n monitorNode, mode, reason string, streak int) {
	admins := enabledAdmins()
	if len(admins) == 0 {
		log.Printf("[node] %s down but no enabled admin to notify", n.id)
		return
	}
	subject := "[IPW-BOCE] 服务节点掉线：" + n.label
	body := buildNodeDownBody(n, mode, reason, streak)
	for i := range admins {
		a := &admins[i]
		// 1) 邮件：有邮箱且 SMTP 可用才发；缺邮箱/不可用/失败仅记日志，不影响站内信
		if e := strings.TrimSpace(a.Email); e != "" {
			if !smtpReady() {
				log.Printf("[node] %s down: admin#%d email set but smtp not ready, email skipped", n.id, a.ID)
			} else if err := mailSender([]string{e}, subject, body); err != nil {
				log.Printf("[node] ERROR send node-down mail to %s: %v", e, err)
			} else {
				log.Printf("[node] sent node-down mail -> admin#%d <%s>", a.ID, e)
			}
		} else {
			log.Printf("[node] %s down: admin#%d has no email, email skipped", n.id, a.ID)
		}
		// 2) 站内信：admin 启用即落（与邮件并存）
		if err := createNotice(a.ID, noticeKindNode, 0, subject, body); err != nil {
			log.Printf("[node] ERROR create node-down notice admin#%d: %v", a.ID, err)
		} else {
			log.Printf("[node] in-app node-down notice -> admin#%d", a.ID)
		}
	}
}

func buildNodeDownBody(n monitorNode, mode, reason string, streak int) string {
	var b strings.Builder
	b.WriteString("拨测服务节点疑似掉线，请及时处理。\n\n")
	fmt.Fprintf(&b, "节点: %s (%s)\n", n.label, n.id)
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

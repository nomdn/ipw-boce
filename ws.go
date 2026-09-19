package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// ==================== WS 通信（middleware 作服务端） ====================
//
// 后端节点作为 WS 客户端连接 middleware 的 WS 服务端（默认端口 8092，路径 /ws，环境变量 WS_PORT 覆盖，0 关闭）。
// 节点配置项 `"ws": true`（或 "true"）时，该节点的拨测请求改走 WS 通道转发；缺省 false 保持原有 HTTP 转发。
// 现有 HTTP 接口（/v1/* /middleware/*）完全不变，WS 是独立端口上的新增数据面。
//
// 相比原版新增：注册/断开写入 nodes + node_events（在线快照与历史），
// 心跳批量刷新 last_seen_at；节点经本通道发 report 消息上报统计与拨测明细（见 report.go）。
//
// nodes.remote_addr 记注册来源：取"尽力还原的真实来源地址"，不是 TCP 最后一跳——详见 remoteAddrForDisplay。
//
// 消息信封（JSON 文本帧）：{ "type": "...", "nodeId": "...", "ts": <unix秒>, "data": {...} }
//   - 节点 → middleware：register / probe_result / pong / report / config_result / ota_result
//   - middleware → 节点：register_ok / register_error / probe / ping / status / config / ota
//
// probe 消息 data：{ "requestId": "...", "apiType": "tcping", "raw": "qq.com", "query": {"port":"443"} }
// probe_result data：{ "requestId": "...", "status": 200, "body": <JSON 值> }（body 为 JSON 字符串时按原文透传）
// ota 消息 data：{ "requestId": "...", "url"|"version"+"assetBase", "sha256" }，节点按阶段回 ota_result（见 ota.go）

type wsMessage struct {
	Type   string          `json:"type"`
	NodeID string          `json:"nodeId,omitempty"`
	TS     int64           `json:"ts"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type wsProbeRequest struct {
	RequestID string            `json:"requestId"`
	APIType   string            `json:"apiType"`
	Raw       string            `json:"raw"`
	Query     map[string]string `json:"query,omitempty"`
	// Scheduler=true 表示本次拨测由收集中心(本机定时/手动一键)主动调度下发，
	// 节点侧识别后跳过把这次执行计入 stats/明细上报，避免与本地落库(source=sched/biz)双算。
	Scheduler bool `json:"scheduler,omitempty"`
}

type wsProbeResult struct {
	RequestID string          `json:"requestId"`
	Status    int             `json:"status"`
	Body      json.RawMessage `json:"body"`
	Error     string          `json:"error,omitempty"`
}

type wsCommand struct {
	Command string          `json:"command"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type wsPeer struct {
	conn *websocket.Conn
	id   string
	// lastAtomic 最后收到消息的时间（unix nano）。读循环写、maintenanceLoop 读，
	// 跨 goroutine 且不在 s.mu 保护内，必须原子访问
	lastAtomic atomic.Int64
}

type wsServer struct {
	mu    sync.Mutex
	peers map[string]*wsPeer

	probeMu   sync.Mutex
	probeWait map[string]chan wsProbeResult

	// configMu/configWait：配置管理指令（get / patch / refresh）的应答通道，
	// 与拨测分开维护——两者报文结构不同，复用 probeWait 会污染类型
	configMu   sync.Mutex
	configWait map[string]chan json.RawMessage

	// otaMu/otaWait：OTA 下发的 ack 通道（等节点首个 ota_result）。
	// 之后的进度回报不再走这里（已无等待方），由 ota_result 分支按 requestId 直接更新任务（见 ota.go）
	otaMu   sync.Mutex
	otaWait map[string]chan json.RawMessage

	// 统计（内存快照，随 status 消息上报节点；持久化走 store）
	statMu    sync.Mutex
	totalReqs int64
	errReqs   int64
	startedAt time.Time
}

func newWSServer() *wsServer {
	// 进程重启后 WS 连接全部断开，把存量在线快照重置为离线，等节点重新注册
	markAllNodesOffline()
	return &wsServer{
		peers:      make(map[string]*wsPeer),
		probeWait:  make(map[string]chan wsProbeResult),
		configWait: make(map[string]chan json.RawMessage),
		otaWait:    make(map[string]chan json.RawMessage),
		startedAt:  time.Now(),
	}
}

// ==================== 来源地址解析（仅用于展示） ====================
//
// 注册时能直接拿到的只有 r.RemoteAddr —— 它是 TCP 的最后一跳。生产环境下节点与中心往往
// 不在同一台机器，但中心前面常还有一层本机/内网代理（nginx、Caddy、frpc、CDN 回源代理……），
// 此时最后一跳恒为 127.0.0.1:<临时端口>，看起来像"节点就在本机"。
//
// 所以这里做一次尽力而为的还原：
//  1. 只有对端确实是代理时才采信转发头——对端是回环 / 内网地址，或命中 trusted-proxies 名单。
//     从公网直连的节点即便自带 X-Forwarded-For 也不采信，避免这个展示字段被随意伪造。
//  2. 转发头优先取 X-Forwarded-For 最左一个合法 IP（标准语义：最左即原始客户端），其次 X-Real-IP。
//  3. 都不成立则回退对端 IP 本身，并去掉临时源端口——该端口每次重连都变，留着只会被误读成服务端口。
//
// 注意：转发头可被伪造，本值仅供控制台展示，不参与鉴权（wsKeys）与限流（ClientIP + trusted-proxies）。
func remoteAddrForDisplay(r *http.Request) string {
	peer := stripPort(r.RemoteAddr)
	if ip := forwardedClientIP(r); ip != "" && (isLocalOrPrivate(peer) || isTrustedProxy(peer)) {
		return ip
	}
	return peer
}

// forwardedClientIP 从转发头里取原始客户端 IP，取不到返回空串。
func forwardedClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for _, part := range strings.Split(xff, ",") {
			if ip := net.ParseIP(strings.TrimSpace(part)); ip != nil {
				return ip.String()
			}
		}
	}
	if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
		return ip.String()
	}
	return ""
}

// stripPort 去掉 host:port 的端口；不带端口的写法（如 ::1）原样返回。
func stripPort(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.Trim(addr, "[]")
}

// isLocalOrPrivate 判断是否回环 / 内网 / 链路本地 / 未指定地址——这些都不可能是公网节点的真实
// 来源，出现即说明它前面还有一跳本机或内网代理。
func isLocalOrPrivate(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// isTrustedProxy 判断地址是否命中 trusted-proxies（IP 或 CIDR，逗号分隔）。该键为需重启键，
// 全局量仅在启动期由 readConfig 赋值一次，故此处直接读不涉及并发写。
func isTrustedProxy(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil || strings.TrimSpace(TRUSTED_PROXIES) == "" {
		return false
	}
	for _, entry := range splitAndTrim(TRUSTED_PROXIES, ",") {
		if entry == addr {
			return true
		}
		if _, cidr, err := net.ParseCIDR(entry); err == nil && cidr.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *wsServer) Handler(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("[ws] accept failed: %v", err)
		return
	}
	defer c.Close(websocket.StatusInternalError, "closed")

	ctx := r.Context()
	peer := &wsPeer{conn: c}
	peer.lastAtomic.Store(time.Now().UnixNano())
	registered := false

	// 读循环
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			if !registered {
				log.Printf("[ws] peer closed before register")
			} else {
				// 仅当仍是被 peers 引用的当前连接才置离线——同 id 新连接替换旧连接时，
				// 旧连接读错误属正常收尾，不能把刚注册的新连接误标离线。
				s.mu.Lock()
				cur, isCur := s.peers[peer.id]
				s.mu.Unlock()
				if !isCur || cur != peer {
					log.Printf("[ws] node %s old connection closed (replaced), ignore", peer.id)
				} else {
					log.Printf("[ws] node %s disconnected: %v", peer.id, err)
					recordNodeOffline(peer.id, err.Error())
				}
			}
			break
		}

		var msg wsMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("[ws] invalid message from %s: %v", peer.id, err)
			continue
		}
		peer.lastAtomic.Store(time.Now().UnixNano())

		// ===== 数据阶段 =====
		switch msg.Type {
		case "register":
			var reg struct {
				NodeID  string `json:"nodeId"`
				Key     string `json:"key"`     // 注册凭证：与 setting.json wsKeys 配置比对
				Version string `json:"version"` // 节点版本号（老版本节点不发，留空）
				// Capabilities 节点支持的管理能力（probe/report/config）。
				// 老版本节点不发 → 留空，表示"未知"而非"不支持"
				Capabilities []string `json:"capabilities"`
			}
			_ = json.Unmarshal(msg.Data, &reg)
			if reg.NodeID == "" {
				s.sendJSON(c, wsMessage{Type: "register_error", Data: mustRaw(wsCommand{Command: "empty nodeId"})})
				continue
			}
			if registered {
				s.sendJSON(c, wsMessage{Type: "register_error", Data: mustRaw(wsCommand{Command: "already registered"})})
				continue
			}
			// 注册 key 校验：节点配置了 wsKeys[节点id] 就必须传对 key，否则 401 并断开；
			// 未配置 key 的节点（开放）无需传 key
			if expected := lookupWSKey(reg.NodeID); expected != "" && reg.Key != expected {
				log.Printf("[ws] AUTH FAILED for node %s: invalid key", reg.NodeID)
				s.sendJSON(c, wsMessage{Type: "register_error", Data: mustRaw(struct {
					Code    int    `json:"code"`
					Command string `json:"command"`
				}{Code: 401, Command: "invalid key"})})
				c.Close(websocket.StatusPolicyViolation, "invalid key")
				return
			}
			registered = true
			peer.id = reg.NodeID
			s.mu.Lock()
			// 同 id 新连接替换旧连接（旧连接由读循环自然退出）
			if old, ok := s.peers[reg.NodeID]; ok && old.conn != c {
				old.conn.Close(websocket.StatusPolicyViolation, "replaced by new connection")
			}
			s.peers[reg.NodeID] = peer
			s.mu.Unlock()
			// 来源地址：有本机/内网代理时 r.RemoteAddr 只是最后一跳，这里还原为真实来源；
			// 日志同时打两者，便于排查"节点到底从哪连上来的"（口径见 remoteAddrForDisplay）
			remote := remoteAddrForDisplay(r)
			log.Printf("[ws] node registered: %s from %s (peer %s)", reg.NodeID, remote, r.RemoteAddr)
			// 持久化：在线快照 + online 事件（含节点上报的版本号与能力清单，供节点状态页展示）
			recordNodeOnline(reg.NodeID, remote, reg.Version, reg.Capabilities)
			// OTA 任务追踪钩子：节点重连上报的版本号命中在途任务判定 → 即时判 success（见 ota.go）
			otaOnNodeRegistered(reg.NodeID, reg.Version)
			s.sendJSON(c, wsMessage{Type: "register_ok", NodeID: reg.NodeID, TS: time.Now().Unix(), Data: mustRaw(struct {
				Heartbeat int `json:"heartbeatSeconds"`
			}{Heartbeat: 20})})

		case "probe_result":
			if !registered {
				continue
			}
			var res wsProbeResult
			if err := json.Unmarshal(msg.Data, &res); err != nil {
				log.Printf("[ws] bad probe_result from %s: %v", peer.id, err)
				continue
			}
			s.probeMu.Lock()
			ch, ok := s.probeWait[res.RequestID]
			delete(s.probeWait, res.RequestID)
			s.probeMu.Unlock()
			if ok {
				ch <- res
			}

		case "config_result":
			// 配置管理指令的应答（get / patch / refresh），原样投递给等待方
			if !registered {
				continue
			}
			var head struct {
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(msg.Data, &head); err != nil || head.RequestID == "" {
				log.Printf("[ws] bad config_result from %s", peer.id)
				continue
			}
			s.configMu.Lock()
			ch, ok := s.configWait[head.RequestID]
			delete(s.configWait, head.RequestID)
			s.configMu.Unlock()
			if ok {
				// msg.Data 底层数组会随下一次 Read 复用，必须拷贝
				ch <- append(json.RawMessage(nil), msg.Data...)
			}

		case "ota_result":
			// OTA 下发的应答/进度：首帧（accepted）投递给下发等待方；所有帧同步更新任务状态（见 ota.go）
			if !registered {
				continue
			}
			var head struct {
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(msg.Data, &head); err != nil || head.RequestID == "" {
				log.Printf("[ws] bad ota_result from %s", peer.id)
				continue
			}
			s.otaMu.Lock()
			ch, ok := s.otaWait[head.RequestID]
			delete(s.otaWait, head.RequestID)
			s.otaMu.Unlock()
			if ok {
				ch <- append(json.RawMessage(nil), msg.Data...)
			}
			otaOnNodeResult(peer.id, msg.Data)

		case "pong":
			// 心跳应答，仅刷新 last

		case "report":
			// 数据上报（协议见 report.go）：已注册节点经 WS 通道推统计/拨测增量
			if registered {
				handleWSReport(peer.id, msg.Data)
			}

		case "ping":
			// 节点主动心跳：回 pong
			if registered {
				s.sendJSON(c, wsMessage{Type: "pong", NodeID: peer.id, TS: time.Now().Unix()})
			}

		default:
			log.Printf("[ws] unknown message type from %s: %s", peer.id, msg.Type)
		}
	}

	// 断开清理
	if registered {
		s.mu.Lock()
		if s.peers[peer.id] == peer {
			delete(s.peers, peer.id)
		}
		s.mu.Unlock()
	}
}

// sendJSON 发送 JSON 消息并返回写错误（写失败意味着对端连接已不可用，调用方应尽快失败）
func (s *wsServer) sendJSON(c *websocket.Conn, msg wsMessage) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Write(ctx, websocket.MessageText, mustRaw(msg))
}

// RequestProbe 通过 WS 通道向指定节点发送拨测请求并等待结果（超时返回错误）。
// RequestProbe 通过 WS 通道向单个节点下发拨测请求并等待结果。
// scheduler=true 表示本次为收集中心主动调度拨测（定时/手动一键），报文带 Scheduler 标记，
// 节点识别后跳过把该次执行计入 stats/明细上报（避免与本地落库双算）；
// scheduler=false 用于 middlewareHandler 转发真实业务（forwardWSProbe），节点照常上报。
func (s *wsServer) RequestProbe(nodeID, apiType, raw string, query map[string]string, timeout time.Duration, scheduler bool) (int, []byte, error) {
	s.mu.Lock()
	peer, ok := s.peers[nodeID]
	s.mu.Unlock()
	if !ok || peer.conn == nil {
		return 0, nil, fmt.Errorf("ws node %s not connected", nodeID)
	}

	reqID := "m" + genRequestID()
	ch := make(chan wsProbeResult, 1)
	s.probeMu.Lock()
	s.probeWait[reqID] = ch
	s.probeMu.Unlock()
	defer func() {
		s.probeMu.Lock()
		delete(s.probeWait, reqID)
		s.probeMu.Unlock()
	}()

	payload := wsProbeRequest{RequestID: reqID, APIType: apiType, Raw: raw, Query: query, Scheduler: scheduler}
	if err := s.sendJSON(peer.conn, wsMessage{Type: "probe", NodeID: nodeID, TS: time.Now().Unix(), Data: mustRaw(payload)}); err != nil {
		// 连接已死（写失败立刻可知），直接返回错误，不再让请求干等满超时
		s.statMu.Lock()
		s.errReqs++
		s.statMu.Unlock()
		return 0, nil, fmt.Errorf("ws probe send failed for node %s: %w", nodeID, err)
	}

	s.statMu.Lock()
	s.totalReqs++
	s.statMu.Unlock()

	select {
	case res := <-ch:
		if res.Error != "" {
			s.statMu.Lock()
			s.errReqs++
			s.statMu.Unlock()
			return 0, nil, fmt.Errorf("ws probe error: %s", res.Error)
		}
		body := res.Body
		// body 为 JSON 字符串时按原文透传（兼容文本响应）
		if len(body) > 0 && body[0] == '"' {
			var str string
			if json.Unmarshal(body, &str) == nil {
				body = []byte(str)
			}
		}
		return res.Status, body, nil
	case <-time.After(timeout):
		s.statMu.Lock()
		s.errReqs++
		s.statMu.Unlock()
		return 0, nil, fmt.Errorf("ws probe timeout for node %s", nodeID)
	}
}

// RequestConfig 经 WS 通道向节点下发配置管理指令（get / patch / refresh）并等待 config_result。
// 返回节点应答的原始 data（含 ok / applied / unknown / restartRequired / config 等字段）。
// 与拨测不同：配置指令不产生拨测数据，故不带 scheduler 标记，也不计入请求统计。
func (s *wsServer) RequestConfig(nodeID, action string, cfg map[string]any, persist bool, timeout time.Duration) (json.RawMessage, error) {
	s.mu.Lock()
	peer, ok := s.peers[nodeID]
	s.mu.Unlock()
	if !ok || peer.conn == nil {
		return nil, fmt.Errorf("ws node %s not connected", nodeID)
	}

	reqID := "c" + genRequestID()
	ch := make(chan json.RawMessage, 1)
	s.configMu.Lock()
	s.configWait[reqID] = ch
	s.configMu.Unlock()
	defer func() {
		s.configMu.Lock()
		delete(s.configWait, reqID)
		s.configMu.Unlock()
	}()

	payload := map[string]any{"requestId": reqID, "action": action}
	if len(cfg) > 0 {
		payload["config"] = cfg
	}
	if persist {
		payload["persist"] = true
	}

	if err := s.sendJSON(peer.conn, wsMessage{Type: "config", NodeID: nodeID, TS: time.Now().Unix(), Data: mustRaw(payload)}); err != nil {
		return nil, fmt.Errorf("ws config send failed for node %s: %w", nodeID, err)
	}

	select {
	case data := <-ch:
		var head struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &head)
		if !head.OK {
			if head.Error == "" {
				head.Error = "节点返回失败但未给出原因"
			}
			return nil, fmt.Errorf("%s", head.Error)
		}
		return data, nil
	case <-time.After(timeout):
		// 型别化：调用方据此区分"节点不认指令 / 连接半死"与"节点明确拒绝"
		return nil, &wsTimeoutError{NodeID: nodeID, Command: "config"}
	}
}

// RequestOTA 经 WS 通道向节点下发 OTA 升级指令并等待首个 ota_result（节点 ack）。
// 与 config 指令同构：老版本节点不认识该指令会静默忽略 → 超时（*wsTimeoutError），
// 调用方用 ProbeAlive 分型给出"程序版本过旧"提示。之后的进度回报不经此通道
// （等待方已消失），由 ota_result 分支直接更新任务状态（见 ota.go）。
func (s *wsServer) RequestOTA(nodeID string, payload map[string]any, timeout time.Duration) (json.RawMessage, error) {
	s.mu.Lock()
	peer, ok := s.peers[nodeID]
	s.mu.Unlock()
	if !ok || peer.conn == nil {
		return nil, fmt.Errorf("ws node %s not connected", nodeID)
	}

	reqID, _ := payload["requestId"].(string)
	if reqID == "" {
		reqID = "o" + genRequestID()
		payload["requestId"] = reqID
	}
	ch := make(chan json.RawMessage, 1)
	s.otaMu.Lock()
	s.otaWait[reqID] = ch
	s.otaMu.Unlock()
	defer func() {
		s.otaMu.Lock()
		delete(s.otaWait, reqID)
		s.otaMu.Unlock()
	}()

	if err := s.sendJSON(peer.conn, wsMessage{Type: "ota", NodeID: nodeID, TS: time.Now().Unix(), Data: mustRaw(payload)}); err != nil {
		return nil, fmt.Errorf("ws ota send failed for node %s: %w", nodeID, err)
	}

	select {
	case data := <-ch:
		var head struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &head)
		if !head.OK {
			if head.Error == "" {
				head.Error = "节点返回失败但未给出原因"
			}
			return nil, fmt.Errorf("%s", head.Error)
		}
		return data, nil
	case <-time.After(timeout):
		return nil, &wsTimeoutError{NodeID: nodeID, Command: "ota"}
	}
}

// NodeConnected 判断节点当前是否有活跃的 WS 连接（决定配置指令走 WS 还是回退 HTTP）
func (s *wsServer) NodeConnected(nodeID string) bool {
	s.mu.Lock()
	peer, ok := s.peers[nodeID]
	s.mu.Unlock()
	return ok && peer.conn != nil
}

// Start 启动 WS 服务端（阻塞）。
func (s *wsServer) Start(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.Handler)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("[ws] server listening on %s/ws", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("[ws] server stopped: %v", err)
	}
}

// wsTimeoutError 节点在超时窗口内**什么都没回**（区别于"节点明确回了 ok:false + 原因"）。
// 调用方据此区分"节点不认这条指令 / 连接半死"与"节点拒绝了指令"，给出不同的排障提示。
type wsTimeoutError struct {
	NodeID  string
	Command string // probe | config
}

func (e *wsTimeoutError) Error() string {
	return fmt.Sprintf("ws %s timeout for node %s", e.Command, e.NodeID)
}

// PeerLastActive 节点当前连接的最近收包时间（unix nano）；无该节点的活跃连接返回 0。
// 供"超时后判断节点是否还活着"使用（见 ProbeAlive）。
func (s *wsServer) PeerLastActive(nodeID string) int64 {
	s.mu.Lock()
	peer, ok := s.peers[nodeID]
	s.mu.Unlock()
	if !ok || peer.conn == nil {
		return 0
	}
	return peer.lastAtomic.Load()
}

// ProbeAlive 主动探活节点当前 WS 连接：发一条 ping，等节点回包（pong，任意消息都会刷新收包时间）。
//
// 用途：管理指令超时后区分两种截然不同的情况——
//   - true  连接活着，只是不认这条指令 → 几乎必然是节点程序版本过旧
//   - false 连接已半死（对端进程没了但 TCP 未感知）→ 指令根本没到节点，等空闲剔除自行收敛
//
// 只有在已发生超时（15s 白等）之后才调用，代价可忽略。
func (s *wsServer) ProbeAlive(nodeID string, wait time.Duration) bool {
	s.mu.Lock()
	peer, ok := s.peers[nodeID]
	s.mu.Unlock()
	if !ok || peer.conn == nil {
		return false
	}
	before := peer.lastAtomic.Load()
	if err := s.sendJSON(peer.conn, wsMessage{Type: "ping", NodeID: nodeID, TS: time.Now().Unix()}); err != nil {
		return false
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if peer.lastAtomic.Load() > before {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// maintenanceLoop 心跳 + 状态上报（middleware → 节点）+ 在线快照落库。
func (s *wsServer) maintenanceLoop() {
	for {
		time.Sleep(20 * time.Second)
		s.mu.Lock()
		peers := make([]*wsPeer, 0, len(s.peers))
		for _, p := range s.peers {
			peers = append(peers, p)
		}
		s.mu.Unlock()

		s.statMu.Lock()
		stats := struct {
			UptimeSeconds int64 `json:"uptimeSeconds"`
			TotalRequests int64 `json:"totalRequests"`
			ErrorRequests int64 `json:"errorRequests"`
			ConnectedAt   int64 `json:"connectedAt"`
		}{
			UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
			TotalRequests: s.totalReqs,
			ErrorRequests: s.errReqs,
		}
		s.statMu.Unlock()

		alive := make([]string, 0, len(peers))
		for _, p := range peers {
			// 空闲剔除：超过 75s（约 3 个心跳周期）没有任何消息的节点视为僵死。
			// 不剔除的话死连接会永久占位，路由到它的探测全部干等超时
			s.mu.Lock()
			idle := time.Since(time.Unix(0, p.lastAtomic.Load()))
			s.mu.Unlock()
			if idle > 75*time.Second {
				log.Printf("[ws] node %s idle for %s, evicting", p.id, idle.Round(time.Second))
				p.conn.Close(websocket.StatusPolicyViolation, "idle timeout")
				s.mu.Lock()
				if cur, ok := s.peers[p.id]; ok && cur == p {
					delete(s.peers, p.id)
				}
				s.mu.Unlock()
				recordNodeOffline(p.id, "idle timeout")
				continue
			}
			alive = append(alive, p.id)
			// 心跳
			s.sendJSON(p.conn, wsMessage{Type: "ping", NodeID: p.id, TS: time.Now().Unix()})
			// 状态/统计上报
			s.sendJSON(p.conn, wsMessage{Type: "status", NodeID: p.id, TS: time.Now().Unix(), Data: mustRaw(stats)})
		}
		// 持久化：批量刷新在线节点的 last_seen_at
		touchNodes(alive)
	}
}

func mustRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

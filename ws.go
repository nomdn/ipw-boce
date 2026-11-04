package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// ==================== 节点 WS 数据面（独立端口，缺省 8092，路径 /ws） ====================
//
// 信封：{ "type", "nodeId", "ts"(unix 秒), "data" }
//   节点 → 中心：register / probe_result / pong / ping / report / config_result
//   中心 → 节点：register_ok / register_error / probe / ping / pong / status
// probe data：{requestId, apiType, raw, query?, scheduler?}
// probe_result data：{requestId, status, body, error?}（body 为 JSON 字符串时按原文透传）

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
	Scheduler bool              `json:"scheduler,omitempty"`
	// Body 仅 apiType=toolbox 使用：网络查询工具箱的 POST 请求体，节点进程内执行 POST /v1/<raw>
	Body json.RawMessage `json:"body,omitempty"`
}

type wsProbeResult struct {
	RequestID string          `json:"requestId"`
	Status    int             `json:"status"`
	Body      json.RawMessage `json:"body"`
	Error     string          `json:"error,omitempty"`
}

type wsCommand struct {
	Command string `json:"command"`
}

type wsPeer struct {
	conn *websocket.Conn
	id   string
	last atomic.Int64
	wmu  sync.Mutex // coder/websocket 不允许并发写
}

type wsServer struct {
	mu        sync.Mutex
	peers     map[string]*wsPeer
	probeMu   sync.Mutex
	probeWait map[string]chan wsProbeResult
	// cmdWait config / ota 指令的应答通道（按 requestId；报文结构与拨测不同，单独维护）
	cmdMu     sync.Mutex
	cmdWait   map[string]chan json.RawMessage
	totalReqs atomic.Int64
	errReqs   atomic.Int64
	startedAt time.Time
}

var wsSrv *wsServer

func newWSServer() *wsServer {
	markAllNodesOffline()
	return &wsServer{peers: map[string]*wsPeer{}, probeWait: map[string]chan wsProbeResult{},
		cmdWait: map[string]chan json.RawMessage{}, startedAt: time.Now()}
}

// wsTimeoutError 节点在超时窗口内什么都没回（区别于节点明确回 ok:false）
type wsTimeoutError struct{ NodeID, Command string }

func (e *wsTimeoutError) Error() string {
	return fmt.Sprintf("ws %s timeout for node %s", e.Command, e.NodeID)
}

// RequestCommand 下发 config / ota 指令并等待首个应答（data 内须含 requestId）；
// 应答 ok=false 时返回节点给出的原因。
func (s *wsServer) RequestCommand(nodeID, msgType string, payload map[string]any, timeout time.Duration) (json.RawMessage, error) {
	s.mu.Lock()
	peer, ok := s.peers[nodeID]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("ws node %s not connected", nodeID)
	}
	reqID, _ := payload["requestId"].(string)
	if reqID == "" {
		reqID = msgType[:1] + genRequestID()
		payload["requestId"] = reqID
	}
	ch := make(chan json.RawMessage, 1)
	s.cmdMu.Lock()
	s.cmdWait[reqID] = ch
	s.cmdMu.Unlock()
	defer func() {
		s.cmdMu.Lock()
		delete(s.cmdWait, reqID)
		s.cmdMu.Unlock()
	}()
	if err := s.send(peer, wsMessage{Type: msgType, NodeID: nodeID, Data: mustRaw(payload)}); err != nil {
		return nil, fmt.Errorf("ws %s send failed for node %s: %w", msgType, nodeID, err)
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
			return data, fmt.Errorf("%s", head.Error)
		}
		return data, nil
	case <-time.After(timeout):
		return nil, &wsTimeoutError{NodeID: nodeID, Command: msgType}
	}
}

// ProbeAlive 指令超时后探活：发 ping 等任意回包。true = 连接活着只是不认指令（节点版本过旧）
func (s *wsServer) ProbeAlive(nodeID string, wait time.Duration) bool {
	s.mu.Lock()
	peer, ok := s.peers[nodeID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	before := peer.last.Load()
	if s.send(peer, wsMessage{Type: "ping", NodeID: nodeID}) != nil {
		return false
	}
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if peer.last.Load() > before {
			return true
		}
	}
	return false
}

func (s *wsServer) deliverCommandResult(data json.RawMessage) {
	var head struct {
		RequestID string `json:"requestId"`
	}
	if json.Unmarshal(data, &head) != nil || head.RequestID == "" {
		return
	}
	s.cmdMu.Lock()
	ch, ok := s.cmdWait[head.RequestID]
	delete(s.cmdWait, head.RequestID)
	s.cmdMu.Unlock()
	if ok {
		ch <- data
	}
}

func (s *wsServer) send(p *wsPeer, msg wsMessage) error {
	if msg.TS == 0 {
		msg.TS = time.Now().Unix()
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.conn.Write(ctx, websocket.MessageText, mustRaw(msg))
}

// remoteAddrForDisplay 对端是回环/内网/可信代理时采信 XFF，否则取对端 IP（仅展示用）
func remoteAddrForDisplay(r *http.Request) string {
	peer := r.RemoteAddr
	if h, _, err := net.SplitHostPort(peer); err == nil {
		peer = h
	}
	ip := net.ParseIP(peer)
	trusted := ip != nil && (ip.IsLoopback() || ip.IsPrivate())
	if !trusted && ip != nil {
		for _, e := range splitAndTrim(TRUSTED_PROXIES, ",") {
			if _, cidr, err := net.ParseCIDR(e); (err == nil && cidr.Contains(ip)) || e == peer {
				trusted = true
			}
		}
	}
	if trusted {
		for _, part := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
			if x := net.ParseIP(strings.TrimSpace(part)); x != nil {
				return x.String()
			}
		}
		if x := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); x != nil {
			return x.String()
		}
	}
	return peer
}

func (s *wsServer) Handler(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	c.SetReadLimit(8 << 20)
	defer c.Close(websocket.StatusInternalError, "closed")
	peer := &wsPeer{conn: c}
	peer.last.Store(time.Now().UnixNano())
	registered := false

	for {
		typ, data, err := c.Read(r.Context())
		if err != nil {
			if registered {
				s.mu.Lock()
				cur := s.peers[peer.id]
				if cur == peer {
					delete(s.peers, peer.id)
				}
				s.mu.Unlock()
				if cur == peer {
					reason := describeWSDisconnect(err)
						log.Printf("[ws] node %s disconnected: %s (%v)", peer.id, reason, err)
					recordNodeOffline(peer.id, reason)
				}
			}
			return
		}
		if typ == websocket.MessageBinary {
			// 测速数据帧（见 speedtest.go）：算作活跃，不走 JSON 解析
			if registered {
				peer.last.Store(time.Now().UnixNano())
				speedtestOnFrame(peer.id, data)
			}
			continue
		}
		var msg wsMessage
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		peer.last.Store(time.Now().UnixNano())

		switch msg.Type {
		case "register":
			var reg struct {
				NodeID       string   `json:"nodeId"`
				Key          string   `json:"key"`
				Version      string   `json:"version"`
				Capabilities []string `json:"capabilities"`
			}
			_ = json.Unmarshal(msg.Data, &reg)
			if reg.NodeID == "" {
				s.send(peer, wsMessage{Type: "register_error", Data: mustRaw(wsCommand{Command: "empty nodeId"})})
				continue
			}
			if registered {
				s.send(peer, wsMessage{Type: "register_error", Data: mustRaw(wsCommand{Command: "already registered"})})
				continue
			}
			if expected := lookupWSKey(reg.NodeID); expected != "" && reg.Key != expected {
				log.Printf("[ws] AUTH FAILED for node %s: invalid key", reg.NodeID)
				s.send(peer, wsMessage{Type: "register_error", Data: mustRaw(struct {
					Code    int    `json:"code"`
					Command string `json:"command"`
				}{401, "invalid key"})})
				c.Close(websocket.StatusPolicyViolation, "invalid key")
				return
			}
			// 严格模式：未预配密钥的节点一律拒绝（捐赠 / 管理员建的节点都有密钥）
			if WS_REQUIRE_KEY && lookupWSKey(reg.NodeID) == "" {
				log.Printf("[ws] AUTH FAILED for node %s: no key configured (ws-require-key)", reg.NodeID)
				s.send(peer, wsMessage{Type: "register_error", Data: mustRaw(struct {
					Code    int    `json:"code"`
					Command string `json:"command"`
				}{401, "key required"})})
				c.Close(websocket.StatusPolicyViolation, "key required")
				return
			}
			registered = true
			peer.id = reg.NodeID
			s.mu.Lock()
			if old, ok := s.peers[reg.NodeID]; ok && old != peer {
				old.conn.Close(websocket.StatusPolicyViolation, "replaced by new connection")
			}
			s.peers[reg.NodeID] = peer
			s.mu.Unlock()
			remote := remoteAddrForDisplay(r)
			log.Printf("[ws] node registered: %s from %s", reg.NodeID, remote)
			recordNodeOnline(reg.NodeID, remote, reg.Version, reg.Capabilities)
			otaOnNodeRegistered(reg.NodeID, reg.Version)
			s.send(peer, wsMessage{Type: "register_ok", NodeID: reg.NodeID, Data: mustRaw(struct {
				Heartbeat int `json:"heartbeatSeconds"`
			}{20})})

		case "probe_result":
			if !registered {
				continue
			}
			var res wsProbeResult
			if json.Unmarshal(msg.Data, &res) != nil {
				continue
			}
			res.Body = append(json.RawMessage(nil), res.Body...)
			s.probeMu.Lock()
			ch, ok := s.probeWait[res.RequestID]
			delete(s.probeWait, res.RequestID)
			s.probeMu.Unlock()
			if ok {
				ch <- res
			}

		case "report":
			if registered {
				handleWSReport(peer.id, append(json.RawMessage(nil), msg.Data...))
			}

		case "ping":
			if registered {
				s.send(peer, wsMessage{Type: "pong", NodeID: peer.id})
			}

		case "config_result":
			if registered {
				s.deliverCommandResult(append(json.RawMessage(nil), msg.Data...))
			}

		case "ota_result":
			// 首帧（accepted）投递给下发等待方；所有帧同步更新任务阶段
			if registered {
				data := append(json.RawMessage(nil), msg.Data...)
				s.deliverCommandResult(data)
				otaOnNodeResult(peer.id, data)
			}

		case "pong":
			// 心跳应答，仅刷新 last

		default:
			log.Printf("[ws] unknown message type from %s: %s", peer.id, msg.Type)
		}
	}
}

// RequestProbe 向单个 WS 节点下发拨测并等待结果。scheduler=true 表示中心主动调度（节点不重复上报）
func (s *wsServer) RequestProbe(nodeID, apiType, raw string, query map[string]string, timeout time.Duration, scheduler bool) (int, []byte, error) {
	return s.requestProbe(wsProbeRequest{APIType: apiType, Raw: raw, Query: query, Scheduler: scheduler}, nodeID, timeout)
}

// RequestToolbox 下发网络查询工具箱探测（apiType=toolbox，raw=节点 /v1/ 后的路径，body 原样转给节点）
func (s *wsServer) RequestToolbox(nodeID, path string, body []byte, timeout time.Duration) (int, []byte, error) {
	return s.requestProbe(wsProbeRequest{APIType: apiTypeToolbox, Raw: path, Body: json.RawMessage(body)}, nodeID, timeout)
}

func (s *wsServer) requestProbe(payload wsProbeRequest, nodeID string, timeout time.Duration) (int, []byte, error) {
	s.mu.Lock()
	peer, ok := s.peers[nodeID]
	s.mu.Unlock()
	if !ok {
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
	payload.RequestID = reqID
	if err := s.send(peer, wsMessage{Type: "probe", NodeID: nodeID, Data: mustRaw(payload)}); err != nil {
		s.errReqs.Add(1)
		return 0, nil, fmt.Errorf("ws probe send failed for node %s: %w", nodeID, err)
	}
	s.totalReqs.Add(1)
	select {
	case res := <-ch:
		if res.Error != "" {
			s.errReqs.Add(1)
			return 0, nil, fmt.Errorf("ws probe error: %s", res.Error)
		}
		body := []byte(res.Body)
		if len(body) > 0 && body[0] == '"' {
			var str string
			if json.Unmarshal(body, &str) == nil {
				body = []byte(str)
			}
		}
		return res.Status, body, nil
	case <-time.After(timeout):
		s.errReqs.Add(1)
		return 0, nil, fmt.Errorf("ws probe timeout for node %s", nodeID)
	}
}

// Kick 断开某节点连接（捐赠被停用 / 换 key 时）
func (s *wsServer) Kick(id, reason string) {
	s.mu.Lock()
	p, ok := s.peers[id]
	s.mu.Unlock()
	if ok {
		p.conn.Close(websocket.StatusPolicyViolation, reason)
	}
}

func (s *wsServer) NodeConnected(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.peers[id]
	return ok
}

func (s *wsServer) PeerIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.peers))
	for id := range s.peers {
		out = append(out, id)
	}
	return out
}

func (s *wsServer) PeerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.peers)
}

func (s *wsServer) Start(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.Handler)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("[ws] server listening on %s/ws", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("[ws] server stopped: %v", err)
	}
}

// maintenanceLoop 每 20s：心跳 ping + status 下发；>75s 无消息剔除；刷新 last_seen_at
func (s *wsServer) maintenanceLoop() {
	for {
		time.Sleep(20 * time.Second)
		s.mu.Lock()
		peers := make([]*wsPeer, 0, len(s.peers))
		for _, p := range s.peers {
			peers = append(peers, p)
		}
		s.mu.Unlock()
		stats := map[string]int64{
			"uptimeSeconds": int64(time.Since(s.startedAt).Seconds()),
			"totalRequests": s.totalReqs.Load(), "errorRequests": s.errReqs.Load(),
		}
		alive := make([]string, 0, len(peers))
		for _, p := range peers {
			if idle := time.Since(time.Unix(0, p.last.Load())); idle > 75*time.Second {
				log.Printf("[ws] node %s idle for %s, evicting", p.id, idle.Round(time.Second))
				p.conn.Close(websocket.StatusPolicyViolation, "idle timeout")
				s.mu.Lock()
				if s.peers[p.id] == p {
					delete(s.peers, p.id)
				}
				s.mu.Unlock()
				recordNodeOffline(p.id, "idle timeout")
				continue
			}
			alive = append(alive, p.id)
			s.send(p, wsMessage{Type: "ping", NodeID: p.id})
			s.send(p, wsMessage{Type: "status", NodeID: p.id, Data: mustRaw(stats)})
		}
		touchNodes(alive)
	}
}

// describeWSDisconnect 把 WS 读错误翻译成给人看的离线原因（写进节点事件和掉线通知）。
// 最常见的 "failed to get reader: failed to read frame header: EOF" 是对端没发关闭帧就断开 TCP，
// 不是中心的故障：节点断网 / 进程被杀或重启 / 中间的代理或 Cloudflare 掐了连接。
func describeWSDisconnect(err error) string {
	if err == nil {
		return "连接已断开"
	}
	if code := websocket.CloseStatus(err); code != -1 {
		switch code {
		case websocket.StatusNormalClosure:
			return "节点正常关闭了连接"
		case websocket.StatusGoingAway:
			return "节点正在重启或退出"
		default:
			return fmt.Sprintf("节点关闭了连接（关闭码 %d）", int(code))
		}
	}
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), strings.Contains(err.Error(), "EOF"):
		return "连接中断：节点未正常关闭连接（节点网络波动、进程重启，或被中间代理断开）"
	case errors.Is(err, net.ErrClosed), strings.Contains(err.Error(), "use of closed network connection"):
		return "连接已被中心关闭（节点重复连接或被替换）"
	case errors.Is(err, context.Canceled):
		return "连接已取消"
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(err.Error(), "i/o timeout"):
		return "连接读取超时"
	case strings.Contains(err.Error(), "connection reset"):
		return "连接被对端重置（节点网络波动或被防火墙中断）"
	}
	return "连接异常断开：" + err.Error()
}

package main

// ==================== 捐赠节点速度测试转发（给 1IPW 主站后端用） ====================
//
//	GET  /speedtest/:nodeId/payload?bytes=N   下行：节点经 WS 推二进制帧，中心边收边写给 HTTP 客户端
//	POST /speedtest/:nodeId/upload            上行：中心把请求体切成二进制帧经 WS 推给节点，节点统计后回结果
//	Authorization: Bearer <sync-token>
//
// 捐赠节点是 WS 节点、没有公网地址，浏览器没法直连。测到的是「浏览器 → 1IPW → BOCE → WS → 节点」整条链路的速度，
// 瓶颈通常在 BOCE 出口 / Cloudflare 而不是节点本身，调用方应明示这一点。
//
// 二进制帧：[1 字节 id 长度][requestId][载荷]；载荷为空的帧表示上行结束。
// 节点不上报 "speedtest" 能力（旧版程序）时直接 501，不下发。
// 每个节点同一时刻只允许一次测速，全局最多 speedGlobalMax 路并发，避免把 BOCE 的出口跑满。

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

const (
	apiTypeSpeedtest  = "speedtest"
	speedtestCapName  = "speedtest"
	speedFrameChunk   = 64 * 1024
	speedStreamBuf    = 16 // 下行缓冲块数（约 1MB），满了就让节点那一侧因 TCP 背压而放慢
	speedMinBytes     = 1 << 20
	speedMaxBytes     = 512 << 20
	speedDefaultBytes = 20 << 20
	speedDownloadTime = 25 * time.Second
	speedUploadBody   = 64 << 20
	speedUploadTime   = 40 * time.Second
	speedResultWait   = 15 * time.Second
	speedGlobalMax    = 4
	speedStallTimeout = 8 * time.Second // 消费端（HTTP 客户端）长时间不读就放弃这路测速
)

// speedStream 是一路下行测速：节点推来的数据块经 ch 交给 HTTP 处理协程。
type speedStream struct {
	nodeID string
	ch     chan []byte
	ctx    context.Context
	cancel context.CancelFunc
}

var (
	speedStreams  sync.Map // requestId -> *speedStream
	speedNodeBusy sync.Map // nodeId -> struct{}
	speedGlobal   = make(chan struct{}, speedGlobalMax)
)

// speedtestOnFrame 处理节点发来的二进制帧（在 WS 读循环里调用，阻塞即对该节点连接施加背压）。
func speedtestOnFrame(nodeID string, data []byte) {
	if len(data) < 2 {
		return
	}
	n := int(data[0])
	if n == 0 || len(data) < 1+n {
		return
	}
	v, ok := speedStreams.Load(string(data[1 : 1+n]))
	if !ok {
		return
	}
	st := v.(*speedStream)
	if st.nodeID != nodeID { // 只认该路测速所属节点发来的帧
		return
	}
	chunk := data[1+n:]
	if len(chunk) == 0 {
		return
	}
	select {
	case st.ch <- chunk:
		return
	default:
	}
	timer := time.NewTimer(speedStallTimeout)
	defer timer.Stop()
	select {
	case st.ch <- chunk:
	case <-st.ctx.Done():
	case <-timer.C:
		st.cancel() // 客户端不读了：结束这一路，节点收到 cancel 后停止发送
	}
}

func (s *wsServer) sendFrame(p *wsPeer, requestID string, payload []byte) error {
	frame := make([]byte, 0, 1+len(requestID)+len(payload))
	frame = append(frame, byte(len(requestID)))
	frame = append(frame, requestID...)
	frame = append(frame, payload...)
	return s.writeBinary(p, frame)
}

func (s *wsServer) writeBinary(p *wsPeer, frame []byte) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.conn.Write(ctx, websocket.MessageBinary, frame)
}

func (s *wsServer) peerFor(id string) (*wsPeer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.peers[id]
	return p, ok
}

func nodeHasSpeedtest(id string) bool {
	n, ok := nodeSnapshot(id)
	if !ok {
		return false
	}
	for _, c := range strings.Split(n.Capabilities, ",") {
		if strings.TrimSpace(c) == speedtestCapName {
			return true
		}
	}
	return false
}

// speedtestSession 是一次测速的公共准备：校验节点、占位并发、登记结果通道、下发 probe。
type speedtestSession struct {
	nodeID, reqID string
	peer          *wsPeer
	result        chan wsProbeResult
	release       func()
}

func (s *wsServer) cancelSpeedtest(ss *speedtestSession) {
	_ = s.send(ss.peer, wsMessage{Type: "speedtest_cancel", NodeID: ss.nodeID, Data: mustRaw(map[string]string{"requestId": ss.reqID})})
}

func beginSpeedtest(c *gin.Context, raw string, query map[string]string) *speedtestSession {
	if SYNC_TOKEN == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "speedtest disabled"})
		return nil
	}
	if bearerToken(c) != SYNC_TOKEN {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid sync token"})
		return nil
	}
	id := c.Param("nodeId")
	n := findNode(apiPoolSnapshot(), id)
	if n == nil {
		n = findNode(locationPoolSnapshot(), id)
	}
	if n == nil || !n.UseWS() {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return nil
	}
	if wsSrv == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "node offline"})
		return nil
	}
	peer, ok := wsSrv.peerFor(id)
	if !ok {
		c.JSON(http.StatusBadGateway, gin.H{"error": "node offline"})
		return nil
	}
	if !nodeHasSpeedtest(id) {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "node program does not support speedtest; update the node"})
		return nil
	}
	if _, busy := speedNodeBusy.LoadOrStore(id, struct{}{}); busy {
		c.Header("Retry-After", "3")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "this node is busy with another speed test"})
		return nil
	}
	select {
	case speedGlobal <- struct{}{}:
	default:
		speedNodeBusy.Delete(id)
		c.Header("Retry-After", "3")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "too many speed tests are running; try again shortly"})
		return nil
	}
	ss := &speedtestSession{nodeID: id, reqID: "s" + genRequestID(), peer: peer, result: make(chan wsProbeResult, 1)}
	ss.release = func() {
		wsSrv.probeMu.Lock()
		delete(wsSrv.probeWait, ss.reqID)
		wsSrv.probeMu.Unlock()
		speedNodeBusy.Delete(id)
		<-speedGlobal
	}
	wsSrv.probeMu.Lock()
	wsSrv.probeWait[ss.reqID] = ss.result
	wsSrv.probeMu.Unlock()
	return ss
}

func (s *wsServer) sendSpeedtestProbe(ss *speedtestSession, raw string, query map[string]string) error {
	payload := wsProbeRequest{RequestID: ss.reqID, APIType: apiTypeSpeedtest, Raw: raw, Query: query}
	return s.send(ss.peer, wsMessage{Type: "probe", NodeID: ss.nodeID, Data: mustRaw(payload)})
}

func speedtestDownload(c *gin.Context) {
	size := int64(speedDefaultBytes)
	if raw := strings.TrimSpace(c.Query("bytes")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < speedMinBytes || v > speedMaxBytes {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bytes must be between 1 MiB and 512 MiB"})
			return
		}
		size = v
	}
	ss := beginSpeedtest(c, "download", nil)
	if ss == nil {
		return
	}
	defer ss.release()
	ctx, cancel := context.WithTimeout(c.Request.Context(), speedDownloadTime)
	defer cancel()
	st := &speedStream{nodeID: ss.nodeID, ch: make(chan []byte, speedStreamBuf), ctx: ctx, cancel: cancel}
	speedStreams.Store(ss.reqID, st)
	defer speedStreams.Delete(ss.reqID)

	if err := wsSrv.sendSpeedtestProbe(ss, "download", map[string]string{"bytes": strconv.FormatInt(size, 10)}); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "node did not respond"})
		return
	}
	wsSrv.totalReqs.Add(1)

	started := false
	start := func() {
		if started {
			return
		}
		started = true
		c.Header("Content-Type", "application/octet-stream")
		c.Header("Cache-Control", "no-store")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)
	}
	write := func(chunk []byte) bool {
		start()
		if _, err := c.Writer.Write(chunk); err != nil {
			return false
		}
		c.Writer.Flush()
		return true
	}
	for {
		select {
		case chunk := <-st.ch:
			if !write(chunk) {
				wsSrv.cancelSpeedtest(ss)
				return
			}
		case res := <-ss.result:
			for { // 结果帧晚于数据帧到达：把缓冲里剩下的数据写完
				select {
				case chunk := <-st.ch:
					if !write(chunk) {
						return
					}
					continue
				default:
				}
				break
			}
			if !started {
				status := res.Status
				if status == 0 || status == http.StatusOK {
					status = http.StatusBadGateway
				}
				c.Data(status, "application/json; charset=utf-8", []byte(res.Body))
			}
			return
		case <-ctx.Done():
			wsSrv.cancelSpeedtest(ss)
			if !started {
				c.JSON(http.StatusGatewayTimeout, gin.H{"error": "node did not respond"})
			}
			return
		}
	}
}

func speedtestUpload(c *gin.Context) {
	if c.Request.ContentLength > speedUploadBody {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body is too large"})
		return
	}
	ss := beginSpeedtest(c, "upload", nil)
	if ss == nil {
		return
	}
	defer ss.release()
	ctx, cancel := context.WithTimeout(c.Request.Context(), speedUploadTime)
	defer cancel()
	if err := wsSrv.sendSpeedtestProbe(ss, "upload", nil); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "node did not respond"})
		return
	}
	wsSrv.totalReqs.Add(1)

	body := http.MaxBytesReader(c.Writer, c.Request.Body, speedUploadBody)
	buf := make([]byte, speedFrameChunk)
	for {
		if ctx.Err() != nil {
			wsSrv.cancelSpeedtest(ss)
			c.JSON(http.StatusGatewayTimeout, gin.H{"error": "speed test timed out"})
			return
		}
		n, err := body.Read(buf)
		if n > 0 {
			if werr := wsSrv.sendFrame(ss.peer, ss.reqID, buf[:n]); werr != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": "node did not respond"})
				return
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil { // 客户端中途断开（到 15 秒主动 abort）：通知节点收尾
			wsSrv.cancelSpeedtest(ss)
			return
		}
	}
	if err := wsSrv.sendFrame(ss.peer, ss.reqID, nil); err != nil { // 空载荷 = 上行结束
		c.JSON(http.StatusBadGateway, gin.H{"error": "node did not respond"})
		return
	}
	select {
	case res := <-ss.result:
		status := res.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		c.Header("Cache-Control", "no-store")
		c.Data(status, "application/json; charset=utf-8", []byte(res.Body))
	case <-time.After(speedResultWait):
		wsSrv.cancelSpeedtest(ss)
		c.JSON(http.StatusGatewayTimeout, gin.H{"error": "node did not respond"})
	}
}

func registerSpeedtestRoutes(r *gin.Engine) {
	r.GET("/speedtest/:nodeId/payload", speedtestDownload)
	r.POST("/speedtest/:nodeId/upload", speedtestUpload)
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeSpeedNode 模拟节点程序的测速行为：download 推 bytes 字节的二进制帧后回结果，
// upload 统计收到的字节直到空帧再回结果，speedtest_cancel 打断下行。
func fakeSpeedNode(t *testing.T, url, nodeID, key string, caps []string, cancelled *atomic.Int32) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		c, _, err := websocket.Dial(ctx, url, nil)
		if err != nil {
			return
		}
		c.SetReadLimit(1 << 20)
		defer c.Close(websocket.StatusNormalClosure, "")
		send := func(typ string, data any) {
			_ = c.Write(ctx, websocket.MessageText, mustRaw(wsMessage{Type: typ, NodeID: nodeID, TS: time.Now().Unix(), Data: mustRaw(data)}))
		}
		send("register", map[string]any{"nodeId": nodeID, "key": key, "version": "3.1.0", "capabilities": caps})
		uploads := map[string]*int64{}
		stop := map[string]chan struct{}{}
		frame := func(id string, payload []byte) []byte {
			return append(append([]byte{byte(len(id))}, id...), payload...)
		}
		for {
			typ, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				n := int(b[0])
				id := string(b[1 : 1+n])
				counter := uploads[id]
				if counter == nil {
					continue
				}
				if len(b) == 1+n { // 空载荷 = 上行结束
					send("probe_result", map[string]any{"requestId": id, "status": 200, "body": map[string]any{"received": *counter}})
					continue
				}
				*counter += int64(len(b) - 1 - n)
				continue
			}
			var m wsMessage
			_ = json.Unmarshal(b, &m)
			switch m.Type {
			case "speedtest_cancel":
				var p struct {
					RequestID string `json:"requestId"`
				}
				_ = json.Unmarshal(m.Data, &p)
				if cancelled != nil {
					cancelled.Add(1)
				}
				if ch := stop[p.RequestID]; ch != nil {
					close(ch)
					delete(stop, p.RequestID)
				}
			case "probe":
				var p wsProbeRequest
				_ = json.Unmarshal(m.Data, &p)
				if p.APIType != apiTypeSpeedtest {
					send("probe_result", map[string]any{"requestId": p.RequestID, "status": 400, "body": map[string]any{"error": "not speedtest"}})
					continue
				}
				switch p.Raw {
				case "upload":
					var n int64
					uploads[p.RequestID] = &n
				case "download":
					size, _ := strconv.Atoi(p.Query["bytes"])
					ch := make(chan struct{})
					stop[p.RequestID] = ch
					go func(id string, size int) {
						chunk := bytes.Repeat([]byte{0xAB}, 64*1024)
						sent := 0
						for sent < size {
							select {
							case <-ch:
								return
							default:
							}
							n := len(chunk)
							if size-sent < n {
								n = size - sent
							}
							if c.Write(ctx, websocket.MessageBinary, frame(id, chunk[:n])) != nil {
								return
							}
							sent += n
						}
						send("probe_result", map[string]any{"requestId": id, "status": 200, "body": map[string]any{"sent": sent}})
					}(p.RequestID, size)
				}
			}
		}
	}()
	return cancel
}

func speedReq(r http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestSpeedtestOverWS(t *testing.T) {
	r := setupTest(t)
	registerSpeedtestRoutes(r)
	wsSrv = newWSServer()
	srv := httptest.NewServer(httpHandlerFunc(wsSrv.Handler))
	defer srv.Close()
	defer func() { wsSrv = nil }()
	SYNC_TOKEN = "sync-secret"
	defer func() { SYNC_TOKEN = "" }()

	db.Create(&NodeDef{NodeID: "don-1", Label: "捐赠", WS: true, Pool: "api", Enabled: true, WSKey: "k1"})
	db.Create(&NodeDef{NodeID: "old-1", Label: "旧版", WS: true, Pool: "api", Enabled: true, WSKey: "k2"})
	db.Create(&NodeDef{NodeID: "http-1", Label: "直连", URL: "https://h.test", Pool: "api", Enabled: true})
	reloadNodePools()

	if w := speedReq(r, "GET", "/speedtest/don-1/payload?bytes=1048576", "sync-secret", nil); w.Code != 502 {
		t.Fatalf("offline want 502 got %d", w.Code)
	}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	var cancelled atomic.Int32
	defer fakeSpeedNode(t, wsURL, "don-1", "k1", []string{"toolbox:tcp-test", "speedtest"}, &cancelled)()
	defer fakeSpeedNode(t, wsURL, "old-1", "k2", []string{"toolbox:tcp-test"}, nil)()
	waitFor(t, func() bool { return wsSrv.NodeConnected("don-1") && wsSrv.NodeConnected("old-1") })
	waitFor(t, func() bool { return nodeHasSpeedtest("don-1") })

	if w := speedReq(r, "GET", "/speedtest/don-1/payload?bytes=1048576", "wrong", nil); w.Code != 401 {
		t.Fatalf("bad token code=%d", w.Code)
	}
	if w := speedReq(r, "GET", "/speedtest/http-1/payload?bytes=1048576", "sync-secret", nil); w.Code != 404 {
		t.Fatalf("non-ws code=%d", w.Code)
	}
	if w := speedReq(r, "GET", "/speedtest/old-1/payload?bytes=1048576", "sync-secret", nil); w.Code != 501 {
		t.Fatalf("old node want 501 got %d %s", w.Code, w.Body.String())
	}
	if w := speedReq(r, "GET", "/speedtest/don-1/payload?bytes=10", "sync-secret", nil); w.Code != 400 {
		t.Fatalf("small bytes want 400 got %d", w.Code)
	}

	// 下行：收到的字节数必须与请求的一致（结果帧晚于数据帧也不能丢尾巴）
	const size = 3*1024*1024 + 12345
	w := speedReq(r, "GET", "/speedtest/don-1/payload?bytes="+strconv.Itoa(size), "sync-secret", nil)
	if w.Code != 200 || w.Body.Len() != size {
		t.Fatalf("download: code=%d len=%d want %d", w.Code, w.Body.Len(), size)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content-type %q", ct)
	}

	// 上行：节点统计到的字节数 == 请求体大小
	payload := bytes.Repeat([]byte{0xCD}, 8*1024*1024+777)
	w = speedReq(r, "POST", "/speedtest/don-1/upload", "sync-secret", payload)
	var res map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if w.Code != 200 || int(res["received"].(float64)) != len(payload) {
		t.Fatalf("upload: code=%d body=%s", w.Code, w.Body.String())
	}

	// 一次测速结束后并发占位要释放，下一次能继续
	if w := speedReq(r, "GET", "/speedtest/don-1/payload?bytes=1048576", "sync-secret", nil); w.Code != 200 {
		t.Fatalf("second download code=%d", w.Code)
	}
}

func TestSpeedtestNodeBusyAndCancel(t *testing.T) {
	r := setupTest(t)
	registerSpeedtestRoutes(r)
	wsSrv = newWSServer()
	srv := httptest.NewServer(httpHandlerFunc(wsSrv.Handler))
	defer srv.Close()
	defer func() { wsSrv = nil }()
	SYNC_TOKEN = "sync-secret"
	defer func() { SYNC_TOKEN = "" }()
	db.Create(&NodeDef{NodeID: "don-1", Label: "捐赠", WS: true, Pool: "api", Enabled: true, WSKey: "k1"})
	reloadNodePools()
	var cancelled atomic.Int32
	defer fakeSpeedNode(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", "don-1", "k1", []string{"speedtest"}, &cancelled)()
	waitFor(t, func() bool { return nodeHasSpeedtest("don-1") })

	// 占着节点：第二次应 429
	speedNodeBusy.Store("don-1", struct{}{})
	if w := speedReq(r, "GET", "/speedtest/don-1/payload?bytes=1048576", "sync-secret", nil); w.Code != 429 {
		t.Fatalf("busy want 429 got %d", w.Code)
	}
	speedNodeBusy.Delete("don-1")

	// 客户端中途断开：中心应给节点发 speedtest_cancel
	ctx, cancelReq := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/speedtest/don-1/payload?bytes=536870912", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer sync-secret")
	done := make(chan struct{})
	go func() {
		r.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)
	cancelReq()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("download handler did not return after client cancel")
	}
	waitFor(t, func() bool { return cancelled.Load() == 1 })
	waitFor(t, func() bool { _, busy := speedNodeBusy.Load("don-1"); return !busy })
}

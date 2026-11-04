package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeToolboxNode 只应答 apiType=toolbox：回显 path 与 body，模拟节点进程内执行 POST /v1/<path>
func fakeToolboxNode(t *testing.T, url, nodeID, key string) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		c, _, err := websocket.Dial(ctx, url, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		send := func(typ string, data any) {
			_ = c.Write(ctx, websocket.MessageText, mustRaw(wsMessage{Type: typ, NodeID: nodeID, TS: time.Now().Unix(), Data: mustRaw(data)}))
		}
		send("register", map[string]any{"nodeId": nodeID, "key": key, "version": "3.0.0",
			"capabilities": []string{"probe", "toolbox:tcp-test", "toolbox:http-test"}})
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			var m wsMessage
			_ = json.Unmarshal(b, &m)
			if m.Type != "probe" {
				continue
			}
			var p wsProbeRequest
			_ = json.Unmarshal(m.Data, &p)
			if p.APIType != apiTypeToolbox {
				send("probe_result", map[string]any{"requestId": p.RequestID, "status": 400, "body": map[string]any{"error": "not toolbox"}})
				continue
			}
			status := 200
			if string(p.Body) == "{}" {
				status = 400
			}
			send("probe_result", map[string]any{"requestId": p.RequestID, "status": status,
				"body": map[string]any{"path": p.Raw, "echo": json.RawMessage(p.Body)}})
		}
	}()
	return cancel
}

func TestToolboxForwardOverWS(t *testing.T) {
	r := setupTest(t)
	registerToolboxRoutes(r)
	registerSyncRoutes(r)
	wsSrv = newWSServer()
	srv := httptest.NewServer(httpHandlerFunc(wsSrv.Handler))
	defer srv.Close()
	defer func() { wsSrv = nil }()
	SYNC_TOKEN = "sync-secret"
	defer func() { SYNC_TOKEN = "" }()

	db.Create(&NodeDef{NodeID: "don-1", Label: "捐赠", WS: true, Pool: "api", Enabled: true, WSKey: "k1"})
	db.Create(&NodeDef{NodeID: "http-1", Label: "直连", URL: "https://h.test", Pool: "api", Enabled: true})
	reloadNodePools()

	// 未连上：502
	if code, body := call(r, "POST", "/toolbox/don-1/tcp-test", "sync-secret", map[string]any{"host": "1.1.1.1"}); code != 502 || body["error"] != "node offline" {
		t.Fatalf("offline: %d %v", code, body)
	}
	stop := fakeToolboxNode(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", "don-1", "k1")
	defer stop()
	waitFor(t, func() bool { return wsSrv.NodeConnected("don-1") })

	if code, _ := call(r, "POST", "/toolbox/don-1/tcp-test", "wrong", map[string]any{}); code != 401 {
		t.Fatalf("bad token code=%d", code)
	}
	if code, _ := call(r, "POST", "/toolbox/don-1/rm-rf", "sync-secret", map[string]any{}); code != 404 {
		t.Fatalf("bad path code=%d", code)
	}
	if code, _ := call(r, "POST", "/toolbox/http-1/tcp-test", "sync-secret", map[string]any{}); code != 404 {
		t.Fatalf("non-ws node code=%d", code)
	}
	if code, _ := call(r, "POST", "/toolbox/ghost/tcp-test", "sync-secret", map[string]any{}); code != 404 {
		t.Fatalf("unknown node code=%d", code)
	}
	// 空体：节点回 400（能力探针用这个判定"路由存在"）
	if code, body := call(r, "POST", "/toolbox/don-1/tcp-test", "sync-secret", map[string]any{}); code != 400 || body["path"] != "tcp-test" {
		t.Fatalf("empty probe: %d %v", code, body)
	}
	code, body := call(r, "POST", "/toolbox/don-1/http-test", "sync-secret", map[string]any{"url": "https://example.com"})
	if code != 200 || body["path"] != "http-test" || body["echo"].(map[string]any)["url"] != "https://example.com" {
		t.Fatalf("forward: %d %v", code, body)
	}

	// 同步结果带上节点上报的能力与版本
	waitFor(t, func() bool { n, _ := nodeSnapshot("don-1"); return n.Version == "3.0.0" })
	code, sync := call(r, "GET", "/api/sync/nodes", "sync-secret", nil)
	if code != 200 {
		t.Fatalf("sync %d", code)
	}
	found := false
	for _, n := range sync["nodes"].([]any) {
		m := n.(map[string]any)
		if m["id"] == "don-1" {
			found = true
			caps := m["capabilities"].([]any)
			if m["version"] != "3.0.0" || len(caps) != 3 || caps[1] != "toolbox:tcp-test" {
				t.Fatalf("don-1 sync: %v", m)
			}
		}
		if m["id"] == "http-1" && m["capabilities"] == nil {
			t.Fatalf("capabilities must be [] not null: %v", m)
		}
	}
	if !found {
		t.Fatal("don-1 missing from sync")
	}
}

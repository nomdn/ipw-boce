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

// fakeWSNode 连接中心并按协议应答 config / ota；ota 成功后用新版本重连
func fakeWSNode(t *testing.T, url, nodeID, key, version string, caps []string) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		ver := version
		for ctx.Err() == nil {
			c, _, err := websocket.Dial(ctx, url, nil)
			if err != nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			send := func(typ string, data any) {
				_ = c.Write(ctx, websocket.MessageText, mustRaw(wsMessage{Type: typ, NodeID: nodeID, TS: time.Now().Unix(), Data: mustRaw(data)}))
			}
			send("register", map[string]any{"nodeId": nodeID, "key": key, "version": ver, "capabilities": caps})
			restart := false
			for !restart {
				_, b, err := c.Read(ctx)
				if err != nil {
					break
				}
				var m wsMessage
				_ = json.Unmarshal(b, &m)
				var d map[string]any
				_ = json.Unmarshal(m.Data, &d)
				switch m.Type {
				case "config":
					send("config_result", map[string]any{"requestId": d["requestId"], "ok": true, "applied": []string{"rate-limit"},
						"config": map[string]any{"rate-limit": 60, "access-token": "***"}})
				case "ota":
					send("ota_result", map[string]any{"requestId": d["requestId"], "ok": true, "stage": "accepted"})
					ver = strings.TrimPrefix(d["version"].(string), "v")
					restart = true
				}
			}
			c.Close(websocket.StatusNormalClosure, "")
		}
	}()
	return cancel
}

func TestNodeTargetRelay(t *testing.T) {
	cases := map[[3]string]string{
		{"http://1.2.3.4:18080/", "tcping", "1.1.1.1"}:                              "http://1.2.3.4:18080/v1/tcping/1.1.1.1",
		{"http://1.2.3.4:18080", "dns", "a/x.com"}:                                  "http://1.2.3.4:18080/v1/dns/a/x.com",
		{"https://1ipw.cn/middleware/hongkong/{path}", "detail", "https://qq.com"}: "https://1ipw.cn/middleware/hongkong/detail/https://qq.com",
	}
	for in, want := range cases {
		if got := nodeTarget(in[0], in[1], in[2]); got != want {
			t.Fatalf("nodeTarget(%v)=%s want %s", in, got, want)
		}
	}
}

func TestWSConfigAndOTA(t *testing.T) {
	setupTest(t)
	wsSrv = newWSServer()
	srv := httptest.NewServer(httpHandlerFunc(wsSrv.Handler))
	defer srv.Close()
	defer func() { wsSrv = nil }()
	WS_KEYS = map[string]string{"ws-1": "secret"}
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	stop := fakeWSNode(t, url, "ws-1", "secret", "1.0.0", []string{"probe", "config", "ota"})
	defer stop()
	waitFor(t, func() bool { return wsSrv.NodeConnected("ws-1") })

	res, err := requestNodeConfig("ws-1", "get", nil, false)
	if err != nil || res.Channel != "ws" || res.Config["rate-limit"].(float64) != 60 {
		t.Fatalf("config get: %+v %v", res, err)
	}

	// 能力门：明确不含 ota 的节点直接拒绝
	db.Create(&Node{NodeID: "old", Capabilities: "probe"})
	if err := nodeCapabilityGate("old", capabilityOTA, "OTA"); err == nil {
		t.Fatal("capability gate should deny")
	}

	r := setupRouterOnly()
	code, body := call(r, "POST", "/admin/nodes/ws-1/ota", ADMIN_TOKEN, map[string]any{"version": "v1.2.0"})
	if code != 200 {
		t.Fatalf("ota dispatch %d %v", code, body)
	}
	waitFor(t, func() bool {
		var task OTATask
		db.Order("id desc").First(&task)
		return task.Status == otaSuccess
	})
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

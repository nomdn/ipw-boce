// mocknode 模拟拨测节点：用于本地联调与冒烟测试。
//
// 功能：
//   - 作为 WS 客户端注册到 middleware（register + 心跳 + 应答 probe）
//   - 同时在本地起 HTTP 服务模拟 HTTP 上游节点（/v1/* 回显）
//
// 用法：
//
//	go run ./scripts/mocknode -ws ws://127.0.0.1:18092/ws -id test-node -key secret -http 127.0.0.1:18093
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

type envelope struct {
	Type   string          `json:"type"`
	NodeID string          `json:"nodeId,omitempty"`
	TS     int64           `json:"ts"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// mockVersion 当前对外展示的版本号：-version 初值，OTA 模拟成功后变化（健康检查与 WS 注册共用）
var mockVersion = "v0.0.1-mock"

func currentVersion() string { return mockVersion }

func main() {
	wsURL := flag.String("ws", "", "middleware ws 地址（空 = 纯 HTTP 节点模式，仅跑 -http 上游）")
	nodeID := flag.String("id", "mock-node", "节点 id")
	key := flag.String("key", "", "注册 key（middleware 配了 ws-keys 时必填）")
	httpAddr := flag.String("http", "", "同时启动的本地 HTTP 上游监听地址（空 = 不启动）")
	reportFile := flag.String("report", "", "注册成功后经 WS 发送一次数据上报（JSON 文件路径，测 report 协议用）")
	version := flag.String("version", "v0.0.1-mock", "注册时上报的版本号（OTA 模拟成功后会变化）")
	flag.Parse()
	mockVersion = *version

	if *httpAddr != "" {
		go runHTTPUpstream(*httpAddr)
	}

	// -ws 为空 = 纯 HTTP 节点模式：只跑 HTTP 上游，不连 WS（收集中心直接转发 + /report 上报）
	if *wsURL == "" {
		if *httpAddr == "" {
			log.Fatal("nothing to do: 需要 -ws（WS 节点）或 -http（纯 HTTP 节点上游）至少其一")
		}
		log.Printf("[mocknode] http-only mode, upstream on %s (no WS registration)", *httpAddr)
		select {} // 常驻
	}

	// OTA 模拟：收到 ota 指令后回报各阶段成功，然后断开重连并上报新版本号，
	// 模拟"节点替换二进制重启后重新注册"——收集中心据此把任务判成功
	for {
		if err := runWS(*wsURL, *nodeID, *key, *reportFile, &mockVersion); err != nil {
			log.Printf("[mocknode] ws session ended: %v, reconnect in 3s", err)
		} else {
			log.Printf("[mocknode] ws session closed, reconnect in 3s")
		}
		time.Sleep(3 * time.Second)
	}
}

func runWS(url, nodeID, key, reportFile string, version *string) error {
	ctx, cancel := contextWithSignal()
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close(websocket.StatusInternalError, "bye")

	reg, _ := json.Marshal(map[string]any{
		"nodeId":       nodeID,
		"key":          key,
		"version":      *version,
		"capabilities": []string{"probe", "report", "config", "ota"},
	})
	if err := send(conn, envelope{Type: "register", NodeID: nodeID, TS: time.Now().Unix(), Data: reg}); err != nil {
		return err
	}
	log.Printf("[mocknode] registering as %s (version %s) -> %s", nodeID, *version, url)

	// 注册成功后发一次 WS 数据上报（测试 report 协议）
	go func() {
		if reportFile == "" {
			return
		}
		time.Sleep(500 * time.Millisecond)
		raw, err := os.ReadFile(reportFile)
		if err != nil {
			log.Printf("[mocknode] read report file: %v", err)
			return
		}
		if err := send(conn, envelope{Type: "report", NodeID: nodeID, TS: time.Now().Unix(), Data: raw}); err != nil {
			log.Printf("[mocknode] ws report send: %v", err)
			return
		}
		log.Printf("[mocknode] ws report sent (%d bytes)", len(raw))
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var msg envelope
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "register_ok":
			log.Printf("[mocknode] registered ok")
		case "register_error":
			log.Printf("[mocknode] register rejected: %s", string(msg.Data))
			return fmt.Errorf("register rejected")
		case "probe":
			var req struct {
				RequestID string            `json:"requestId"`
				APIType   string            `json:"apiType"`
				Raw       string            `json:"raw"`
				Query     map[string]string `json:"query,omitempty"`
			}
			_ = json.Unmarshal(msg.Data, &req)
			log.Printf("[mocknode] probe %s %s %v", req.APIType, req.Raw, req.Query)
			body, _ := json.Marshal(map[string]any{
				"ok":      true,
				"apiType": req.APIType,
				"raw":     req.Raw,
				"port":    req.Query["port"],
				"node":    nodeID,
			})
			res, _ := json.Marshal(map[string]any{
				"requestId": req.RequestID,
				"status":    200,
				"body":      json.RawMessage(body),
			})
			if err := send(conn, envelope{Type: "probe_result", NodeID: nodeID, TS: time.Now().Unix(), Data: res}); err != nil {
				return err
			}
		case "ping":
			_ = send(conn, envelope{Type: "pong", NodeID: nodeID, TS: time.Now().Unix()})
		case "config":
			// 模拟配置指令：总是成功（回执带原 requestId），供控制台 patch/refresh 链路联调
			var req struct {
				RequestID string `json:"requestId"`
			}
			_ = json.Unmarshal(msg.Data, &req)
			res, _ := json.Marshal(map[string]any{"requestId": req.RequestID, "ok": true, "config": map[string]any{}})
			_ = send(conn, envelope{Type: "config_result", NodeID: nodeID, TS: time.Now().Unix(), Data: res})
		case "ota":
			// 模拟 OTA：逐阶段回报成功后断开重连，重连时上报 ota.version 里的新版本号
			var req struct {
				RequestID string `json:"requestId"`
				Version   string `json:"version"`
				URL       string `json:"url"`
			}
			_ = json.Unmarshal(msg.Data, &req)
			log.Printf("[mocknode] ota received: version=%q url=%q", req.Version, req.URL)
			stages := []string{"downloading", "verifying", "installing", "restarting"}
			for _, st := range stages {
				res, _ := json.Marshal(map[string]any{"requestId": req.RequestID, "ok": true, "stage": st})
				if err := send(conn, envelope{Type: "ota_result", NodeID: nodeID, TS: time.Now().Unix(), Data: res}); err != nil {
					return err
				}
				time.Sleep(200 * time.Millisecond)
			}
			if req.Version != "" {
				*version = req.Version
			} else {
				*version = *version + "-url" // url 直发：版本号变了即算更新
			}
			log.Printf("[mocknode] ota restart simulated, will re-register as version %s", *version)
			return fmt.Errorf("ota restart simulated")
		}
	}
}

// runHTTPUpstream 模拟 HTTP 上游节点：/v1/* 原样回显路径与 query；
// / 为健康检查（看门狗探活打 url 根路径，回 2xx + version/capabilities，对齐真实节点）
func runHTTPUpstream(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":       "ok",
			"version":      currentVersion(),
			"capabilities": []string{"probe", "report", "config", "ota"},
		})
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[mocknode] http hit %s query=%s", r.URL.Path, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"path":   r.URL.Path,
			"query":  r.URL.RawQuery,
			"source": "mocknode-http",
		})
	})
	log.Printf("[mocknode] http upstream listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("[mocknode] http upstream stopped: %v", err)
	}
}

func send(c *websocket.Conn, msg envelope) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Write(ctx, websocket.MessageText, data)
}

// contextWithSignal 返回收到 SIGINT/SIGTERM 时取消的 context
func contextWithSignal() (ctx context.Context, cancel context.CancelFunc) {
	ctx, cancel = context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

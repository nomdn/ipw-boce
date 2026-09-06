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

func main() {
	wsURL := flag.String("ws", "ws://127.0.0.1:8092/ws", "middleware ws 地址")
	nodeID := flag.String("id", "mock-node", "节点 id")
	key := flag.String("key", "", "注册 key（middleware 配了 ws-keys 时必填）")
	httpAddr := flag.String("http", "", "同时启动的本地 HTTP 上游监听地址（空 = 不启动）")
	reportFile := flag.String("report", "", "注册成功后经 WS 发送一次数据上报（JSON 文件路径，测 report 协议用）")
	flag.Parse()

	if *httpAddr != "" {
		go runHTTPUpstream(*httpAddr)
	}

	for {
		if err := runWS(*wsURL, *nodeID, *key, *reportFile); err != nil {
			log.Printf("[mocknode] ws session ended: %v, reconnect in 3s", err)
		} else {
			log.Printf("[mocknode] ws session closed, reconnect in 3s")
		}
		time.Sleep(3 * time.Second)
	}
}

func runWS(url, nodeID, key, reportFile string) error {
	ctx, cancel := contextWithSignal()
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close(websocket.StatusInternalError, "bye")

	reg, _ := json.Marshal(map[string]string{"nodeId": nodeID, "key": key})
	if err := send(conn, envelope{Type: "register", NodeID: nodeID, TS: time.Now().Unix(), Data: reg}); err != nil {
		return err
	}
	log.Printf("[mocknode] registering as %s -> %s", nodeID, url)

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
		}
	}
}

// runHTTPUpstream 模拟 HTTP 上游节点：/v1/* 原样回显路径与 query
func runHTTPUpstream(addr string) {
	mux := http.NewServeMux()
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

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestAgent(t *testing.T, status int, body string) (*agent, *[]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer nat_x" || r.URL.Path != "/api/agent/bootstrap" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	var calls []string
	a := &agent{base: srv.URL, token: "nat_x", nodeID: "d1-abc", service: "svc",
		envFile: filepath.Join(t.TempDir(), "node.env"), client: srv.Client(),
		systemctl: func(args ...string) error { calls = append(calls, strings.Join(args, " ")); return nil }}
	return a, &calls
}

func TestApprovedWritesEnvAndRestartsOnce(t *testing.T) {
	a, calls := newTestAgent(t, 200, `{"nodeId":"d1-abc","nodeKey":"nk_1","wsUrl":"wss://boce/ws"}`)
	if got, err := a.step(); err != nil || got != "restarted" {
		t.Fatalf("first step %q %v", got, err)
	}
	b, _ := os.ReadFile(a.envFile)
	if string(b) != "WS_URL=wss://boce/ws\nNODE_ID=d1-abc\nNODE_KEY=nk_1\n" {
		t.Fatalf("env %q", b)
	}
	if got, _ := a.step(); got != "unchanged" {
		t.Fatalf("second step %q", got)
	}
	if strings.Join(*calls, "|") != "enable svc|restart svc|start svc" {
		t.Fatalf("calls %v", *calls)
	}
}

func TestPendingKeepsStopped(t *testing.T) {
	a, calls := newTestAgent(t, 403, `{"error":{"code":"not_approved","message":"pending"}}`)
	if got, _ := a.step(); got != "pending" || len(*calls) != 0 {
		t.Fatalf("pending %q %v", got, *calls)
	}
}

func TestRevokedStopsService(t *testing.T) {
	a, calls := newTestAgent(t, 403, `{"error":{"code":"not_approved","message":"disabled"}}`)
	_ = os.WriteFile(a.envFile, []byte("NODE_KEY=old\n"), 0o600)
	if got, _ := a.step(); got != "stopped" {
		t.Fatalf("revoked %q", got)
	}
	if b, _ := os.ReadFile(a.envFile); len(b) != 0 {
		t.Fatalf("env not cleared: %q", b)
	}
	if strings.Join(*calls, "|") != "disable svc|stop svc" {
		t.Fatalf("calls %v", *calls)
	}
}

func TestServerErrorRetries(t *testing.T) {
	a, calls := newTestAgent(t, 502, `bad gateway`)
	if got, _ := a.step(); got != "retry" || len(*calls) != 0 {
		t.Fatalf("retry %q %v", got, *calls)
	}
}

func shaOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestAutoUpgradeNodeAndSelf(t *testing.T) {
	newNode, newAgent := []byte("node-v3"), []byte("agent-v2")
	nodeSHA, agentSHA := shaOf(newNode), shaOf(newAgent)
	corrupt := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agent/bootstrap":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["arch"] != "amd64" {
				t.Errorf("arch=%q", in["arch"])
			}
			fmt.Fprintf(w, `{"nodeId":"d1","nodeKey":"nk","wsUrl":"wss://b/ws","nodeSha256":%q,"agentSha256":%q}`, nodeSHA, agentSHA)
		case "/agent/download/lemonipw-linux-amd64":
			if corrupt {
				w.Write([]byte("tampered"))
				return
			}
			w.Write(newNode)
		case "/agent/download/boce-agent-linux-amd64":
			w.Write(newAgent)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	var calls []string
	exited := -1
	a := &agent{base: srv.URL, token: "nat_x", nodeID: "d1", service: "svc", envFile: filepath.Join(dir, "node.env"),
		arch: "amd64", agentPath: filepath.Join(dir, "boce-agent"), nodePath: filepath.Join(dir, "lemonipw"), client: srv.Client(),
		systemctl: func(args ...string) error { calls = append(calls, strings.Join(args, " ")); return nil },
		exit:      func(code int) { exited = code }}
	_ = os.WriteFile(a.nodePath, []byte("node-v2"), 0o755)
	_ = os.WriteFile(a.agentPath, []byte("agent-v1"), 0o755)

	// 校验不过：不替换节点程序，也不退出
	corrupt = true
	_ = os.WriteFile(a.envFile, []byte(renderEnv(&bootstrap{NodeID: "d1", NodeKey: "nk", WSURL: "wss://b/ws"})), 0o600)
	agentSHA = shaOf([]byte("agent-v1")) // 本轮 agent 视为最新
	if got, _ := a.step(); got != "unchanged" {
		t.Fatalf("corrupt step %q", got)
	}
	if b, _ := os.ReadFile(a.nodePath); string(b) != "node-v2" || exited != -1 {
		t.Fatalf("corrupt download replaced node: %q exit=%d", b, exited)
	}

	// 正常：节点程序换新并重启服务，随后 agent 自身换新并退出
	corrupt = false
	agentSHA = shaOf(newAgent)
	calls = nil
	if got, _ := a.step(); got != "restarted" {
		t.Fatalf("upgrade step %q", got)
	}
	if b, _ := os.ReadFile(a.nodePath); string(b) != "node-v3" {
		t.Fatalf("node not upgraded: %q", b)
	}
	if b, _ := os.ReadFile(a.agentPath); string(b) != "agent-v2" || exited != 0 {
		t.Fatalf("agent not upgraded: %q exit=%d", b, exited)
	}
	if strings.Join(calls, "|") != "enable svc|restart svc" {
		t.Fatalf("calls %v", calls)
	}
	// 已是最新：不再动
	exited, calls = -1, nil
	if got, _ := a.step(); got != "unchanged" || exited != -1 {
		t.Fatalf("idle step %q exit=%d", got, exited)
	}
}

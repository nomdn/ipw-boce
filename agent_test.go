package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentBootstrapOnlyAfterApproval(t *testing.T) {
	r := setupTest(t)
	registerAgentRoutes(r)
	PUBLIC_WS_URL = "wss://boce.test/ws"
	defer func() { PUBLIC_WS_URL = "" }()
	u := mkUser(t, "donor")
	tok := agentTokenPrefix + strings.Repeat("a", 32)
	db.Model(&User{}).Where("id = ?", u.ID).Update("agent_token", tok)
	d := NodeDonation{UserID: u.ID, Username: u.Username, NodeID: "d-agent", Label: "x", NetType: NetHome,
		Stack: stackDualStack, Channel: "ws", WSKey: "nk_agent", Status: DonPending}
	db.Create(&d)

	if code, _ := call(r, "POST", "/api/agent/bootstrap", "nat_bad", gin_H("nodeId", "d-agent")); code != 401 {
		t.Fatalf("bad token code=%d", code)
	}
	code, body := call(r, "POST", "/api/agent/bootstrap", tok, gin_H("nodeId", "d-agent"))
	if code != 403 || body["error"].(map[string]any)["message"] != "pending" {
		t.Fatalf("pending code=%d body=%v", code, body)
	}
	// 别人的节点：同 Token 拿不到
	other := mkUser(t, "other")
	db.Create(&NodeDonation{UserID: other.ID, NodeID: "d-other", Channel: "ws", WSKey: "k", Status: DonApproved})
	if code, _ := call(r, "POST", "/api/agent/bootstrap", tok, gin_H("nodeId", "d-other")); code != 404 {
		t.Fatalf("other's node code=%d", code)
	}

	if err := approveDonation(&d, "admin", "ok"); err != nil {
		t.Fatal(err)
	}
	code, body = call(r, "POST", "/api/agent/bootstrap", tok, gin_H("nodeId", "d-agent"))
	if code != 200 || body["nodeKey"] != "nk_agent" || body["wsUrl"] != "wss://boce.test/ws" {
		t.Fatalf("approved code=%d body=%v", code, body)
	}
	_ = setDonationOffPool(&d, DonDisabled, "admin", "x")
	code, body = call(r, "POST", "/api/agent/bootstrap", tok, gin_H("nodeId", "d-agent"))
	if code != 403 || body["error"].(map[string]any)["message"] != DonDisabled {
		t.Fatalf("disabled code=%d body=%v", code, body)
	}
}

func TestAgentInstallScriptAndDownloadGuard(t *testing.T) {
	r := setupTest(t)
	registerAgentRoutes(r)
	PUBLIC_BASE_URL = "https://boce.test"
	defer func() { PUBLIC_BASE_URL = "" }()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/agent/install.sh", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `BOCE_URL="https://boce.test"`) {
		t.Fatalf("script code=%d", w.Code)
	}
	for _, p := range []string{"/agent/download/..%2Fsetting.json", "/agent/download/boce-agent-linux-amd64"} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s code=%d want 404", p, w.Code)
		}
	}
	if got := agentInstallCmd("https://boce.test", "nat_1", "d1"); got != "curl -fsSL https://boce.test/agent/install.sh | sudo bash -s -- --token nat_1 --node d1" {
		t.Fatal(got)
	}
}

func TestSyncNodesRequiresToken(t *testing.T) {
	r := setupTest(t)
	registerSyncRoutes(r)
	if code, _ := call(r, "GET", "/api/sync/nodes", "x", nil); code != 404 {
		t.Fatalf("disabled code=%d", code)
	}
	SYNC_TOKEN = "sync-secret"
	defer func() { SYNC_TOKEN = "" }()
	db.Create(&NodeDef{NodeID: "h1", Label: "H1", URL: "https://h1.test", Pool: "api,location", Stack: stackIPv4, Enabled: true, APIKey: "k1"})
	db.Create(&NodeDef{NodeID: "w1", Label: "W1", WS: true, Pool: "api", Enabled: true, WSKey: "wk"})
	reloadNodePools()
	if code, _ := call(r, "GET", "/api/sync/nodes", "wrong", nil); code != 401 {
		t.Fatalf("wrong token code=%d", code)
	}
	code, body := call(r, "GET", "/api/sync/nodes", "sync-secret", nil)
	if code != 200 {
		t.Fatalf("code=%d", code)
	}
	nodes := body["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("nodes=%v", nodes)
	}
	for _, n := range nodes {
		m := n.(map[string]any)
		switch m["id"] {
		case "h1":
			if m["url"] != "https://h1.test" || m["apiKey"] != "k1" || len(m["pools"].([]any)) != 2 {
				t.Fatalf("h1 %v", m)
			}
		case "w1":
			if m["url"] != "{self}/v1/w1/{path}" || m["apiKey"] != nil || m["wsKey"] != nil {
				t.Fatalf("w1 %v", m)
			}
		}
	}
}

func gin_H(k, v string) map[string]string { return map[string]string{k: v} }

func TestBootstrapCarriesBinarySHA(t *testing.T) {
	r := setupTest(t)
	registerAgentRoutes(r)
	old := AGENT_DIR
	AGENT_DIR = t.TempDir()
	defer func() { AGENT_DIR = old }()
	_ = os.WriteFile(filepath.Join(AGENT_DIR, "lemonipw-linux-arm64"), []byte("node-bin"), 0o755)
	u := mkUser(t, "sha")
	tok := agentTokenPrefix + strings.Repeat("b", 32)
	db.Model(&User{}).Where("id = ?", u.ID).Update("agent_token", tok)
	db.Create(&NodeDonation{UserID: u.ID, NodeID: "d-sha", Channel: "ws", WSKey: "k", Status: DonApproved})

	sum := sha256.Sum256([]byte("node-bin"))
	code, body := call(r, "POST", "/api/agent/bootstrap", tok, map[string]string{"nodeId": "d-sha", "arch": "arm64"})
	if code != 200 || body["nodeSha256"] != hex.EncodeToString(sum[:]) || body["agentSha256"] != nil {
		t.Fatalf("sha: %d %v", code, body)
	}
	// 非法架构名不得拼出路径
	if _, body = call(r, "POST", "/api/agent/bootstrap", tok, map[string]string{"nodeId": "d-sha", "arch": "../../setting"}); body["nodeSha256"] != nil {
		t.Fatalf("arch traversal: %v", body)
	}
}

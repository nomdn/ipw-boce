// boce-agent：1IPW-BOCE 捐赠节点守护程序。
//
// 周期性用用户的节点 Token 向中心拉取接入配置（仅审核通过才下发），
// 写入节点程序的环境文件（WS_URL / NODE_ID / NODE_KEY），配置变化时重启节点服务；
// 捐赠被拒绝 / 停用 / 撤回时停掉节点服务。只用标准库，便于交叉编译到各架构。
//
// 环境变量：BOCE_URL、AGENT_TOKEN、NODE_ID、NODE_SERVICE（缺省 lemon-ipw-boce）、
// NODE_ENV_FILE（缺省 /etc/boce-agent/node.env）、AGENT_INTERVAL（秒，缺省 300）
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var version = "dev"

type bootstrap struct {
	NodeID   string `json:"nodeId"`
	NodeKey  string `json:"nodeKey"`
	WSURL    string `json:"wsUrl"`
	AgentSHA string `json:"agentSha256"`
	NodeSHA  string `json:"nodeSha256"`
}

type agent struct {
	base, token, nodeID, service, envFile string
	arch                                  string // amd64 / arm64 / armv7 / 386，下载文件名用
	agentPath, nodePath                   string // 自身与节点程序路径（自动升级时替换）
	client                                *http.Client
	systemctl                             func(args ...string) error
	exit                                  func(code int)
}

// result 一次拉取的结论
type result int

const (
	resApproved result = iota // 拿到配置
	resPending                // 还在审核：保持现状
	resRevoked                // 被拒 / 停用 / 撤回 / Token 失效：停服务
	resRetry                  // 网络等临时错误：保持现状
)

func (a *agent) fetch() (result, *bootstrap, string) {
	body, _ := json.Marshal(map[string]string{"nodeId": a.nodeID, "arch": a.arch})
	req, _ := http.NewRequest(http.MethodPost, a.base+"/api/agent/bootstrap", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "boce-agent/"+version)
	resp, err := a.client.Do(req)
	if err != nil {
		return resRetry, nil, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	switch resp.StatusCode {
	case http.StatusOK:
		var b bootstrap
		if json.Unmarshal(raw, &b) != nil || b.NodeKey == "" || b.WSURL == "" {
			return resRetry, nil, "invalid bootstrap body"
		}
		return resApproved, &b, ""
	case http.StatusForbidden:
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error.Message == "pending" {
			return resPending, nil, "pending"
		}
		return resRevoked, nil, e.Error.Message
	case http.StatusUnauthorized, http.StatusNotFound:
		return resRevoked, nil, fmt.Sprintf("http %d", resp.StatusCode)
	}
	return resRetry, nil, fmt.Sprintf("http %d", resp.StatusCode)
}

func renderEnv(b *bootstrap) string {
	return fmt.Sprintf("WS_URL=%s\nNODE_ID=%s\nNODE_KEY=%s\n", b.WSURL, b.NodeID, b.NodeKey)
}

// step 执行一轮；返回是否改动了节点服务（测试用）
func (a *agent) step() (string, error) {
	res, b, msg := a.fetch()
	switch res {
	case resApproved:
		upgraded, err := a.upgradeNode(b)
		if err != nil {
			log.Printf("节点程序升级失败：%v（继续用旧版本）", err)
		}
		want := renderEnv(b)
		old, _ := os.ReadFile(a.envFile)
		if string(old) == want && !upgraded {
			// 配置没变，确保服务在跑（被手动停掉或崩溃后由 systemd 之外兜底）
			_ = a.systemctl("start", a.service)
			a.upgradeSelf(b)
			return "unchanged", nil
		}
		if err := os.WriteFile(a.envFile, []byte(want), 0o600); err != nil {
			return "", err
		}
		log.Printf("接入配置或节点程序已更新（node=%s），重启 %s", b.NodeID, a.service)
		_ = a.systemctl("enable", a.service)
		err = a.systemctl("restart", a.service)
		a.upgradeSelf(b)
		return "restarted", err
	case resRevoked:
		log.Printf("捐赠不再有效（%s），停止 %s", msg, a.service)
		_ = os.WriteFile(a.envFile, nil, 0o600)
		_ = a.systemctl("disable", a.service)
		return "stopped", a.systemctl("stop", a.service)
	case resPending:
		return "pending", nil
	}
	log.Printf("拉取配置失败：%s（稍后重试）", msg)
	return "retry", nil
}

func main() {
	for _, v := range os.Args[1:] {
		if v == "-v" || v == "--version" || v == "version" {
			fmt.Println("boce-agent " + version)
			return
		}
	}
	self, _ := os.Executable()
	a := &agent{
		base:      strings.TrimSuffix(os.Getenv("BOCE_URL"), "/"),
		token:     os.Getenv("AGENT_TOKEN"),
		nodeID:    os.Getenv("NODE_ID"),
		service:   envOr("NODE_SERVICE", "lemon-ipw-boce"),
		envFile:   envOr("NODE_ENV_FILE", "/etc/boce-agent/node.env"),
		arch:      archName(),
		agentPath: self,
		nodePath:  envOr("NODE_BIN", filepath.Join(filepath.Dir(self), "lemonipw")),
		client:    &http.Client{Timeout: 5 * time.Minute},
		exit:      os.Exit,
		systemctl: func(args ...string) error {
			out, err := exec.Command("systemctl", args...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("systemctl %s: %v %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
			return nil
		},
	}
	if a.base == "" || a.token == "" || a.nodeID == "" {
		log.Fatal("BOCE_URL / AGENT_TOKEN / NODE_ID 未配置")
	}
	interval := 300
	if n, err := strconv.Atoi(os.Getenv("AGENT_INTERVAL")); err == nil && n >= 30 {
		interval = n
	}
	log.Printf("boce-agent %s 启动：center=%s node=%s 间隔 %ds", version, a.base, a.nodeID, interval)
	for {
		if _, err := a.step(); err != nil {
			log.Printf("ERROR %v", err)
		}
		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// ==================== 自动升级 ====================
//
// 中心在 bootstrap 里给出本架构 agent / 节点程序的 SHA256；本地文件不一致就下载、校验、原子替换。
// 节点程序换完由调用方重启服务；agent 自身换完直接退出，由 systemd（Restart=always）拉起新版本。

func fileSHA(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// fetchVerified 下载到 dst.new 并校验 SHA256，通过后改名覆盖 dst
func (a *agent) fetchVerified(name, dst, want string) error {
	resp, err := a.client.Get(a.base + "/agent/download/" + name)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s：http %d", name, resp.StatusCode)
	}
	tmp := dst + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 256<<20))
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(tmp)
		return fmt.Errorf("%s 校验失败", name)
	}
	return os.Rename(tmp, dst)
}

func (a *agent) upgradeNode(b *bootstrap) (bool, error) {
	if b.NodeSHA == "" || a.arch == "" || a.nodePath == "" || fileSHA(a.nodePath) == b.NodeSHA {
		return false, nil
	}
	if err := a.fetchVerified("lemonipw-linux-"+a.arch, a.nodePath, b.NodeSHA); err != nil {
		return false, err
	}
	log.Printf("节点程序已升级到 %s", b.NodeSHA[:12])
	return true, nil
}

func (a *agent) upgradeSelf(b *bootstrap) {
	if b.AgentSHA == "" || a.arch == "" || a.agentPath == "" || fileSHA(a.agentPath) == b.AgentSHA {
		return
	}
	if err := a.fetchVerified("boce-agent-linux-"+a.arch, a.agentPath, b.AgentSHA); err != nil {
		log.Printf("agent 升级失败：%v", err)
		return
	}
	log.Printf("agent 已升级到 %s，退出由 systemd 重启", b.AgentSHA[:12])
	a.exit(0)
}

// archName 与中心 /agent/download 的文件名后缀一致
func archName() string {
	if runtime.GOARCH == "arm" {
		return "armv7"
	}
	return runtime.GOARCH
}

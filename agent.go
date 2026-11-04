package main

// ==================== 捐赠节点 Agent（一键安装 + 用户节点 Token） ====================
//
// 每个用户有一个「节点 Token」（nat_ + 32 hex），只能用来给自己【已通过审核】的 WS 捐赠节点
// 拉取接入配置，权限比账号小得多，所以明文存库、可随时重置，方便直接写进安装命令。
//
//   GET  /admin/agent/token              我的节点 Token（没有则为空）
//   POST /admin/agent/token              生成 / 重置（旧 Token 立即失效）
//   POST /api/agent/bootstrap            Bearer <节点 Token>，body {"nodeId"} → 接入配置（仅已通过审核）
//   GET  /agent/install.sh               一键安装脚本（按 public-base-url 渲染）
//   GET  /agent/download/:file           agent 与节点程序二进制（agent-dir 目录）
//
// 安装命令只在捐赠通过审核后才返回给用户（donationView.installCmd）。

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const agentTokenPrefix = "nat_"

// agentLimiter bootstrap 单 IP 限流（agent 5 分钟轮询一次，30 次/分足够）
var agentLimiter = newRateLimiter(30)

var agentFileRe = regexp.MustCompile(`^(boce-agent|lemonipw)-linux-(amd64|arm64|armv7|386)$`)

func publicBaseURL(c *gin.Context) string {
	if PUBLIC_BASE_URL != "" {
		return PUBLIC_BASE_URL
	}
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

// agentInstallCmd 一键安装命令（含用户节点 Token 与节点 ID）
func agentInstallCmd(base, token, nodeID string) string {
	if token == "" {
		return ""
	}
	return "curl -fsSL " + base + "/agent/install.sh | sudo bash -s -- --token " + token + " --node " + nodeID
}

func userAgentToken(uid uint) string {
	ctx, cancel := dbCtx()
	defer cancel()
	var u User
	db.WithContext(ctx).Select("id", "agent_token").First(&u, uid)
	return u.AgentToken
}

func registerAgentRoutes(r *gin.Engine) {
	r.POST("/api/agent/bootstrap", agentBootstrap)
	r.GET("/agent/install.sh", func(c *gin.Context) {
		script := strings.ReplaceAll(agentInstallScript, "__BOCE_URL__", publicBaseURL(c))
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "text/x-shellscript; charset=utf-8", []byte(script))
	})
	r.GET("/agent/download/:file", func(c *gin.Context) {
		name := c.Param("file")
		if !agentFileRe.MatchString(name) {
			apiError(c, http.StatusNotFound, "Not Found")
			return
		}
		p := filepath.Join(AGENT_DIR, name)
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			apiError(c, http.StatusNotFound, "Not Found")
			return
		}
		c.Header("Content-Type", "application/octet-stream")
		c.File(p)
	})
}

// registerAgentTokenRoutes 登录用户管理自己的节点 Token（挂在 /admin 组下）
func registerAgentTokenRoutes(admin *gin.RouterGroup) {
	admin.GET("/agent/token", func(c *gin.Context) {
		uid, _, _ := currentUserFromCtx(c)
		c.JSON(http.StatusOK, gin.H{"token": userAgentToken(uid), "baseUrl": publicBaseURL(c)})
	})
	admin.POST("/agent/token", func(c *gin.Context) {
		u := meUser(c)
		if u == nil {
			return
		}
		tok := agentTokenPrefix + randHex(16)
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Model(&User{}).Where("id = ?", u.ID).Update("agent_token", tok).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"token": tok, "baseUrl": publicBaseURL(c)})
	})
}

func agentBootstrap(c *gin.Context) {
	if !agentLimiter.allow(c.ClientIP()) {
		restError(c, http.StatusTooManyRequests, "rate_limited", "too many requests")
		return
	}
	tok := bearerToken(c)
	if !strings.HasPrefix(tok, agentTokenPrefix) || len(tok) != len(agentTokenPrefix)+32 {
		restError(c, http.StatusUnauthorized, "unauthorized", "invalid node token")
		return
	}
	var b struct {
		NodeID string `json:"nodeId"`
		Arch   string `json:"arch"`
	}
	_ = c.ShouldBindJSON(&b)
	ctx, cancel := dbCtx()
	defer cancel()
	var u User
	if err := db.WithContext(ctx).Where("agent_token = ?", tok).Limit(1).Find(&u).Error; err != nil || u.ID == 0 || !u.Enabled {
		restError(c, http.StatusUnauthorized, "unauthorized", "invalid node token")
		return
	}
	var d NodeDonation
	db.WithContext(ctx).Where("user_id = ? AND node_id = ?", u.ID, strings.TrimSpace(b.NodeID)).Limit(1).Find(&d)
	if d.ID == 0 {
		restError(c, http.StatusNotFound, "not_found", "donation not found for this token")
		return
	}
	if d.Status != DonApproved {
		// pending：agent 继续等待；其余状态：agent 停掉节点程序
		restError(c, http.StatusForbidden, "not_approved", d.Status)
		return
	}
	if d.Channel != "ws" {
		restError(c, http.StatusBadRequest, "unsupported", "only ws donations can use the agent")
		return
	}
	out := gin.H{"status": d.Status, "nodeId": d.NodeID, "nodeKey": d.WSKey,
		"wsUrl": publicWSURL(c), "label": d.Label, "stack": d.Stack}
	// 自动升级：给出本架构 agent / 节点程序的 SHA256，agent 发现不一致就下载替换
	if arch := strings.TrimSpace(b.Arch); arch != "" {
		if h := agentFileSHA("boce-agent-linux-" + arch); h != "" {
			out["agentSha256"] = h
		}
		if h := agentFileSHA("lemonipw-linux-" + arch); h != "" {
			out["nodeSha256"] = h
		}
	}
	c.JSON(http.StatusOK, out)
}

type shaEntry struct {
	mod  time.Time
	size int64
	sum  string
}

var (
	shaMu    sync.Mutex
	shaCache = map[string]shaEntry{}
)

// agentFileSHA agent-dir 下文件的 SHA256（按 mtime+size 缓存）；文件不存在或名字不合法返回空
func agentFileSHA(name string) string {
	if !agentFileRe.MatchString(name) {
		return ""
	}
	p := filepath.Join(AGENT_DIR, name)
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return ""
	}
	shaMu.Lock()
	defer shaMu.Unlock()
	if e, ok := shaCache[p]; ok && e.mod.Equal(st.ModTime()) && e.size == st.Size() {
		return e.sum
	}
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	sum := hex.EncodeToString(h.Sum(nil))
	shaCache[p] = shaEntry{mod: st.ModTime(), size: st.Size(), sum: sum}
	return sum
}

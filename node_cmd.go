package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 节点管理指令：能力门 + 运行时配置（与原版协议一致） ====================
//
// 通道：WS 优先（节点在线即走，无需暴露端口）；WS 不可用或失败时回退 HTTP（需 api-keys[nodeId]，
// 即节点的 access-token）。节点在 register / GET /info 里上报 capabilities：
//   清单明确不含该能力 → 立即拒绝；清单为空（老版本）→ 视为未知，仍尝试，超时后提示"程序版本过旧"。

const (
	capabilityConfig  = "config"
	capabilityOTA     = "ota"
	nodeConfigTimeout = 15 * time.Second
)

// 凭据类键：节点 GET 快照里是 ***，下发时必须剔除，否则会写回三个星号
var nodeConfigSecretKeys = map[string]bool{"access-token": true, "node-key": true, "report-token": true}

func nodeReportedInfo(nodeID string) (version string, caps []string, known bool) {
	n, ok := nodeSnapshot(nodeID)
	if !ok {
		return "", nil, false
	}
	caps = splitAndTrim(n.Capabilities, ",")
	return strings.TrimSpace(n.Version), caps, len(caps) > 0
}

func nodeCapabilityGate(nodeID, capability, action string) error {
	_, caps, known := nodeReportedInfo(nodeID)
	if !known {
		return nil
	}
	for _, c := range caps {
		if strings.EqualFold(c, capability) {
			return nil
		}
	}
	return fmt.Errorf("节点 %s 不支持 %s：其上报的能力清单 [%s] 不含 %s，请把该节点升级到包含此能力的版本",
		nodeID, action, strings.Join(caps, ","), capability)
}

// wsTimeoutDiagnosis 把"WS 指令超时"细化：连接活着 = 节点不认指令（版本过旧）；不活 = 连接僵死
func wsTimeoutDiagnosis(nodeID string, err error) error {
	if wsSrv != nil && wsSrv.ProbeAlive(nodeID, 3*time.Second) {
		hint := ""
		if _, _, known := nodeReportedInfo(nodeID); !known {
			hint = "；该节点未上报能力清单，基本可判定其程序版本过旧（收到指令后静默忽略）"
		}
		return fmt.Errorf("WS 指令无应答（%v）%s", err, hint)
	}
	return fmt.Errorf("WS 指令无应答（%v），且该连接已失活，指令未送达，稍后会被自动剔除并重连", err)
}

func nodeHTTPBase(nodeID string) string {
	if n := findNode(allPoolNodes(), nodeID); n != nil {
		return strings.TrimSpace(n.URL)
	}
	return ""
}

// sendNodeCommand WS 优先，失败回退 HTTP。httpFn 为 HTTP 回退实现（token、base 已校验）
func sendNodeCommand(nodeID, msgType string, payload map[string]any, timeout time.Duration,
	httpFn func(base, token string) (json.RawMessage, error)) (json.RawMessage, string, error) {
	var wsErr error
	switch {
	case wsSrv == nil:
		wsErr = errors.New("WS 服务未启用")
	case !wsSrv.NodeConnected(nodeID):
		wsErr = errors.New("节点 WS 离线")
	default:
		raw, err := wsSrv.RequestCommand(nodeID, msgType, payload, timeout)
		if err == nil {
			return raw, "ws", nil
		}
		var te *wsTimeoutError
		if !errors.As(err, &te) && raw != nil {
			return raw, "ws", err // 节点明确拒绝：不回退，直接返回原因
		}
		if errors.As(err, &te) {
			wsErr = wsTimeoutDiagnosis(nodeID, err)
		} else {
			wsErr = fmt.Errorf("WS 指令失败（%w）", err)
		}
	}
	token := lookupAPIKey(nodeID)
	if token == "" {
		return nil, "", fmt.Errorf("节点 %s 无法管理：%v；且未配置 access-token（api-keys），节点 HTTP 管理接口已关闭，无可回退通道", nodeID, wsErr)
	}
	base := nodeHTTPBase(nodeID)
	if base == "" {
		return nil, "", fmt.Errorf("节点 %s 无法管理：%v；且节点池里没有该节点的 HTTP 地址", nodeID, wsErr)
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	raw, err := httpFn(base, token)
	if err != nil {
		return nil, "", fmt.Errorf("节点 %s 管理失败：%v；HTTP 回退也失败：%v", nodeID, wsErr, err)
	}
	return raw, "http", nil
}

func httpJSON(method, url, token string, body any, timeout time.Duration) (json.RawMessage, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(mustRaw(body))
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("节点返回 %d：%s", resp.StatusCode, truncateStr(strings.TrimSpace(string(b)), 200))
	}
	return b, nil
}

// ==================== 运行时配置 GET/POST /admin/nodes/:nodeId/config ====================

type nodeConfigResult struct {
	NodeID              string         `json:"nodeId"`
	Channel             string         `json:"channel"`
	Action              string         `json:"action"`
	Config              map[string]any `json:"config"`
	SecretKeys          []string       `json:"secretKeys"`
	RestartKeys         []string       `json:"restartKeys"`
	Applied             []string       `json:"applied"`
	Unknown             []string       `json:"unknown"`
	RestartRequired     []string       `json:"restartRequired"`
	IgnoredKeys         []string       `json:"ignoredKeys"`
	RemoteProtectedKeys []string       `json:"remoteProtectedKeys"`
	Persisted           bool           `json:"persisted"`
	UnpersistedKeys     []string       `json:"unpersistedKeys,omitempty"`
	PersistHint         string         `json:"persistHint,omitempty"`
}

func requestNodeConfig(nodeID, action string, cfg map[string]any, persist bool) (*nodeConfigResult, error) {
	if err := nodeCapabilityGate(nodeID, capabilityConfig, "运行时配置管理"); err != nil {
		return nil, err
	}
	payload := map[string]any{"action": action}
	if len(cfg) > 0 {
		payload["config"] = cfg
	}
	if persist {
		payload["persist"] = true
	}
	raw, ch, err := sendNodeCommand(nodeID, "config", payload, nodeConfigTimeout, func(base, token string) (json.RawMessage, error) {
		switch action {
		case "get":
			return httpJSON(http.MethodGet, base+"v1/config", token, nil, nodeConfigTimeout)
		case "patch":
			u := base + "v1/config"
			if persist {
				u += "?persist=true"
			}
			return httpJSON(http.MethodPatch, u, token, cfg, nodeConfigTimeout)
		default:
			return httpJSON(http.MethodPost, base+"v1/config/refresh", token, nil, nodeConfigTimeout)
		}
	})
	if err != nil {
		return nil, err
	}
	var in struct {
		Config          map[string]any `json:"config"`
		SecretKeys      []string       `json:"secretKeys"`
		RestartKeys     []string       `json:"restartRequiredKeys"`
		Applied         []string       `json:"applied"`
		Unknown         []string       `json:"unknown"`
		RestartRequired []string       `json:"restartRequired"`
		Persisted       bool           `json:"persisted"`
		ProtectedKeys   []string       `json:"remoteProtectedKeys"`
	}
	_ = json.Unmarshal(raw, &in)
	res := &nodeConfigResult{NodeID: nodeID, Channel: ch, Action: action, Config: in.Config, SecretKeys: in.SecretKeys,
		RestartKeys: in.RestartKeys, Applied: in.Applied, Unknown: in.Unknown, RestartRequired: in.RestartRequired,
		Persisted: in.Persisted, RemoteProtectedKeys: in.ProtectedKeys}
	if res.SecretKeys == nil {
		for k := range nodeConfigSecretKeys {
			res.SecretKeys = append(res.SecretKeys, k)
		}
		sort.Strings(res.SecretKeys)
	}
	if res.RemoteProtectedKeys == nil {
		res.RemoteProtectedKeys = []string{"access-token", "report-token"}
	}
	return res, nil
}

// keysNotInHostedConfig 本次改动里没有以同值进入托管配置（global + 节点合并）的键
func keysNotInHostedConfig(nodeID string, cfg map[string]any) []string {
	ctx, cancel := dbCtx()
	defer cancel()
	var rows []NodeConfig
	db.WithContext(ctx).Where("node_id IN ?", []string{"global", nodeID}).Find(&rows)
	hosted := map[string]any{}
	for _, id := range []string{"global", nodeID} {
		for _, r := range rows {
			if r.NodeID != id {
				continue
			}
			obj := map[string]any{}
			if json.Unmarshal([]byte(r.Config), &obj) == nil {
				for k, v := range obj {
					hosted[k] = v
				}
			}
		}
	}
	var out []string
	for k, v := range cfg {
		if hv, ok := hosted[k]; !ok || fmt.Sprint(hv) != fmt.Sprint(v) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func registerNodeRuntimeConfigRoutes(g *gin.RouterGroup) {
	g.GET("/nodes/:nodeId/config", func(c *gin.Context) {
		res, err := requestNodeConfig(c.Param("nodeId"), "get", nil, false)
		if err != nil {
			apiError(c, http.StatusBadGateway, err.Error())
			return
		}
		c.JSON(http.StatusOK, res)
	})
	g.POST("/nodes/:nodeId/config", func(c *gin.Context) {
		var in struct {
			Action  string         `json:"action"`
			Config  map[string]any `json:"config"`
			Persist bool           `json:"persist"`
		}
		if err := c.ShouldBindJSON(&in); err != nil && err != io.EOF {
			apiError(c, http.StatusBadRequest, "请求体不是合法 JSON："+err.Error())
			return
		}
		action := strings.ToLower(strings.TrimSpace(in.Action))
		if action == "" {
			action = "patch"
		}
		if action != "patch" && action != "refresh" {
			apiError(c, http.StatusBadRequest, "action 只能是 patch 或 refresh")
			return
		}
		var ignored []string
		if action == "patch" {
			kept := map[string]any{}
			for k, v := range in.Config {
				if nodeConfigSecretKeys[k] {
					ignored = append(ignored, k)
					continue
				}
				kept[k] = v
			}
			if len(kept) == 0 {
				apiError(c, http.StatusBadRequest, "没有可下发的配置（凭据类字段不可下发）")
				return
			}
			in.Config = kept
		}
		res, err := requestNodeConfig(c.Param("nodeId"), action, in.Config, in.Persist)
		if err != nil {
			apiError(c, http.StatusBadGateway, err.Error())
			return
		}
		res.IgnoredKeys = ignored
		if action == "patch" {
			if res.UnpersistedKeys = keysNotInHostedConfig(c.Param("nodeId"), in.Config); len(res.UnpersistedKeys) > 0 {
				res.PersistHint = "以上改动未持久化：节点重启后 ENV 与本地 setting.json 会覆盖内存改动。建议合并进托管配置（优先级最高，重启后自动生效）。"
			}
		}
		c.JSON(http.StatusOK, res)
	})
	// 运行时改动合并进节点托管配置（不允许 global：单节点改动不应扩散）
	g.POST("/node-configs/:nodeId/merge", func(c *gin.Context) {
		nodeID := c.Param("nodeId")
		if nodeID == "" || nodeID == "global" {
			apiError(c, http.StatusBadRequest, "global 不支持合并：这里持久化的是单节点覆盖值")
			return
		}
		var body struct {
			Config map[string]any `json:"config"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || len(body.Config) == 0 {
			apiError(c, http.StatusBadRequest, "config 不能为空")
			return
		}
		merged := map[string]any{}
		ctx, cancel := dbCtx()
		var row NodeConfig
		if db.WithContext(ctx).Where("node_id = ?", nodeID).Limit(1).Find(&row); row.ID != 0 {
			_ = json.Unmarshal([]byte(row.Config), &merged)
		}
		cancel()
		for k, v := range body.Config {
			merged[k] = v
		}
		norm := mustRaw(merged)
		if err := saveNodeConfig(nodeID, norm); err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"nodeId": nodeID, "merged": len(body.Config), "sizeBytes": len(norm)})
	})
}

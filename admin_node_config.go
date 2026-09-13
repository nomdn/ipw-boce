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

// ==================== 节点运行时配置管理（控制台 → 节点） ====================
//
// 控制台读写的是**节点进程当前生效的配置**（ipw-cn 的 /v1/config），与「托管配置」
// （本库 node_configs 表、供节点 remote-config-url 拉取）是两回事：
//   - 托管配置：存本库数据库，节点启动时拉的远端配置
//   - 运行时配置：节点进程内存里的实际值，改完立即生效（部分键需重启）
//
// 通道优先级：WS 优先（节点在线即走 WS，无需暴露端口、无需 access-token）；
// WS 离线时回退 HTTP —— 但 HTTP 需要凭据，且节点未配置 access-token 时其 HTTP 管理面
// 是关闭的，此时只能提示改用 WS。

// nodeConfigSecretKeys 凭据类键：节点 GET 快照里这些值是 ***，
// 提交时必须剔除，否则会把 "***" 写回节点，等于把 access-token 改成三个星号。
var nodeConfigSecretKeys = map[string]bool{
	"access-token": true,
	"node-key":     true,
	"report-token": true,
}

// nodeConfigRemoteProtectedKeys 节点侧「远端下发永不覆盖」的凭据键。
// 权威名单在节点（ipw-cn config_api.go 的 configRemoteProtectedKeys，节点自己执行拦截）；
// 这里只是节点未回传 remoteProtectedKeys 时的兜底展示用，**不参与任何拦截逻辑**。
var nodeConfigRemoteProtectedKeys = []string{"access-token", "report-token"}

// nodeConfigTimeout 单次配置指令超时（refresh 需要节点回源拉远端配置，比拨测宽一些）
const nodeConfigTimeout = 15 * time.Second

// secretKeyList 凭据类键的有序列表（节点未回传 secretKeys 时的兜底）
func secretKeyList() []string {
	out := make([]string, 0, len(nodeConfigSecretKeys))
	for k := range nodeConfigSecretKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// nodeAdminToken 中间件调用节点 HTTP 管理接口的凭据。
// 只认 api-keys[nodeId]（其值就是节点的 access-token），**不做 node-key 回退**：
// 节点未配置 access-token 时会主动关闭 HTTP 管理面，中间件侧回退没有意义，
// 那时只能走 WS（WS 由节点外连、注册需 ws-keys 校验，鉴权在中间件侧完成）。
func nodeAdminToken(nodeID string) string {
	return lookupAPIKey(nodeID)
}

// nodeHTTPBase 取节点的 HTTP 上游地址（配置管理不分池，两个池都找）
func nodeHTTPBase(nodeID string) string {
	for _, n := range apiPoolSnapshot() {
		if n.ID == nodeID {
			return strings.TrimSpace(n.URL)
		}
	}
	for _, n := range locationPoolSnapshot() {
		if n.ID == nodeID {
			return strings.TrimSpace(n.URL)
		}
	}
	return ""
}

// nodeConfigResult 规整后的统一响应（WS 与 HTTP 两种通道的返回字段不完全一致）
type nodeConfigResult struct {
	NodeID          string         `json:"nodeId"`
	Channel         string         `json:"channel"`         // ws | http
	Action          string         `json:"action"`          // get | patch | refresh
	Config          map[string]any `json:"config"`          // 节点当前生效配置（凭据键为 ***）
	SecretKeys      []string       `json:"secretKeys"`      // 凭据类键（不可下发）
	RestartKeys     []string       `json:"restartKeys"`     // 该节点改动后需重启才生效的键
	Applied         []string       `json:"applied"`         // 本次实际应用的键
	Unknown         []string       `json:"unknown"`         // 节点不认识的键
	RestartRequired []string       `json:"restartRequired"` // 本次改动中需重启才生效的键
	IgnoredKeys     []string       `json:"ignoredKeys"`     // 本中间件剔除的键（凭据类）
	// RemoteProtectedKeys 节点侧「远端下发永不覆盖」的凭据键（access-token / report-token）：
	// 托管配置里写了也不会生效，控制台据此在编辑器与配置行上提示。
	RemoteProtectedKeys []string `json:"remoteProtectedKeys"`
	// ProtectedIgnored 本次 refresh 中因凭据保护被跳过的键（节点如实回报）
	ProtectedIgnored []string `json:"protectedIgnored,omitempty"`
	Persisted       bool           `json:"persisted"`       // 是否写回节点 setting.json
	// UnpersistedKeys 本次改动中未进托管远端配置的键：节点重启后 ENV 与本地 setting.json
	// 会顶掉这些内存改动（persist 写盘也顶不过 ENV），建议合并进托管配置持久化（见 merge 接口）
	UnpersistedKeys []string `json:"unpersistedKeys,omitempty"`
	// PersistHint 人类可读的持久化提醒（UnpersistedKeys 非空时给出）
	PersistHint string `json:"persistHint,omitempty"`
}

// registerNodeRuntimeConfigRoutes 注册节点运行时配置路由（admin only）
func registerNodeRuntimeConfigRoutes(g *gin.RouterGroup) {
	g.GET("/nodes/:nodeId/config", nodeRuntimeConfigGet)
	g.POST("/nodes/:nodeId/config", nodeRuntimeConfigApply)
}

// nodeRuntimeConfigGet 拉取节点当前生效配置
func nodeRuntimeConfigGet(c *gin.Context) {
	nodeID := strings.TrimSpace(c.Param("nodeId"))
	if nodeID == "" {
		apiError(c, http.StatusBadRequest, "nodeId 不能为空")
		return
	}
	res, err := requestNodeConfig(nodeID, "get", nil, false)
	if err != nil {
		apiError(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// nodeRuntimeConfigApply 下发配置变更 / 触发远端配置刷新
func nodeRuntimeConfigApply(c *gin.Context) {
	nodeID := strings.TrimSpace(c.Param("nodeId"))
	if nodeID == "" {
		apiError(c, http.StatusBadRequest, "nodeId 不能为空")
		return
	}
	var in struct {
		Action  string         `json:"action"` // patch | refresh，缺省 patch
		Config  map[string]any `json:"config"`
		Persist bool           `json:"persist"`
	}
	// 空 body（refresh 场景）不应报解析错误
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
		// 剔除凭据类字段：节点回显是 ***，原样提交会覆盖成真值 "***"
		kept := make(map[string]any, len(in.Config))
		for k, v := range in.Config {
			if nodeConfigSecretKeys[k] {
				ignored = append(ignored, k)
				continue
			}
			kept[k] = v
		}
		in.Config = kept
		if len(in.Config) == 0 {
			apiError(c, http.StatusBadRequest, "没有可下发的配置（凭据类字段不可下发）")
			return
		}
	}

	res, err := requestNodeConfig(nodeID, action, in.Config, in.Persist)
	if err != nil {
		apiError(c, http.StatusBadGateway, err.Error())
		return
	}
	res.IgnoredKeys = ignored
	if action == "patch" && len(in.Config) > 0 {
		// 持久化检查：本次改动里哪些键没有（以同值）写进托管远端配置。
		// 节点侧优先级 远端 > ENV > setting.json：patch 只改内存，persist 也只写本地文件；
		// 重启后 ENV/本地文件会顶掉这些改动，只有托管配置能在重启后继续生效。
		res.UnpersistedKeys = keysNotInHostedConfig(nodeID, in.Config)
		if len(res.UnpersistedKeys) > 0 {
			res.PersistHint = "以上改动未持久化：节点重启后 ENV 与本地 setting.json 会覆盖内存改动" +
				"（勾选\"写回 setting.json\"也优先级低于 ENV）。建议同步到托管配置" +
				"（remote-config-url 拉取，优先级最高，重启后自动生效）。"
		}
	}
	c.JSON(http.StatusOK, res)
}

// keysNotInHostedConfig 找出 cfg 中未进托管远端配置（该节点 + global 合并视图）的键。
// 值比对做了宽松处理（fmt.Sprint 归一）：数字与字符串形态（8080 vs "8080"）视为相同，减少误报。
func keysNotInHostedConfig(nodeID string, cfg map[string]any) []string {
	if db == nil {
		return nil
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var rows []NodeConfig
	if err := db.WithContext(ctx).Where("node_id IN ?", []string{"global", nodeID}).Find(&rows).Error; err != nil {
		return nil // 查询失败时不打扰流程（宁可不提醒，也不错杀）
	}
	hosted := map[string]any{}
	for _, r := range rows {
		obj := map[string]any{}
		if json.Unmarshal([]byte(r.Config), &obj) != nil {
			continue
		}
		for k, v := range obj {
			hosted[k] = v // 节点级覆盖 global
		}
	}
	var out []string
	for k, v := range cfg {
		hv, ok := hosted[k]
		if !ok || fmt.Sprint(hv) != fmt.Sprint(v) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// requestNodeConfig 向节点下发配置指令，WS 优先、HTTP 回退，返回规整结果
func requestNodeConfig(nodeID, action string, cfg map[string]any, persist bool) (*nodeConfigResult, error) {
	raw, channel, err := dispatchNodeConfig(nodeID, action, cfg, persist)
	if err != nil {
		return nil, err
	}
	return normalizeNodeConfigResult(nodeID, channel, action, raw), nil
}

// dispatchNodeConfig 选通道并发送；返回节点应答原文与最终使用的通道。
// 通道优先级：WS 在线 → 走 WS；WS 不可用 → 回退 HTTP（需节点配了 access-token）。
func dispatchNodeConfig(nodeID, action string, cfg map[string]any, persist bool) (json.RawMessage, string, error) {
	// 前置门：能力清单明确不含 config 的节点直接拒绝（老节点不上报清单 → 不拦截）
	if deny, _ := nodeCapabilityGate(nodeID, capabilityConfig, "运行时配置管理"); deny != nil {
		return nil, "", deny
	}
	// 注意：这里**不**对"未上报能力清单"的老节点缩短超时。
	// config 的 refresh 需要节点回源拉取远端配置，耗时取决于节点到远端配置源的网络，
	// 缩短超时会误杀慢网络下的正常指令。

	// wsErr 记录 WS 侧为何没走通（离线 / 未启用 / 指令失败），仅用于拼装最终错误信息
	var wsErr error
	switch {
	case wsSrv == nil:
		wsErr = fmt.Errorf("WS 服务未启用")
	case !wsSrv.NodeConnected(nodeID):
		wsErr = fmt.Errorf("节点 WS 离线")
	default:
		raw, err := wsSrv.RequestConfig(nodeID, action, cfg, persist, nodeConfigTimeout)
		if err == nil {
			return raw, "ws", nil
		}
		// WS 已连上但指令失败，仍尝试 HTTP 回退：patch/refresh 均幂等，重复应用无副作用。
		// 超时则先探活分型，把"节点静默忽略"与"连接僵死"区分开
		var te *wsTimeoutError
		if errors.As(err, &te) {
			wsErr = wsTimeoutDiagnosis(nodeID, err)
		} else {
			wsErr = fmt.Errorf("WS 指令失败（%w）", err)
		}
	}

	token := nodeAdminToken(nodeID)
	if token == "" {
		return nil, "", fmt.Errorf("节点 %s 无法管理：%v；且未配置 access-token（api-keys），"+
			"节点侧 HTTP 管理接口已关闭，无可回退通道", nodeID, wsErr)
	}
	raw, err := nodeConfigViaHTTP(nodeID, action, cfg, persist, token)
	if err != nil {
		return nil, "", fmt.Errorf("节点 %s 管理失败：%v；HTTP 回退也失败：%v", nodeID, wsErr, err)
	}
	return raw, "http", nil
}

// nodeConfigViaHTTP 直连节点 HTTP 管理接口（WS 不可用时回退）
func nodeConfigViaHTTP(nodeID, action string, cfg map[string]any, persist bool, token string) (json.RawMessage, error) {
	base := nodeHTTPBase(nodeID)
	if base == "" {
		return nil, fmt.Errorf("节点 %s 没有配置 HTTP 上游地址", nodeID)
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}

	var method, target string
	var body []byte
	switch action {
	case "get":
		method, target = http.MethodGet, base+"v1/config"
	case "patch":
		method, target = http.MethodPatch, base+"v1/config"
		if persist {
			target += "?persist=true"
		}
		body, _ = json.Marshal(cfg)
	case "refresh":
		method, target = http.MethodPost, base+"v1/config/refresh"
	default:
		return nil, fmt.Errorf("未知的 action: %s", action)
	}

	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	// 配置管理不是拨测，不带 X-Scheduler-Probe（节点侧该路由也不在统计中间件下）

	client := &http.Client{Timeout: nodeConfigTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求节点失败：%w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		snippet := strings.TrimSpace(string(respBody))
		if len([]rune(snippet)) > 200 {
			snippet = string([]rune(snippet)[:200]) + "…"
		}
		return nil, fmt.Errorf("节点返回 %d：%s", resp.StatusCode, snippet)
	}
	return json.RawMessage(respBody), nil
}

// normalizeNodeConfigResult 把节点应答（WS/HTTP 字段略有差异）规整成统一结构
func normalizeNodeConfigResult(nodeID, channel, action string, raw json.RawMessage) *nodeConfigResult {
	var in struct {
		Config          map[string]any `json:"config"`
		SecretKeys      []string       `json:"secretKeys"`
		RestartKeys     []string       `json:"restartRequiredKeys"` // HTTP 侧字段名
		Applied         []string       `json:"applied"`
		Unknown         []string       `json:"unknown"`
		RestartRequired []string       `json:"restartRequired"`
		Persisted       bool           `json:"persisted"`
		ProtectedKeys   []string       `json:"remoteProtectedKeys"`
		ProtectedIgnore []string       `json:"protectedIgnored"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
		// WS 应答里也可能用 restartRequiredKeys（防御两种命名）
		if len(in.RestartKeys) == 0 {
			var alt struct {
				RestartKeys []string `json:"restartRequiredKeys"`
			}
			_ = json.Unmarshal(raw, &alt)
			in.RestartKeys = alt.RestartKeys
		}
	}
	res := &nodeConfigResult{
		NodeID:          nodeID,
		Channel:         channel,
		Action:          action,
		Config:          in.Config,
		SecretKeys:      in.SecretKeys,
		RestartKeys:     in.RestartKeys,
		Applied:         in.Applied,
		Unknown:         in.Unknown,
		RestartRequired: in.RestartRequired,
		Persisted:       in.Persisted,
		// 保护名单以节点回报为准（节点才是执行拦截的一方），未回报时用本地兜底供展示
		RemoteProtectedKeys: in.ProtectedKeys,
		ProtectedIgnored:    in.ProtectedIgnore,
	}
	if res.SecretKeys == nil {
		res.SecretKeys = secretKeyList()
	}
	if res.RemoteProtectedKeys == nil {
		res.RemoteProtectedKeys = nodeConfigRemoteProtectedKeys
	}
	sort.Strings(res.Applied)
	sort.Strings(res.Unknown)
	return res
}

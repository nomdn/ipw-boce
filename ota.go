package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

// ==================== 节点 OTA 升级（控制台下发，节点执行） ====================
//
// 链路：控制台建任务 → WS type=ota 下发（HTTP 回退 POST 节点 /v1/ota，鉴权语义同运行时配置）
//   → 节点下载/校验/替换/重启（见 ipw-cn/src/ota.go）→ 节点连接断开 → 重连注册上报新版本号
//   → 控制台判定任务 success。
//
// 成败判定：节点重启期间 WS 必然断开，最终结果无法经原连接回传，因此以"观测到节点重连后的
// 版本号"为准（OTA_TASK_TERMINAL 注释与 db.go OTATask 一致）：
//   - 节点经 WS 回 ok:false（下载/校验/替换失败）→ 立即 failed（进度阶段同步更新到任务）
//   - 重连版本命中判定（见 otaVersionHit）→ success（WS 注册钩子即时触发 + 兜底轮询）
//   - 超过 otaTaskTimeout 未观测到版本变化 → failed（超时：节点离线/升级失败/回滚）

const (
	otaAckTimeout  = 15 * time.Second  // 等节点首个 ota_result（accepted；老版本节点静默忽略会打满）
	otaTaskTimeout = 15 * time.Minute  // 等节点重连上报新版本的总窗口
	otaSweepEvery  = 10 * time.Second  // 任务追踪轮询间隔
	otaListLimit   = 100               // 任务列表单页上限
)

// otaStatusDispatched / otaStatusSuccess / otaStatusFailed 任务状态取值
const (
	otaStatusDispatched = "dispatched"
	otaStatusSuccess    = "success"
	otaStatusFailed     = "failed"
)

// otaDispatchHint 下发成功后给管理员的说明（成败判定口径，UI 原样展示）
const otaDispatchHint = "已下发。节点重启会断开 WS，最终结果以节点重连后的版本号为准（任务列表可见进度；" +
	"节点侧下载/校验失败会立即回传失败原因）。"

// registerOTARoutes 注册 OTA 管理路由（admin only）：
//   POST /nodes/:nodeId/ota  下发升级任务（body: {version?, url?, sha256?}，version 与 url 二选一）
//   GET  /ota-tasks          最近任务列表（倒序）
func registerOTARoutes(group *gin.RouterGroup) {
	group.POST("/nodes/:nodeId/ota", otaDispatchHandler)
	group.GET("/ota-tasks", otaListHandler)
}

// otaDispatchHandler POST /nodes/:nodeId/ota
func otaDispatchHandler(c *gin.Context) {
	nodeID := strings.TrimSpace(c.Param("nodeId"))
	if nodeID == "" {
		apiError(c, http.StatusBadRequest, "nodeId 不能为空")
		return
	}
	var in struct {
		Version string `json:"version"` // 目标版本（release tag，v1.2.3 / 1.2.3 均可）
		URL     string `json:"url"`     // 直发下载地址（与 version 二选一，url 优先）
		SHA256  string `json:"sha256"`  // 可选内容校验（hex64，兼容 sha256: 前缀）
	}
	if err := c.ShouldBindJSON(&in); err != nil && err != io.EOF {
		apiError(c, http.StatusBadRequest, "请求体不是合法 JSON："+err.Error())
		return
	}
	in.Version = strings.TrimSpace(in.Version)
	in.URL = strings.TrimSpace(in.URL)
	in.SHA256 = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(in.SHA256), "sha256:")))
	if in.Version == "" && in.URL == "" {
		apiError(c, http.StatusBadRequest, "version 与 url 至少填一项")
		return
	}
	if in.SHA256 != "" {
		if len(in.SHA256) != 64 {
			apiError(c, http.StatusBadRequest, "sha256 必须是 64 位 hex")
			return
		}
		if _, err := hex.DecodeString(in.SHA256); err != nil {
			apiError(c, http.StatusBadRequest, "sha256 必须是合法 hex")
			return
		}
	}
	if in.URL != "" && !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		apiError(c, http.StatusBadRequest, "url 必须是 http(s) 地址")
		return
	}

	// 能力门：节点明确上报过清单且不含 ota → 直接拒绝（老节点静默忽略指令只能白等超时）
	if deny, _ := nodeCapabilityGate(nodeID, capabilityOTA, "OTA 升级"); deny != nil {
		apiError(c, http.StatusConflict, deny.Error())
		return
	}

	// 下发时的节点版本快照（成败判定基准）
	fromVersion, _, _ := nodeReportedInfo(nodeID)

	task := OTATask{
		NodeID:        nodeID,
		RequestID:     "o" + genRequestID(),
		FromVersion:   fromVersion,
		TargetVersion: in.Version,
		URL:           in.URL,
		SHA256:        in.SHA256,
		Status:        otaStatusDispatched,
		DispatchedAt:  time.Now().UTC(),
	}
	if _, role, username := currentUserFromCtx(c); username != "" {
		task.DispatchedBy = username
	} else if role != "" {
		task.DispatchedBy = "static-token"
	}

	// 组装下发报文：url 直发原样；按版本下发时附带资产基址（节点按自身平台计算资产名）
	payload := map[string]any{"requestId": task.RequestID}
	if in.URL != "" {
		payload["url"] = in.URL
	} else {
		payload["version"] = in.Version
		payload["assetBase"] = otaAssetBase()
	}
	if in.SHA256 != "" {
		payload["sha256"] = in.SHA256
	}

	channel, stage, dispatchErr := otaSend(nodeID, payload)
	task.Channel = channel
	task.Stage = stage
	if dispatchErr != nil {
		task.Status = otaStatusFailed
		now := time.Now().UTC()
		task.FinishedAt = &now
		task.Error = truncateStr(dispatchErr.Error(), 512)
		if err := saveOTATask(&task); err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		apiError(c, http.StatusBadGateway, dispatchErr.Error())
		return
	}
	if err := saveOTATask(&task); err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[ota] task#%d dispatched to %s via %s (from=%q target=%q)",
		task.ID, nodeID, channel, task.FromVersion, task.TargetVersion)
	c.JSON(http.StatusOK, gin.H{"task": task, "hint": otaDispatchHint})
}

// otaListHandler GET /ota-tasks?limit=
func otaListHandler(c *gin.Context) {
	limit := clampLimit(c.Query("limit"), 50)
	ctx, cancel := dbCtx()
	defer cancel()
	var rows []OTATask
	if err := db.WithContext(ctx).Order("id desc").Limit(limit).Find(&rows).Error; err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, rows)
}

// otaSend 下发 OTA 指令：WS 优先，HTTP 回退（节点 /v1/ota，凭据语义同运行时配置管理）。
// 返回实际使用的通道、节点回报的最新阶段、错误。
func otaSend(nodeID string, payload map[string]any) (channel, stage string, err error) {
	// WS 通道：在线即走，无需节点暴露端口/access-token
	var wsErr error
	switch {
	case wsSrv == nil:
		wsErr = fmt.Errorf("WS 服务未启用")
	case !wsSrv.NodeConnected(nodeID):
		wsErr = fmt.Errorf("节点 WS 离线")
	default:
		raw, rerr := wsSrv.RequestOTA(nodeID, payload, otaAckTimeout)
		if rerr == nil {
			head := struct {
				OK    bool   `json:"ok"`
				Stage string `json:"stage"`
				Error string `json:"error"`
			}{}
			_ = json.Unmarshal(raw, &head)
			if !head.OK {
				return "ws", head.Stage, fmt.Errorf("节点拒绝 OTA（%s）：%s", head.Stage, head.Error)
			}
			return "ws", head.Stage, nil
		}
		var te *wsTimeoutError
		if errors.As(rerr, &te) {
			wsErr = wsTimeoutDiagnosis(nodeID, rerr)
		} else {
			wsErr = fmt.Errorf("WS 指令失败（%w）", rerr)
		}
	}

	// HTTP 回退：需要节点配了 access-token（api-keys[nodeId]），否则节点 HTTP OTA 管理面是关闭的
	token := nodeAdminToken(nodeID)
	if token == "" {
		return "", "", fmt.Errorf("节点 %s 无法下发 OTA：%v；且未配置 access-token（api-keys），"+
			"节点侧 HTTP 接口已关闭，无可回退通道", nodeID, wsErr)
	}
	base := nodeHTTPBase(nodeID)
	if base == "" {
		return "", "", fmt.Errorf("节点 %s 无法下发 OTA：%v；且节点池里没有该节点的 HTTP 上游地址", nodeID, wsErr)
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, base+"v1/ota", strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("节点 %s 无法下发 OTA：%v；HTTP 回退也失败：%v", nodeID, wsErr, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= http.StatusBadRequest {
		return "", "", fmt.Errorf("节点 %s HTTP 回退返回 %d：%s", nodeID, resp.StatusCode,
			strings.TrimSpace(string(respBody)))
	}
	return "http", "accepted", nil
}

// otaAssetBase 按版本下发时使用的发布资产基址（ota-asset-base 配置，缺省 GitHub Releases）
func otaAssetBase() string {
	base := strings.TrimSpace(viper.GetString("ota-asset-base"))
	if base == "" {
		return "https://github.com/nomdn/ipw-cn/releases/download"
	}
	return strings.TrimSuffix(base, "/")
}

// ==================== 任务追踪 ====================

// saveOTATask 新建任务（update 语义由追踪函数按字段更新，不走全量 Save 避免并发覆盖）
func saveOTATask(t *OTATask) error {
	ctx, cancel := dbCtx()
	defer cancel()
	return db.WithContext(ctx).Create(t).Error
}

// otaOnNodeResult 节点经 WS 回报 ota_result：更新任务阶段；ok=false 立即判 failed。
// 首帧（accepted）已被下发方消费作为 ack，这里照常幂等更新。
func otaOnNodeResult(nodeID string, data json.RawMessage) {
	if db == nil {
		return
	}
	var res struct {
		RequestID string `json:"requestId"`
		OK        bool   `json:"ok"`
		Stage     string `json:"stage"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(data, &res); err != nil || res.RequestID == "" {
		return
	}
	ctx, cancel := dbCtx()
	defer cancel()
	q := db.WithContext(ctx).Model(&OTATask{}).Where("request_id = ?", res.RequestID)
	updates := map[string]any{"stage": res.Stage, "updated_at": time.Now().UTC()}
	if !res.OK {
		updates["status"] = otaStatusFailed
		now := time.Now().UTC()
		updates["finished_at"] = &now
		updates["error"] = truncateStr(res.Error, 512)
		log.Printf("[ota] task for node %s FAILED at stage=%s: %s", nodeID, res.Stage, res.Error)
	}
	if err := q.Updates(updates).Error; err != nil {
		log.Printf("[ota] WARN update task stage: %v", err)
		return
	}
	if !res.OK {
		// 失败终结：节点若仍未恢复在线（在途掉线已被豁免），此时补报
		var t OTATask
		if err := db.WithContext(ctx).Where("request_id = ?", res.RequestID).Limit(1).Find(&t).Error; err == nil && t.ID != 0 {
			otaNotifyIfStillDown(t, fmt.Sprintf("OTA 任务#%d 失败（%s：%s），节点未恢复上线", t.ID, res.Stage, res.Error))
		}
	}
}

// otaOnNodeRegistered WS 注册钩子（ws.go register 后调用）：节点重连上报的版本号命中
// 在途任务的判定条件 → 即时判 success。
func otaOnNodeRegistered(nodeID, version string) {
	if db == nil || version == "" {
		return
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var t OTATask
	// Find + ID 判空而非 First：无在途任务是常态，避免 GORM 把 record not found 当错误刷日志
	if err := db.WithContext(ctx).
		Where("node_id = ? AND status = ?", nodeID, otaStatusDispatched).
		Order("id desc").Limit(1).Find(&t).Error; err != nil || t.ID == 0 {
		return // 无在途任务
	}
	if !otaVersionHit(t, version) {
		return
	}
	finishOTATask(t.ID, otaStatusSuccess, "")
	log.Printf("[ota] task#%d SUCCESS: node %s re-registered with version %s", t.ID, nodeID, version)
}

// otaTaskInFlight 该节点是否存在在途 OTA 任务（status=dispatched）。
// 供掉线告警豁免：OTA 计划内重启必然伴随一次真实断连，不应按意外掉线告警。
func otaTaskInFlight(nodeID string) bool {
	if db == nil {
		return false
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var t OTATask
	// Find+ID 判空：无在途任务是常态，避免 GORM 把 record not found 当错误刷日志
	if err := db.WithContext(ctx).
		Where("node_id = ? AND status = ?", nodeID, otaStatusDispatched).
		Limit(1).Find(&t).Error; err != nil || t.ID == 0 {
		return false
	}
	return true
}

// otaNotifyIfStillDown OTA 任务失败终结后，若节点仍未恢复在线则补发掉线告警。
// 背景：在途任务期间的掉线被豁免了（计划内重启不 page）；任务失败且节点没回来，
// 说明"升级没成、节点也没回来"，这时才该报。节点已在线（含 url 模式装了同版本）则不报。
func otaNotifyIfStillDown(t OTATask, reason string) {
	ctx, cancel := dbCtx()
	defer cancel()
	var n Node
	if err := db.WithContext(ctx).Where("node_id = ?", t.NodeID).Limit(1).Find(&n).Error; err != nil || n.ID == 0 || n.Online {
		return
	}
	// 撤销在途期间打下的"上线豁免"：这次是任务终结仍离线的**真掉线**，已按掉线通报，
	// 节点日后复联就该正常补一条"恢复上线"，与这条掉线配对。
	takeOtaExempt(t.NodeID)
	go notifyNodeDown(monitorNode{id: t.NodeID, label: n.Label, ws: true}, "WS 版", reason, 1)
}

// ==================== OTA 计划内重启的"上线"豁免 ====================
//
// OTA 重启必然伴随一次真实断连：掉线告警已豁免（见 store.go recordNodeOffline），
// 同理这次计划内的复联也不该单独报一条"节点恢复上线"——否则群里会出现一条没有对应掉线的孤立上线。
// 断连时打标记（markOtaExempt），节点下次注册时一次性消费（takeOtaExempt）。
// 纯内存、与 down 通知锁存同生命周期：进程重启即清空，最坏情况是多收到一次上线通知。
var (
	otaExemptMu sync.Mutex
	otaExemptS  = map[string]bool{}
)

// markOtaExempt 记下"这次断连是 OTA 计划内重启"，供下次注册时抑制上线通知
func markOtaExempt(nodeID string) {
	otaExemptMu.Lock()
	otaExemptS[nodeID] = true
	otaExemptMu.Unlock()
}

// takeOtaExempt 取出并清除豁免标记，返回此前是否存在（一次性消费）
func takeOtaExempt(nodeID string) bool {
	otaExemptMu.Lock()
	defer otaExemptMu.Unlock()
	found := otaExemptS[nodeID]
	delete(otaExemptS, nodeID)
	return found
}

// otaVersionHit 重连版本是否命中任务判定：
//   - 按版本下发：重连版本 == 目标版本
//   - 直发 url：重连版本 != 下发时版本（未知原版本时无法判定，交超时兜底）
func otaVersionHit(t OTATask, version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}
	if t.TargetVersion != "" {
		return version == t.TargetVersion ||
			"v"+version == t.TargetVersion || version == "v"+t.TargetVersion
	}
	return t.FromVersion != "" && version != t.FromVersion
}

// finishOTATask 任务终结（success/failed），幂等：仅 dispatched 状态可被终结
func finishOTATask(id uint, status, errMsg string) {
	ctx, cancel := dbCtx()
	defer cancel()
	now := time.Now().UTC()
	updates := map[string]any{
		"status":      status,
		"finished_at": &now,
		"updated_at":  now,
	}
	if errMsg != "" {
		updates["error"] = truncateStr(errMsg, 512)
	}
	db.WithContext(ctx).Model(&OTATask{}).
		Where("id = ? AND status = ?", id, otaStatusDispatched).
		Updates(updates)
}

// otaReconcilerLoop 任务追踪兜底轮询：WS 注册钩子覆盖 WS 节点的即时判定，
// 这里补两类场景——HTTP 节点（版本号由看门狗每小时探活回填）与超时收敛。
func otaReconcilerLoop() {
	if db == nil {
		return
	}
	for {
		time.Sleep(otaSweepEvery)
		otaSweep()
	}
}

func otaSweep() {
	ctx, cancel := dbCtx()
	defer cancel()
	var tasks []OTATask
	if err := db.WithContext(ctx).
		Where("status = ?", otaStatusDispatched).
		Order("id asc").Limit(otaListLimit).Find(&tasks).Error; err != nil {
		return
	}
	for _, t := range tasks {
		// 版本命中判定（依赖 nodes 表快照：WS 注册钩子 / 看门狗探活都会写入）
		var n Node
		if err := db.WithContext(ctx).Where("node_id = ?", t.NodeID).Limit(1).Find(&n).Error; err == nil && n.ID != 0 {
			if otaVersionHit(t, n.Version) {
				finishOTATask(t.ID, otaStatusSuccess, "")
				log.Printf("[ota] task#%d SUCCESS (sweep): node %s version %s", t.ID, t.NodeID, n.Version)
				continue
			}
		}
		// 超时：等不到新版本（节点离线 / 升级失败回滚 / 下载卡死）
		if time.Since(t.DispatchedAt) > otaTaskTimeout {
			finishOTATask(t.ID, otaStatusFailed,
				fmt.Sprintf("超时（%s）未观测到节点版本变化：节点可能离线、升级失败或已回滚，请到节点日志确认", otaTaskTimeout))
			log.Printf("[ota] task#%d TIMEOUT: node %s", t.ID, t.NodeID)
			otaNotifyIfStillDown(t, fmt.Sprintf("OTA 任务#%d 超时（%s）未观测到版本变化，且节点仍未恢复上线", t.ID, otaTaskTimeout))
		}
	}
}

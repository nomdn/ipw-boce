package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 节点 OTA 升级（与原版协议一致） ====================
//
// 控制台建任务 → WS type=ota 下发 {requestId, url | version+assetBase, sha256?}（HTTP 回退 POST {url}v1/ota）
// → 节点下载/校验/替换/重启，逐阶段回 ota_result → 重连注册上报新版本 → 判定 success。
//   - 节点回 ok:false → 立即 failed
//   - 按版本下发：重连版本 == 目标版本；直发 url：重连版本 != 下发时版本
//   - HTTP-only 节点由轮询现场探测 GET {url}info 判定
//   - 15 分钟未观测到版本变化 → failed（超时）
// 在途任务期间的掉线/恢复不发管理员告警（计划内重启）。

type OTATask struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	NodeID        string     `gorm:"index;size:128" json:"nodeId"`
	RequestID     string     `gorm:"size:64;index" json:"requestId"`
	FromVersion   string     `gorm:"size:64" json:"fromVersion"`
	TargetVersion string     `gorm:"size:64" json:"targetVersion"`
	URL           string     `gorm:"size:512" json:"url,omitempty"`
	SHA256        string     `gorm:"size:64" json:"sha256,omitempty"`
	Channel       string     `gorm:"size:8" json:"channel"`
	Status        string     `gorm:"index;size:16" json:"status"`
	Stage         string     `gorm:"size:32" json:"stage"`
	Error         string     `gorm:"size:512" json:"error,omitempty"`
	DispatchedBy  string     `gorm:"size:64" json:"dispatchedBy,omitempty"`
	DispatchedAt  time.Time  `json:"dispatchedAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
}

const (
	otaDispatched  = "dispatched"
	otaSuccess     = "success"
	otaFailed      = "failed"
	otaAckTimeout  = 15 * time.Second
	otaTaskTimeout = 15 * time.Minute
)

var OTA_ASSET_BASE string

func otaAssetBase() string {
	if b := strings.TrimSuffix(strings.TrimSpace(OTA_ASSET_BASE), "/"); b != "" {
		return b
	}
	return "https://github.com/nomdn/ipw-cn/releases/download"
}

func otaTaskInFlight(nodeID string) bool {
	ctx, cancel := dbCtx()
	defer cancel()
	var n int64
	db.WithContext(ctx).Model(&OTATask{}).Where("node_id = ? AND status = ?", nodeID, otaDispatched).Count(&n)
	return n > 0
}

// otaExempt 在途 OTA 期间发生的断连：对应的恢复也不报（一次性消费）
var otaExempt sync.Map

func otaVersionHit(t OTATask, v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if t.TargetVersion != "" {
		return strings.TrimPrefix(v, "v") == strings.TrimPrefix(t.TargetVersion, "v")
	}
	return t.FromVersion != "" && v != t.FromVersion
}

func finishOTATask(id uint, status, stage, errMsg string) {
	ctx, cancel := dbCtx()
	defer cancel()
	now := time.Now().UTC()
	up := map[string]any{"status": status, "finished_at": &now, "updated_at": now}
	if stage != "" {
		up["stage"] = stage
	}
	if errMsg != "" {
		up["error"] = truncateStr(errMsg, 512)
	}
	db.WithContext(ctx).Model(&OTATask{}).Where("id = ? AND status = ?", id, otaDispatched).Updates(up)
}

func otaOnNodeRegistered(nodeID, version string) {
	if version == "" {
		return
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var t OTATask
	db.WithContext(ctx).Where("node_id = ? AND status = ?", nodeID, otaDispatched).Order("id desc").Limit(1).Find(&t)
	if t.ID != 0 && otaVersionHit(t, version) {
		finishOTATask(t.ID, otaSuccess, "", "")
		log.Printf("[ota] task#%d SUCCESS: %s re-registered with %s", t.ID, nodeID, version)
	}
}

func otaOnNodeResult(nodeID string, data json.RawMessage) {
	var r struct {
		RequestID string `json:"requestId"`
		OK        bool   `json:"ok"`
		Stage     string `json:"stage"`
		Error     string `json:"error"`
	}
	if json.Unmarshal(data, &r) != nil || r.RequestID == "" {
		return
	}
	ctx, cancel := dbCtx()
	defer cancel()
	up := map[string]any{"stage": r.Stage, "updated_at": time.Now().UTC()}
	if !r.OK {
		now := time.Now().UTC()
		up["status"], up["finished_at"], up["error"] = otaFailed, &now, truncateStr(r.Error, 512)
		log.Printf("[ota] node %s FAILED at stage=%s: %s", nodeID, r.Stage, r.Error)
	}
	db.WithContext(ctx).Model(&OTATask{}).Where("request_id = ? AND status = ?", r.RequestID, otaDispatched).Updates(up)
}

func otaLiveVersion(nodeID string) (string, bool) {
	base := strings.TrimSuffix(nodeHTTPBase(nodeID), "/")
	if base == "" {
		return "", false
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(base + "/info")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var info struct {
		Version string `json:"version"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 400 || json.Unmarshal(b, &info) != nil || info.Version == "" {
		return "", false
	}
	return strings.TrimSpace(info.Version), true
}

func otaNodeIsHTTPOnly(nodeID string) bool {
	n := findNode(allPoolNodes(), nodeID)
	return n != nil && !n.UseWS()
}

// otaReconcilerLoop 每 10s：HTTP-only 节点现场探版本；超时收敛
func otaReconcilerLoop() {
	for range time.Tick(10 * time.Second) {
		ctx, cancel := dbCtx()
		var tasks []OTATask
		db.WithContext(ctx).Where("status = ?", otaDispatched).Order("id asc").Limit(100).Find(&tasks)
		cancel()
		for _, t := range tasks {
			version := ""
			if n, ok := nodeSnapshot(t.NodeID); ok {
				version = n.Version
			}
			if otaNodeIsHTTPOnly(t.NodeID) {
				if live, ok := otaLiveVersion(t.NodeID); ok {
					version = live
					ctx, cancel := dbCtx()
					db.WithContext(ctx).Model(&Node{}).Where("node_id = ?", t.NodeID).Update("version", live)
					cancel()
				}
			}
			if otaVersionHit(t, version) {
				finishOTATask(t.ID, otaSuccess, "observed", "")
				continue
			}
			if time.Since(t.DispatchedAt) > otaTaskTimeout {
				finishOTATask(t.ID, otaFailed, "", fmt.Sprintf("超时（%s）未观测到节点版本变化：节点可能离线、升级失败或已回滚", otaTaskTimeout))
				if n, ok := nodeSnapshot(t.NodeID); ok && !n.Online {
					otaExempt.Delete(t.NodeID)
					notifyAdmins("node_down", "[1IPW-BOCE] 节点 OTA 后未恢复："+nodeDisplayName(t.NodeID, n.Label),
						fmt.Sprintf("OTA 任务#%d 超时，节点 %s 仍未恢复上线。", t.ID, t.NodeID))
				}
			}
		}
	}
}

func registerOTARoutes(g *gin.RouterGroup) {
	g.POST("/nodes/:nodeId/ota", func(c *gin.Context) {
		nodeID := strings.TrimSpace(c.Param("nodeId"))
		var in struct {
			Version string `json:"version"`
			URL     string `json:"url"`
			SHA256  string `json:"sha256"`
		}
		if err := c.ShouldBindJSON(&in); err != nil && err != io.EOF {
			apiError(c, http.StatusBadRequest, "请求体不是合法 JSON："+err.Error())
			return
		}
		in.Version, in.URL = strings.TrimSpace(in.Version), strings.TrimSpace(in.URL)
		in.SHA256 = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(in.SHA256), "sha256:"))
		switch {
		case in.Version == "" && in.URL == "":
			apiError(c, http.StatusBadRequest, "version 与 url 至少填一项")
			return
		case in.URL != "" && !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://"):
			apiError(c, http.StatusBadRequest, "url 必须是 http(s) 地址")
			return
		case in.SHA256 != "":
			if _, err := hex.DecodeString(in.SHA256); err != nil || len(in.SHA256) != 64 {
				apiError(c, http.StatusBadRequest, "sha256 必须是 64 位 hex")
				return
			}
		}
		if err := nodeCapabilityGate(nodeID, capabilityOTA, "OTA 升级"); err != nil {
			apiError(c, http.StatusConflict, err.Error())
			return
		}
		if otaTaskInFlight(nodeID) {
			apiError(c, http.StatusConflict, "该节点已有进行中的 OTA 任务")
			return
		}
		from, _, _ := nodeReportedInfo(nodeID)
		if otaNodeIsHTTPOnly(nodeID) {
			if live, ok := otaLiveVersion(nodeID); ok {
				from = live
			}
		}
		task := OTATask{NodeID: nodeID, RequestID: "o" + genRequestID(), FromVersion: from, TargetVersion: in.Version,
			URL: in.URL, SHA256: in.SHA256, Status: otaDispatched, DispatchedAt: time.Now().UTC(), DispatchedBy: currentUsername(c)}
		payload := map[string]any{"requestId": task.RequestID}
		if in.URL != "" {
			payload["url"] = in.URL
		} else {
			payload["version"], payload["assetBase"] = in.Version, otaAssetBase()
		}
		if in.SHA256 != "" {
			payload["sha256"] = in.SHA256
		}
		raw, ch, err := sendNodeCommand(nodeID, "ota", payload, otaAckTimeout, func(base, token string) (json.RawMessage, error) {
			return httpJSON(http.MethodPost, base+"v1/ota", token, payload, 15*time.Second)
		})
		task.Channel = ch
		var head struct {
			Stage string `json:"stage"`
		}
		_ = json.Unmarshal(raw, &head)
		task.Stage = head.Stage
		if task.Stage == "" && err == nil {
			task.Stage = "accepted"
		}
		if err != nil {
			now := time.Now().UTC()
			task.Status, task.FinishedAt, task.Error = otaFailed, &now, truncateStr(err.Error(), 512)
		}
		ctx, cancel := dbCtx()
		defer cancel()
		if e := db.WithContext(ctx).Create(&task).Error; e != nil {
			apiError(c, http.StatusInternalServerError, e.Error())
			return
		}
		if err != nil {
			apiError(c, http.StatusBadGateway, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"task": task, "hint": "已下发。最终结果以观测到的节点版本号为准：WS 节点重连时上报，HTTP 节点由控制台定时现场探测。"})
	})

	g.GET("/ota-tasks", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var rows []OTATask
		q := db.WithContext(ctx).Order("id desc").Limit(clampLimit(c.Query("limit"), 50))
		if v := c.Query("nodeId"); v != "" {
			q = q.Where("node_id = ?", v)
		}
		q.Find(&rows)
		labels := poolLabelMap()
		out := make([]gin.H, 0, len(rows))
		for _, t := range rows {
			h := gin.H{}
			_ = json.Unmarshal(mustRaw(t), &h)
			h["label"] = nodeDisplayName(t.NodeID, labels[t.NodeID])
			out = append(out, h)
		}
		c.JSON(http.StatusOK, out)
	})
}

func nodeDisplayName(nodeID, label string) string {
	if s := poolLabelFor(nodeID); s != "" {
		return s
	}
	if s := strings.TrimSpace(label); s != "" {
		return s
	}
	return nodeID
}

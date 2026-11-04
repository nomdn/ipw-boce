package main

import (
	"fmt"
	"io"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm/clause"
)

// ==================== 节点在线快照（nodes + node_events） ====================
//
// WS 节点：注册/断开/剔除时即时写；HTTP 节点：看门狗探活（每 10 分钟，GET {url}/info 或根路径）。
// 掉线通知对管理员走 20s 宽限：秒级闪断回来的不通报，对应的恢复也不报。

func joinCapabilities(caps []string) string {
	var out []string
	for _, c := range caps {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" && !contains(out, c) {
			out = append(out, c)
		}
	}
	return strings.Join(out, ",")
}

func nodeSnapshot(id string) (Node, bool) {
	ctx, cancel := dbCtx()
	defer cancel()
	var n Node
	db.WithContext(ctx).Where("node_id = ?", id).Limit(1).Find(&n)
	return n, n.ID != 0
}

var (
	startupExempt sync.Map // 中心重启时被置离线的节点：重连不报"恢复"
	downPendingMu sync.Mutex
	downPending   = map[string]*time.Timer{}
)

const nodeDownGraceDelay = 20 * time.Second

func recordNodeOnline(nodeID, remoteAddr, version string, capabilities []string) {
	prev, prevOK := nodeSnapshot(nodeID)
	_, exempt := startupExempt.LoadAndDelete(nodeID)
	if _, ok := otaExempt.LoadAndDelete(nodeID); ok {
		exempt = true
	}
	if _, ok := maintSuppressed.LoadAndDelete(nodeID); ok {
		exempt = true
	}
	downPendingMu.Lock()
	t, pending := downPending[nodeID]
	if pending {
		t.Stop()
		delete(downPending, nodeID)
	}
	downPendingMu.Unlock()
	recovered := prevOK && !prev.Online && !exempt && !pending

	now := time.Now().UTC()
	updates := map[string]any{"online": true, "last_seen_at": now}
	if remoteAddr != "" {
		updates["remote_addr"] = remoteAddr
	}
	if v := strings.TrimSpace(version); v != "" {
		updates["version"] = v
	}
	caps := joinCapabilities(capabilities)
	if caps != "" {
		updates["capabilities"] = caps
	}
	label := poolLabelFor(nodeID)
	if label != "" {
		updates["label"] = label
	}
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_id"}}, DoUpdates: clause.Assignments(updates),
	}).Create(&Node{NodeID: nodeID, Label: label, Online: true, RemoteAddr: remoteAddr,
		Version: strings.TrimSpace(version), Capabilities: caps, FirstSeenAt: now, LastSeenAt: now}).Error; err != nil {
		log.Printf("[store] ERROR upsert node online: %v", err)
		return
	}
	if !prevOK || !prev.Online {
		db.WithContext(ctx).Create(&NodeEvent{NodeID: nodeID, Event: "online", Reason: "registered", CreatedAt: now})
	}
	if recovered {
		name := label
		if name == "" {
			name = nodeID
		}
		go notifyAdmins("node_up", fmt.Sprintf("[1IPW-BOCE] 节点恢复上线：%s", name),
			fmt.Sprintf("节点 %s（%s）已恢复在线。\n时间：%s", name, nodeID, now.Local().Format("2006-01-02 15:04:05")))
	}
}

func recordNodeOffline(nodeID, reason string) {
	prev, prevOK := nodeSnapshot(nodeID)
	now := time.Now().UTC()
	ctx, cancel := dbCtx()
	defer cancel()
	db.WithContext(ctx).Model(&Node{}).Where("node_id = ?", nodeID).Updates(map[string]any{"online": false, "last_seen_at": now})
	if prevOK && prev.Online {
		db.WithContext(ctx).Create(&NodeEvent{NodeID: nodeID, Event: "offline", Reason: truncateStr(reason, 256), CreatedAt: now})
		// OTA 计划内重启 / 维护窗口：事件照写，通知不发（对应的恢复也一并吞掉）
		if otaTaskInFlight(nodeID) {
			otaExempt.Store(nodeID, true)
			return
		}
		if maintenanceHit(nodeID, time.Now()) {
			maintSuppressed.Store(nodeID, true)
			return
		}
		downPendingMu.Lock()
		if old, ok := downPending[nodeID]; ok {
			old.Stop()
		}
		downPending[nodeID] = time.AfterFunc(nodeDownGraceDelay, func() {
			downPendingMu.Lock()
			delete(downPending, nodeID)
			downPendingMu.Unlock()
			if cur, ok := nodeSnapshot(nodeID); ok && !cur.Online {
				name := cur.Label
				if name == "" {
					name = nodeID
				}
				notifyAdmins("node_down", fmt.Sprintf("[1IPW-BOCE] 节点掉线：%s", name),
					fmt.Sprintf("节点 %s（%s）已离线。\n原因：%s\n时间：%s", name, nodeID, reason, now.Local().Format("2006-01-02 15:04:05")))
			}
		})
		downPendingMu.Unlock()
	}
}

func touchNodes(ids []string) {
	if len(ids) == 0 {
		return
	}
	ctx, cancel := dbCtx()
	defer cancel()
	db.WithContext(ctx).Model(&Node{}).Where("node_id IN ?", ids).Update("last_seen_at", time.Now().UTC())
}

func markAllNodesOffline() {
	ctx, cancel := dbCtx()
	defer cancel()
	var ids []string
	db.WithContext(ctx).Model(&Node{}).Where("online = ?", true).Pluck("node_id", &ids)
	db.WithContext(ctx).Model(&Node{}).Where("online = ?", true).Update("online", false)
	for _, id := range ids {
		startupExempt.Store(id, true)
	}
}

// ==================== HTTP 节点看门狗 ====================

const healthInterval = 10 * time.Minute

var healthFails sync.Map // nodeId → 连续失败次数

func startNodeWatcher() {
	go func() {
		time.Sleep(3 * time.Second)
		for {
			checkHTTPNodes()
			time.Sleep(healthInterval)
		}
	}()
}

func checkHTTPNodes() {
	client := &http.Client{Timeout: 10 * time.Second}
	var wg sync.WaitGroup
	for _, n := range allPoolNodes() {
		if n.UseWS() || n.URL == "" {
			continue
		}
		wg.Add(1)
		go func(n apiInfo) {
			defer wg.Done()
			// 中继节点（url 含 {path}）没有 /info，用一次轻量 tcping 判活
			if strings.Contains(n.URL, "{path}") {
				status, _, err := probeOneHTTP(n, "tcping", "1.1.1.1", url.Values{"count": {"1"}})
				if err == nil && status == http.StatusOK {
					healthFails.Delete(n.ID)
					recordNodeOnline(n.ID, "relay", "", nil)
					return
				}
				v, _ := healthFails.LoadOrStore(n.ID, 0)
				fails := v.(int) + 1
				healthFails.Store(n.ID, fails)
				if fails >= 2 {
					recordNodeOffline(n.ID, "relay probe failed")
				}
				return
			}
			base := strings.TrimSuffix(n.URL, "/")
			version, caps, ok := "", []string(nil), false
			req, _ := http.NewRequest(http.MethodGet, base+"/info", nil)
			if k := lookupAPIKey(n.ID); k != "" {
				req.Header.Set("Authorization", "Bearer "+k)
			}
			if resp, err := client.Do(req); err == nil {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
				resp.Body.Close()
				ok = resp.StatusCode < 500
				var info struct {
					Version      string   `json:"version"`
					Capabilities []string `json:"capabilities"`
				}
				if resp.StatusCode == 200 && json.Unmarshal(b, &info) == nil {
					version, caps = info.Version, info.Capabilities
				}
			}
			if !ok {
				if resp, err := client.Get(base + "/"); err == nil {
					resp.Body.Close()
					ok = resp.StatusCode < 500
				}
			}
			if ok {
				healthFails.Delete(n.ID)
				recordNodeOnline(n.ID, "", version, caps)
				return
			}
			v, _ := healthFails.LoadOrStore(n.ID, 0)
			fails := v.(int) + 1
			healthFails.Store(n.ID, fails)
			if fails >= 2 {
				recordNodeOffline(n.ID, "http health check failed")
			}
		}(n)
	}
	wg.Wait()
}

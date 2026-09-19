package main

import (
	"errors"
	"log"
	"strings"
	"time"

	"gorm.io/gorm/clause"
)

// ==================== HTTP 版节点在线快照维护 ====================
//
// WS 版节点的 nodes 快照由 ws.go 心跳维护（recordNodeOnline/recordNodeOffline）。
// HTTP 版节点没有常驻连接，其在线状态由 nodeHealth.go 看门狗探活得出；
// 这里在探活 up/down 翻转时同步写 nodes 表 + node_events，使 NodeView/节点事件与 WS 节点一致。

// markNodeUp 某节点判 up：upsert 在线快照 + 追加 online 事件（HTTP 看门狗探活成功时）。
// version / capabilities 取自节点健康检查返回；为空（老版本节点）时不覆盖库里已有值。
func markNodeUp(n monitorNode, reason, version string, capabilities []string) {
	if db == nil {
		return
	}
	now := time.Now().UTC()
	ctx, cancel := dbCtx()
	defer cancel()
	updates := map[string]any{
		"label":        n.label,
		"online":       true,
		"last_seen_at": now,
	}
	if v := strings.TrimSpace(version); v != "" {
		updates["version"] = v
	}
	caps := joinCapabilities(capabilities)
	if caps != "" {
		updates["capabilities"] = caps
	}
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.Assignments(updates),
	}).Create(&Node{NodeID: n.id, Label: n.label, Online: true, Version: strings.TrimSpace(version), Capabilities: caps, FirstSeenAt: now, LastSeenAt: now}).Error; err != nil {
		log.Printf("[node] ERROR upsert node up(%s): %v", n.id, err)
		return
	}
	// 幂等去重：仅在先前为离线/不存在时补 online 事件（避免每次探活成功都刷一条）
	if wasOffline := nodeWasOffline(n.id); wasOffline {
		if err := db.WithContext(ctx).Create(&NodeEvent{NodeID: n.id, Event: "online", Reason: truncateStr(reason, 256), CreatedAt: now}).Error; err != nil {
			log.Printf("[node] ERROR create node online event(%s): %v", n.id, err)
		}
	}
}

// markNodeDown 某节点判 down：置离线 + 追加 offline 事件（HTTP 看门狗连续探活失败）。
func markNodeDown(n monitorNode, reason string) {
	if db == nil {
		return
	}
	now := time.Now().UTC()
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).Model(&Node{}).Where("node_id = ?", n.id).
		Updates(map[string]any{"online": false, "last_seen_at": now}).Error; err != nil {
		log.Printf("[node] ERROR update node down(%s): %v", n.id, err)
		return
	}
	if wasOnline := nodeWasOnline(n.id); wasOnline {
		if err := db.WithContext(ctx).Create(&NodeEvent{NodeID: n.id, Event: "offline", Reason: truncateStr(reason, 256), CreatedAt: now}).Error; err != nil {
			log.Printf("[node] ERROR create node offline event(%s): %v", n.id, err)
		}
	}
}

// nodeWasOnline 判断该节点在 nodes 表中是否处于在线（供 down 事件去重）
func nodeWasOnline(nodeID string) bool {
	if db == nil {
		return false
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var n Node
	if err := db.WithContext(ctx).Where("node_id = ?", nodeID).Limit(1).Find(&n).Error; err != nil {
		return false
	}
	return n.ID != 0 && n.Online
}

// nodeWasOffline 判断该节点在 nodes 表中是否处于离线或不存在（供 up 事件去重）
func nodeWasOffline(nodeID string) bool {
	if db == nil {
		return true // 无记录视为离线
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var n Node
	if err := db.WithContext(ctx).Where("node_id = ?", nodeID).Limit(1).Find(&n).Error; err != nil {
		return true
	}
	return n.ID == 0 || !n.Online
}

// deleteNodeRecord 从 nodes 表删除该节点的在线快照行（节点从池里移除后清理状态页残留）。
// 只删快照行，node_events 历史保留；若节点仍在池内，看门狗下次探活会重新写回（属预期）。
// 返回行是否存在。
func deleteNodeRecord(nodeID string) (bool, error) {
	if db == nil {
		return false, errors.New("db not ready")
	}
	ctx, cancel := dbCtx()
	defer cancel()
	res := db.WithContext(ctx).Where("node_id = ?", nodeID).Delete(&Node{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

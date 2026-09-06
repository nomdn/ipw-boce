package main

import (
	"log"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm/clause"
)

// ==================== 数据源标记 ====================

const (
	sourceWS   = "ws"
	sourceHTTP = "http"
	// sourceSched 定时拨测任务产出的样本标记：SLA/在线率统计只认它。
	// 节点自主上报走 ws|http；控制台手动一键拨测走 biz；三者都不计入 SLA（SLA 只认 sched）。
	sourceSched = "sched"
	// sourceBiz 控制台手动一键拨测（/admin/nodes/probe）落库的标记，
	// 归入明细页"业务拨测"类别（业务拨测 = 除定时拨测 sched 外的全部记录）。
	sourceBiz = "biz"
)
// isProbeType 拨测类 API（上报的拨测明细只收这些类；其余接口只进统计聚合）。
// 明细覆盖：连通性/证书类 detail、ssl，DNS 查询类 dns，直连类 tcping/speed。
// dnssec 属功能性校验，不逐条进明细，只进统计聚合。
func isProbeType(apiType string) bool {
	switch apiType {
	case "tcping", "speed", "detail", "ssl", "dns":
		return true
	}
	return false
}

// ==================== 后台任务 ====================

// dataStore 持有后台协程：数据保留期清理。
// 统计与拨测明细不在转发路径采集——节点自己上报，由 report.go 直接入库。
type dataStore struct {
	gdb      *storeDB
	stopCh   chan struct{}
	stopWait sync.WaitGroup
}

func newDataStore(s *storeDB) *dataStore {
	return &dataStore{
		gdb:    s,
		stopCh: make(chan struct{}),
	}
}

// Start 启动后台协程：数据保留期清理 + 定时拨测调度（实现见 probe_task.go）
func (s *dataStore) Start() {
	s.stopWait.Add(2)
	go s.retentionLoop()
	go s.schedulerLoop()
}

// Stop 停止后台协程
func (s *dataStore) Stop() {
	close(s.stopCh)
	s.stopWait.Wait()
}

// ---------- 节点在线状态 ----------

// recordNodeOnline WS 注册成功：upsert 节点快照并追加 online 事件
func recordNodeOnline(nodeID, remoteAddr string) {
	if db == nil {
		return
	}
	now := time.Now().UTC()
	// 节点重新在线：清掉 down 通知锁存，下次再掉线可再次告警
	clearNodeDownFired(nodeID)
	ctx, cancel := dbCtx()
	defer cancel()
	err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"online":       true,
			"remote_addr":  remoteAddr,
			"last_seen_at": now,
		}),
	}).Create(&Node{NodeID: nodeID, Online: true, RemoteAddr: remoteAddr, FirstSeenAt: now, LastSeenAt: now}).Error
	if err != nil {
		log.Printf("[store] ERROR upsert node online: %v", err)
		return
	}
	if err := db.WithContext(ctx).Create(&NodeEvent{NodeID: nodeID, Event: "online", Reason: "registered", CreatedAt: now}).Error; err != nil {
		log.Printf("[store] ERROR create node event: %v", err)
	}
}

// recordNodeOffline WS 断开/剔除：置离线并追加 offline 事件
//
// 这是 WS 节点掉线的**单一汇聚点**（断连/空闲剔除都走这里，节点状态页的红即由此置位）。
// 在真实 online→offline 翻转时补发一次节点掉线通知（发给所有启用 admin，见 notifyNodeDown），
// 使通知与节点状态页严格一致——不再依赖 nodeHealth 看门狗对配置池节点的独立判定。
func recordNodeOffline(nodeID, reason string) {
	if db == nil {
		return
	}
	wasOnline := nodeWasOnline(nodeID)
	now := time.Now().UTC()
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).Model(&Node{}).Where("node_id = ?", nodeID).
		Updates(map[string]any{"online": false, "last_seen_at": now}).Error; err != nil {
		log.Printf("[store] ERROR update node offline: %v", err)
	}
	if err := db.WithContext(ctx).Create(&NodeEvent{NodeID: nodeID, Event: "offline", Reason: truncateStr(reason, 256), CreatedAt: now}).Error; err != nil {
		log.Printf("[store] ERROR create node event: %v", err)
	}
	// 仅在"此前在线"的真掉线翻转时通知，且同一次掉线只通知一次（复联后由 recordNodeOnline 复位）。
	// 启动后从未在线的节点不上报，避免冷启动误报。
	if wasOnline && markNodeDownFired(nodeID) {
		label := ""
		var n Node
		if err := db.WithContext(ctx).Where("node_id = ?", nodeID).Limit(1).Find(&n).Error; err == nil && n.ID != 0 {
			label = n.Label
		}
		go notifyNodeDown(monitorNode{id: nodeID, label: label, ws: true}, "WS 版", reason, 1)
	}
}

// 掉线通知锁存：某节点是否已就当前这次 down 触发过通知（WS registry 路径）
var (
	nodeDownMu     sync.Mutex
	nodeDownFiredS = map[string]bool{}
)

// markNodeDownFired 返回 true 表示"本次 down 尚未通知过"，并随即标记已通知。
func markNodeDownFired(nodeID string) bool {
	nodeDownMu.Lock()
	defer nodeDownMu.Unlock()
	if nodeDownFiredS[nodeID] {
		return false
	}
	nodeDownFiredS[nodeID] = true
	return true
}

// clearNodeDownFired 节点重新在线时复位，允许下次 down 再次通知
func clearNodeDownFired(nodeID string) {
	nodeDownMu.Lock()
	delete(nodeDownFiredS, nodeID)
	nodeDownMu.Unlock()
}

// touchNodes 心跳批量刷新 last_seen_at（maintenanceLoop 每 20 秒调用一次）
func touchNodes(nodeIDs []string) {
	if db == nil || len(nodeIDs) == 0 {
		return
	}
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).Model(&Node{}).Where("node_id IN ?", nodeIDs).
		Update("last_seen_at", time.Now().UTC()).Error; err != nil {
		log.Printf("[store] ERROR touch nodes: %v", err)
	}
}

// markAllNodesOffline 进程启动时（WS 通道重启）把存量在线快照全部置离线，
// 之后由真实注册/心跳重新置位，避免重启后出现僵尸在线记录
func markAllNodesOffline() {
	if db == nil {
		return
	}
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).Model(&Node{}).Where("online = ?", true).Update("online", false).Error; err != nil {
		log.Printf("[store] ERROR reset nodes offline: %v", err)
	}
}

// ---------- 保留期清理 ----------

// retentionLoop 每小时清理超过保留期的拨测/统计/事件数据（data-retention-days，0 = 永久）
func (s *dataStore) retentionLoop() {
	defer s.stopWait.Done()
	if DATA_RETENTION_DAYS <= 0 {
		return
	}
	run := func() {
		cutoff := time.Now().UTC().AddDate(0, 0, -DATA_RETENTION_DAYS)
		ctx, cancel := dbCtx()
		defer cancel()
		for _, m := range []any{&ProbeResult{}, &NodeEvent{}} {
			if err := s.gdb.WithContext(ctx).Where("created_at < ?", cutoff).Delete(m).Error; err != nil {
				log.Printf("[store] ERROR retention cleanup %T: %v", m, err)
			}
		}
		if err := s.gdb.WithContext(ctx).Where("minute < ?", cutoff.Unix()/60).Delete(&RequestStat{}).Error; err != nil {
			log.Printf("[store] ERROR retention cleanup request_stats: %v", err)
		}
	}
	run()
	for {
		select {
		case <-time.After(time.Hour):
			run()
		case <-s.stopCh:
			return
		}
	}
}

// ---------- 工具 ----------

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

var reqSeq int64
var reqSeqMu sync.Mutex

// genRequestID 进程内唯一请求 ID（WS probe 的 requestId 用）
func genRequestID() string {
	reqSeqMu.Lock()
	reqSeq++
	seq := reqSeq
	reqSeqMu.Unlock()
	return strings.TrimSpace(time.Now().Format("20060102150405")) + "-" + itoa(seq)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

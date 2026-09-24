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
// 明细覆盖：连通性/证书类 detail、ssl，DNS 查询类 dns，直连类 tcping/speed，
// 归属地类 location。
// dnssec 属功能性校验、whois/asn 属查询类，都不逐条进明细，只进统计聚合。
func isProbeType(apiType string) bool {
	switch apiType {
	case "tcping", "speed", "detail", "ssl", "dns", "location":
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
// recordNodeOnline WS 注册：置在线并追加 online 事件。
// version / capabilities 为空时不覆盖库里的旧值（老版本节点不上报，避免把已知信息清空）。
func recordNodeOnline(nodeID, remoteAddr, version string, capabilities []string) {
	if db == nil {
		return
	}
	now := time.Now().UTC()
	// 本次注册是不是"掉线后回归"：必须**在 upsert 把 online 置 true 之前**读快照。
	// 判定 = 库里已有该节点 && 此前为离线 && 上次断连不是 OTA 计划内重启（豁免标记一次性消费）。
	// 这样冷启动首次上线（库里没记录）、从未探活成功的节点都不会误报"恢复上线"。
	prev, prevOK := nodeSnapshot(nodeID)
	otaExempt := takeOtaExempt(nodeID)
	// 中心重启时被批量置离线的节点：它们的"上线"不算"掉线后回归"——重启本身没产生过掉线通知，
	// 不豁免的话每次重启中心都会给群里补一批没有前置掉线的孤立上线通知（见 markAllNodesOffline）。
	startupExempt := takeStartupExempt(nodeID)
	// 维护窗口里被静默掉的掉线：其恢复也要一并静默，否则同样会出现孤立的上线通知（见 maintenance.go）
	maintExempt := takeMaintSuppressed(nodeID)
	// 宽限窗口内回来的：那次掉线通报已登记但被取消，对应的"恢复上线"也必须一并抑制，
	// 否则群里会出现一条没有对应掉线的孤立上线通知（返回值 = 是否确实撤销了一次待确认）。
	graceSkipped := cancelNodeDownPending(nodeID)
	recovered := prevOK && !prev.Online && !otaExempt && !graceSkipped && !startupExempt && !maintExempt
	// 节点重新在线：清掉 down 通知锁存，下次再掉线可再次告警
	clearNodeDownFired(nodeID)
	ctx, cancel := dbCtx()
	defer cancel()
	updates := map[string]any{
		"online":       true,
		"remote_addr":  remoteAddr,
		"last_seen_at": now,
	}
	// 版本可能为空（老版本节点不上报）：只在有值时更新，避免把已知版本清空
	if v := strings.TrimSpace(version); v != "" {
		updates["version"] = v
	}
	// 能力清单同理：老版本节点不发则该列为空（= "未知"），新版本节点每次注册都会带上
	caps := joinCapabilities(capabilities)
	if caps != "" {
		updates["capabilities"] = caps
	}
	// 节点名以控制台的节点定义（node_defs.label）为准：注册报文里不带名字，这里主动从池里取，
	// 否则新节点首次注册时 nodes 表会留空名（展示层 monitorNode.display() / 大盘早就 label 优先，
	// 把落库这一侧也对齐，节点列表与事件导出才不会出现空名或改名前的旧名）。
	// 池里没配名字时不动原值（可能已有历史同步结果）。
	label := poolLabelFor(nodeID)
	if label != "" {
		updates["label"] = label
	}
	err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.Assignments(updates),
	}).Create(&Node{NodeID: nodeID, Label: label, Online: true, RemoteAddr: remoteAddr, Version: strings.TrimSpace(version), Capabilities: caps, FirstSeenAt: now, LastSeenAt: now}).Error
	if err != nil {
		log.Printf("[store] ERROR upsert node online: %v", err)
		return
	}
	if err := db.WithContext(ctx).Create(&NodeEvent{NodeID: nodeID, Event: "online", Reason: "registered", CreatedAt: now}).Error; err != nil {
		log.Printf("[store] ERROR create node event: %v", err)
	}
	// 写库成功后才发"恢复上线"通知（发给所有启用 admin，与掉线告警同一套投递）
	if recovered {
		log.Printf("[store] node %s back online after outage, notify admins", nodeID)
		go notifyNodeUp(monitorNode{id: nodeID, label: prev.Label, ws: true}, "WS 版", "重新注册")
	}
}

// nodeSnapshot 读节点在 nodes 表的当前快照（upsert 之前调用才有意义）。
// ok=false 表示库里还没有该节点 —— 即从未上线过，不能当"掉线后回归"处理。
func nodeSnapshot(nodeID string) (Node, bool) {
	if db == nil {
		return Node{}, false
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var n Node
	if err := db.WithContext(ctx).Where("node_id = ?", nodeID).Limit(1).Find(&n).Error; err != nil {
		return Node{}, false
	}
	return n, n.ID != 0
}

// recordNodeOffline WS 断开/剔除：置离线并追加 offline 事件
//
// 这是 WS 节点掉线的**单一汇聚点**（断连/空闲剔除都走这里，节点状态页的红即由此置位）。
// 在真实 online→offline 翻转时**登记**一次掉线通报（发给所有启用 admin，见 notifyNodeDown），
// 但要过 nodeDownGraceDelay 宽限窗口、到点确认仍离线才真发（见 scheduleNodeDownAlert）。
// 不再依赖 nodeHealth 看门狗对配置池节点的独立判定；库内快照与事件当场写，**界面即时、告警去抖**。
func recordNodeOffline(nodeID, reason string) {
	if db == nil {
		return
	}
	// 先读快照：下面的 UPDATE 会把 online 置 false，之后就判不出"此前是否在线"了；
	// 顺带取 label 供告警文案使用（省掉第二次查询）。
	prev, prevOK := nodeSnapshot(nodeID)
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
	if prevOK && prev.Online && markNodeDownFired(nodeID) {
		// OTA 在途豁免：计划内重启伴随的真实断连不 page（offline 事件已照记，可追溯）；
		// 同时打上豁免标记，使这次计划内的复联也不再单独报一条"恢复上线"——否则群里会出现
		// 一条没有对应掉线的孤立上线通知（见 recordNodeOnline）。
		// 任务终结仍失败且节点未恢复时由 otaNotifyIfStillDown 补报并撤销豁免（见 ota.go）
		if otaTaskInFlight(nodeID) {
			markOtaExempt(nodeID)
			log.Printf("[store] node %s offline while OTA task in-flight, down/up alert suppressed", nodeID)
			return
		}
		// 宽限窗口：这里只登记"待确认"，不立刻通报——秒级闪断不该惊动管理员（见 scheduleNodeDownAlert）
		scheduleNodeDownAlert(nodeID, prev.Label, reason)
	}
}

// ==================== 掉线通报的宽限窗口 ====================
//
// 短闪断（节点秒级重连、进程重启、反代/隧道抖动）不该惊动管理员，所以真实 online→offline 翻转后
// **不立刻通报**：推迟 nodeDownGraceDelay，到点重新读库确认该节点**仍然离线**才发掉线通知；
// 期间节点重新注册，recordNodeOnline 会把这次待确认取消掉（连带抑制对应的"恢复上线"）。
//
// 去抖只作用于**通知**：库内 online 快照与 offline 事件都在 recordNodeOffline 里当场写，
// 所以节点状态页 / 事件历史是即时的，只有告警晚 nodeDownGraceDelay 才到。
//
// 待确认任务按 nodeID 唯一（同一节点不会同时有两种掉线），再次翻转会替换掉上一个，不会叠加出多条；
// 到点用**库内快照**而非内存标记判定，避免与重连、节点被删等并发写打架。
// 纯内存、与 down 锁存同生命周期：进程重启即丢弃待确认任务，最坏情况是少一条掉线通报
//（该节点下次掉线会重新登记），不会产生重复或错配。
const nodeDownGraceDelay = 20 * time.Second

var (
	nodeDownPendingMu sync.Mutex
	nodeDownPending   = map[string]*time.Timer{}
)

// scheduleNodeDownAlert 登记一次"延迟确认"的掉线通报（幂等：同节点已有待确认任务时先取消）。
func scheduleNodeDownAlert(nodeID, label, reason string) {
	nodeDownPendingMu.Lock()
	defer nodeDownPendingMu.Unlock()
	if old, ok := nodeDownPending[nodeID]; ok {
		old.Stop()
	}
	nodeDownPending[nodeID] = time.AfterFunc(nodeDownGraceDelay, func() {
		nodeDownPendingMu.Lock()
		delete(nodeDownPending, nodeID)
		nodeDownPendingMu.Unlock()
		confirmNodeDownAlert(nodeID, label, reason)
	})
	log.Printf("[store] node %s offline, down alert held %s to skip transient blips", nodeID, nodeDownGraceDelay)
}

// cancelNodeDownPending 撤销待确认的掉线通报（节点在宽限窗口内重新注册时调用），
// 返回"是否确实撤销了一次"。返回 true 说明这次故障的掉线通知被宽限窗口吃掉了，
// 调用方（recordNodeOnline）必须据此抑制对应的"恢复上线"，否则会出现孤立的上线通知。
// 没登记时是 no-op —— 例如节点离线已超过宽限、掉线通知早已发出，此时复联就该正常报上线。
func cancelNodeDownPending(nodeID string) bool {
	nodeDownPendingMu.Lock()
	defer nodeDownPendingMu.Unlock()
	t, ok := nodeDownPending[nodeID]
	if ok {
		t.Stop()
		delete(nodeDownPending, nodeID)
	}
	return ok
}

// confirmNodeDownAlert 宽限窗口到点：只有节点**仍然离线**才真发掉线通知。
func confirmNodeDownAlert(nodeID, label, reason string) {
	cur, ok := nodeSnapshot(nodeID)
	if !ok || cur.Online {
		log.Printf("[store] node %s back online within %s grace window, down alert skipped", nodeID, nodeDownGraceDelay)
		return
	}
	// 宽限期间可能又起了 OTA 任务：仍按"计划内重启"豁免，并打标记使随后的复联也不报上线（见 ota.go）
	if otaTaskInFlight(nodeID) {
		markOtaExempt(nodeID)
		log.Printf("[store] node %s still offline but OTA task in-flight, down/up alert suppressed", nodeID)
		return
	}
	if v := strings.TrimSpace(cur.Label); v != "" {
		label = v // 窗口内 label 可能被改过，以最新为准
	}
	log.Printf("[store] node %s still offline after %s grace, notify admins", nodeID, nodeDownGraceDelay)
	go notifyNodeDown(monitorNode{id: nodeID, label: label, ws: true}, "WS 版", reason, 1)
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
// 之后由真实注册/心跳重新置位，避免重启后出现僵尸在线记录。
//
// 顺带打上"启动豁免"：被这里置离线的节点并不是真的掉过线（没有产生过掉线通知），
// 它们随后的重连就不该发"恢复上线"——否则每重启一次中心，群里都会多出一批
// 没有对应掉线的孤立上线通知（见 recordNodeOnline 的 startupExempt）。
func markAllNodesOffline() {
	if db == nil {
		return
	}
	ctx, cancel := dbCtx()
	defer cancel()
	// 先取出当前的在线节点 id：只有它们会因为重启由在线翻成离线
	var onlineIDs []string
	if err := db.WithContext(ctx).Model(&Node{}).Where("online = ?", true).Pluck("node_id", &onlineIDs).Error; err != nil {
		log.Printf("[store] ERROR list online nodes at startup: %v", err)
	}
	if err := db.WithContext(ctx).Model(&Node{}).Where("online = ?", true).Update("online", false).Error; err != nil {
		log.Printf("[store] ERROR reset nodes offline: %v", err)
	}
	for _, id := range onlineIDs {
		markStartupExempt(id)
	}
	if len(onlineIDs) > 0 {
		log.Printf("[store] startup: %d online node snapshots reset to offline; their reconnect won't emit node_up", len(onlineIDs))
	}
}

// ==================== 中心重启导致的"上线"豁免 ====================
//
// 与 OTA 豁免（ota.go）同构、同生命周期：纯内存，进程重启即清空。
// 区别在于语义——OTA 豁免是"节点自己计划内重启"，启动豁免是"中心自己重启"，
// 两者的共同点是：这次断开都不是节点的故障，所以掉线没报、上线也不该报。

var (
	startupExemptMu sync.Mutex
	startupExemptS  = map[string]bool{}
)

// markStartupExempt 记下"这次离线是中心重启造成的"，供节点重连时抑制上线通知
func markStartupExempt(nodeID string) {
	startupExemptMu.Lock()
	startupExemptS[nodeID] = true
	startupExemptMu.Unlock()
}

// takeStartupExempt 取出并清除标记（一次性消费），返回此前是否存在
func takeStartupExempt(nodeID string) bool {
	startupExemptMu.Lock()
	defer startupExemptMu.Unlock()
	found := startupExemptS[nodeID]
	delete(startupExemptS, nodeID)
	return found
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
		// 已结束的一次性维护窗口（每日重复窗口保留）：留着只会让列表越长越乱
		cleanupMaintenanceWindows()
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

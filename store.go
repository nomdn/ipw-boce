package main

import (
	"log"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ==================== 数据源标记 ====================

const (
	sourceWS   = "ws"
	sourceHTTP = "http"
)

// isProbeType 拨测类 API（结果需要落 probe_results 表）；whois/dns 等数据类接口只进统计
func isProbeType(apiType string) bool {
	switch apiType {
	case "tcping", "udping", "speed":
		return true
	}
	return false
}

// ==================== 异步持久化器 ====================

// probeResult 落库前struct（与 model ProbeResult 同构，body/error 由调用方补充）
type probeResult struct {
	RequestID string
	NodeID    string
	APIType   string
	Raw       string
	Query     string
	Status    int
	LatencyMs int64
	Error     string
	Source    string
	Body      string
}

// dataStore 汇聚所有异步持久化通道：拨测结果批写、统计落库、节点状态、保留期清理。
// 写通道满时丢弃并告警计数——持久化永远不阻塞请求路径。
type dataStore struct {
	gdb     *storeDB
	probeCh chan probeResult

	probeMu    sync.Mutex
	probeBatch []ProbeResult

	dropMu       sync.Mutex
	probeDropped int64

	stats    *statsCollector
	stopCh   chan struct{}
	stopWait sync.WaitGroup
}

func newDataStore(s *storeDB) *dataStore {
	flush := STATS_FLUSH_SECONDS
	if flush <= 0 {
		flush = 30
	}
	return &dataStore{
		gdb:     s,
		probeCh: make(chan probeResult, 2048),
		stopCh:  make(chan struct{}),
		stats:   newStatsCollector(flush),
	}
}

// Start 启动后台写入与清理协程
func (s *dataStore) Start() {
	s.stopWait.Add(2)
	go s.probeWriterLoop()
	go s.retentionLoop()
	s.stats.Start(s.gdb.DB, s.stopCh, &s.stopWait)
}

// Stop 停止后台协程并冲刷残余数据
func (s *dataStore) Stop() {
	close(s.stopCh)
	s.stopWait.Wait()
	s.flushProbeBatch()
}

// ---------- 拨测结果 ----------

// recordProbeResult 记录一次拨测：err 非 nil 或 body 提供时补充结果字段，随后入异步写通道
func recordProbeResult(pr probeResult, err error, body []byte) {
	if store == nil {
		return
	}
	if err != nil {
		pr.Status = 0
		pr.Error = truncateStr(err.Error(), 512)
	} else {
		pr.Body = truncateStr(string(body), 64*1024)
	}
	if pr.RequestID == "" {
		pr.RequestID = genRequestID()
	}
	select {
	case store.probeCh <- pr:
	default:
		store.dropMu.Lock()
		store.probeDropped++
		n := store.probeDropped
		store.dropMu.Unlock()
		if n%100 == 1 {
			log.Printf("[store] WARN probe write queue full, dropped %d records so far", n)
		}
	}
	// 上报客户端同步出队一份（多入口冗余汇聚；收集中心因 report-url 为空不会走这里）
	if reporter != nil {
		reporter.outProbe(pr)
	}
}

// probeWriterLoop 批量写拨测结果：攒满 200 条或每 2 秒冲刷一次
func (s *dataStore) probeWriterLoop() {
	defer s.stopWait.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case pr := <-s.probeCh:
			s.probeMu.Lock()
			s.probeBatch = append(s.probeBatch, ProbeResult{
				RequestID: pr.RequestID, NodeID: pr.NodeID, APIType: pr.APIType,
				Raw: pr.Raw, Query: pr.Query, Status: pr.Status, LatencyMs: pr.LatencyMs,
				Error: pr.Error, Source: pr.Source, Body: pr.Body, CreatedAt: time.Now().UTC(),
			})
			full := len(s.probeBatch) >= 200
			s.probeMu.Unlock()
			if full {
				s.flushProbeBatch()
			}
		case <-ticker.C:
			s.flushProbeBatch()
		case <-s.stopCh:
			// 排干通道中残余记录
			for {
				select {
				case pr := <-s.probeCh:
					s.probeMu.Lock()
					s.probeBatch = append(s.probeBatch, ProbeResult{
						RequestID: pr.RequestID, NodeID: pr.NodeID, APIType: pr.APIType,
						Raw: pr.Raw, Query: pr.Query, Status: pr.Status, LatencyMs: pr.LatencyMs,
						Error: pr.Error, Source: pr.Source, Body: pr.Body, CreatedAt: time.Now().UTC(),
					})
					s.probeMu.Unlock()
				default:
					s.flushProbeBatch()
					return
				}
			}
		}
	}
}

func (s *dataStore) flushProbeBatch() {
	s.probeMu.Lock()
	batch := s.probeBatch
	s.probeBatch = nil
	s.probeMu.Unlock()
	if len(batch) == 0 {
		return
	}
	ctx, cancel := dbCtx()
	defer cancel()
	if err := s.gdb.WithContext(ctx).CreateInBatches(batch, 200).Error; err != nil {
		log.Printf("[store] ERROR persist probe results: %v", err)
	}
}

// ---------- 统计 ----------

// recordAPIRequest 记录一次 API 转发（所有 apiType）：内存计数器累计，定时批量落库 request_stats
func recordAPIRequest(nodeID, apiType string, status int, latency time.Duration, isErr bool, source string) {
	if store != nil {
		store.stats.Record(nodeID, apiType, status, latency, isErr)
	}
}

// statKey 统计维度：节点 × API 类型
type statKey struct{ Node, API string }

type statVal struct {
	total, errs, latSum, latMax int64
}

// statsCollector 内存计数器（per-flush 周期），周期结束时以分钟桶 upsert 进 request_stats
type statsCollector struct {
	mu    sync.Mutex
	cur   map[statKey]*statVal
	flush time.Duration
}

func newStatsCollector(flushSec int) *statsCollector {
	return &statsCollector{cur: make(map[statKey]*statVal), flush: time.Duration(flushSec) * time.Second}
}

func (sc *statsCollector) Record(nodeID, apiType string, status int, latency time.Duration, isErr bool) {
	k := statKey{Node: nodeID, API: apiType}
	ms := latency.Milliseconds()
	sc.mu.Lock()
	v, ok := sc.cur[k]
	if !ok {
		v = &statVal{}
		sc.cur[k] = v
	}
	v.total++
	if isErr || status == 0 || status >= 500 {
		v.errs++
	}
	v.latSum += ms
	if ms > v.latMax {
		v.latMax = ms
	}
	sc.mu.Unlock()
}

// Start 启动周期落库协程
func (sc *statsCollector) Start(g *gorm.DB, stopCh chan struct{}, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(sc.flush)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sc.Flush(g)
			case <-stopCh:
				sc.Flush(g)
				return
			}
		}
	}()
}

// Flush 将当前计数器快照 upsert 进 request_stats（分钟桶取落库时刻）；
// 若本实例开启了上报（report-url），同一份快照同时交给上报客户端推给收集中心
func (sc *statsCollector) Flush(g *gorm.DB) {
	sc.mu.Lock()
	snap := sc.cur
	sc.cur = make(map[statKey]*statVal, len(snap))
	sc.mu.Unlock()
	if len(snap) == 0 {
		return
	}
	minute := time.Now().UTC().Unix() / 60
	for k, v := range snap {
		if err := upsertStatDelta(g, minute, k.Node, k.API, *v); err != nil {
			log.Printf("[store] ERROR upsert request stat: %v", err)
		}
	}
	// 本实例"自己统计上报"：把观测增量交给收集中心（多入口冗余汇聚）
	if reporter != nil {
		reporter.outStats(snap, minute)
	}
}

// ---------- 节点在线状态 ----------

// recordNodeOnline WS 注册成功：upsert 节点快照并追加 online 事件
func recordNodeOnline(nodeID, remoteAddr string) {
	if db == nil {
		return
	}
	now := time.Now().UTC()
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
func recordNodeOffline(nodeID, reason string) {
	if db == nil {
		return
	}
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

// genRequestID 进程内唯一请求 ID（与原 ws.go 的 genSeq 同思路）
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

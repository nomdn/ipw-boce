package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// ==================== 节点告警的批量汇总（风暴抑制） ====================
//
// 问题：中心侧网络抖动、上游运营商割接、机房整体重启这类事件会让**多个节点在同一时刻掉线**，
// 逐条推送会把管理员的通知渠道刷屏（N 个节点 = N 条邮件 + N 条 webhook），真正的单点故障反而被淹没。
//
// 做法：掉线/恢复通知不再立即投递，而是先进"批次队列"，等一个很短的时间窗（alert.batch-seconds，
// 缺省 10 秒）再统一发出：
//   - 窗口内只有 1 个节点 → 与原来完全一致（单条通知，文案不变）
//   - 窗口内有 ≥2 个节点 → 合并成一条汇总通知（列清是哪些节点）
//
// 为什么放在通知层而不是采集层：采集层要保证"事实完整"（事件、快照、可用率都不受影响），
// 汇总只影响"怎么告诉人"。
//
// 与既有去抖机制的关系（三层各管一段，互不重叠）：
//   1. 20s 宽限窗口（store.go nodeDownGraceDelay）—— 滤掉秒级闪断：窗口内恢复就整条不报
//   2. 维护窗口（maintenance.go）—— 按计划静默：窗口内的掉线/恢复都不报
//   3. 批次汇总（本文件）—— 同批多节点合并成一条
// 三层都是纯内存、与 down 锁存同生命周期；进程重启即丢弃待发批次，最坏情况是少发一条通知。

// nodeAlertItem 一条待投递的节点告警
type nodeAlertItem struct {
	n      monitorNode
	mode   string // "WS 版" / "HTTP 版"
	reason string
	streak int // 仅掉线有意义（连续判离线次数）
}

// nodeAlertKind 告警类别（站内信 kind + webhook event）
type nodeAlertKind struct {
	kind  string
	event string
}

var (
	nodeAlertDown = nodeAlertKind{kind: noticeKindNode, event: "node_down"}
	nodeAlertUp   = nodeAlertKind{kind: noticeKindNodeUp, event: "node_up"}
)

var (
	nodeAlertBatchMu      sync.Mutex
	nodeAlertBatchPending = map[string][]nodeAlertItem{}
	nodeAlertBatchTimers  = map[string]*time.Timer{}
)

// defaultAlertBatchSeconds 批次汇总窗口缺省值（秒）；setting.json / env 的 alert.batchSeconds 可覆盖，
// 显式写 0 = 关闭汇总（逐条立即发）。main.go readConfig 用它补缺省。
const defaultAlertBatchSeconds = 10

// alertBatchWindow 批次汇总的时间窗；<=0 表示关闭汇总（逐条立即发）
func alertBatchWindow() time.Duration {
	s := ALERT_CONF.BatchSeconds
	if s <= 0 {
		return 0
	}
	if s > 300 {
		s = 300
	}
	return time.Duration(s) * time.Second
}

// enqueueNodeAlert 把一条告警送入批次队列；关闭汇总时直接投递。
func enqueueNodeAlert(k nodeAlertKind, it nodeAlertItem) {
	win := alertBatchWindow()
	if win <= 0 {
		dispatchNodeAlerts(k, []nodeAlertItem{it})
		return
	}
	nodeAlertBatchMu.Lock()
	nodeAlertBatchPending[k.event] = append(nodeAlertBatchPending[k.event], it)
	if _, ok := nodeAlertBatchTimers[k.event]; !ok {
		nodeAlertBatchTimers[k.event] = time.AfterFunc(win, func() { flushNodeAlerts(k) })
	}
	nodeAlertBatchMu.Unlock()
}

// flushNodeAlerts 批次窗口到点：取出该类别全部待发项并投递（单条 / 汇总）。
func flushNodeAlerts(k nodeAlertKind) {
	nodeAlertBatchMu.Lock()
	items := nodeAlertBatchPending[k.event]
	delete(nodeAlertBatchPending, k.event)
	delete(nodeAlertBatchTimers, k.event)
	nodeAlertBatchMu.Unlock()
	if len(items) == 0 {
		return
	}
	dispatchNodeAlerts(k, items)
}

// pendingNodeAlerts 当前待发条数（排障用）
func pendingNodeAlerts() int {
	nodeAlertBatchMu.Lock()
	defer nodeAlertBatchMu.Unlock()
	n := 0
	for _, v := range nodeAlertBatchPending {
		n += len(v)
	}
	return n
}

// dispatchNodeAlerts 实际投递：1 条走原有单条文案，多条走汇总文案。
func dispatchNodeAlerts(k nodeAlertKind, items []nodeAlertItem) {
	if len(items) == 1 {
		it := &items[0]
		if k.event == "node_up" {
			notifyAdmins(it.n, k.kind, k.event,
				"[IPW-BOCE] 服务节点恢复："+it.n.display(), buildNodeUpBody(it.n, it.mode, it.reason))
			return
		}
		notifyAdmins(it.n, k.kind, k.event,
			"[IPW-BOCE] 服务节点掉线："+it.n.display(), buildNodeDownBody(it.n, it.mode, it.reason, it.streak))
		return
	}
	notifyAdminsBatch(k, items)
}

// notifyAdminsBatch 汇总通知：一条消息列清本批次的全部节点。
// generic webhook 报文的 nodeId 字段写成本批节点 id 的逗号串（单条时仍是单个 id，向后兼容）。
func notifyAdminsBatch(k nodeAlertKind, items []nodeAlertItem) {
	ids := make([]string, 0, len(items))
	var b strings.Builder
	if k.event == "node_up" {
		fmt.Fprintf(&b, "拨测服务节点批量恢复上线（%d 个）。\n\n", len(items))
	} else {
		fmt.Fprintf(&b, "拨测服务节点批量掉线（%d 个），请及时处理。\n\n", len(items))
	}
	for i := range items {
		it := &items[i]
		ids = append(ids, it.n.id)
		d := it.n.display()
		if d == it.n.id {
			fmt.Fprintf(&b, "- %s\t%s\t%s\n", it.n.id, it.mode, it.reason)
		} else {
			fmt.Fprintf(&b, "- %s (%s)\t%s\t%s\n", d, it.n.id, it.mode, it.reason)
		}
	}
	fmt.Fprintf(&b, "\n时间: %s\n\n", time.Now().UTC().Format(time.RFC3339))
	verb := "掉线"
	if k.event == "node_up" {
		verb = "恢复"
		b.WriteString("—— ipw-boce 自动通知")
	} else {
		b.WriteString("—— ipw-boce 自动告警")
	}
	subject := fmt.Sprintf("[IPW-BOCE] 服务节点批量%s：%d 个节点", verb, len(items))
	log.Printf("[node] batch %s -> %d nodes (%s)", k.event, len(items), strings.Join(ids, ","))
	notifyAdminsForNodeIDs(ids, k.kind, k.event, subject, b.String())
}

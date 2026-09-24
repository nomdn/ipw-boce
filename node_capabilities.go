package main

import (
	"fmt"
	"strings"
	"time"
)

// ==================== 节点能力清单（capabilities） ====================
//
// 背景：中间件向节点下发**管理指令**（运行时配置 config）走的是 WS 消息，
// 而老版本节点的消息循环没有这个分支，收到后会**静默忽略**——既不执行也不回错。
// 于是中间件只能一直等到超时（15s），最后给出一句含糊的 "ws config timeout"，
// 管理员看着"节点明明在线、指令也发出去了"完全无从下手。
//
// 解法：节点在 register 报文里上报自己支持的能力清单（probe / report / config，
// 同时也在 HTTP `GET /info` 里返回，供纯 HTTP 节点探活后取回）。
//
//	清单里明确没有该能力  → 立刻拒绝，不必空等超时（真·不支持，如精简构建）
//	清单为空（未上报）    → 视为"未知"：老版本节点不发这个字段，仍尝试下发，
//	                       但失败时给出"程序版本过旧"的定向提示
//
// 三种状态必须分清，不能把"未知"当成"不支持"，否则升级过的老节点会被永久挡在门外。

// capabilityConfig 中间件需要探测的管理能力标识（与节点侧 nodeCapabilities 对齐）
const capabilityConfig = "config"

// capabilityOTA OTA 升级能力标识（节点侧 ota.go 实现，见 registerOTARoutes）
const capabilityOTA = "ota"

// joinCapabilities 把节点上报的能力数组规整成入库用的逗号分隔串（去空、去重、保持顺序）
func joinCapabilities(caps []string) string {
	seen := make(map[string]bool, len(caps))
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return strings.Join(out, ",")
}

// splitCapabilities 把库里的逗号分隔能力串拆成数组（空串 → 空数组）
func splitCapabilities(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// nodeReportedInfo 读节点最近一次上报的版本号与能力清单。
// known=false 表示库里没有该节点的能力记录（老版本节点不上报），
// 此时**不能**据此判定"不支持"，只能给出"程序可能过旧"的提示。
func nodeReportedInfo(nodeID string) (version string, caps []string, known bool) {
	if db == nil {
		return "", nil, false
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var n Node
	if err := db.WithContext(ctx).Where("node_id = ?", nodeID).Limit(1).Find(&n).Error; err != nil {
		return "", nil, false
	}
	if n.ID == 0 {
		return "", nil, false
	}
	caps = splitCapabilities(n.Capabilities)
	return strings.TrimSpace(n.Version), caps, len(caps) > 0
}

// nodeCapabilityGate 下发管理指令前的"能力门"，一次读库给出两个结论：
//   - denyErr != nil → 该节点上报的能力清单明确不含 capability，**不要发起指令**，直接回这个错误
//   - known         → 节点是否上报过能力清单；false 表示老版本节点（未知，不能据此拦截）
//
// 三态语义是这里的关键：只有"明确上报且不含"才拒绝，"未上报"必须放行去尝试，
// 否则升级过的老节点会被永久挡在门外。
func nodeCapabilityGate(nodeID, capability, action string) (denyErr error, known bool) {
	_, caps, known := nodeReportedInfo(nodeID)
	if !known {
		return nil, false
	}
	for _, c := range caps {
		if strings.EqualFold(c, capability) {
			return nil, true
		}
	}
	return capabilityUnsupportedError(nodeID, capability, action, caps), true
}

// capabilityUnsupportedError 能力清单明确不含该能力时的拒绝错误（不发起任何指令）
func capabilityUnsupportedError(nodeID, capability, action string, caps []string) error {
	return fmt.Errorf("节点 %s 不支持 %s：其上报的能力清单 [%s] 不含 %s。%s",
		nodeID, action, strings.Join(caps, ","), capability,
		"请换用支持的通道，或把该节点升级到包含此能力的版本")
}

// wsAliveProbeWait 管理指令超时后主动探活节点的等待时长（见 wsServer.ProbeAlive）。
// 探活本身很快（节点活着时几十毫秒即回），只有在已白等一轮超时之后才使用
const wsAliveProbeWait = 3 * time.Second

// wsTimeoutDiagnosis 把"WS 指令超时"细化成一句可执行的结论。
// err 必须是已判定为超时（*wsTimeoutError）的错误。
func wsTimeoutDiagnosis(nodeID string, err error) error {
	if wsSrv != nil && wsSrv.ProbeAlive(nodeID, wsAliveProbeWait) {
		// 连接活着 ⇒ 节点收到了但静默忽略：老版本节点的消息循环没有该指令分支
		return fmt.Errorf("WS 指令无应答（%v）%s", err, legacyNodeHint(nodeID))
	}
	// 连接失活 ⇒ 指令根本没送达（对端进程已没但 TCP 未有感知），等空闲剔除自行收敛
	return fmt.Errorf("WS 指令无应答（%v），且该连接已失活（探活无响应），"+
		"本次指令未送达节点，该连接稍后会被自动剔除并重连", err)
}

// legacyNodeHint 节点未上报能力清单时的排障提示（老版本节点的典型特征），
// 追加在 WS 超时错误之后，把"含糊的超时"变成"可执行的结论"。
func legacyNodeHint(nodeID string) string {
	version, _, known := nodeReportedInfo(nodeID)
	if known {
		return ""
	}
	if version == "" {
		return "；该节点未上报能力清单与版本号，基本可判定其程序版本过旧（不认识该指令，收到后静默忽略）"
	}
	return "；该节点未上报能力清单（程序版本较旧），很可能不认识该指令"
}

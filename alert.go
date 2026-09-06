package main

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// ==================== SMTP 掉线告警 ====================
//
// 当某个定时拨测(SLA)任务连续 N 轮判定"服务掉线"时，通知该任务的"所有者"（创建者）。
// 语义（与用户确认）：
//   - "掉线" = 本轮没有任何节点判定 up，且至少有 1 个有效(可判定)样本 —— 即整组节点都探不到服务。
//     无效样本(如无 body、无法判定)不计入 up/down，只有全是无效或没有样本时才不算掉线(避免误报)。
//   - "连续 down 阈值" = alert.down-threshold（缺省 3）。连续达到阈值那一轮触发一次。
//   - "每事件一封"：已触发过的告警在该次故障持续期间不重复；一旦出现 up 轮即复位，
//     下次新的故障再从 1 累计并可再次告警。
//   - 送达对象 = 任务 owner_id 对应用户（创建者）：
//       * 所有者存在且启用 → 邮件与站内信**同时**送达（非二选一回退）：
//           - 邮件：所有者有邮箱且 SMTP 可用才发；缺邮箱/SMTP 不可用/发送失败仅记日志，不影响站内信。
//           - 站内信：所有者启用即可落一条 AppNotice（控制台铃铛可见），与邮件并存。
//       * 任务无归属(ownerId=0，静态 token 建的旧任务)或所有者已不存在 → 不通知。
//   - alert.to 全局收件人字段已不再作为告警兜底（按需即发所有者；无归属不发）。
//
// 配置：setting.json 的 "smtp"（发信服务器）与 "alert"（告警策略），见 main.go smtpConfig/alertConfig。
// 仅当 smtp.host/user/from 与 alert.enabled 齐备才启用邮件路径；站内信不依赖 SMTP 配置。

// 站内信类型（AppNotice.Kind）
const (
	noticeKindSLA  = "sla_down"  // SLA 定时拨测任务掉线 → 发给任务 owner
	noticeKindNode = "node_down" // 拨测服务节点掉线 → 发给所有启用 admin
)

// smtpReady SMTP 邮件路径是否可用（host/user/from 齐备且 alert.enabled）。
// 注意：与旧版不同，这里不再要求收件人列表——收件人是按任务的 owner 动态解析的。
func smtpReady() bool {
	c := SMTP_CONF
	return ALERT_CONF.Enabled &&
		strings.TrimSpace(c.Host) != "" &&
		strings.TrimSpace(c.User) != ""
}

// mailSender 实际发信函数（可被测试替换；生产 = sendMail）
var mailSender = sendMail

// taskAlertState 掉线检测的在内存状态（任务级串行，见 probe_task.go runTaskSamples）
var (
	alertMu     sync.Mutex
	alertStreak = map[uint]int{}  // taskID → 连续 down 轮数
	alertFired  = map[uint]bool{} // taskID → 是否已就本次故障触发过告警
)

// resetTaskAlerts 任务被删除时清掉其掉线告警内存态（连续计数 + 已触发标记），
// 防止内存里残留的 map 条目占用；下次同 id 复用任务也不会误触发旧告警。
func resetTaskAlerts(id uint) {
	alertMu.Lock()
	delete(alertStreak, id)
	delete(alertFired, id)
	alertMu.Unlock()
}

// judgeRoundDown 判定本轮是否"整组 down"。rows 为刚落库(或待落库)的一轮 sched 样本。
// 返回 (roundDown, hasValid)。roundDown=true 仅当有有效样本且无任何节点 up。
func judgeRoundDown(t *ProbeTask, rows []ProbeResult) (bool, bool) {
	upCnt, validCnt := 0, 0
	for i := range rows {
		r := &rows[i]
		if r.Error != "" && r.Status == 0 {
			// 链路请求本身失败：视为该节点探不到，计入有效 down（除非下游会落 invalid）
			continue
		}
		ev := evalSample(t, r.Status, r.Body, r.LatencyMs)
		if ev.invalid {
			continue // 无法判定，跳过
		}
		validCnt++
		if ev.up {
			upCnt++
		}
	}
	if validCnt == 0 {
		return false, false
	}
	return upCnt == 0, true
}

// noteRoundOutcome 每轮落库后由 runTaskSamples 调用：
//   - down 轮 → 累计；达到阈值当轮触发一次（此前同次故障未触发过才触发）
//   - up 轮 → 复位连续计数与已触发标记
func noteRoundOutcome(t *ProbeTask, rows []ProbeResult) {
	// 无归属(ownerId=0)的任务不参与掉线告警（无收件对象），仍累计计数以防日后被挂归属后误触发。
	if t.OwnerID == 0 {
		return
	}
	roundDown, _ := judgeRoundDown(t, rows)
	alertMu.Lock()
	defer alertMu.Unlock()
	id := t.ID
	if !roundDown {
		// 恢复：清计数与已触发，下次新故障可再告警
		if alertStreak[id] != 0 || alertFired[id] {
			alertStreak[id] = 0
			delete(alertFired, id)
			log.Printf("[alert] task#%d %s recovered, reset down streak", id, t.Name)
		}
		return
	}
	alertStreak[id]++
	streak := alertStreak[id]
	thr := ALERT_CONF.DownThreshold
	if thr <= 0 {
		thr = 3
	}
	if streak >= thr && !alertFired[id] {
		alertFired[id] = true
		log.Printf("[alert] task#%d %s DOWN x%d -> notify owner#%d", id, t.Name, streak, t.OwnerID)
		// 站内信同步写；邮件异步发，不阻塞调度
		go deliverDownAlert(t, rows, streak)
	}
}

// deliverDownAlert 按任务所有者送达掉线告警（邮件 + 站内信**同时**发，互不回退）：
//   - 所有者不存在 → 丢弃（不应发生：删用户会级联删任务；防御兜底）
//   - 所有者有邮箱且 SMTP 可用 → 发 SMTP 邮件（失败仅记日志）
//   - 所有者启用（能登录看到站内信）→ 无论是否已发邮件，都落一条站内信
func deliverDownAlert(t *ProbeTask, rows []ProbeResult, streak int) {
	owner, err := userByID(t.OwnerID)
	if err != nil || owner == nil {
		log.Printf("[alert] task#%d %s: owner#%d not found, skip alert", t.ID, t.Name, t.OwnerID)
		return
	}
	subject := fmt.Sprintf("[IPW-BOCE] SLA 掉线告警：%s 连续 %d 轮不可达", t.Name, streak)
	body := buildAlertBody(t, rows, streak)

	// 1) 邮件路径：所有者有邮箱且 SMTP 可用才发；失败/缺邮箱仅记日志，不短路站内信
	if e := strings.TrimSpace(owner.Email); e != "" {
		if !smtpReady() {
			log.Printf("[alert] task#%d %s owner#%d email %s but smtp not ready, email skipped (in-app notice still sent)", t.ID, t.Name, owner.ID, e)
		} else if err := mailSender([]string{e}, subject, body); err != nil {
			log.Printf("[alert] ERROR send down alert task#%d to %s: %v", t.ID, e, err)
		} else {
			log.Printf("[alert] sent down alert task#%d to owner#%d <%s>", t.ID, owner.ID, e)
		}
	} else {
		log.Printf("[alert] task#%d %s owner#%d has no email, email skipped (in-app notice still sent)", t.ID, t.Name, owner.ID)
	}
	// 2) 站内信路径：所有者启用即落（与邮件并存）；停用账号收不到站内信
	if !owner.Enabled {
		log.Printf("[alert] task#%d %s owner#%d disabled, skip in-app notice (email above may still go out)", t.ID, t.Name, owner.ID)
		return
	}
	if err := createNotice(owner.ID, noticeKindSLA, t.ID, subject, body); err != nil {
		log.Printf("[alert] ERROR create in-app notice task#%d owner#%d: %v", t.ID, owner.ID, err)
	} else {
		log.Printf("[alert] in-app notice task#%d -> owner#%d (sent with email)", t.ID, owner.ID)
	}
}

// buildAlertBody 组装告警正文（纯文本，中文可读）
func buildAlertBody(t *ProbeTask, rows []ProbeResult, streak int) string {
	var b strings.Builder
	b.WriteString("拨测服务疑似掉线，请及时处理。\n\n")
	fmt.Fprintf(&b, "任务: %s (#%d)\n", t.Name, t.ID)
	fmt.Fprintf(&b, "类型: %s\n", t.APIType)
	fmt.Fprintf(&b, "目标: %s\n", t.Target)
	fmt.Fprintf(&b, "连续不可达轮数: %d\n", streak)
	if t.Interval > 0 {
		fmt.Fprintf(&b, "拨测间隔: %d 秒\n", t.Interval)
	}
	fmt.Fprintf(&b, "时间: %s\n\n", time.Now().UTC().Format(time.RFC3339))
	b.WriteString("本轮节点明细（链路状态 / 判定）：\n")
	for i := range rows {
		r := &rows[i]
		ev := evalSample(t, r.Status, r.Body, r.LatencyMs)
		state := "up"
		if ev.invalid {
			state = "unknown"
		} else if !ev.up {
			state = "DOWN"
		}
		fmt.Fprintf(&b, "  - %s\t%s\tstatus=%d latency=%dms\n", r.NodeID, state, r.Status, r.LatencyMs)
	}
	b.WriteString("\n—— ipw-boce 自动告警")
	return b.String()
}

// ==================== SMTP 客户端 ====================

// sendMail 发送一封邮件到 to 指定收件人（调用方已按任务 owner 解析）。支持：
//   - smtp.ssl=true（典型 465）：直接 TLS 连接
//   - smtp.startTLS=true（典型 587）：明文连接后 STARTTLS
//   - 缺省：明文（587 无 STARTTLS）或由服务器协商（smtp 库对显式 TLS 服务器自动 STARTTLS）
//
// host 若带 :port 则以 host 为准，否则补 smtp.port（缺省 25）。
func sendMail(to []string, subject, body string) error {
	c := SMTP_CONF
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 10
	}
	from := c.From
	if from == "" {
		from = c.User
	}
	if c.Host == "" || c.User == "" || from == "" || len(to) == 0 {
		return fmt.Errorf("smtp not fully configured (need host/user/from/to)")
	}
	addr := hostWithPort(c.Host, c.Port)
	auth := smtp.PlainAuth("", c.User, c.Password, hostOnly(addr))

	msg := encodeMail(from, c.FromName, to, subject, body)

	timeout := time.Duration(c.TimeoutSec) * time.Second

	if c.Ssl {
		// 直接 TLS（465）
		tlsConf := &tls.Config{ServerName: hostOnly(addr), InsecureSkipVerify: c.Insecure}
		conn, err := tls.Dial("tcp", addr, tlsConf)
		if err != nil {
			return fmt.Errorf("tls dial: %w", err)
		}
		conn.SetDeadline(time.Now().Add(timeout))
		cl, err := smtp.NewClient(conn, hostOnly(addr))
		if err != nil {
			conn.Close()
			return err
		}
		defer cl.Close()
		return clientSend(cl, auth, from, to, msg)
	}

	// 明文/STARTTLS（缺省走标准库自动协商 STARTTLS）
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	conn.SetDeadline(time.Now().Add(timeout))
	cl, err := smtp.NewClient(conn, hostOnly(addr))
	if err != nil {
		conn.Close()
		return err
	}
	defer cl.Close()
	// 若服务器公告 STARTTLS，则升级（Insecure 时仍校验主机名，除非显式跳过）
	if ok, _ := cl.Extension("STARTTLS"); ok {
		tlsConf := &tls.Config{ServerName: hostOnly(addr), InsecureSkipVerify: c.Insecure}
		if err := cl.StartTLS(tlsConf); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	return clientSend(cl, auth, from, to, msg)
}

// clientSend 完成 smtp.Client 的 MAIL/RCPT/DATA 发送
func clientSend(cl *smtp.Client, auth smtp.Auth, from string, to, msg []string) error {
	if auth != nil {
		if err := cl.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := cl.Mail(from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, r := range to {
		if err := cl.Rcpt(r); err != nil {
			return fmt.Errorf("rcpt %s: %w", r, err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write([]byte(strings.Join(msg, "\r\n"))); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

// hostWithPort host 带端口则原样返回，否则补 defaultPort
func hostWithPort(host string, defaultPort int) string {
	if strings.Contains(host, ":") {
		return host
	}
	port := defaultPort
	if port <= 0 {
		port = 25
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// hostOnly 去掉 host:port 中的端口，返回主机名（证书 ServerName 与 PlainAuth 用）
func hostOnly(addr string) string {
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return addr[:i]
	}
	return addr
}

// encodeMail 编码一封 MIME 邮件（UTF-8 subject/body），返回一行一条的 []string 便于 \r\n 连接
func encodeMail(from, fromName string, to []string, subject, body string) []string {
	var h strings.Builder
	if fromName != "" {
		fmt.Fprintf(&h, "From: =?UTF-8?B?%s?= <%s>", base64.StdEncoding.EncodeToString([]byte(fromName)), from)
	} else {
		fmt.Fprintf(&h, "From: %s", from)
	}
	h.WriteString("\r\nTo: ")
	h.WriteString(strings.Join(to, ", "))
	fmt.Fprintf(&h, "\r\nSubject: =?UTF-8?B?%s?=", base64.StdEncoding.EncodeToString([]byte(subject)))
	h.WriteString("\r\nMIME-Version: 1.0")
	h.WriteString("\r\nContent-Type: text/plain; charset=UTF-8")
	h.WriteString("\r\nContent-Transfer-Encoding: 8bit")
	h.WriteString("\r\nDate: " + time.Now().Format(time.RFC1123Z))
	h.WriteString("\r\n\r\n")
	// body 转 \r\n
	body = strings.ReplaceAll(body, "\n", "\r\n")
	lines := append(strings.Split(h.String(), "\n"), strings.Split(body, "\r\n")...)
	return lines
}

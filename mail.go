package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// ==================== 通知投递：邮件 / 站内信 / Webhook ====================

// sendMail SMTP 发信（465 直连 TLS；startTLS 先明文再升级；其余明文）
func sendMail(to []string, subject, body string) error {
	c := currentSMTP()
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("smtp not configured")
	}
	port := c.Port
	if port == 0 {
		port = 465
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(port))
	from := c.From
	if from == "" {
		from = c.User
	}
	timeout := time.Duration(c.TimeoutSec) * time.Second
	tlsCfg := &tls.Config{ServerName: c.Host, InsecureSkipVerify: c.Insecure}

	var conn net.Conn
	var err error
	if c.Ssl || port == 465 {
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", addr, tlsCfg)
	} else {
		conn, err = net.DialTimeout("tcp", addr, timeout)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer cl.Close()
	if c.StartTLS && !c.Ssl {
		if err := cl.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if c.User != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.User, c.Password, c.Host)); err != nil {
			return err
		}
	}
	if err := cl.Mail(from); err != nil {
		return err
	}
	for _, r := range to {
		if err := cl.Rcpt(r); err != nil {
			return err
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	fromHdr := from
	if c.FromName != "" {
		fromHdr = mime.BEncoding.Encode("UTF-8", c.FromName) + " <" + from + ">"
	}
	var msg bytes.Buffer
	msg.WriteString("From: " + fromHdr + "\r\n")
	msg.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	msg.WriteString("Subject: " + mime.BEncoding.Encode("UTF-8", subject) + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	msg.WriteString(base64.StdEncoding.EncodeToString([]byte(body)))
	if _, err := w.Write(msg.Bytes()); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

func createNotice(userID uint, kind string, taskID uint, title, body string) {
	if userID == 0 {
		return
	}
	ctx, cancel := dbCtx()
	defer cancel()
	db.WithContext(ctx).Create(&AppNotice{UserID: userID, Kind: kind, TaskID: taskID, Title: title, Body: body})
}

// webhookDeliver 按用户 webhookType 构造报文推送：generic 结构化 JSON / wecom、feishu text
func webhookDeliver(u *User, text, event string, taskID uint, nodeID string) error {
	url := strings.TrimSpace(u.WebhookURL)
	if url == "" {
		return fmt.Errorf("webhook url empty")
	}
	var payload any
	switch u.WebhookType {
	case "wecom":
		payload = map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
	case "feishu":
		payload = map[string]any{"msg_type": "text", "content": map[string]string{"text": text}}
	default:
		payload = map[string]any{"event": event, "taskId": taskID, "nodeId": nodeID, "text": text, "ts": time.Now().Unix()}
	}
	b, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// notifyUser 三路投递：站内信恒发；有已验证邮箱发邮件；配了 webhook 推 webhook
func notifyUser(u *User, kind string, taskID uint, title, body string) {
	createNotice(u.ID, kind, taskID, title, body)
	if u.Email != "" && smtpReady() {
		go func(to string) {
			if err := sendMail([]string{to}, title, body); err != nil {
				log.Printf("[notify] mail to %s failed: %v", to, err)
			}
		}(u.Email)
	}
	if u.WebhookURL != "" {
		go func(cp User) {
			if err := webhookDeliver(&cp, title+"\n"+body, kind, taskID, ""); err != nil {
				log.Printf("[notify] webhook user#%d failed: %v", cp.ID, err)
			}
		}(*u)
	}
}

func notifyAdmins(kind, title, body string) {
	for _, u := range enabledAdmins() {
		u := u
		notifyUser(&u, kind, 0, title, body)
	}
}

func smtpReady() bool { return ALERT_CONF.Enabled && regSmtpReady() }

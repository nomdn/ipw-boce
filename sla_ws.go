package main

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// ==================== 控制台实时推送（浏览器 WS，独立于节点通道） ====================
//
// 节点通道（ws.go，端口 8092，/ws）是「下游节点 ↔ middleware」的数据面。
// 本模块是「浏览器控制台 ↔ middleware」的推送面：与 HTTP 主服务同端口升级（/console/sla）。
// 路径刻意避开 /ws 前缀（节点数据面预留），即使将来 8092 的 /ws 合并进主服务也不会路由冲突。
// 复用现有 JWT/admin-token 鉴权。调度器每轮定时拨测落库后，把受影响任务的整窗 SLA 聚合
// 主动推给订阅的浏览器，SLA 看板因此实时刷新，无需手动拉取或轮询。
//
// 推送帧：{ "type": "sla", "taskId": <uint>, "sla": {完整 slaTaskResp} }
// 每个连接在握手 query 里声明自己关心的窗口 ?hours=N（缺省 24），后端按该窗口聚合，
// 因此前端切换窗口后只需重连（query 换 hours）即可持续收到正确窗口的实时数据。

type consoleSub struct {
	conn  *websocket.Conn
	hours int // 该浏览器关心的 SLA 窗口（小时）
	uid   uint   // 订阅者 user id（0=静态 token/admin-token）
	role  string // admin | user
}

type consoleHub struct {
	mu   sync.Mutex
	subs map[*consoleSub]struct{}
}

var hub = &consoleHub{subs: make(map[*consoleSub]struct{})}

// count 当前订阅者数量
func (h *consoleHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *consoleHub) add(s *consoleSub) {
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
}

func (h *consoleHub) remove(s *consoleSub) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
}

// snapshot 返回当前全部订阅（带锁拷贝），避免广播遍历时持锁写阻塞
func (h *consoleHub) snapshot() []*consoleSub {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*consoleSub, 0, len(h.subs))
	for s := range h.subs {
		out = append(out, s)
	}
	return out
}

// pushSLA 触发一次实时推送：对每个"有权限"订阅者按各自窗口聚合 taskID 的 SLA 并广播。
// 权限：admin/静态 token 收全部；user 只收自己(owner_id=该用户)的任务，避免越权读到他人 SLA。
// 无订阅者时直接返回（省 DB）。聚合 + 写均异步执行，不阻塞调度器。
func (h *consoleHub) pushSLA(taskID uint) {
	if h.count() == 0 {
		log.Printf("[ws-sla] pushSLA task#%d SKIP (no subscriber)", taskID)
		return
	}
	// 查该任务归属，用于 user 订阅者过滤
	taskOwner := uint(0)
	taskFound := false
	if db != nil {
		ctx, cancel := dbCtx()
		var t ProbeTask
		if err := db.WithContext(ctx).First(&t, taskID).Error; err == nil {
			taskOwner = t.OwnerID
			taskFound = true
		}
		cancel()
	}
	log.Printf("[ws-sla] pushSLA task#%d begin, subs=%d", taskID, h.count())
	for _, s := range h.snapshot() {
		// 权限过滤：user 且非该任务 owner → 跳过（任务不存在/无归属时 user 也收不到）
		if s.role == RoleUser && (!taskFound || taskOwner == 0 || taskOwner != s.uid) {
			continue
		}
		go func(sub *consoleSub) {
			// sub.hours 已在握手时约束在 [1, 24*90]，直接换算窗口
			dur := time.Duration(sub.hours) * time.Hour
			resp, err := computeTaskSla(taskID, dur)
			if err != nil {
				log.Printf("[ws-sla] pushSLA task#%d compute err: %v", taskID, err)
				return
			}
			msg := wsMessage{Type: "sla", TS: time.Now().Unix(), Data: mustRaw(struct {
				TaskID uint         `json:"taskId"`
				SLA    *slaTaskResp `json:"sla"`
			}{TaskID: taskID, SLA: resp})}
			raw := mustRaw(msg)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			log.Printf("[ws-sla] pushSLA task#%d writing %d bytes", taskID, len(raw))
			if err := sub.conn.Write(ctx, websocket.MessageText, raw); err != nil {
				// 写失败说明对端已断，静默移除并关闭（读循环也会自然退出清理）
				log.Printf("[ws-sla] pushSLA task#%d write err: %v", taskID, err)
				h.remove(sub)
				_ = sub.conn.Close(websocket.StatusNormalClosure, "gone")
			} else {
				log.Printf("[ws-sla] pushSLA task#%d wrote OK", taskID)
			}
		}(s)
	}
}

// registerConsoleSlaRoute 注册 /console/sla 升级路由（挂在 HTTP 主服务，同端口复用 CORS/鉴权语义）
func registerConsoleSlaRoute(router *gin.Engine) {
	router.GET("/console/sla", func(c *gin.Context) {
		// 鉴权：与 /admin/* 一致（静态 admin-token 或有效 JWT）。未过鉴权直接拒绝升级。
		// 浏览器原生 WebSocket 无法自定义 Authorization 头，故 token 兼容 query 传递（?token=）。
		tok := bearerToken(c)
		if tok == "" {
			tok = c.Query("token")
		}
		if !validAuth(tok) {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		uid, role, _ := identityOfAuthToken(tok)
		// 窗口声明：?hours=N（缺省 24），前端切换窗口后靠重连改 query 声明
		hours := 24
		if v := c.Query("hours"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 24*90 {
				hours = n
			}
		}
		conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
			OriginPatterns: []string{"*"},
		})
		if err != nil {
			log.Printf("[ws-sla] accept failed: %v", err)
			return
		}
		sub := &consoleSub{conn: conn, hours: hours, uid: uid, role: role}
		hub.add(sub)
		defer func() {
			hub.remove(sub)
			_ = conn.Close(websocket.StatusNormalClosure, "bye")
		}()
		log.Printf("[ws-sla] browser subscribed (hours=%d uid=%d role=%s)", hours, uid, role)
		// 只读循环保活 + 消费对端关闭事件；浏览器端不做下行指令，忽略内容。
		for {
			_, _, err := conn.Read(c.Request.Context())
			if err != nil {
				return // 对端断开或读错误 → 清理退出
			}
			// 收到帧不解析（预留扩展），仅维持连接活性
		}
	})
}

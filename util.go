package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 报文格式（与原版一致） ====================

// apiError 内部接口错误体（h3 createError 形态）：{"statusCode":N,"statusMessage":"..."}
func apiError(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"statusCode": status, "statusMessage": msg})
}

// restError /api/v1 与公开接口错误体：{"error":{"code","message"}}
func restError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

// isRestPath /api/v1 前缀按 REST 错误体返回，其余维持 apiError 形态
func isRestPath(c *gin.Context) bool {
	return strings.HasPrefix(c.Request.URL.Path, "/api/v1/")
}

// failBy 按路径前缀选择错误体（同一 handler 同时挂在 /admin 与 /api/v1 时使用）
func failBy(c *gin.Context, status int, code, msg string) {
	if isRestPath(c) {
		restError(c, status, code, msg)
		return
	}
	apiError(c, status, msg)
}

// ==================== 小工具 ====================

func idParam(c *gin.Context) uint {
	n, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	return uint(n)
}

func clampLimit(raw string, def int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > 1000 {
		return 1000
	}
	return n
}

func clampFloat(raw string, def, min, max float64) float64 {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f <= 0 {
		return def
	}
	if f < min {
		return min
	}
	if f > max {
		return max
	}
	return f
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func jsonCopy(src, dst any) error { return json.Unmarshal(mustRaw(src), dst) }

func mustRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

var reqSeq atomic.Int64

// genRequestID 进程内唯一请求 ID（WS probe 的 requestId 用）
func genRequestID() string {
	return time.Now().Format("20060102150405") + "-" + strconv.FormatInt(reqSeq.Add(1), 10)
}

// randHex n 字节随机数 → hex
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func isUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unique") || strings.Contains(s, "duplicate")
}

// ==================== 限流（单 IP 每分钟固定窗口） ====================

type rateBucket struct {
	count int
	start time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	max     int
	buckets map[string]*rateBucket
}

func newRateLimiter(max int) *rateLimiter {
	rl := &rateLimiter{max: max, buckets: make(map[string]*rateBucket)}
	go func() {
		for {
			time.Sleep(10 * time.Minute)
			rl.mu.Lock()
			for k, b := range rl.buckets {
				if time.Since(b.start) > 2*time.Minute {
					delete(rl.buckets, k)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *rateLimiter) allow(key string) bool {
	if rl.max <= 0 {
		return true
	}
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[key]
	if !ok || now.Sub(b.start) >= time.Minute {
		rl.buckets[key] = &rateBucket{count: 1, start: now}
		return true
	}
	b.count++
	return b.count <= rl.max
}

func rateLimitMiddleware(rl *rateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !rl.allow(c.ClientIP()) {
			apiError(c, http.StatusTooManyRequests, "Too Many Requests")
			c.Abort()
			return
		}
		c.Next()
	}
}

// corsMiddleware 配置了域名白名单则仅允许这些来源，否则允许所有（回显具体 origin）
func corsMiddleware() gin.HandlerFunc {
	allowAll := len(ACCEPT_DOMAINS) == 0
	allowed := map[string]bool{}
	for _, d := range ACCEPT_DOMAINS {
		if d == "*" {
			allowAll = true
			continue
		}
		allowed[strings.ToLower(d)] = true
	}
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); origin != "" {
			o := strings.ToLower(origin)
			host := strings.TrimPrefix(strings.TrimPrefix(o, "https://"), "http://")
			if allowAll || allowed[o] || allowed[host] {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
				c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
				c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization, Idempotency-Key")
				c.Header("Access-Control-Max-Age", "86400")
			}
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== JWT 登录鉴权 ====================
//
// 管理控制台登录：POST /admin/login 校验 admin-user/admin-password 后签发 HMAC-SHA256 JWT，
// 前端持 JWT 以 Authorization: Bearer <jwt> 访问 /admin/*。
//
// 鉴权双轨（向后兼容，不破坏既有脚本）：
//   - 静态 admin-token（Bearer <admin-token>）原样放行；
//   - 合法且未过期的 JWT 放行。
//
// 未配置 jwt-secret（同时无 admin-password）时 JWT 登录禁用，仅静态 admin-token 可用。
// JWT 使用纯标准库实现（HMAC-SHA256 + exp），零第三方依赖。

// jwtConfig 运行时 JWT 配置（由 readConfig 填充全局变量）
var (
	JWT_SECRET     string // HMAC 密钥；空 = 不启用 JWT 登录
	JWT_EXPIRY     int    // token 有效期秒，缺省 86400
	ADMIN_USER     string // 登录用户名，缺省 admin
	ADMIN_PASSWORD string // 登录口令；与 jwt-secret 都空则登录接口禁用
)

// jwtClaims JWT 负载
type jwtClaims struct {
	Sub      string `json:"sub"`
	UID      uint   `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"`
	IAT      int64  `json:"iat"`
	Exp      int64  `json:"exp"`
}

// jwtEnabled JWT 登录是否启用（密钥齐备即可；口令经 users 表或 admin-password 校验）
func jwtEnabled() bool { return JWT_SECRET != "" }

// base64urlEncode / base64urlDecode：JWT 使用无填充的 base64url
func base64urlEncode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signJWT 用密钥对 header.payload 计算 HMAC-SHA256 签名
func signJWT(secret string, signingInput []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(signingInput)
	return base64urlEncode(mac.Sum(nil))
}

// issueToken 为用户签发 JWT（HS256），返回 token 与有效秒数
func issueToken(uid uint, username, role string) (string, int64, error) {
	now := time.Now().Unix()
	ttl := int64(JWT_EXPIRY)
	if ttl <= 0 {
		ttl = 86400
	}
	claims := jwtClaims{Sub: username, UID: uid, Username: username, Role: role, IAT: now, Exp: now + ttl}
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", 0, err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", 0, err
	}
	signingInput := base64urlEncode(header) + "." + base64urlEncode(payload)
	return signingInput + "." + signJWT(JWT_SECRET, []byte(signingInput)), ttl, nil
}

// parseJWT 校验 token 签名与过期，返回 claims；失败返回 error
func parseJWT(token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	// 重算签名比对（常量时间）
	signingInput := parts[0] + "." + parts[1]
	expected := signJWT(JWT_SECRET, []byte(signingInput))
	if !hmac.Equal([]byte(parts[2]), []byte(expected)) {
		return nil, errors.New("invalid signature")
	}
	// 解出负载
	payload, err := base64urlDecode(parts[1])
	if err != nil {
		return nil, errors.New("invalid payload")
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, errors.New("invalid payload json")
	}
	if claims.Exp <= time.Now().Unix() {
		return nil, errors.New("token expired")
	}
	return &claims, nil
}

func base64urlDecode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// validAuth 判定请求是否通过鉴权：静态 admin-token 或有效 JWT 皆可
func validAuth(token string) bool {
	// 静态 admin-token 兼容（向后不破坏既有调用）
	if ADMIN_TOKEN != "" && token == ADMIN_TOKEN {
		return true
	}
	if jwtEnabled() {
		if _, err := parseJWT(token); err == nil {
			return true
		}
	}
	return false
}

// identityOfAuthToken 解析 token → (uid, role)。静态 admin-token 视为 uid=0/role=admin；
// 有效 JWT 取其 uid/role（旧 token 无 role 时保守给 user）。token 无效返回 ok=false。
func identityOfAuthToken(token string) (uid uint, role string, ok bool) {
	if ADMIN_TOKEN != "" && token == ADMIN_TOKEN {
		return 0, RoleAdmin, true
	}
	if jwtEnabled() {
		if cl, err := parseJWT(token); err == nil {
			r := cl.Role
			if r == "" {
				r = RoleUser // 旧 token 无 role：保守给普通权限
			}
			return cl.UID, r, true
		}
	}
	return 0, "", false
}

// ==================== 登录与鉴权中间件 ====================

// loginHandler POST /admin/login（免鉴权）：body {username,password}，通过则签发 JWT
//
// 校验顺序：
//  1. users 表按 username 查用户；命中且未禁用 → bcrypt 校验口令。
//  2. 未命中（或 seed 前 / db 不可用）→ 回退到 admin-user/admin-password 配置比对
//     （向后兼容，且保证 seed 失败仍可登录）；回退命中视为 role=admin。
func loginHandler(c *gin.Context) {
	if !jwtEnabled() {
		apiError(c, http.StatusForbidden, "JWT login is not enabled (set jwt-secret)")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		apiError(c, http.StatusBadRequest, "invalid login body: "+err.Error())
		return
	}
	username := strings.TrimSpace(body.Username)

	// 1) DB 用户优先
	if db != nil {
		if u, err := userByUsername(username); err != nil {
			log.Printf("[auth] WARN query user: %v", err)
		} else if u != nil {
			if !u.Enabled {
				apiError(c, http.StatusUnauthorized, "account disabled")
				return
			}
			if !verifyPassword(u.PasswordHash, body.Password) {
				apiError(c, http.StatusUnauthorized, "invalid username or password")
				return
			}
			token, ttl, err := issueToken(u.ID, u.Username, u.Role)
			if err != nil {
				apiError(c, http.StatusInternalServerError, "issue token: "+err.Error())
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"token":         token,
				"tokenType":     "Bearer",
				"userId":        u.ID,
				"username":      u.Username,
				"role":          u.Role,
				"email":         u.Email,
				"emailVerified": u.EmailVerified,
				"expiresIn":     ttl,
				"expiresAt":     time.Now().Add(time.Duration(ttl) * time.Second).Format(time.RFC3339),
			})
			return
		}
	}

	// 2) 回退：admin-user/admin-password（seed 前的兼容路径；口令为空则禁用，防空口令放行）
	if ADMIN_PASSWORD != "" && username == ADMIN_USER && body.Password == ADMIN_PASSWORD {
		token, ttl, err := issueToken(0, ADMIN_USER, RoleAdmin)
		if err != nil {
			apiError(c, http.StatusInternalServerError, "issue token: "+err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"token":     token,
			"tokenType": "Bearer",
			"userId":    0,
			"username":  ADMIN_USER,
			"role":      RoleAdmin,
			"expiresIn": ttl,
			"expiresAt": time.Now().Add(time.Duration(ttl) * time.Second).Format(time.RFC3339),
		})
		return
	}
	apiError(c, http.StatusUnauthorized, "invalid username or password")
}

// adminAuthMiddleware /admin/* 鉴权（双轨：静态 admin-token 或有效 JWT）。
// JWT 命中时将 {uid, role, username} 写入 gin 上下文，供 adminOnly/currentUser 使用；
// 静态 admin-token 不携带角色，视为 role=admin（全权）。
func adminAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			apiError(c, http.StatusUnauthorized, "Unauthorized")
			c.Abort()
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		// 静态 admin-token：直接放行（视为 admin）
		if ADMIN_TOKEN != "" && token == ADMIN_TOKEN {
			c.Set(ctxUserKey, ctxUser{ID: 0, Role: RoleAdmin, Username: "admin-token"})
			c.Next()
			return
		}
		if jwtEnabled() {
			if cl, err := parseJWT(token); err == nil {
				if cl.Role == "" {
					cl.Role = RoleUser // 旧 token 无 role：保守给普通权限（若需管理走重新登录）
				}
				c.Set(ctxUserKey, ctxUser{ID: cl.UID, Role: cl.Role, Username: cl.Username})
				c.Next()
				return
			}
		}
		apiError(c, http.StatusUnauthorized, "Unauthorized")
		c.Abort()
	}
}

// adminOnly 中间件：仅 role=admin 放行（用户管理等敏感操作）。
// 无上下文（异常路径）默认拒绝。
func adminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, role, _ := currentUserFromCtx(c)
		if role != RoleAdmin {
			// 静态 token(uid=0) 也视为 admin，已在 adminAuth 写入 role=admin
			_ = uid
			apiError(c, http.StatusForbidden, "admin role required")
			c.Abort()
			return
		}
		c.Next()
	}
}

// bearerToken 从 Authorization 头解析出裸 token（登录态判断用）
func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

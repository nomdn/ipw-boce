package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 鉴权三轨（与原版一致） ====================
//
//   - 静态 admin-token（Bearer <admin-token>）：uid=0、role=admin
//   - JWT（POST /admin/login 签发，HS256 纯标准库）：claims {sub,uid,username,role,iat,exp}
//   - 个人 API Token（Bearer ipt_<40hex>）：等价账号身份，库里只存 bcrypt 哈希
//
// 未配置 jwt-secret 时 JWT 登录禁用，仅静态 admin-token 可用。

type jwtClaims struct {
	Sub      string `json:"sub"`
	UID      uint   `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"`
	IAT      int64  `json:"iat"`
	Exp      int64  `json:"exp"`
}

func jwtEnabled() bool { return JWT_SECRET != "" }

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func signJWT(input []byte) string {
	mac := hmac.New(sha256.New, []byte(JWT_SECRET))
	mac.Write(input)
	return b64url(mac.Sum(nil))
}

// issueToken 签发 JWT，返回 token 与有效秒数
func issueToken(uid uint, username, role string) (string, int64, error) {
	now := time.Now().Unix()
	ttl := int64(JWT_EXPIRY)
	if ttl <= 0 {
		ttl = 86400
	}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err := json.Marshal(jwtClaims{Sub: username, UID: uid, Username: username, Role: role, IAT: now, Exp: now + ttl})
	if err != nil {
		return "", 0, err
	}
	input := b64url(header) + "." + b64url(payload)
	return input + "." + signJWT([]byte(input)), ttl, nil
}

func parseJWT(token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	if !hmac.Equal([]byte(parts[2]), []byte(signJWT([]byte(parts[0]+"."+parts[1])))) {
		return nil, errors.New("invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid payload")
	}
	var cl jwtClaims
	if err := json.Unmarshal(payload, &cl); err != nil {
		return nil, errors.New("invalid payload json")
	}
	if cl.Exp <= time.Now().Unix() {
		return nil, errors.New("token expired")
	}
	return &cl, nil
}

// userByAPIToken 个人 API Token 校验（bcrypt 比对持有者；命中且启用才返回）
func userByAPIToken(token string) *User {
	if db == nil {
		return nil
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var users []User
	if err := db.WithContext(ctx).Where("api_token_hash <> ''").Find(&users).Error; err != nil {
		return nil
	}
	for i := range users {
		if verifyPassword(users[i].APITokenHash, token) {
			if !users[i].Enabled {
				return nil
			}
			return &users[i]
		}
	}
	return nil
}

// identityOfAuthToken token → (uid, role, username)；无效 ok=false
func identityOfAuthToken(token string) (ctxUser, bool) {
	if token == "" {
		return ctxUser{}, false
	}
	if ADMIN_TOKEN != "" && token == ADMIN_TOKEN {
		return ctxUser{ID: 0, Role: RoleAdmin, Username: "admin-token"}, true
	}
	if strings.HasPrefix(token, "ipt_") {
		if u := userByAPIToken(token); u != nil {
			return ctxUser{ID: u.ID, Role: u.Role, Username: u.Username}, true
		}
		return ctxUser{}, false
	}
	if jwtEnabled() {
		if cl, err := parseJWT(token); err == nil {
			if cl.Role == "" {
				cl.Role = RoleUser // 旧 token 无 role：保守给普通权限
			}
			return ctxUser{ID: cl.UID, Role: cl.Role, Username: cl.Username}, true
		}
	}
	return ctxUser{}, false
}

func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

// adminAuthMiddleware /admin/* 与 /api/v1/* 鉴权；错误体按路径前缀区分
func adminAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			if isRestPath(c) {
				restError(c, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			} else {
				apiError(c, http.StatusUnauthorized, "Unauthorized")
			}
			c.Abort()
			return
		}
		token := bearerToken(c)
		if u, ok := identityOfAuthToken(token); ok {
			c.Set(ctxUserKey, u)
			c.Next()
			return
		}
		if isRestPath(c) {
			msg := "invalid credentials"
			if strings.HasPrefix(token, "ipt_") {
				msg = "invalid or revoked token"
			}
			restError(c, http.StatusUnauthorized, "unauthorized", msg)
		} else {
			apiError(c, http.StatusUnauthorized, "Unauthorized")
		}
		c.Abort()
	}
}

// adminOnly 仅 role=admin（含静态 token）放行
func adminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, role, _ := currentUserFromCtx(c); role != RoleAdmin {
			failBy(c, http.StatusForbidden, "forbidden", "admin role required")
			c.Abort()
			return
		}
		c.Next()
	}
}

// authLimiter 登录 / 注册验证码接口的单 IP 限流（10 次/分），防爆破
var authLimiter = newRateLimiter(10)

// loginHandler POST /admin/login：users 表优先，未命中回退 admin-user/admin-password
func loginHandler(c *gin.Context) {
	if !authLimiter.allow(c.ClientIP()) {
		apiError(c, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
		return
	}
	if !jwtEnabled() {
		apiError(c, http.StatusForbidden, "JWT login is not enabled (set jwt-secret)")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		captchaInput
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		apiError(c, http.StatusBadRequest, "invalid login body: "+err.Error())
		return
	}
	if err := checkCaptcha(c, "login", body.captchaInput); err != nil {
		apiError(c, http.StatusBadRequest, err.Error())
		return
	}
	username := strings.TrimSpace(body.Username)
	if u, err := userByUsername(username); err == nil && u != nil {
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
			"token": token, "tokenType": "Bearer", "userId": u.ID, "username": u.Username,
			"role": u.Role, "email": u.Email, "emailVerified": u.EmailVerified, "expiresIn": ttl,
			"expiresAt": time.Now().Add(time.Duration(ttl) * time.Second).Format(time.RFC3339),
		})
		return
	}
	if ADMIN_PASSWORD != "" && username == ADMIN_USER && body.Password == ADMIN_PASSWORD {
		token, ttl, err := issueToken(0, ADMIN_USER, RoleAdmin)
		if err != nil {
			apiError(c, http.StatusInternalServerError, "issue token: "+err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"token": token, "tokenType": "Bearer", "userId": 0, "username": ADMIN_USER,
			"role": RoleAdmin, "expiresIn": ttl,
			"expiresAt": time.Now().Add(time.Duration(ttl) * time.Second).Format(time.RFC3339),
		})
		return
	}
	apiError(c, http.StatusUnauthorized, "invalid username or password")
}

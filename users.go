package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// ==================== 控制台用户管理 ====================
//
// 多用户登录：账号存 users 表（bcrypt 口令哈希），取代单一 admin-user/admin-password。
// 角色：
//   - role=admin：拥有全部权限，可进入"用户管理"页管理其它账号。
//   - role=user ：普通用户，可用工具页，不可管理账号。
//
// 兼容迁移：首次启动(users 表空)且配置了 admin-password 时，自动 seed 一个
// role=admin 的初始账号（username=admin-user）。若 admin-password 为空，
// 则 seed 一个默认 admin/admin（并打印提醒，避免锁死）。
//
// 邮箱：可选；任务掉线告警按任务的 owner_id 找创建者的邮箱发信（见 alert.go deliverDownAlert）；
// 创建者无邮箱时落站内信（AppNotice）。无归属任务不发告警。删除用户会级联删除其任务与站内信。

// 角色常量
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// User 控制台登录用户
type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"uniqueIndex;size:64" json:"username"`
	PasswordHash string    `gorm:"size:128" json:"-"`
	Email        string    `gorm:"size:256" json:"email,omitempty"`
	Role         string    `gorm:"size:16;index" json:"role"` // admin | user
	Enabled      bool      `json:"enabled"`
	// EmailVerified 邮箱是否已验证。自助注册走验证码，创建即 true；
	// 管理员直建(带邮箱)默认为 false → 该用户登录受限(SLA 需先验证邮箱)。
	EmailVerified bool      `gorm:"default:false" json:"emailVerified"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"-"`
}

// userPublic 返回给前端的视图（绝不带口令哈希）
func (u *User) userPublic() gin.H {
	return gin.H{
		"id":            u.ID,
		"username":      u.Username,
		"email":         u.Email,
		"emailVerified": u.EmailVerified,
		"role":          u.Role,
		"enabled":       u.Enabled,
		"createdAt":     u.CreatedAt,
	}
}

// hashPassword bcrypt 哈希口令
func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// verifyPassword 校验明文口令与 bcrypt 哈希是否一致
func verifyPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// seedUsers 在 users 表为空时写入初始 admin，避免空表导致无人可登录。
// 在 openDB 之后调用（db 已就绪）。
func seedUsers() {
	if db == nil {
		return
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var n int64
	if err := db.WithContext(ctx).Model(&User{}).Count(&n).Error; err != nil {
		log.Printf("[users] WARN count users: %v", err)
		return
	}
	if n > 0 {
		return // 已有用户，不再覆盖
	}
	username := strings.TrimSpace(ADMIN_USER)
	if username == "" {
		username = "admin"
	}
	password := ADMIN_PASSWORD
	if password == "" {
		password = "admin"
		log.Printf("[users] WARN no admin-password configured; seeding initial admin with default password %q — please change it", password)
	} else {
		log.Printf("[users] seeding initial admin user %q from admin-user/admin-password", username)
	}
	hash, err := hashPassword(password)
	if err != nil {
		log.Printf("[users] ERROR hash seed admin: %v", err)
		return
	}
	u := &User{Username: username, PasswordHash: hash, Role: RoleAdmin, Enabled: true}
	if err := db.WithContext(ctx).Create(u).Error; err != nil {
		log.Printf("[users] ERROR seed admin: %v", err)
		return
	}
	log.Printf("[users] seeded initial admin user id=%d", u.ID)
}

// currentUserFromCtx 从鉴权中间件注入的上下文里取当前用户身份。
// 返回 (userID, role, username)；静态 admin-token 访问时视为 uid=0、role=admin。
func currentUserFromCtx(c *gin.Context) (uint, string, string) {
	if v, ok := c.Get(ctxUserKey); ok {
		if cu, ok := v.(ctxUser); ok {
			return cu.ID, cu.Role, cu.Username
		}
	}
	return 0, RoleAdmin, "" // 静态 token（无角色信息）默认最高权限
}

// ctxUser 放入 gin 上下文请求级的当前登录者
type ctxUser struct {
	ID       uint
	Role     string
	Username string
}

const ctxUserKey = "ctx-user"

// userByUsername 按用户名取用户（含禁用态），供登录用；未命中返回 (nil,nil)
func userByUsername(username string) (*User, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var u User
	err := db.WithContext(ctx).Where("username = ?", username).Limit(1).Find(&u).Error
	if err != nil {
		return nil, err
	}
	if u.ID == 0 {
		return nil, nil
	}
	return &u, nil
}

// userByID 按主键取用户；未命中返回 (nil,nil)
func userByID(id uint) (*User, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var u User
	err := db.WithContext(ctx).Limit(1).Find(&u, id).Error
	if err != nil {
		return nil, err
	}
	if u.ID == 0 {
		return nil, nil
	}
	return &u, nil
}

func (u *User) String() string { return fmt.Sprintf("%s(#%d)", u.Username, u.ID) }

// ==================== 用户管理路由（/admin/users，仅 admin） ====================

// registerUserRoutes 注册用户 CRUD。注意 admin 组已过 adminAuthMiddleware，
// 此处再套 adminOnly()（仅 role=admin 或静态 token 可进）。
func registerUserRoutes(admin *gin.RouterGroup) {
	users := admin.Group("/users", adminOnly())

	// 列表（不回显口令哈希）
	users.GET("", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var list []User
		if err := db.WithContext(ctx).Order("role desc, username asc").Find(&list).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]gin.H, 0, len(list))
		for i := range list {
			out = append(out, list[i].userPublic())
		}
		c.JSON(http.StatusOK, out)
	})

	// 创建用户：body {username, password, email?, role?}
	users.POST("", func(c *gin.Context) {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Email    string `json:"email"`
			Role     string `json:"role"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		username := strings.TrimSpace(body.Username)
		if username == "" {
			apiError(c, http.StatusBadRequest, "username required")
			return
		}
		if len(body.Password) < 6 {
			apiError(c, http.StatusBadRequest, "password must be at least 6 chars")
			return
		}
		role := strings.TrimSpace(body.Role)
		if role == "" {
			role = RoleUser
		}
		if role != RoleAdmin && role != RoleUser {
			apiError(c, http.StatusBadRequest, "role must be admin|user")
			return
		}
		hash, err := hashPassword(body.Password)
		if err != nil {
			apiError(c, http.StatusInternalServerError, "hash: "+err.Error())
			return
		}
		u := &User{
			Username:     username,
			PasswordHash: hash,
			Email:        strings.TrimSpace(body.Email),
			Role:         role,
			Enabled:      true,
		}
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Create(u).Error; err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				apiError(c, http.StatusConflict, "username already exists")
				return
			}
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[users] admin %s created user %s", currentUsername(c), username)
		c.JSON(http.StatusCreated, u.userPublic())
	})

	// 编辑：改 email / role / enabled（不在此处改口令，见 /password）
	users.PATCH("/:id", func(c *gin.Context) {
		id := idParam(c)
		target, err := userByID(id)
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		if target == nil {
			apiError(c, http.StatusNotFound, "user not found")
			return
		}
		var body struct {
			Email   *string `json:"email"`
			Role    *string `json:"role"`
			Enabled *bool   `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		myID, myRole, myName := currentUserFromCtx(c)

		// 自我保护：不能降级自己为普通 / 禁用自己（防止把自己锁出管理后台）
		if target.ID == myID && myID != 0 {
			if body.Role != nil && *body.Role != RoleAdmin {
				apiError(c, http.StatusBadRequest, "cannot demote your own admin role")
				return
			}
			if body.Enabled != nil && !*body.Enabled {
				apiError(c, http.StatusBadRequest, "cannot disable your own account")
				return
			}
		}

		updates := map[string]any{}
		if body.Email != nil {
			updates["email"] = strings.TrimSpace(*body.Email)
		}
		if body.Role != nil {
			r := strings.TrimSpace(*body.Role)
			if r != RoleAdmin && r != RoleUser {
				apiError(c, http.StatusBadRequest, "role must be admin|user")
				return
			}
			// 确保系统始终至少保留一个启用 admin（防止删光管理员）
			if r == RoleUser && target.Role == RoleAdmin {
				if !hasAnotherEnabledAdmin(target.ID) {
					apiError(c, http.StatusBadRequest, "must keep at least one enabled admin")
					return
				}
			}
			updates["role"] = r
		}
		if body.Enabled != nil {
			if *body.Enabled == false && target.Role == RoleAdmin {
				if !hasAnotherEnabledAdmin(target.ID) {
					apiError(c, http.StatusBadRequest, "must keep at least one enabled admin")
					return
				}
			}
			updates["enabled"] = *body.Enabled
		}
		if len(updates) == 0 {
			c.JSON(http.StatusOK, target.userPublic())
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Model(&User{}).Where("id = ?", target.ID).Updates(updates).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[users] admin %s updated user %s fields=%v", myName, target.String(), keysOf(updates))
		_ = myRole
		c.JSON(http.StatusOK, gin.H{"id": target.ID})
	})

	// 重置口令：body {password}（admin 直接改该用户口令，不需要旧口令）
	users.PATCH("/:id/password", func(c *gin.Context) {
		id := idParam(c)
		target, err := userByID(id)
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		if target == nil {
			apiError(c, http.StatusNotFound, "user not found")
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if len(body.Password) < 6 {
			apiError(c, http.StatusBadRequest, "password must be at least 6 chars")
			return
		}
		hash, err := hashPassword(body.Password)
		if err != nil {
			apiError(c, http.StatusInternalServerError, "hash: "+err.Error())
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Model(&User{}).Where("id = ?", target.ID).
			Update("password_hash", hash).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[users] admin %s reset password of %s", currentUsername(c), target.String())
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// 删除（禁止删自己）
	users.DELETE("/:id", func(c *gin.Context) {
		id := idParam(c)
		myID, _, myName := currentUserFromCtx(c)
		if id == myID && myID != 0 {
			apiError(c, http.StatusBadRequest, "cannot delete your own account")
			return
		}
		target, err := userByID(id)
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		if target == nil {
			apiError(c, http.StatusNotFound, "user not found")
			return
		}
		// 保留至少一个启用 admin
		if target.Role == RoleAdmin && !hasAnotherEnabledAdmin(target.ID) {
			apiError(c, http.StatusBadRequest, "must keep at least one enabled admin")
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		// 级联：删除该用户创建的所有定时拨测任务（掉线告警只发给所有者，避免残留"所有者已删"任务）
		if err := db.WithContext(ctx).Where("owner_id = ?", target.ID).Delete(&ProbeTask{}).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		// 级联：删除该用户的站内信（见 alert.go notices）
		if err := db.WithContext(ctx).Where("user_id = ?", target.ID).Delete(&AppNotice{}).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		if err := db.WithContext(ctx).Delete(&User{}, target.ID).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[users] admin %s deleted user %s", myName, target.String())
		c.JSON(http.StatusOK, gin.H{"deleted": true})
	})
}

// currentUsername 取当前登录用户名（日志用）
func currentUsername(c *gin.Context) string {
	_, _, name := currentUserFromCtx(c)
	return name
}

// enabledAdmins 返回所有启用的 admin 账号（节点掉线等系统级告警的收件对象）。
func enabledAdmins() []User {
	if db == nil {
		return nil
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var list []User
	if err := db.WithContext(ctx).Where("role = ? AND enabled = ?", RoleAdmin, true).Find(&list).Error; err != nil {
		log.Printf("[users] WARN list enabled admins: %v", err)
		return nil
	}
	return list
}

// backfillEmailVerified 兼容迁移：首次引入 EmailVerified 后，把既有且已填邮箱的账号标记为已验证，
// 避免老账号(历史由 admin 建、邮箱本就可用)被新规则误判为"未验证"而限制 SLA。
// 幂等：只把 email_verified 仍为 false 且 email 非空的行置 true。
func backfillEmailVerified() {
	if db == nil {
		return
	}
	ctx, cancel := dbCtx()
	defer cancel()
	res := db.WithContext(ctx).Model(&User{}).
		Where("email <> '' AND email IS NOT NULL AND email_verified = ?", false).
		Update("email_verified", true)
	if res.Error != nil {
		log.Printf("[users] WARN backfill email_verified: %v", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		log.Printf("[users] backfilled email_verified=true for %d existing users (email set)", res.RowsAffected)
	}
}

// emailVerifiedBlocked 判定当前操作者是否因"邮箱未验证"而被限制（仅针对 role=user 且已填邮箱但未验证的账号）。
// admin / 静态 token / 未填邮箱的账号不受限（无邮箱无从验证，不应因此封死其 SLA）。
// 返回被限制原因（空=放行）。SLA 建/启用任务等会触发"掉线告警邮件到所有者"的写操作应调用。
func emailVerifiedBlocked(c *gin.Context) string {
	uid, role, _ := currentUserFromCtx(c)
	if uid == 0 || role != RoleUser {
		return "" // admin / 静态 token 放行
	}
	u, err := userByID(uid)
	if err != nil || u == nil {
		return ""
	}
	if strings.TrimSpace(u.Email) != "" && !u.EmailVerified {
		return "请先在「个人资料」验证邮箱后再创建/启用 SLA 任务"
	}
	return ""
}

// hasAnotherEnabledAdmin 除 excludeID 外是否仍存在启用的 admin
func hasAnotherEnabledAdmin(excludeID uint) bool {
	ctx, cancel := dbCtx()
	defer cancel()
	var n int64
	if err := db.WithContext(ctx).Model(&User{}).
		Where("role = ? AND enabled = ? AND id <> ?", RoleAdmin, true, excludeID).
		Count(&n).Error; err != nil {
		return false
	}
	return n > 0
}

// keysOf 取 map 键列表（日志/调试用）
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

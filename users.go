package main

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// ==================== 控制台用户 ====================
//
// 角色：admin（全部权限）/ user（只见自己的任务与数据）。
// users 表为空时 seed 初始管理员：配置了 admin-password 用它，否则 admin/admin 并打印提醒。

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

type User struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	Username          string    `gorm:"uniqueIndex;size:64" json:"username"`
	PasswordHash      string    `gorm:"size:128" json:"-"`
	Email             string    `gorm:"size:256" json:"email,omitempty"`
	Role              string    `gorm:"size:16;index" json:"role"`
	Enabled           bool      `json:"enabled"`
	EmailVerified     bool      `gorm:"default:false" json:"emailVerified"`
	WebhookURL        string    `gorm:"size:512" json:"webhookUrl,omitempty"`
	WebhookType       string    `gorm:"size:16" json:"webhookType,omitempty"`
	APITokenHash      string    `gorm:"size:128" json:"-"`
	APITokenHint      string    `gorm:"size:8" json:"apiTokenHint,omitempty"`
	APITokenCreatedAt time.Time `json:"apiTokenCreatedAt,omitempty"`
	// 节点 Token：只能给自己已通过审核的捐赠节点拉接入配置（见 agent.go）
	AgentToken string `gorm:"size:48;index" json:"-"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"-"`
}

func (u *User) userPublic() gin.H {
	return gin.H{
		"id": u.ID, "username": u.Username, "email": u.Email, "emailVerified": u.EmailVerified,
		"role": u.Role, "enabled": u.Enabled, "webhookUrl": u.WebhookURL, "webhookType": u.WebhookType,
		"apiTokenHint": u.APITokenHint, "hasApiToken": u.APITokenHash != "",
		"apiTokenCreatedAt": u.APITokenCreatedAt, "createdAt": u.CreatedAt,
	}
}

func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func verifyPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

func seedUsers() {
	ctx, cancel := dbCtx()
	defer cancel()
	var n int64
	if err := db.WithContext(ctx).Model(&User{}).Count(&n).Error; err != nil || n > 0 {
		return
	}
	password := ADMIN_PASSWORD
	if password == "" {
		password = "admin"
		log.Printf("[users] WARN no admin-password configured; seeding initial admin with default password %q — please change it", password)
	}
	hash, err := hashPassword(password)
	if err != nil {
		log.Printf("[users] ERROR hash seed admin: %v", err)
		return
	}
	u := &User{Username: ADMIN_USER, PasswordHash: hash, Role: RoleAdmin, Enabled: true}
	if err := db.WithContext(ctx).Create(u).Error; err != nil {
		log.Printf("[users] ERROR seed admin: %v", err)
		return
	}
	log.Printf("[users] seeded initial admin %q id=%d", u.Username, u.ID)
}

// backfillEmailVerified 存量已填邮箱账号补齐已验证标记（幂等）
func backfillEmailVerified() {
	ctx, cancel := dbCtx()
	defer cancel()
	db.WithContext(ctx).Model(&User{}).
		Where("email <> '' AND email IS NOT NULL AND email_verified = ?", false).
		Update("email_verified", true)
}

type ctxUser struct {
	ID       uint
	Role     string
	Username string
}

const ctxUserKey = "ctx-user"

// currentUserFromCtx 返回 (uid, role, username)；无上下文时视为静态 token（admin）
func currentUserFromCtx(c *gin.Context) (uint, string, string) {
	if v, ok := c.Get(ctxUserKey); ok {
		if cu, ok := v.(ctxUser); ok {
			return cu.ID, cu.Role, cu.Username
		}
	}
	return 0, RoleAdmin, ""
}

func currentUsername(c *gin.Context) string {
	_, _, name := currentUserFromCtx(c)
	return name
}

func userByUsername(username string) (*User, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var u User
	if err := db.WithContext(ctx).Where("username = ?", username).Limit(1).Find(&u).Error; err != nil {
		return nil, err
	}
	if u.ID == 0 {
		return nil, nil
	}
	return &u, nil
}

func userByID(id uint) (*User, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var u User
	if err := db.WithContext(ctx).Limit(1).Find(&u, id).Error; err != nil {
		return nil, err
	}
	if u.ID == 0 {
		return nil, nil
	}
	return &u, nil
}

func enabledAdmins() []User {
	ctx, cancel := dbCtx()
	defer cancel()
	var list []User
	db.WithContext(ctx).Where("role = ? AND enabled = ?", RoleAdmin, true).Find(&list)
	return list
}

func hasAnotherEnabledAdmin(excludeID uint) bool {
	ctx, cancel := dbCtx()
	defer cancel()
	var n int64
	if err := db.WithContext(ctx).Model(&User{}).
		Where("role = ? AND enabled = ? AND id <> ?", RoleAdmin, true, excludeID).Count(&n).Error; err != nil {
		return false
	}
	return n > 0
}

// emailVerifiedBlocked 普通用户填了邮箱但未验证时，限制建/启用 SLA 任务
func emailVerifiedBlocked(c *gin.Context) string {
	uid, role, _ := currentUserFromCtx(c)
	if uid == 0 || role != RoleUser {
		return ""
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

// registerUserRoutes /admin/users（仅 admin）
func registerUserRoutes(admin *gin.RouterGroup) {
	users := admin.Group("/users", adminOnly())

	users.GET("", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var list []User
		if err := db.WithContext(ctx).Order("role asc, username asc").Find(&list).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		balances := balancesOf(list)
		out := make([]gin.H, 0, len(list))
		for i := range list {
			h := list[i].userPublic()
			h["points"] = balances[list[i].ID]
			out = append(out, h)
		}
		c.JSON(http.StatusOK, out)
	})

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
		u := &User{Username: username, PasswordHash: hash, Email: strings.TrimSpace(body.Email), Role: role, Enabled: true}
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Create(u).Error; err != nil {
			if isUniqueErr(err) {
				apiError(c, http.StatusConflict, "username already exists")
				return
			}
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[users] admin %s created user %s", currentUsername(c), username)
		c.JSON(http.StatusCreated, u.userPublic())
	})

	users.PATCH("/:id", func(c *gin.Context) {
		target, err := userByID(idParam(c))
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
		myID, _, _ := currentUserFromCtx(c)
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
			if r == RoleUser && target.Role == RoleAdmin && !hasAnotherEnabledAdmin(target.ID) {
				apiError(c, http.StatusBadRequest, "must keep at least one enabled admin")
				return
			}
			updates["role"] = r
		}
		if body.Enabled != nil {
			if !*body.Enabled && target.Role == RoleAdmin && !hasAnotherEnabledAdmin(target.ID) {
				apiError(c, http.StatusBadRequest, "must keep at least one enabled admin")
				return
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
		log.Printf("[users] admin %s updated user %s fields=%v", currentUsername(c), target.Username, keysOf(updates))
		c.JSON(http.StatusOK, gin.H{"id": target.ID})
	})

	users.PATCH("/:id/password", func(c *gin.Context) {
		target, err := userByID(idParam(c))
		if err != nil || target == nil {
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
		if err := db.WithContext(ctx).Model(&User{}).Where("id = ?", target.ID).Update("password_hash", hash).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	users.DELETE("/:id", func(c *gin.Context) {
		id := idParam(c)
		myID, _, myName := currentUserFromCtx(c)
		if id == myID && myID != 0 {
			apiError(c, http.StatusBadRequest, "cannot delete your own account")
			return
		}
		target, err := userByID(id)
		if err != nil || target == nil {
			apiError(c, http.StatusNotFound, "user not found")
			return
		}
		if target.Role == RoleAdmin && !hasAnotherEnabledAdmin(target.ID) {
			apiError(c, http.StatusBadRequest, "must keep at least one enabled admin")
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		// 级联：任务、站内信、积分账户与签到记录（积分流水与订单保留，供审计）
		db.WithContext(ctx).Where("owner_id = ?", target.ID).Delete(&ProbeTask{})
		db.WithContext(ctx).Where("user_id = ?", target.ID).Delete(&AppNotice{})
		db.WithContext(ctx).Where("user_id = ?", target.ID).Delete(&PointAccount{})
		db.WithContext(ctx).Where("user_id = ?", target.ID).Delete(&PointCheckin{})
		if err := db.WithContext(ctx).Delete(&User{}, target.ID).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("[users] admin %s deleted user %s", myName, target.Username)
		c.JSON(http.StatusOK, gin.H{"deleted": true})
	})
}

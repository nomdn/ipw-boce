package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ==================== /admin/tasks（user 仅自己创建的；admin 全量） ====================

func taskForbidden(c *gin.Context, t *ProbeTask) bool {
	uid, role, _ := currentUserFromCtx(c)
	return role == RoleUser && t.OwnerID != uid
}

// loadVisibleTask 404 不存在 / 403 非本人；错误体按路径前缀区分
func loadVisibleTask(c *gin.Context) (*ProbeTask, bool) {
	var t ProbeTask
	ctx, cancel := dbCtx()
	defer cancel()
	if err := db.WithContext(ctx).First(&t, idParam(c)).Error; err != nil {
		failBy(c, http.StatusNotFound, "not_found", "task not found")
		return nil, false
	}
	if taskForbidden(c, &t) {
		failBy(c, http.StatusForbidden, "forbidden", "not your task")
		return nil, false
	}
	return &t, true
}

func tasksWithOwner(rows []ProbeTask) []map[string]any {
	ids := map[uint]bool{}
	for _, t := range rows {
		ids[t.OwnerID] = true
	}
	names := map[uint]string{}
	if len(ids) > 0 {
		var keys []uint
		for k := range ids {
			keys = append(keys, k)
		}
		var us []User
		ctx, cancel := dbCtx()
		db.WithContext(ctx).Select("id, username").Where("id IN ?", keys).Find(&us)
		cancel()
		for _, u := range us {
			names[u.ID] = u.Username
		}
	}
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		m := rows[i].toMap()
		m["ownerUsername"] = names[rows[i].OwnerID]
		out = append(out, m)
	}
	return out
}

func registerTaskRoutes(admin *gin.RouterGroup) {
	admin.GET("/tasks/meta", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"types": knownProbeTaskTypes(), "recordTypes": knownDNSRecordTypes(),
			"defaultInterval": 60, "minInterval": 10})
	})

	admin.GET("/tasks", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&ProbeTask{})
		if uid, role, _ := currentUserFromCtx(c); role == RoleUser {
			q = q.Where("owner_id = ?", uid)
		}
		if v := strings.TrimSpace(c.Query("tag")); v != "" {
			q = q.Where("tags LIKE ?", "%"+v+"%")
		}
		var rows []ProbeTask
		if err := q.Order("id asc").Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, tasksWithOwner(rows))
	})

	admin.GET("/tasks/mine/series", func(c *gin.Context) {
		tr := parseTimeRange(c, 24)
		uid, role, _ := currentUserFromCtx(c)
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&ProbeTask{})
		if role == RoleUser {
			q = q.Where("owner_id = ?", uid)
		}
		var tasks []ProbeTask
		q.Find(&tasks)
		byID := map[uint]*ProbeTask{}
		var ids []uint
		for i := range tasks {
			byID[tasks[i].ID] = &tasks[i]
			ids = append(ids, tasks[i].ID)
		}
		step := stepForWindow(tr.Hours)
		if len(ids) == 0 {
			c.JSON(http.StatusOK, gin.H{"stepMinutes": step, "series": []gin.H{}})
			return
		}
		var rows []ProbeResult
		db.WithContext(ctx).Where("task_id IN ? AND source = ? AND created_at >= ? AND created_at <= ?",
			ids, sourceSched, tr.From, tr.To).Order("created_at asc").Find(&rows)
		c.JSON(http.StatusOK, gin.H{"stepMinutes": step, "series": rowsToSeries(byID, rows, step)})
	})

	admin.GET("/tasks/:id", func(c *gin.Context) {
		if t, ok := loadVisibleTask(c); ok {
			c.JSON(http.StatusOK, tasksWithOwner([]ProbeTask{*t})[0])
		}
	})

	admin.POST("/tasks", func(c *gin.Context) {
		if msg := emailVerifiedBlocked(c); msg != "" {
			apiError(c, http.StatusForbidden, msg)
			return
		}
		var t ProbeTask
		if err := c.ShouldBindJSON(&t); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		t.ID = 0
		if msg := validateTask(&t); msg != "" {
			apiError(c, http.StatusBadRequest, msg)
			return
		}
		uid, role, _ := currentUserFromCtx(c)
		ctx, cancel := dbCtx()
		defer cancel()
		if role == RoleUser {
			if USER_MIN_INTERVAL > 0 && t.Interval < USER_MIN_INTERVAL {
				apiError(c, http.StatusForbidden, fmt.Sprintf("普通用户任务间隔不能低于 %d 秒", USER_MIN_INTERVAL))
				return
			}
			if USER_TASK_LIMIT > 0 {
				var n int64
				db.WithContext(ctx).Model(&ProbeTask{}).Where("owner_id = ?", uid).Count(&n)
				if n >= int64(USER_TASK_LIMIT) {
					apiError(c, http.StatusForbidden, fmt.Sprintf("任务数已达普通用户上限（%d 个），请清理不需要的任务或联系管理员", USER_TASK_LIMIT))
					return
				}
			}
		}
		t.OwnerID = uid
		t.ShareToken = ""
		if err := db.WithContext(ctx).Create(&t).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, t)
	})

	admin.PUT("/tasks/:id", func(c *gin.Context) {
		t, ok := loadVisibleTask(c)
		if !ok {
			return
		}
		var in ProbeTask
		if err := c.ShouldBindJSON(&in); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		in.ID = t.ID
		if msg := validateTask(&in); msg != "" {
			apiError(c, http.StatusBadRequest, msg)
			return
		}
		if _, role, _ := currentUserFromCtx(c); role == RoleUser && USER_MIN_INTERVAL > 0 && in.Interval < USER_MIN_INTERVAL {
			apiError(c, http.StatusForbidden, fmt.Sprintf("普通用户任务间隔不能低于 %d 秒", USER_MIN_INTERVAL))
			return
		}
		// 编辑不改所有者 / 运行状态 / 分享令牌（启停与分享各有专用接口）
		in.CreatedAt, in.OwnerID, in.Enabled, in.ShareToken = t.CreatedAt, t.OwnerID, t.Enabled, t.ShareToken
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Save(&in).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, tasksWithOwner([]ProbeTask{in})[0])
	})

	admin.PATCH("/tasks/:id/enabled", func(c *gin.Context) {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			apiError(c, http.StatusBadRequest, "invalid body")
			return
		}
		if body.Enabled {
			if msg := emailVerifiedBlocked(c); msg != "" {
				apiError(c, http.StatusForbidden, msg)
				return
			}
		}
		t, ok := loadVisibleTask(c)
		if !ok {
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		res := db.WithContext(ctx).Model(&ProbeTask{}).Where("id = ?", t.ID).Update("enabled", body.Enabled)
		c.JSON(http.StatusOK, gin.H{"id": t.ID, "enabled": body.Enabled, "updated": res.RowsAffected > 0})
	})

	admin.DELETE("/tasks/:id", func(c *gin.Context) {
		t, ok := loadVisibleTask(c)
		if !ok {
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		db.WithContext(ctx).Where("task_id = ?", t.ID).Delete(&ProbeResult{})
		res := db.WithContext(ctx).Delete(&ProbeTask{}, t.ID)
		resetTaskAlerts(t.ID)
		c.JSON(http.StatusOK, gin.H{"deleted": res.RowsAffected > 0})
	})

	admin.GET("/tasks/:id/sla", func(c *gin.Context) {
		t, ok := loadVisibleTask(c)
		if !ok {
			return
		}
		tr := parseTimeRange(c, 24)
		resp, err := computeTaskSlaRange(t.ID, tr.From, tr.To)
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, resp)
	})

	admin.GET("/tasks/:id/series", func(c *gin.Context) {
		t, ok := loadVisibleTask(c)
		if !ok {
			return
		}
		tr := parseTimeRange(c, 24)
		node := strings.TrimSpace(c.Query("node"))
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Where("task_id = ? AND source = ? AND created_at >= ? AND created_at <= ?",
			t.ID, sourceSched, tr.From, tr.To)
		if node != "" {
			q = q.Where("node_id = ?", node)
		}
		var rows []ProbeResult
		q.Order("created_at asc, id asc").Find(&rows)
		resp := gin.H{"taskId": t.ID, "stepMinutes": stepForWindow(tr.Hours), "mode": "round", "series": rowsToRoundSeries(t, rows)}
		if node != "" {
			resp["node"] = node
		}
		c.JSON(http.StatusOK, resp)
	})

	// ---- 公开状态页分享：令牌即分享组 ----
	admin.POST("/tasks/share", func(c *gin.Context) {
		var body struct {
			IDs   []uint `json:"ids"`
			Token string `json:"token"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || len(body.IDs) == 0 {
			apiError(c, http.StatusBadRequest, "ids 不能为空")
			return
		}
		token := strings.ToLower(strings.TrimSpace(body.Token))
		if token == "" {
			token = randHex(3)
		}
		if len(token) < 3 || len(token) > 32 {
			apiError(c, http.StatusBadRequest, "token 长度须为 3~32 位")
			return
		}
		for _, ch := range token {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				apiError(c, http.StatusBadRequest, "token 只能包含小写字母、数字、连字符")
				return
			}
		}
		uid, role, _ := currentUserFromCtx(c)
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&ProbeTask{}).Where("id IN ?", body.IDs)
		if role == RoleUser {
			q = q.Where("owner_id = ?", uid)
		}
		res := q.Update("share_token", token)
		if res.Error != nil {
			apiError(c, http.StatusInternalServerError, res.Error.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"token": token, "url": "/s/" + token, "shared": res.RowsAffected, "ids": body.IDs})
	})

	admin.DELETE("/tasks/share", func(c *gin.Context) {
		var body struct {
			IDs []uint `json:"ids"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || len(body.IDs) == 0 {
			apiError(c, http.StatusBadRequest, "ids 不能为空")
			return
		}
		uid, role, _ := currentUserFromCtx(c)
		ctx, cancel := dbCtx()
		defer cancel()
		q := db.WithContext(ctx).Model(&ProbeTask{}).Where("id IN ?", body.IDs)
		if role == RoleUser {
			q = q.Where("owner_id = ?", uid)
		}
		res := q.Update("share_token", "")
		c.JSON(http.StatusOK, gin.H{"closed": res.RowsAffected})
	})

	admin.GET("/tasks/:id/share", func(c *gin.Context) {
		t, ok := loadVisibleTask(c)
		if !ok {
			return
		}
		if t.ShareToken == "" {
			c.JSON(http.StatusOK, gin.H{"enabled": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{"enabled": true, "token": t.ShareToken, "url": "/s/" + t.ShareToken})
	})
}

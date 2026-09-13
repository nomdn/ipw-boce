package main

// ==================== 上游节点池：数据库托管（替代 setting.json 静态配置） ====================
//
// 原节点池读自 setting.json 的 api-base-url（三栈）与 ip-location-api（纯数组），改一次要改文件 + 重启。
// 现改由管理员在控制台「配置分发 → 上游节点池」录入，存 node_defs 表，保存即热更新全局池。
//
// 迁移策略（按池分别判定，避免老部署一升级就空池）：
//   - 某池在库里有任意记录 → 由库接管，覆盖 setting.json 的同名池（该池全停用即空池，属管理员明确意图）
//   - 某池一条记录都没有 → 保留 setting.json 的原始值作为兜底

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// 归属池与三栈分组取值（与 setting.json 的 api-base-url 键名保持一致）
//
// 一个节点可同时归属多个池：Pool 以逗号分隔存储，如 "api" / "location" / "api,location"。
// 双归属节点在转发时按 apiType 选池，location/asn 走 location 池，其余走 api 池。
const (
	poolAPI      = "api"
	poolLocation = "location"

	stackDualStack = "DualStack"
	stackIPv4      = "IPv4"
	stackIPv6      = "IPv6"
)

// parsePools 拆分存储的 pool 字段（"api,location" → ["api","location"]）
func parsePools(raw string) []string {
	out := make([]string, 0, 2)
	for _, seg := range strings.Split(raw, ",") {
		v := strings.ToLower(strings.TrimSpace(seg))
		if v == poolAPI || v == poolLocation {
			out = append(out, v)
		}
	}
	return out
}

// poolHas 判断节点是否归属某池
func poolHas(pools []string, want string) bool {
	for _, p := range pools {
		if p == want {
			return true
		}
	}
	return false
}

// nodePoolMu 保护全局节点池：管理员保存时持写锁，转发/拨测/探活读取时持读锁
var nodePoolMu sync.RWMutex

// apiPoolSnapshot 返回 api 池（三栈平铺）快照；与管理员热更新互斥
func apiPoolSnapshot() []apiInfo {
	nodePoolMu.RLock()
	defer nodePoolMu.RUnlock()
	return flattenStack(API_BASE_URLS)
}

// locationPoolSnapshot 返回 location/asn 池快照
func locationPoolSnapshot() []apiInfo {
	nodePoolMu.RLock()
	defer nodePoolMu.RUnlock()
	out := make([]apiInfo, len(IP_LOCATION_APIS))
	copy(out, IP_LOCATION_APIS)
	return out
}

// loadNodePoolsFromDB 进程启动时用数据库节点定义重建节点池（库未就绪则沿用 setting.json）
func loadNodePoolsFromDB() {
	if db == nil {
		return
	}
	if err := reloadNodePools(); err != nil {
		log.Printf("[node-defs] WARN load node pool from db, fallback to setting.json: %v", err)
	}
}

// reloadNodePools 重读 node_defs 并重建全局节点池（管理员增删改后热更新，无需重启）
func reloadNodePools() error {
	if db == nil {
		return errors.New("数据库未就绪")
	}
	ctx, cancel := dbCtx()
	defer cancel()
	var rows []NodeDef
	if err := db.WithContext(ctx).Order("sort_order asc, id asc").Find(&rows).Error; err != nil {
		return err
	}
	applyNodeDefs(rows)
	return nil
}

// applyNodeDefs 用节点定义重建全局池：某池在库有记录才接管，否则保留 setting.json 兜底值。
// 同步刷新看门狗监控清单，使新节点立即纳入掉线监控、删除的节点立即移出。
func applyNodeDefs(rows []NodeDef) {
	var apiStack stackConfig
	var locationPool []apiInfo
	// 计数含停用节点：只要该池被管理员接管过，就完全按库走
	totalAPI, totalLocation := 0, 0

	for _, row := range rows {
		pools := parsePools(row.Pool)
		if len(pools) == 0 {
			pools = []string{poolAPI} // 历史脏数据兜底
		}
		inAPI, inLocation := poolHas(pools, poolAPI), poolHas(pools, poolLocation)
		if inAPI {
			totalAPI++
		}
		if inLocation {
			totalLocation++
		}
		if !row.Enabled {
			continue // 停用节点不进池（两个池都不进）
		}
		item := apiInfo{
			Label: row.Label,
			ID:    row.NodeID,
			URL:   strings.TrimSpace(row.URL),
			WS:    wsFlag(row.WS),
		}
		// 双归属节点可同时进两个池，转发时按 apiType 选池
		if inLocation {
			locationPool = append(locationPool, item)
		}
		if inAPI {
			switch row.Stack {
			case stackIPv4:
				apiStack.IPv4 = append(apiStack.IPv4, item)
			case stackIPv6:
				apiStack.IPv6 = append(apiStack.IPv6, item)
			default:
				apiStack.DualStack = append(apiStack.DualStack, item)
			}
		}
	}

	nodePoolMu.Lock()
	if totalAPI > 0 {
		API_BASE_URLS = apiStack
	}
	if totalLocation > 0 {
		IP_LOCATION_APIS = locationPool
	}
	nodePoolMu.Unlock()

	refreshWatchedNodes()
}

// ==================== 已接入但未配置的节点（控制台补录下拉框数据源） ====================

// onlineCandidate 已接入本中间件、但库里没有对应节点定义的候选节点
type onlineCandidate struct {
	NodeID     string     `json:"nodeId"`
	Label      string     `json:"label"`
	RemoteAddr string     `json:"remoteAddr,omitempty"`
	ViaWS      bool       `json:"viaWs"`                // 当前是否持有 WS 长连接
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"` // 最近一次在线时间（WS 实时连接可能为空）
}

// unconfiguredOnlineNodes 汇总"已接入但数据库无节点定义"的在线节点。
// 取两处来源的并集：WS 实时连接（wsSrv.peers）+ nodes 表在线快照（HTTP 探活 / 历史 WS 记录）。
func unconfiguredOnlineNodes() ([]onlineCandidate, error) {
	if db == nil {
		return nil, errors.New("数据库未就绪")
	}
	ctx, cancel := dbCtx()
	defer cancel()

	// 库里已定义的节点（含停用）不算"未配置"
	var defs []NodeDef
	if err := db.WithContext(ctx).Select("node_id").Find(&defs).Error; err != nil {
		return nil, err
	}
	configured := make(map[string]bool, len(defs))
	for _, d := range defs {
		configured[d.NodeID] = true
	}

	candidates := make(map[string]*onlineCandidate)

	// 在线快照
	var snapshots []Node
	if err := db.WithContext(ctx).Where("online = ?", true).Order("node_id asc").Find(&snapshots).Error; err != nil {
		return nil, err
	}
	for _, s := range snapshots {
		if configured[s.NodeID] {
			continue
		}
		last := s.LastSeenAt
		candidates[s.NodeID] = &onlineCandidate{
			NodeID: s.NodeID, Label: s.Label, RemoteAddr: s.RemoteAddr, LastSeenAt: &last,
		}
	}

	// WS 实时连接（可能尚未落快照，或快照已过期）
	if wsSrv != nil {
		wsSrv.mu.Lock()
		for id := range wsSrv.peers {
			if configured[id] {
				continue
			}
			if found, ok := candidates[id]; ok {
				found.ViaWS = true
				continue
			}
			candidates[id] = &onlineCandidate{NodeID: id, ViaWS: true}
		}
		wsSrv.mu.Unlock()
	}

	out := make([]onlineCandidate, 0, len(candidates))
	for _, item := range candidates {
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out, nil
}

// ==================== 入参校验 ====================

// nodeDefInput 节点定义的新增/更新入参（指针字段用于区分"未传"与"显式 false"）
type nodeDefInput struct {
	NodeID    string   `json:"nodeId"`
	Label     string   `json:"label"`
	URL       string   `json:"url"`
	WS        *bool    `json:"ws"`
	Pool      string   `json:"pool"`       // 逗号分隔多选："api" / "location" / "api,location"
	Pools     []string `json:"pools"`      // 数组形式，与 pool 二选一；两者都给时以 pools 为准
	Stack     string   `json:"stack"`
	Enabled   *bool    `json:"enabled"`
	SortOrder *int     `json:"sortOrder"`
}

// normalize 校验并规整入参，返回规整后的 pool 与 stack。
// fallbackPool：仅传了部分字段的更新请求用它兜底（不传 pool 时沿用原归属），新增时传空串（缺省 api）。
// 规则：pool 可多选（api / location 任意组合，缺省 api）；
// 纯 location 节点不区分栈；ws=false 时 url 必填；nodeId 不得为 global（该名保留给全局远端配置）。
func (in *nodeDefInput) normalize(fallbackPool string) (pool string, stack string, err error) {
	in.NodeID = strings.TrimSpace(in.NodeID)
	if in.NodeID == "" {
		return "", "", errors.New("nodeId 不能为空")
	}
	if in.NodeID == "global" {
		return "", "", errors.New("nodeId 不能为 global（该名字保留给全局远端配置）")
	}

	pools, err := normalizePools(in.Pools, in.Pool, fallbackPool)
	if err != nil {
		return "", "", err
	}
	pool = strings.Join(pools, ",")

	in.URL = strings.TrimSpace(in.URL)
	useWS := in.WS != nil && *in.WS
	if !useWS && in.URL == "" {
		return "", "", errors.New("HTTP 节点（ws=false）必须填写 url")
	}

	// 不归属 api 池则无需栈分组（location 池是纯数组）
	if !poolHas(pools, poolAPI) {
		return pool, "", nil
	}
	stack = strings.TrimSpace(in.Stack)
	switch stack {
	case "":
		stack = stackDualStack
	case stackDualStack, stackIPv4, stackIPv6:
	default:
		return "", "", errors.New("stack 只能是 DualStack / IPv4 / IPv6")
	}
	return pool, stack, nil
}

// normalizePools 规整归属池：去重 + 校验 + 固定顺序（api 在前），空则回退 fallbackPool。
// 入参优先级：pools 数组 > pool 字符串（逗号分隔）> fallbackPool > 缺省 api。
func normalizePools(list []string, raw string, fallback string) ([]string, error) {
	tokens := make([]string, 0, 2)
	if len(list) > 0 {
		for _, item := range list {
			tokens = append(tokens, strings.Split(item, ",")...)
		}
	} else {
		tokens = append(tokens, strings.Split(raw, ",")...)
	}

	seen := map[string]bool{}
	out := make([]string, 0, 2)
	for _, token := range tokens {
		v := strings.ToLower(strings.TrimSpace(token))
		if v == "" {
			continue
		}
		if v != poolAPI && v != poolLocation {
			return nil, errors.New("pool 只能是 api / location（可多选，如 api,location）")
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) > 0 {
		sort.Strings(out) // api < location，保证存储顺序稳定
		return out, nil
	}
	if strings.TrimSpace(fallback) != "" {
		if fb := parsePools(fallback); len(fb) > 0 {
			return fb, nil
		}
	}
	return []string{poolAPI}, nil
}

// ==================== 路由 ====================

// registerNodeDefRoutes 注册 /admin/node-defs（admin only）：节点池 CRUD + 未配置在线节点查询
func registerNodeDefRoutes(group *gin.RouterGroup) {
	// 已接入但未配置的节点（下拉框数据源）；注册在 /:id 之前，避免被通配段吃掉
	group.GET("/node-defs/online", func(c *gin.Context) {
		list, err := unconfiguredOnlineNodes()
		if err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, list)
	})

	// 节点定义列表
	group.GET("/node-defs", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var rows []NodeDef
		if err := db.WithContext(ctx).Order("sort_order asc, id asc").Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, rows)
	})

	// 新增节点
	group.POST("/node-defs", func(c *gin.Context) {
		var in nodeDefInput
		if err := c.ShouldBindJSON(&in); err != nil {
			apiError(c, http.StatusBadRequest, "请求体须为合法 JSON："+err.Error())
			return
		}
		pool, stack, err := in.normalize("")
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()

		// nodeId 唯一（转发路径靠它寻址，重复会导致歧义）
		var dup int64
		if err := db.WithContext(ctx).Model(&NodeDef{}).Where("node_id = ?", in.NodeID).Count(&dup).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		if dup > 0 {
			apiError(c, http.StatusConflict, "节点 "+in.NodeID+" 已存在")
			return
		}

		row := NodeDef{
			NodeID:    in.NodeID,
			Label:     strings.TrimSpace(in.Label),
			URL:       in.URL,
			WS:        in.WS != nil && *in.WS,
			Pool:      pool,
			Stack:     stack,
			Enabled:   in.Enabled == nil || *in.Enabled, // 未传默认启用
			SortOrder: derefInt(in.SortOrder),
		}
		if err := db.WithContext(ctx).Create(&row).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		applyPoolChange(c, row.NodeID)
		c.JSON(http.StatusOK, row)
	})

	// 更新节点（只覆盖传了的字段）
	group.PUT("/node-defs/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil || id <= 0 {
			apiError(c, http.StatusBadRequest, "id 非法")
			return
		}
		var in nodeDefInput
		if err := c.ShouldBindJSON(&in); err != nil {
			apiError(c, http.StatusBadRequest, "请求体须为合法 JSON："+err.Error())
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()

		var row NodeDef
		if err := db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
			apiError(c, http.StatusNotFound, "节点不存在")
			return
		}
		// 未传 nodeId 时沿用原值（便于只改启用状态）
		if strings.TrimSpace(in.NodeID) == "" {
			in.NodeID = row.NodeID
		}
		// url / ws 允许只改其一，故用现有值补全后再校验
		if strings.TrimSpace(in.URL) == "" {
			in.URL = row.URL
		}
		if in.WS == nil {
			ws := row.WS
			in.WS = &ws
		}
		// stack 同理：只改其他字段时保留原栈分组
		if strings.TrimSpace(in.Stack) == "" {
			in.Stack = row.Stack
		}
		// 未传 pool/pools 时沿用原归属，避免只改启用状态就把节点踢出 location 池
		pool, stack, err := in.normalize(row.Pool)
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		// 改 nodeId 时校验唯一性
		if in.NodeID != row.NodeID {
			var dup int64
			if err := db.WithContext(ctx).Model(&NodeDef{}).Where("node_id = ?", in.NodeID).Count(&dup).Error; err != nil {
				apiError(c, http.StatusInternalServerError, err.Error())
				return
			}
			if dup > 0 {
				apiError(c, http.StatusConflict, "节点 "+in.NodeID+" 已存在")
				return
			}
		}

		updates := map[string]any{
			"node_id":    in.NodeID,
			"url":        in.URL,
			"ws":         *in.WS,
			"pool":       pool,
			"stack":      stack,
			"sort_order": derefInt(in.SortOrder),
			"updated_at": time.Now().UTC(),
		}
		if label := strings.TrimSpace(in.Label); label != "" {
			updates["label"] = label
		}
		if in.Enabled != nil {
			updates["enabled"] = *in.Enabled
		}
		if err := db.WithContext(ctx).Model(&NodeDef{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		var fresh NodeDef
		_ = db.WithContext(ctx).Where("id = ?", id).First(&fresh).Error
		applyPoolChange(c, in.NodeID)
		c.JSON(http.StatusOK, fresh)
	})

	// 删除节点
	group.DELETE("/node-defs/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil || id <= 0 {
			apiError(c, http.StatusBadRequest, "id 非法")
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		res := db.WithContext(ctx).Where("id = ?", id).Delete(&NodeDef{})
		if res.Error != nil {
			apiError(c, http.StatusInternalServerError, res.Error.Error())
			return
		}
		applyPoolChange(c, "")
		c.JSON(http.StatusOK, gin.H{"id": id, "deleted": res.RowsAffected > 0})
	})
}

// applyPoolChange 落库后热更新节点池；失败仅记日志并回 warn，不回滚已保存的数据
func applyPoolChange(c *gin.Context, nodeID string) {
	if err := reloadNodePools(); err != nil {
		log.Printf("[node-defs] WARN reload node pool after change(%s): %v", nodeID, err)
		if c != nil {
			c.Header("X-Node-Pool-Reload", "failed")
		}
	}
}

// derefInt 取指针值，nil 视为 0
func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

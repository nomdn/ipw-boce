package main

// ==================== 上游节点池（node_defs 表托管，保存即热更新） ====================
//
// 迁移策略（按池分别判定）：某池在库里有任意记录 → 由库接管；一条都没有 → 沿用 setting.json。
// 一个节点可同时归属 api 与 location 两池（Pool 逗号分隔）；api 池按 DualStack/IPv4/IPv6 分组。

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

const (
	poolAPI      = "api"
	poolLocation = "location"

	stackDualStack = "DualStack"
	stackIPv4      = "IPv4"
	stackIPv6      = "IPv6"
)

var (
	nodePoolMu   sync.RWMutex
	apiPool      []apiInfo // api 池（三栈平铺，Stack 已填）
	locationPool []apiInfo
	settingAPI   []apiInfo // setting.json 兜底值（启动时快照）
	settingLoc   []apiInfo

	nodeCredMu sync.RWMutex
	nodeCreds  = map[string]NodeDef{} // nodeId → 凭据（APIKey/WSKey）
)

func apiPoolSnapshot() []apiInfo {
	nodePoolMu.RLock()
	defer nodePoolMu.RUnlock()
	return append([]apiInfo(nil), apiPool...)
}

func locationPoolSnapshot() []apiInfo {
	nodePoolMu.RLock()
	defer nodePoolMu.RUnlock()
	return append([]apiInfo(nil), locationPool...)
}

// nodePoolForType location/asn 走 location 池，其余走 api 池
func nodePoolForType(apiType string) []apiInfo {
	if apiType == "location" || apiType == "asn" {
		return locationPoolSnapshot()
	}
	return apiPoolSnapshot()
}

func findNode(list []apiInfo, id string) *apiInfo {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// allPoolNodes 两池去重合并（探活/展示用）
func allPoolNodes() []apiInfo {
	seen := map[string]bool{}
	var out []apiInfo
	for _, n := range append(apiPoolSnapshot(), locationPoolSnapshot()...) {
		if !seen[n.ID] {
			seen[n.ID] = true
			out = append(out, n)
		}
	}
	return out
}

func poolLabelMap() map[string]string {
	m := map[string]string{}
	for _, n := range allPoolNodes() {
		if n.Label != "" {
			m[n.ID] = n.Label
		}
	}
	return m
}

func poolLabelFor(id string) string { return poolLabelMap()[id] }

func parsePools(raw string) []string {
	var out []string
	for _, seg := range strings.Split(raw, ",") {
		v := strings.ToLower(strings.TrimSpace(seg))
		if (v == poolAPI || v == poolLocation) && !contains(out, v) {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func initNodePools() {
	settingAPI = flattenStack(API_BASE_URLS)
	settingLoc = append([]apiInfo(nil), IP_LOCATION_APIS...)
	if err := reloadNodePools(); err != nil {
		log.Printf("[node-defs] WARN load from db: %v", err)
	}
}

// reloadNodePools 重读 node_defs 重建全局池与凭据索引
func reloadNodePools() error {
	ctx, cancel := dbCtx()
	defer cancel()
	var rows []NodeDef
	if err := db.WithContext(ctx).Order("sort_order asc, id asc").Find(&rows).Error; err != nil {
		return err
	}
	var api, loc []apiInfo
	totalAPI, totalLoc := 0, 0
	creds := map[string]NodeDef{}
	for _, r := range rows {
		if r.APIKey != "" || r.WSKey != "" {
			creds[r.NodeID] = r
		}
		pools := parsePools(r.Pool)
		if len(pools) == 0 {
			pools = []string{poolAPI}
		}
		inAPI, inLoc := contains(pools, poolAPI), contains(pools, poolLocation)
		if inAPI {
			totalAPI++
		}
		if inLoc {
			totalLoc++
		}
		if !r.Enabled {
			continue
		}
		item := apiInfo{Label: r.Label, ID: r.NodeID, URL: strings.TrimSpace(r.URL), WS: wsFlag(r.WS), Stack: r.Stack}
		if inLoc {
			loc = append(loc, item)
		}
		if inAPI {
			if item.Stack != stackIPv4 && item.Stack != stackIPv6 {
				item.Stack = stackDualStack
			}
			api = append(api, item)
		}
	}
	// 待审核的捐赠节点：凭据先生效（可连上来注册、看到在线），但不进节点池
	var pending []NodeDonation
	db.WithContext(ctx).Where("status = ?", DonPending).Find(&pending)
	for _, d := range pending {
		if _, ok := creds[d.NodeID]; !ok {
			creds[d.NodeID] = NodeDef{NodeID: d.NodeID, WSKey: d.WSKey, APIKey: d.APIKey}
		}
	}
	nodePoolMu.Lock()
	apiPool, locationPool = settingAPI, settingLoc
	if totalAPI > 0 {
		apiPool = api
	}
	if totalLoc > 0 {
		locationPool = loc
	}
	nodePoolMu.Unlock()
	nodeCredMu.Lock()
	nodeCreds = creds
	nodeCredMu.Unlock()

	// 名称同步到 nodes 快照
	for _, r := range rows {
		if r.Label != "" {
			db.WithContext(ctx).Model(&Node{}).Where("node_id = ? AND label <> ?", r.NodeID, r.Label).Update("label", r.Label)
		}
	}
	return nil
}

// lookupAPIKey 库托管 > setting.json api-keys
func lookupAPIKey(id string) string {
	nodeCredMu.RLock()
	k := nodeCreds[id].APIKey
	nodeCredMu.RUnlock()
	if k != "" {
		return k
	}
	return API_KEYS[id]
}

// lookupWSKey 库托管 > setting.json ws-keys
func lookupWSKey(id string) string {
	nodeCredMu.RLock()
	k := nodeCreds[id].WSKey
	nodeCredMu.RUnlock()
	if k != "" {
		return k
	}
	return WS_KEYS[id]
}

// ==================== /admin/node-defs CRUD（admin only） ====================

type nodeDefInput struct {
	NodeID    string   `json:"nodeId"`
	Label     string   `json:"label"`
	URL       string   `json:"url"`
	WS        *bool    `json:"ws"`
	Pool      string   `json:"pool"`
	Pools     []string `json:"pools"`
	Stack     string   `json:"stack"`
	Enabled   *bool    `json:"enabled"`
	SortOrder *int     `json:"sortOrder"`
	APIKey    *string  `json:"apiKey"`
	WSKey     *string  `json:"wsKey"`
}

func (in *nodeDefInput) normalize(fallbackPool string) (string, string, error) {
	in.NodeID = strings.TrimSpace(in.NodeID)
	if in.NodeID == "" {
		return "", "", errors.New("nodeId 不能为空")
	}
	if in.NodeID == "global" {
		return "", "", errors.New("nodeId 不能为 global（该名字保留给全局远端配置）")
	}
	raw := in.Pool
	if len(in.Pools) > 0 {
		raw = strings.Join(in.Pools, ",")
	}
	for _, t := range strings.Split(raw, ",") {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" && t != poolAPI && t != poolLocation {
			return "", "", errors.New("pool 只能是 api / location（可多选，如 api,location）")
		}
	}
	pools := parsePools(raw)
	if len(pools) == 0 {
		pools = parsePools(fallbackPool)
	}
	if len(pools) == 0 {
		pools = []string{poolAPI}
	}
	in.URL = strings.TrimSpace(in.URL)
	if !(in.WS != nil && *in.WS) && in.URL == "" {
		return "", "", errors.New("HTTP 节点（ws=false）必须填写 url")
	}
	if !contains(pools, poolAPI) {
		return strings.Join(pools, ","), "", nil
	}
	stack := strings.TrimSpace(in.Stack)
	switch stack {
	case "":
		stack = stackDualStack
	case stackDualStack, stackIPv4, stackIPv6:
	default:
		return "", "", errors.New("stack 只能是 DualStack / IPv4 / IPv6")
	}
	return strings.Join(pools, ","), stack, nil
}

func registerNodeDefRoutes(g *gin.RouterGroup) {
	g.GET("/node-defs/online", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var ids []string
		db.WithContext(ctx).Model(&NodeDef{}).Pluck("node_id", &ids)
		configured := map[string]bool{}
		for _, id := range ids {
			configured[id] = true
		}
		out := []gin.H{}
		seen := map[string]bool{}
		var snaps []Node
		db.WithContext(ctx).Where("online = ?", true).Order("node_id asc").Find(&snaps)
		for _, s := range snaps {
			if !configured[s.NodeID] {
				seen[s.NodeID] = true
				out = append(out, gin.H{"nodeId": s.NodeID, "label": s.Label, "remoteAddr": s.RemoteAddr,
					"viaWs": wsSrv != nil && wsSrv.NodeConnected(s.NodeID), "lastSeenAt": s.LastSeenAt})
			}
		}
		if wsSrv != nil {
			for _, id := range wsSrv.PeerIDs() {
				if !configured[id] && !seen[id] {
					out = append(out, gin.H{"nodeId": id, "viaWs": true})
				}
			}
		}
		c.JSON(http.StatusOK, out)
	})

	g.GET("/node-defs", func(c *gin.Context) {
		ctx, cancel := dbCtx()
		defer cancel()
		var rows []NodeDef
		if err := db.WithContext(ctx).Order("sort_order asc, id asc").Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		for i := range rows {
			rows[i].HasAPIKey, rows[i].HasWSKey = rows[i].APIKey != "", rows[i].WSKey != ""
		}
		c.JSON(http.StatusOK, rows)
	})

	g.POST("/node-defs", func(c *gin.Context) {
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
		row := NodeDef{NodeID: in.NodeID, Label: strings.TrimSpace(in.Label), URL: in.URL,
			WS: in.WS != nil && *in.WS, Pool: pool, Stack: stack,
			Enabled: in.Enabled == nil || *in.Enabled, SortOrder: derefInt(in.SortOrder)}
		if in.APIKey != nil {
			row.APIKey = strings.TrimSpace(*in.APIKey)
		}
		if in.WSKey != nil {
			row.WSKey = strings.TrimSpace(*in.WSKey)
		}
		ctx, cancel := dbCtx()
		defer cancel()
		if err := db.WithContext(ctx).Create(&row).Error; err != nil {
			if isUniqueErr(err) {
				apiError(c, http.StatusConflict, "节点 "+in.NodeID+" 已存在")
				return
			}
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		row.HasAPIKey, row.HasWSKey = row.APIKey != "", row.WSKey != ""
		applyPoolChange(c)
		c.JSON(http.StatusOK, row)
	})

	g.PUT("/node-defs/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		var in nodeDefInput
		if err := c.ShouldBindJSON(&in); err != nil {
			apiError(c, http.StatusBadRequest, "请求体须为合法 JSON："+err.Error())
			return
		}
		ctx, cancel := dbCtx()
		defer cancel()
		var row NodeDef
		if err := db.WithContext(ctx).First(&row, id).Error; err != nil {
			apiError(c, http.StatusNotFound, "节点不存在")
			return
		}
		if strings.TrimSpace(in.NodeID) == "" {
			in.NodeID = row.NodeID
		}
		if strings.TrimSpace(in.URL) == "" {
			in.URL = row.URL
		}
		if in.WS == nil {
			ws := row.WS
			in.WS = &ws
		}
		if strings.TrimSpace(in.Stack) == "" {
			in.Stack = row.Stack
		}
		if in.SortOrder == nil {
			in.SortOrder = &row.SortOrder
		}
		pool, stack, err := in.normalize(row.Pool)
		if err != nil {
			apiError(c, http.StatusBadRequest, err.Error())
			return
		}
		updates := map[string]any{"node_id": in.NodeID, "url": in.URL, "ws": *in.WS, "pool": pool,
			"stack": stack, "sort_order": *in.SortOrder, "updated_at": time.Now().UTC()}
		if in.APIKey != nil {
			updates["api_key"] = strings.TrimSpace(*in.APIKey)
		}
		if in.WSKey != nil {
			updates["ws_key"] = strings.TrimSpace(*in.WSKey)
		}
		if l := strings.TrimSpace(in.Label); l != "" {
			updates["label"] = l
		}
		if in.Enabled != nil {
			updates["enabled"] = *in.Enabled
		}
		if err := db.WithContext(ctx).Model(&NodeDef{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			if isUniqueErr(err) {
				apiError(c, http.StatusConflict, "节点 "+in.NodeID+" 已存在")
				return
			}
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		var fresh NodeDef
		db.WithContext(ctx).First(&fresh, id)
		fresh.HasAPIKey, fresh.HasWSKey = fresh.APIKey != "", fresh.WSKey != ""
		applyPoolChange(c)
		c.JSON(http.StatusOK, fresh)
	})

	g.DELETE("/node-defs/:id", func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		ctx, cancel := dbCtx()
		defer cancel()
		var row NodeDef
		found := db.WithContext(ctx).First(&row, id).Error == nil
		res := db.WithContext(ctx).Delete(&NodeDef{}, id)
		if res.Error != nil {
			apiError(c, http.StatusInternalServerError, res.Error.Error())
			return
		}
		if found && row.DonationID != 0 {
			// 捐赠节点被管理员从池里删除：对应捐赠记为停用，停止每日奖励
			db.WithContext(ctx).Model(&NodeDonation{}).Where("id = ? AND status = ?", row.DonationID, DonApproved).
				Updates(map[string]any{"status": DonDisabled, "review_note": "管理员从节点池移除", "reviewed_by": currentUsername(c)})
		}
		applyPoolChange(c)
		if found {
			db.WithContext(ctx).Where("node_id = ?", row.NodeID).Delete(&Node{})
		}
		c.JSON(http.StatusOK, gin.H{"id": id, "deleted": res.RowsAffected > 0})
	})
}

func applyPoolChange(c *gin.Context) {
	if err := reloadNodePools(); err != nil {
		log.Printf("[node-defs] WARN reload pool: %v", err)
		c.Header("X-Node-Pool-Reload", "failed")
	}
	// 新增/修改的 HTTP 节点立刻探活一次，不用等 10 分钟的看门狗周期
	go checkHTTPNodes()
}

// ==================== 节点同步出口（给 1IPW 主站后端用） ====================
//
// GET /api/sync/nodes   Authorization: Bearer <sync-token>
// 返回启用节点的完整接入信息（含 HTTP 地址与 API Key，WS 节点给出经本中心转发的地址），
// 1IPW 主站后端定时拉取，前端与主站后端都不再写死节点表。
// sync-token 未配置时该接口关闭（404）。

func registerSyncRoutes(r *gin.Engine) {
	r.GET("/api/sync/nodes", func(c *gin.Context) {
		if SYNC_TOKEN == "" {
			restError(c, http.StatusNotFound, "not_found", "sync disabled")
			return
		}
		if bearerToken(c) != SYNC_TOKEN {
			restError(c, http.StatusUnauthorized, "unauthorized", "invalid sync token")
			return
		}
		online := onlineNodeSet()
		snaps := map[string]Node{}
		{
			ctx, cancel := dbCtx()
			var rows []Node
			db.WithContext(ctx).Find(&rows)
			cancel()
			for _, s := range rows {
				snaps[s.NodeID] = s
			}
		}
		pools := map[string][]string{}
		for _, n := range apiPoolSnapshot() {
			pools[n.ID] = append(pools[n.ID], poolAPI)
		}
		for _, n := range locationPoolSnapshot() {
			pools[n.ID] = append(pools[n.ID], poolLocation)
		}
		out := make([]gin.H, 0)
		for _, n := range allPoolNodes() {
			stack := n.Stack
			if stack == "" {
				stack = stackDualStack
			}
			caps := splitAndTrim(snaps[n.ID].Capabilities, ",")
			if caps == nil {
				caps = []string{}
			}
			item := gin.H{"id": n.ID, "label": n.Label, "stack": stack, "pools": pools[n.ID], "online": online[n.ID], "ws": n.UseWS(),
				"capabilities": caps, "version": snaps[n.ID].Version}
			if n.UseWS() {
				// WS 节点没有公网地址：经本中心 /v1/<id>/... 转发
				item["url"] = "{self}/v1/" + n.ID + "/{path}"
			} else {
				item["url"] = n.URL
				if k := lookupAPIKey(n.ID); k != "" {
					item["apiKey"] = k
				}
			}
			out = append(out, item)
		}
		c.JSON(http.StatusOK, gin.H{"nodes": out, "generatedAt": time.Now().UTC()})
	})
}

// nodeBriefItems 节点池简表（enabled 节点 + 在线/版本，脱敏）
func nodeBriefItems() []gin.H {
	ctx, cancel := dbCtx()
	defer cancel()
	var snaps []Node
	db.WithContext(ctx).Find(&snaps)
	byID := map[string]*Node{}
	for i := range snaps {
		byID[snaps[i].NodeID] = &snaps[i]
	}
	pools := map[string][]string{}
	for _, n := range apiPoolSnapshot() {
		pools[n.ID] = append(pools[n.ID], poolAPI)
	}
	for _, n := range locationPoolSnapshot() {
		pools[n.ID] = append(pools[n.ID], poolLocation)
	}
	out := make([]gin.H, 0)
	for _, n := range allPoolNodes() {
		item := gin.H{"nodeId": n.ID, "label": n.Label, "ws": n.UseWS(), "pools": strings.Join(pools[n.ID], ","),
			"stack": n.Stack, "online": false, "version": ""}
		if s := byID[n.ID]; s != nil {
			item["online"], item["version"] = s.Online, s.Version
		}
		out = append(out, item)
	}
	return out
}

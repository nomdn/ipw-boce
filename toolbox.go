package main

// ==================== 网络查询工具箱转发（给 1IPW 主站后端用） ====================
//
// POST /toolbox/:nodeId/:path   Authorization: Bearer <sync-token>
// 捐赠节点是 WS 节点、没有公网地址，1IPW 的网络查询工具箱经这里把 POST 探测转给节点：
// 中心经 WS 下发 probe{apiType:"toolbox", raw:path, body}，节点进程内执行 POST /v1/<path> 后回传。
// 只转发节点池里的 WS 节点、白名单路径，请求体上限 8KB；状态码与 body 原样返回。

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	apiTypeToolbox   = "toolbox"
	maxToolboxBody   = 8 * 1024
	toolboxTimeout   = 45 * time.Second
	toolboxTraceTime = 90 * time.Second
)

var toolboxPaths = map[string]bool{
	"http-test": true, "tcp-test": true, "udp-test": true, "traceroute": true,
	"dns-query": true, "dnssec-query": true, "asn": true, "whois": true,
	"email-security": true, "rbl": true, "cdn": true, "security-headers": true,
}

func registerToolboxRoutes(r *gin.Engine) {
	r.POST("/toolbox/:nodeId/:path", func(c *gin.Context) {
		if SYNC_TOKEN == "" {
			c.JSON(http.StatusNotFound, gin.H{"error": "toolbox disabled"})
			return
		}
		if bearerToken(c) != SYNC_TOKEN {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid sync token"})
			return
		}
		path := strings.ToLower(c.Param("path"))
		if !toolboxPaths[path] {
			c.JSON(http.StatusNotFound, gin.H{"error": "unknown toolbox path"})
			return
		}
		id := c.Param("nodeId")
		n := findNode(apiPoolSnapshot(), id)
		if n == nil {
			n = findNode(locationPoolSnapshot(), id)
		}
		if n == nil || !n.UseWS() {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		if wsSrv == nil || !wsSrv.NodeConnected(id) {
			c.JSON(http.StatusBadGateway, gin.H{"error": "node offline"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxToolboxBody+1))
		if err != nil || len(body) > maxToolboxBody {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "query payload is too large"})
			return
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			body = []byte("{}")
		}
		timeout := toolboxTimeout
		if path == "traceroute" {
			timeout = toolboxTraceTime
		}
		status, out, err := wsSrv.RequestToolbox(id, path, body, timeout)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "node did not respond"})
			return
		}
		if status == 0 {
			status = http.StatusBadGateway
		}
		c.Header("Cache-Control", "no-store")
		c.Data(status, "application/json; charset=utf-8", out)
	})
}

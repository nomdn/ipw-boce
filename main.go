package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var startedAt = time.Now()

func main() {
	for _, a := range os.Args[1:] {
		if a == "-v" || a == "--version" || a == "version" {
			fmt.Println("1IPW-BOCE MIDDLEWARE VERSION " + VERSION)
			fmt.Println("COMMIT " + COMMIT)
			fmt.Println("BUILD_TIME " + BUILD_TIME)
			return
		}
	}
	if err := readConfig(); err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	log.Printf("[middleware] version=%s config=%s", VERSION, CONFIG_SOURCE)
	httpClient.Timeout = probeTimeout()

	var err error
	if db, err = openDB(DB_CONF); err != nil {
		log.Fatalf("failed to init database: %v", err)
	}
	initNodePools()
	seedUsers()
	backfillEmailVerified()
	loadPointsConfig()
	loadSiteSettings()
	if JWT_SECRET == "" {
		log.Printf("[auth] WARN jwt-secret not set: console login disabled, only admin-token works")
	}

	if WS_PORT > 0 {
		wsSrv = newWSServer()
		go wsSrv.Start(fmt.Sprintf(":%d", WS_PORT))
		go wsSrv.maintenanceLoop()
	} else {
		markAllNodesOffline()
		log.Printf("[ws] ws channel disabled (ws-port=0)")
	}
	startNodeWatcher()
	go schedulerLoop()
	go retentionLoop()
	go otaReconcilerLoop()
	go donationSettleLoop()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	if p := splitAndTrim(TRUSTED_PROXIES, ","); len(p) > 0 {
		if err := r.SetTrustedProxies(p); err != nil {
			log.Printf("[middleware] WARN invalid trusted-proxies: %v", err)
		}
	} else {
		_ = r.SetTrustedProxies(nil)
	}
	r.Use(corsMiddleware())

	// 健康检查：前端托管时根路径留给 SPA，健康检查另走 /healthz
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	fwd := r.Group("")
	if RATE_LIMIT > 0 {
		limit := rateLimitMiddleware(newRateLimiter(RATE_LIMIT))
		fwd.Use(func(c *gin.Context) {
			// 1IPW 主站后端带 sync-token 转发：所有用户共用一个出口 IP，不按单 IP 限流
			if SYNC_TOKEN != "" && bearerToken(c) == SYNC_TOKEN {
				c.Next()
				return
			}
			limit(c)
		})
	}
	fwd.GET("/v1/*slug", middlewareHandler)
	fwd.GET("/middleware/*slug", middlewareHandler)

	registerReportRoutes(r)
	registerRemoteConfigRoutes(r)
	registerAdminRoutes(r)
	registerRestRoutes(r)
	registerPublicStatusRoutes(r)
	registerConsoleSlaRoute(r)
	registerSyncRoutes(r)
	registerToolboxRoutes(r)
	registerSpeedtestRoutes(r)
	registerAgentRoutes(r)
	mountWeb(r)

	addr := PORT
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	log.Printf("[middleware] http listening on %s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatalf("listen failed: %v", err)
	}
}

// mountWeb 托管前端构建产物（SPA：未命中静态文件的 GET 回落 index.html）；目录不存在则根路径返回 {"status":"ok"}
func mountWeb(r *gin.Engine) {
	index := filepath.Join(WEB_DIR, "index.html")
	if _, err := os.Stat(index); err != nil {
		r.GET("/", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
		return
	}
	log.Printf("[web] serving console from %s", WEB_DIR)
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if c.Request.Method != http.MethodGet || strings.HasPrefix(p, "/admin/") || strings.HasPrefix(p, "/api/") {
			apiError(c, http.StatusNotFound, "Not Found")
			return
		}
		f := filepath.Join(WEB_DIR, filepath.FromSlash(filepath.Clean("/"+p)))
		if st, err := os.Stat(f); err == nil && !st.IsDir() {
			c.File(f)
			return
		}
		c.File(index)
	})
}

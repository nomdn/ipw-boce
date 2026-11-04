package main

import (
	"log"
	"strings"
)

// readConfig 解析全部配置写入全局变量（键名、缺省值与原版保持一致）
func readConfig() error {
	if err := loadSettingFile(); err != nil {
		return err
	}
	var err error

	PORT = confString("PORT", "port")
	if PORT == "" {
		PORT = "8080"
	}
	if HTTP_TIMEOUT, err = confInt("HTTP_TIMEOUT", "http-timeout-seconds", 30); err != nil {
		return err
	}
	// rate-limit：缺省 120；显式 0 = 不限流
	if RATE_LIMIT, err = confInt("RATE_LIMIT", "rate-limit", 120); err != nil {
		return err
	}
	// ws-port：缺省 8092；显式 0 = 关闭 WS 通道
	if WS_PORT, err = confInt("WS_PORT", "ws-port", 8092); err != nil {
		return err
	}
	if cors := confString("CORS", "cors"); cors != "" {
		ACCEPT_DOMAINS = splitAndTrim(cors, ",")
	}
	TRUSTED_PROXIES = confString("TRUSTED_PROXIES", "trusted-proxies")

	if err = confJSON("API_BASE_URLS", "api-base-url", &API_BASE_URLS); err != nil {
		return err
	}
	if err = confJSON("IP_LOCATION_APIS", "ip-location-api", &IP_LOCATION_APIS); err != nil {
		return err
	}
	if err = confJSON("API_KEYS", "api-keys", &API_KEYS); err != nil {
		return err
	}
	if err = confJSON("WS_KEYS", "ws-keys", &WS_KEYS); err != nil {
		return err
	}

	// database：env JSON > 单键覆盖 > setting.json > 缺省 sqlite ./boce.db
	if err = confJSON("DATABASE", "database", &DB_CONF); err != nil {
		return err
	}
	if v := confString("DATABASE_DRIVER", "_"); v != "" {
		DB_CONF.Driver = v
	}
	if v := confString("DATABASE_DSN", "_"); v != "" {
		DB_CONF.DSN = v
	}
	if DB_CONF.Driver == "" {
		DB_CONF.Driver = "sqlite"
	}
	if DB_CONF.DSN == "" {
		DB_CONF.DSN = "boce.db"
	}

	ADMIN_TOKEN = confString("ADMIN_TOKEN", "admin-token")
	REPORT_TOKEN = confString("REPORT_TOKEN", "report-token")
	if DATA_RETENTION_DAYS, err = confInt("DATA_RETENTION_DAYS", "data-retention-days", 30); err != nil {
		return err
	}

	JWT_SECRET = confString("JWT_SECRET", "jwt-secret")
	if JWT_EXPIRY, err = confInt("JWT_EXPIRY_SECONDS", "jwt-expiry-seconds", 86400); err != nil {
		return err
	}
	ADMIN_USER = confString("ADMIN_USER", "admin-user")
	if ADMIN_USER == "" {
		ADMIN_USER = "admin"
	}
	ADMIN_PASSWORD = confString("ADMIN_PASSWORD", "admin-password")

	if USER_TASK_LIMIT, err = confInt("USER_TASK_LIMIT", "user-task-limit", 20); err != nil {
		return err
	}
	if USER_MIN_INTERVAL, err = confInt("USER_MIN_INTERVAL", "user-min-interval", 0); err != nil {
		return err
	}
	// 公开注册开关（缺省 true 与原版一致；SMTP 未配置时注册本来就不可用）
	REGISTRATION_ENABLED = confBool("REGISTRATION_ENABLED", "registration-enabled", true)
	// WS 节点必须带预配密钥（ws-keys / 捐赠密钥）才能注册（缺省 false 与原版一致）
	WS_REQUIRE_KEY = confBool("WS_REQUIRE_KEY", "ws-require-key", false)
	// sync-token：1IPW 主站后端拉取节点表用（空 = 关闭 /api/sync/nodes）
	SYNC_TOKEN = confString("SYNC_TOKEN", "sync-token")
	// public-base-url：本中心对外地址，生成一键安装命令用（如 https://boce.1ipw.cn）
	PUBLIC_BASE_URL = strings.TrimSuffix(confString("PUBLIC_BASE_URL", "public-base-url"), "/")
	// agent-dir：一键安装用的二进制目录（boce-agent-linux-* / lemonipw-linux-*）
	AGENT_DIR = confString("AGENT_DIR", "agent-dir")
	if AGENT_DIR == "" {
		AGENT_DIR = "agent"
	}

	if err = confJSON("SMTP", "smtp", &SMTP_CONF); err != nil {
		return err
	}
	if SMTP_CONF.TimeoutSec <= 0 {
		SMTP_CONF.TimeoutSec = 10
	}
	ALERT_CONF = alertConfig{Enabled: true, DownThreshold: 3}
	if confSet("ALERT", "alert") {
		if err = confJSON("ALERT", "alert", &ALERT_CONF); err != nil {
			return err
		}
	}
	if ALERT_CONF.DownThreshold <= 0 {
		ALERT_CONF.DownThreshold = 3
	}

	OTA_ASSET_BASE = confString("OTA_ASSET_BASE", "ota-asset-base")
	// public-ws-url：给捐赠用户展示的节点接入地址（如 wss://boce.example.com/ws），空则按请求域名 + ws-port 推算
	PUBLIC_WS_URL = confString("PUBLIC_WS_URL", "public-ws-url")

	// web-dir：前端构建产物目录（缺省 ./web/dist，存在才托管）
	WEB_DIR = confString("WEB_DIR", "web-dir")
	if WEB_DIR == "" {
		WEB_DIR = "web/dist"
	}

	if len(flattenStack(API_BASE_URLS)) == 0 && len(IP_LOCATION_APIS) == 0 {
		log.Printf("[config] no node pools in setting.json; pools are managed in console (node_defs)")
	}
	return nil
}

func splitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func flattenStack(s stackConfig) []apiInfo {
	out := make([]apiInfo, 0, len(s.DualStack)+len(s.IPv4)+len(s.IPv6))
	for _, n := range s.DualStack {
		n.Stack = stackDualStack
		out = append(out, n)
	}
	for _, n := range s.IPv4 {
		n.Stack = stackIPv4
		out = append(out, n)
	}
	for _, n := range s.IPv6 {
		n.Stack = stackIPv6
		out = append(out, n)
	}
	return out
}

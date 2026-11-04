package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ==================== 配置（setting.json，键名与原版一致） ====================
//
// 读取优先级：环境变量 > setting.json > 缺省值。
// 查找顺序：SETTING_FILE > ./setting.json > ../setting.json；都没有时全部走 env / 缺省值。
// 直接用 encoding/json 解析：viper 会把 map 键（节点 id）统一转小写，凭据表会因此对不上。

type apiInfo struct {
	Label string `json:"label"`
	ID    string `json:"id"`
	URL   string `json:"url"`
	WS    wsFlag `json:"ws"`
	Stack string `json:"stack"`
}

// wsFlag 兼容 "ws": true 与 "ws": "true"
type wsFlag bool

func (w *wsFlag) UnmarshalJSON(b []byte) error {
	switch strings.Trim(strings.TrimSpace(string(b)), `"`) {
	case "true", "1", "yes":
		*w = true
	case "false", "0", "", "no", "null":
		*w = false
	default:
		return fmt.Errorf("invalid ws flag %s", b)
	}
	return nil
}

func (a *apiInfo) UseWS() bool { return a != nil && bool(a.WS) }

type stackConfig struct {
	DualStack []apiInfo `json:"DualStack"`
	IPv4      []apiInfo `json:"IPv4"`
	IPv6      []apiInfo `json:"IPv6"`
}

type dbConfig struct {
	Driver       string `json:"driver"`
	DSN          string `json:"dsn"`
	MaxOpenConns int    `json:"max-open-conns"`
	MaxIdleConns int    `json:"max-idle-conns"`
}

type smtpConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Password   string `json:"password"`
	From       string `json:"from"`
	FromName   string `json:"fromName"`
	Ssl        bool   `json:"ssl"`
	StartTLS   bool   `json:"startTLS"`
	Insecure   bool   `json:"insecure"`
	TimeoutSec int    `json:"timeoutSec"`
}

type alertConfig struct {
	Enabled       bool `json:"enabled"`
	DownThreshold int  `json:"downThreshold"`
}

var (
	VERSION    = "dev"
	COMMIT     = ""
	BUILD_TIME = ""

	PORT                string
	HTTP_TIMEOUT        int
	RATE_LIMIT          int
	WS_PORT             int
	ACCEPT_DOMAINS      []string
	TRUSTED_PROXIES     string
	API_BASE_URLS       stackConfig
	IP_LOCATION_APIS    []apiInfo
	API_KEYS            map[string]string
	WS_KEYS             map[string]string
	DB_CONF             dbConfig
	ADMIN_TOKEN         string
	REPORT_TOKEN        string
	DATA_RETENTION_DAYS int
	JWT_SECRET          string
	JWT_EXPIRY          int
	ADMIN_USER          string
	ADMIN_PASSWORD      string
	SMTP_CONF           smtpConfig
	ALERT_CONF          alertConfig
	USER_TASK_LIMIT     int
	USER_MIN_INTERVAL    int
	PUBLIC_WS_URL        string
	REGISTRATION_ENABLED bool
	WS_REQUIRE_KEY       bool
	SYNC_TOKEN           string
	PUBLIC_BASE_URL      string
	AGENT_DIR            string
	WEB_DIR              string
	CONFIG_SOURCE       string
)

// rawConf setting.json 顶层键 → 原文
var rawConf = map[string]json.RawMessage{}

func loadSettingFile() error {
	candidates := []string{"setting.json", "../setting.json"}
	if p := os.Getenv("SETTING_FILE"); p != "" {
		candidates = []string{p}
	}
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err != nil {
			if os.Getenv("SETTING_FILE") != "" {
				return err
			}
			continue
		}
		if err := json.Unmarshal(b, &rawConf); err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		CONFIG_SOURCE = p
		return nil
	}
	CONFIG_SOURCE = "(env only)"
	return nil
}

// confString 字符串配置：env > setting.json（兼容数字写法，如 "port": 8080）
func confString(env, key string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	raw, ok := rawConf[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.Trim(string(raw), `" `)
}

// confInt 整数配置：env > setting.json > def
func confInt(env, key string, def int) (int, error) {
	v := os.Getenv(env)
	if v == "" {
		raw, ok := rawConf[key]
		if !ok {
			return def, nil
		}
		v = strings.Trim(string(raw), `" `)
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return n, nil
}

// confBool 布尔配置：env > setting.json > def
func confBool(env, key string, def bool) bool {
	v := os.Getenv(env)
	if v == "" {
		if raw, ok := rawConf[key]; ok {
			v = strings.Trim(string(raw), `" `)
		}
	}
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}

// confJSON 对象/数组配置：env JSON > setting.json
func confJSON(env, key string, target any) error {
	if raw := os.Getenv(env); raw != "" {
		if err := json.Unmarshal([]byte(raw), target); err != nil {
			return fmt.Errorf("parse env %s: %w", env, err)
		}
		return nil
	}
	raw, ok := rawConf[key]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}
	return nil
}

func confSet(env, key string) bool {
	if os.Getenv(env) != "" {
		return true
	}
	_, ok := rawConf[key]
	return ok
}

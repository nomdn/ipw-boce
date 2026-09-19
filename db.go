package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// dbCtx 数据库操作统一超时（写路径不阻塞请求，读路径快速失败）
func dbCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// ==================== GORM 模型 ====================

// Node 节点在线状态表（当前快照）：WS 注册/断开/心跳时更新
type Node struct {
	ID          uint      `gorm:"primaryKey" json:"-"`
	NodeID      string    `gorm:"uniqueIndex;size:128" json:"nodeId"`
	Label       string    `gorm:"size:256" json:"label"`
	Online      bool      `gorm:"index" json:"online"`
	RemoteAddr  string    `gorm:"size:128" json:"remoteAddr"`
	Version     string    `gorm:"size:64" json:"version"` // 节点上报的版本号（WS register / HTTP 健康检查）
	// Capabilities 节点上报的能力清单（逗号分隔，如 "probe,report,config"）。
	// 空 = 老版本节点未上报（无法据此判定"不支持"，只能作为"程序可能过旧"的线索）。
	Capabilities string    `gorm:"size:128" json:"capabilities"`
	FirstSeenAt  time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
	CreatedAt   time.Time `json:"-"`
	UpdatedAt   time.Time `json:"-"`
}

// NodeEvent 节点在线/离线历史事件
type NodeEvent struct {
	ID        uint      `gorm:"primaryKey" json:"-"`
	NodeID    string    `gorm:"index:idx_node_event,priority:1;size:128" json:"nodeId"`
	Event     string    `gorm:"index:idx_node_event,priority:2;size:16" json:"event"` // online | offline
	Reason    string    `gorm:"size:256" json:"reason"`
	CreatedAt time.Time `gorm:"index" json:"createdAt"`
}

// ProbeResult 拨测结果（detail/ssl/dns/tcping/speed，WS 通道与 HTTP 转发两条链路都落库）
type ProbeResult struct {
	ID uint `gorm:"primaryKey" json:"-"`
	// TaskID 归属的定时拨测任务（source=sched 时有效；手动/节点上报无任务归 0）。
	// SLA 聚合按 task_id 精确归属，避免不同任务(即使同 api_type/同目标)相互污染。
	TaskID    uint      `gorm:"index:idx_probe_task;default:0" json:"taskId,omitempty"`
	RequestID string    `gorm:"size:64;index" json:"requestId,omitempty"`
	NodeID    string    `gorm:"index:idx_probe_node,priority:1;size:128" json:"nodeId"`
	APIType   string    `gorm:"index:idx_probe_type,priority:1;size:32" json:"apiType"` // detail|ssl|dns|tcping|speed
	Raw       string    `gorm:"size:512" json:"raw"`                                    // 拨测目标（域名/IP）
	Query     string    `gorm:"size:512" json:"query,omitempty"`                        // url-encoded query
	Status    int       `json:"status"`                                                 // 上游/节点返回的 HTTP 状态码；0 = 请求失败
	LatencyMs int64     `json:"latencyMs"`                                              // 端到端耗时（中间件侧）
	Error     string    `gorm:"size:512" json:"error,omitempty"`
	Source    string    `gorm:"size:16" json:"source"`            // ws|http 节点上报 / sched 定时 / biz 手动一键
	// OwnerID 手动一键拨测(biz)发起者的用户 id；sched 样本的归属走 task_id→任务 owner，不在此冗余。
	// 0 = 无归属（静态 token 发起 / 节点上报）。用户"我的拨测历史"按它过滤。
	OwnerID   uint      `gorm:"index;default:0" json:"ownerId,omitempty"`
	Origin    string    `gorm:"size:128" json:"origin,omitempty"` // 数据来源实例（外部上报方标识；空 = 本机观测）
	Body      string    `gorm:"type:text" json:"body,omitempty"`
	CreatedAt time.Time `gorm:"index:idx_probe_created;index:idx_probe_node,priority:2" json:"createdAt"`
}

// RequestStat 请求统计（按分钟 × 节点 × API 类型聚合的计数器，内存累计后定时落库）
type RequestStat struct {
	ID           uint      `gorm:"primaryKey" json:"-"`
	Minute       int64     `gorm:"uniqueIndex:idx_stat_dim,priority:1" json:"-"` // unix 分钟桶
	NodeID       string    `gorm:"uniqueIndex:idx_stat_dim,priority:2;size:128" json:"nodeId"`
	APIType      string    `gorm:"uniqueIndex:idx_stat_dim,priority:3;size:32" json:"apiType"`
	Total        int64     `json:"total"`
	Errors       int64     `json:"errors"`
	LatencySumMs int64     `json:"latencySumMs"`
	LatencyMaxMs int64     `json:"latencyMaxMs"`
	UpdatedAt    time.Time `json:"-"`
}

// NodeConfig 节点远端配置（按节点托管；nodeId="global" 为全局缺省配置）
type NodeConfig struct {
	ID        uint      `gorm:"primaryKey" json:"-"`
	NodeID    string    `gorm:"uniqueIndex;size:128" json:"nodeId"`
	Config    string    `gorm:"type:text" json:"-"` // JSON 原文
	UpdatedAt time.Time `json:"updatedAt"`
	CreatedAt time.Time `json:"-"`
}

// NodeDef 节点定义（管理员在控制台维护的上游节点池，见 node_defs.go）
//
// 取代 setting.json 的 api-base-url / ip-location-api 两处静态配置：
// 库中有记录即由库接管对应池，空库才回退 setting.json（兼容老部署平滑迁移）。
type NodeDef struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	NodeID string `gorm:"uniqueIndex;size:128" json:"nodeId"` // 节点标识，即转发路径里的 backendID
	Label  string `gorm:"size:256" json:"label"`              // 显示名，如"中国 江苏 移动"
	URL    string `gorm:"size:512" json:"url"`                // HTTP 上游地址；走 WS 通道的节点可留空
	WS     bool   `json:"ws"`                                 // true = 拨测请求经 WS 通道下发
	// Pool 归属池，逗号分隔可多选：api | location | api,location（默认 api）
	// 双归属节点在转发时按 apiType 选池：location/asn 走 location，其余走 api
	Pool string `gorm:"size:16;index" json:"pool"`
	// Stack 仅对 api 池有意义，对应原三栈分组：DualStack / IPv4 / IPv6；location 池为纯数组，留空
	Stack     string    `gorm:"size:16" json:"stack"`
	Enabled   bool      `json:"enabled"`   // 停用后不进节点池（转发/拨测/探活都看不见）
	SortOrder int       `json:"sortOrder"` // 控制台展示与池内排序
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// 凭据（库托管：原只在 env / setting.json 的 api-keys / ws-keys 里，改一次要重启进程）。
	// 控制台在节点表单里填写，保存即生效、无需重启。明文永不经接口回显（json:"-"），
	// 列表只返回 HasAPIKey / HasWSKey 两个布尔标记。
	APIKey string `gorm:"column:api_key;size:256" json:"-"` // 中心 → 该节点 HTTP 接口的访问令牌（对应 api-keys）
	WSKey  string `gorm:"column:ws_key;size:256" json:"-"`  // 该节点 → 中心的 WS 注册校验密钥（对应 ws-keys）

	// HasAPIKey / HasWSKey 仅用于出参展示“是否已设置”，不落库
	HasAPIKey bool `gorm:"-" json:"hasApiKey"`
	HasWSKey  bool `gorm:"-" json:"hasWsKey"`
}

// OTATask 控制台下发的节点 OTA 升级任务（见 ota.go）
//
// 状态机：dispatched（已下发）→ success / failed。
// 节点重启期间 WS 必然断开，最终结果无法经原连接回传，成败以"观测到节点重连后的版本号"为准：
//   - TargetVersion 非空（按版本下发）：重连版本 == 目标版本 → success
//   - TargetVersion 为空（直发 url）：重连版本 != FromVersion → success
//   - 节点主动回报 ok:false（下载/校验失败）→ 立即 failed
//   - 超过超时窗口未观测到版本变化 → failed（超时）
type OTATask struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	NodeID string `gorm:"index;size:128" json:"nodeId"`
	// RequestID 下发时生成的指令 id（"o" 前缀），节点 ota_result 按 it 关联回报
	RequestID string `gorm:"size:64" json:"requestId"`
	// FromVersion 下发时节点的版本号（nodes 表快照，可能为空）
	FromVersion string `gorm:"size:64" json:"fromVersion"`
	// TargetVersion 目标版本（按版本下发时有值；直发 url 时为空）
	TargetVersion string `gorm:"size:64" json:"targetVersion"`
	// URL 直发下载地址；按版本下发时为空（资产名由节点按平台计算）
	URL    string `gorm:"size:512" json:"url,omitempty"`
	SHA256 string `gorm:"size:64" json:"sha256,omitempty"`
	// Channel 实际使用的下发通道：ws | http
	Channel string `gorm:"size:8" json:"channel"`
	// Status 任务状态：dispatched | success | failed
	Status string `gorm:"index;size:16" json:"status"`
	// Stage 节点回报的最新阶段（accepted/downloading/verifying/installing/restarting）
	Stage string `gorm:"size:32" json:"stage"`
	Error string `gorm:"size:512" json:"error,omitempty"`
	// DispatchedBy 操作人用户名（审计；静态 token 操作为空）
	DispatchedBy string     `gorm:"size:64" json:"dispatchedBy,omitempty"`
	DispatchedAt time.Time  `json:"dispatchedAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

// MaintenanceWindow 计划维护窗口（见 maintenance.go）：窗口内该范围内节点的上下线**不推送告警**。
//
// 只影响通知、不影响事实：offline/online 事件照写、nodes.online 快照照改，
// 因此节点状态页、事件历史与可用率统计都不受影响，事后仍能看出"这段时间确实断过"。
type MaintenanceWindow struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Scope 作用范围：global = 全部节点；其余取值 = 该 nodeId
	Scope string `gorm:"size:128;index" json:"scope"`
	// StartAt / EndAt 起止时刻：
	//   - RepeatDaily=false：绝对时间段（割接 / 发布，一次性）
	//   - RepeatDaily=true：只比这两个时刻的**时钟**（服务器本地时区），支持跨零点（如 23:30-00:30）
	StartAt     time.Time `json:"startAt"`
	EndAt       time.Time `json:"endAt"`
	RepeatDaily bool      `json:"repeatDaily"`
	Reason      string    `gorm:"size:256" json:"reason"`
	CreatedBy   string    `gorm:"size:64" json:"createdBy,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// allModels AutoMigrate 的全部模型
var allModels = []any{&Node{}, &NodeEvent{}, &ProbeResult{}, &RequestStat{}, &NodeConfig{}, &NodeDef{}, &ProbeTask{}, &User{}, &AppNotice{}, &OTATask{}, &MaintenanceWindow{}}

// ==================== 数据库初始化 ====================

// openDB 按配置打开 GORM 连接并 AutoMigrate。
// 支持 sqlite（纯 Go 驱动，无 CGO）/ mysql / postgres 三种方言。
func openDB(conf dbConfig) (*storeDB, error) {
	driver := strings.ToLower(strings.TrimSpace(conf.Driver))
	var g *gorm.DB
	var err error

	switch driver {
	case "sqlite", "sqlite3":
		dsn := conf.DSN
		// 纯 Go sqlite 驱动的 pragma 经 DSN 传递：缺省补 busy_timeout 与 WAL，降低并发写冲突
		if !strings.Contains(dsn, "?") {
			dsn += "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
		} else if !strings.Contains(dsn, "_pragma") {
			dsn += "&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
		}
		g, err = gorm.Open(sqlite.Open(dsn), gormConfig())
	case "mysql":
		if conf.DSN == "" {
			return nil, fmt.Errorf("mysql dsn is required (database.dsn)")
		}
		g, err = gorm.Open(mysql.Open(conf.DSN), gormConfig())
	case "postgres", "postgresql", "pgsql":
		if conf.DSN == "" {
			return nil, fmt.Errorf("postgres dsn is required (database.dsn)")
		}
		g, err = gorm.Open(postgres.Open(conf.DSN), gormConfig())
	default:
		return nil, fmt.Errorf("unsupported database driver %q (expected sqlite / mysql / postgres)", conf.Driver)
	}
	if err != nil {
		return nil, err
	}

	sqlDB, err := g.DB()
	if err != nil {
		return nil, err
	}
	maxOpen := conf.MaxOpenConns
	maxIdle := conf.MaxIdleConns
	if maxOpen <= 0 {
		if driver == "sqlite" || driver == "sqlite3" {
			maxOpen = 1 // sqlite 单写者，避免 database locked
		} else {
			maxOpen = 20
		}
	}
	if maxIdle <= 0 {
		if driver == "sqlite" || driver == "sqlite3" {
			maxIdle = 1
		} else {
			maxIdle = 5
		}
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := g.AutoMigrate(allModels...); err != nil {
		return nil, fmt.Errorf("auto migrate: %w", err)
	}
	log.Printf("[db] connected driver=%s dsn=%s (migrated %d tables)", driver, logSafeDSN(conf.DSN), len(allModels))
	return &storeDB{DB: g, driver: driver}, nil
}

func gormConfig() *gorm.Config {
	return &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	}
}

// logSafeDSN 日志中隐去连接串里的密码段
func logSafeDSN(dsn string) string {
	if i := strings.Index(dsn, "@"); i > 0 && strings.Contains(dsn[:i], ":") {
		if s := strings.Index(dsn, "://"); s >= 0 && s < i {
			return dsn[:s+3] + "***" + dsn[i:]
		}
	}
	return dsn
}

// storeDB 包装 *gorm.DB，携带方言标记
type storeDB struct {
	*gorm.DB
	driver string
}

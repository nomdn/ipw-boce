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
	FirstSeenAt time.Time `json:"firstSeenAt"`
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

// allModels AutoMigrate 的全部模型
var allModels = []any{&Node{}, &NodeEvent{}, &ProbeResult{}, &RequestStat{}, &NodeConfig{}, &ProbeTask{}, &User{}, &AppNotice{}}

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

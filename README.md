# ipw-boce middleware-go

重现 [ipw-cn/middleware-go](../ipw-cn) 拨测中间件，技术栈换成 **GIN + GORM**，支持 **SQLite / MySQL / PostgreSQL** 三种数据库，并在重现基础上增加：统计聚合与拨测数据持久化（两者数据均来自节点上报）、节点在线数据持久化、按节点的远端配置托管（本服务作为节点 `remote-config-url` 的提供方）。

## 功能

### 重现自原版

| 功能 | 说明 |
| --- | --- |
| HTTP 转发 | `/{prefix}/{backendID}/{apiType}/{raw...}`，前缀兼容 `/v1/` 与 `/middleware/`；apiType：whois / dns / location / ssl / asn / dnssec / detail / tcping / speed |
| 节点池 | `api-base-url` 三栈（DualStack/IPv4/IPv6）+ `ip-location-api` 纯数组；location/asn 走后者，其余走前者 |
| WS 通道 | 独立端口（默认 8092，`ws-port=0` 关闭），路径 `/ws`；节点 `"ws": true` 时拨测请求走 WS 下发/回传；注册 key 校验（ws-keys）、心跳 20s、空闲 75s 剔除、status 统计上报 |
| 配置体系 | setting.json + 环境变量 + `remote-config-url` 远端拉取（远端 > 环境变量 > 本地；`remote-ignore-config` 免覆盖；api-keys/ws-keys 永不被远端覆盖） |
| 限流 | 单 IP 每分钟固定窗口，默认 120 次，`0` 关闭；超限 429 |
| CORS | 逗号分隔域名白名单，空 = 允许所有 |
| trusted-proxies | 逗号分隔 IP/CIDR；配置后仅信任这些来源的 X-Forwarded-For |
| api-keys | HTTP 转发注入 `Authorization: Bearer <token>` |

### 新增

| 功能 | 说明 |
| --- | --- |
| 拨测数据持久化 | `probe_results` 表：detail/ssl/dns/tcping/speed 的拨测明细，含 raw/status/latency/body(截断64KB)/error/source/origin，由节点上报写入 |
| 统计聚合 | `request_stats` 表按 `分钟 × 节点 × apiType` 累加，数据唯一来源是节点上报（见下方上报协议）；所有 apiType 都计入 |
| 节点在线持久化 | `nodes` 表在线快照（注册/断开/心跳刷新）+ `node_events` 在线/离线历史；进程重启自动清离线快照防僵尸记录 |
| 远端配置托管 | `GET /remote-config/:nodeId`（global 为底、节点配置逐键覆盖）；`/admin/node-configs/*` CRUD 维护 |
| 节点上报 | 统计与拨测明细由节点自己上报：连着 WS 走 WS `report` 消息，没 WS 的走 `POST /report`；中间件不在转发路径上计数，`probe_results.origin` 记录上报方 |
| 管理 API | `/admin/*`，配置 `admin-token` 后需 `Authorization: Bearer` |
| 数据保留期 | `data-retention-days`（默认30，0=永久），每小时清理过期拨测/统计/事件 |

## 数据库配置

```json
"database": {
    "driver": "sqlite",            // sqlite | mysql | postgres
    "dsn": "boce.db",              // sqlite 为文件路径；mysql/postgres 为标准 DSN
    "max-open-conns": 0,           // 0 = 按方言缺省（sqlite 1，其余 20）
    "max-idle-conns": 0
}
```

- SQLite 使用纯 Go 驱动（glebarez/sqlite，无 CGO），DSN 自动追加 `busy_timeout` 与 WAL
- MySQL DSN 例：`user:pass@tcp(127.0.0.1:3306)/ipw_boce?charset=utf8mb4&parseTime=True&loc=Local`
- Postgres DSN 例：`postgres://user:pass@127.0.0.1:5432/ipw_boce?sslmode=disable`
- 环境变量覆盖：`DATABASE_DRIVER` / `DATABASE_DSN` / `DATABASE`（整段 JSON）
- 启动时 AutoMigrate 建表，无需手工迁移

## 节点上报协议

统计与拨测明细由**拨测节点自己上报**，中间件不在转发路径上采集——节点是唯一记录者，不存在双算。
两条通道报文同构，收集端按 `(分钟 × 节点 × apiType)` 冲突累加：

| 通道 | 适用节点 | 方式 |
| --- | --- | --- |
| WS 消息 | 已注册 WS 节点（节点池里 `"ws": true`） | `{"type":"report","data":{...报文...}}` |
| HTTP 批量 | 无 WS 的纯 HTTP 节点 | 每个上报周期 POST 一次统计增量 + 拨测明细 |

节点侧配置（`lemon-ipw` 的 setting.json）：配 `ws-url` + `node-id` 走 WS 通道；
未启用 WS 时配 `report-url`（填收集中心地址，客户端自动拼 `/report`）与 `report-token`。

**报文**（POST /report 的 body，即 WS report 的 data）：

```json
{
  "instance": "cn-jiangsu",                       // 上报方标识，存入 probe_results.origin
  "stats": [                                      // 统计增量（每次上报携带自上次以来的增量）
    { "nodeId": "cn-jiangsu", "apiType": "tcping",
      "total": 1, "errors": 0, "latencySumMs": 12, "latencyMaxMs": 12,
      "minute": 0 }                               // unix 分钟桶；0 = 收集器当前分钟
  ],
  "probes": [                                     // 拨测明细（apiType：detail/ssl/dns/tcping/speed）
    { "nodeId": "cn-jiangsu", "apiType": "tcping", "raw": "qq.com",
      "status": 200, "latencyMs": 33,
      "body": {"ok": true} }                      // body 兼容 JSON 对象或字符串
  ]
}
```

**语义约束**：

- 只上报第一方观测（自己处理的请求），不得转播从别处收到的数据
- stats 是**增量**，收集器按累加入库；上报为 at-most-once（失败丢弃并记日志），不重试——重试会导致重复累加
- 单次报文上限 500 条 stats / 500 条 probes / 4MB
- 未配 WS 且未配 `report-url` 的节点不产生任何统计数据

## API

### 数据面（同原版）

```
GET /                              → {"status":"ok"}
GET /v1/{backendID}/{apiType}/{raw}?query=...    （/middleware/ 前缀同义）
WS  ws://host:8092/ws              → 节点注册/心跳/拨测通道（消息信封见 ws.go 头注释）
```

### 配置托管（公开）

```
GET /remote-config                 → 全局缺省配置
GET /remote-config/:nodeId         → 该节点生效配置（global + 节点覆盖合并）
```

### 管理（配置 admin-token 时需 Bearer）

```
GET    /admin/status                        版本/运行时长/WS在线数/数据库驱动
GET    /admin/nodes                         节点在线快照
GET    /admin/nodes/:nodeId/events?limit=   节点在线/离线历史
GET    /admin/probes?node=&type=&since=&limit=  拨测记录（倒序）
GET    /admin/stats/summary?hours=24        按 apiType / 节点聚合
GET    /admin/stats/timeseries?hours=24     按分钟时间序列
GET    /admin/node-configs                  托管配置列表
GET    /admin/node-configs/:nodeId          读取配置原文
PUT    /admin/node-configs/:nodeId          写入（body=JSON 对象；nodeId=global 即全局）
DELETE /admin/node-configs/:nodeId          删除

# —— 控制台用户（仅 admin，见 users.go；登录态见 auth.go）——
POST   /admin/login                        登录（users 表校验，签 JWT）
GET    /admin/users                        用户列表（不回显口令）
POST   /admin/users                        新增（{username,password,role?,email?}）
PATCH  /admin/users/:id                    改 {email?,role?,enabled?}（保留≥1 个启用 admin）
PATCH  /admin/users/:id/password           重置口令（{password}）
DELETE /admin/users/:id                    删除（禁删自己；级联删其任务与站内信）
```

登录用户存 DB `users` 表（bcrypt 口令哈希），分 `admin`/`user` 两角色；admin 才能访问用户管理与 SLA 任务等写操作。
首次启动 users 表为空时，由 `admin-user`/`admin-password` 自动 seed 首个 admin（未配口令则 seed `admin/admin`，请及时改）。
启用 JWT 登录需配 `jwt-secret`（无需 admin-password，账号改由用户管理页维护）；仍可用静态 `admin-token` 全权访问。

```
# —— 定时拨测任务（SLA 数据源，见 probe_task.go / sla.go）——
GET    /admin/tasks                         任务列表（含 ownerUsername）
POST   /admin/tasks                         新建（自动把当前登录 JWT 用户记为 owner）
PUT    /admin/tasks/:id                     更新（不改 owner）
PATCH  /admin/tasks/:id/enabled             启停
DELETE /admin/tasks/:id                     删除
GET    /admin/tasks/:id/sla?hours=24        某任务整窗 SLA 聚合
GET    /admin/tasks/meta                    可选拨测类型/间隔元信息

# —— 站内信（本人通知，铃铛；见 notices.go）——
GET    /admin/notices                       本人通知列表 {list,unread}
GET    /admin/notices/unread                未读数
POST   /admin/notices/read                  {ids?:[]} 标记已读（缺省全已读）
DELETE /admin/notices                       清空本人通知
```

- **任务所有者 & 掉线告警**：任务创建者记为 `owner`。当某 SLA 任务连续达到 `alert.downThreshold` 轮整组 down，通知该 owner：
  - **邮件 + 站内信同时发**（非二选一回退）：owner 有邮箱且 SMTP 可用 → 发邮件；同时无论是否有邮箱，owner 启用即落一条站内信（铃铛可见）；
  - 任务无归属（静态 token 建的旧任务）→ 不通知。
- **服务节点掉线监控**（见 nodeHealth.go）：监控配置池（api-base-url 三栈 + ip-location-api）里全部节点。
  - HTTP 版节点（非 ws）：每 1 小时 GET 该节点 `url`（health 接口就在根路径、无追加路径）探活；连续 2 次失败判 down（约 2h）。
  - WS 版节点（ws:true）：靠心跳（middleware 每 20s ping、空闲 75s 剔除），连续 3 轮(约 3 分钟)不在线判 down。
  - 仅对"本进程内曾在线"的节点告警（冷启动未连上/从未探活成功不报，避免误报）；down 翻转通知一次、恢复复位后可再报。
  - 投递：发给**所有启用 admin**，每人**邮件+站内信同时发**（kind=`node_down`）。
- **删除用户会级联删除其创建的任务与该用户的站内信**；同时始终保留至少 1 个启用 admin（禁删自己、不能降级/禁用最后一个 admin）。

## 配置项（setting.json）

原版全部键位不变（port / http-timeout-seconds / rate-limit / ws-port / remote-config-url / remote-ignore-config / cors / trusted-proxies / api-base-url / ip-location-api / api-keys / ws-keys），新增：

| 键 | 缺省 | 说明 |
| --- | --- | --- |
| `database` | sqlite boce.db | 见上文 |
| `admin-token` | 空 | /admin/* 鉴权，空=不鉴权 |
| `data-retention-days` | 30 | 0=永久保留 |
| `report-token` | 空 | /report 鉴权 token（节点上报时携带）；空回退 `admin-token`，都空=开放 |
| `smtp` | host 空=禁用 | SMTP 发信配置（host/user/password/from/fromName/port/ssl/startTLS/insecure/timeoutSec）。发信对象按任务 owner 动态解析 |
| `alert` | enabled+threshold3 | 掉线告警策略（enabled/to/downThreshold）。某 SLA 任务连续 N 轮判定整组 down 时，通知其「所有者」（见 alert.go）：邮件+站内信同时发（有邮箱走邮件；站内信恒发）。`to` 字段已不再作为收件兜底（保留兼容）。节点掉线监控参数为固定值（见 nodeHealth.go），无需配置 |

节点接入：节点侧 setting.json 配 `remote-config-url: http://<本服务>/remote-config/<节点id>` 即可托管拉取。

## 构建 / 运行

```bash
go build -ldflags "-X main.VERSION=v1.0.0 -X main.COMMIT=$(git rev-parse --short HEAD) -X main.BUILD_TIME=$(date -u +%FT%TZ)" -o ipw-boce.exe .
cp setting.json.example setting.json   # 修改后
./ipw-boce.exe                          # SETTING_FILE 可指定配置路径；-v 打印版本
```

本地联调：`go run ./scripts/mocknode -ws ws://127.0.0.1:8092/ws -id test-node -key secret -http 127.0.0.1:8093` 模拟一个 WS 注册节点 + HTTP 上游。

## 与原版的差异

- Fiber v3 → **GIN**；新增 GORM 持久化层（原版无任何存储）
- 限流/CORS 为等价手写实现（GIN 无内置对应件），行为对齐原版
- OTA 自更新未迁移（决策：不含 OTA）
- 原版节点池为空时启动失败；本服务允许空池运行（仅作 WS 枢纽/配置托管），改为告警
- 修复原版一处 bug：原版 `main()` 用 `PORT = os.Getenv("PORT")` 覆盖配置值，setting.json 的 `port` 永不生效；本版正确实现 env > setting.json > 8080

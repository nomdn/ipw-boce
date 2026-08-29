# ipw-boce middleware-go

重现 [ipw-cn/middleware-go](../ipw-cn) 拨测中间件，技术栈换成 **GIN + GORM**，支持 **SQLite / MySQL / PostgreSQL** 三种数据库，并在重现基础上增加：统计数据收集与持久化、拨测数据持久化、节点在线数据持久化、按节点的远端配置托管（本服务作为节点 `remote-config-url` 的提供方）。

## 功能

### 重现自原版

| 功能 | 说明 |
| --- | --- |
| HTTP 转发 | `/{prefix}/{backendID}/{apiType}/{raw...}`，前缀兼容 `/v1/` 与 `/middleware/`；apiType：whois / dns / location / ssl / asn / dnssec / detail / tcping / udping / speed |
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
| 拨测数据持久化 | `probe_results` 表：tcping/udping/speed 结果（WS 与 HTTP 转发两条链路都记），含 status/latency/body(截断64KB)/error/source，异步批量写入不阻塞请求 |
| 统计收集+持久化 | 内存计数器按 `分钟 × 节点 × apiType` 聚合，`stats-flush-seconds`（默认30s）upsert 进 `request_stats`；所有 apiType 都计入 |
| 节点在线持久化 | `nodes` 表在线快照（注册/断开/心跳刷新）+ `node_events` 在线/离线历史；进程重启自动清离线快照防僵尸记录 |
| 远端配置托管 | `GET /remote-config/:nodeId`（global 为底、节点配置逐键覆盖）；`/admin/node-configs/*` CRUD 维护 |
| 数据上报汇聚 | 多入口冗余场景（请求不保证经过本中间件）：`POST /report` + WS `report` 消息接收上报，原 Go 中间件定期批量上报，前端内置/边缘函数"调一次上报一次"，统计按 `(分钟×节点×apiType)` 冲突累加自然合并，`probe_results.origin` 记录来源 |
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

## 数据上报协议（多入口冗余汇聚）

同一节点可能被多个入口访问（原 Go 中间件 / 前端内置 TS 中间件 / 边缘函数），请求不保证经过本中间件。
各入口把**自己第一方观测**的数据主动上报到本收集中心，按 `(分钟 × 节点 × apiType)` 冲突累加，自然合并多入口视角。

三条上报通道（报文同构）：

| 通道 | 适用方 | 方式 |
| --- | --- | --- |
| HTTP 批量 | 原 Go 中间件实例（配 `report-url`） | 每个上报周期 POST 一次统计增量 + 拨测明细 |
| HTTP 单条 | 前端内置中间件 / 边缘函数（只能 HTTP） | 转发一次、POST 一次 |
| WS 消息 | 已注册 WS 节点 | `{"type":"report","data":{...报文...}}` |

**报文**（POST /report 的 body，即 WS report 的 data）：

```json
{
  "instance": "frontend-ts",                      // 上报方标识，存入 probe_results.origin
  "stats": [                                      // 统计增量（每次上报携带自上次以来的增量）
    { "nodeId": "cn-jiangsu", "apiType": "tcping",
      "total": 1, "errors": 0, "latencySumMs": 12, "latencyMaxMs": 12,
      "minute": 0 }                               // unix 分钟桶；0 = 收集器当前分钟
  ],
  "probes": [                                     // 拨测明细（apiType 仅 tcping/udping/speed）
    { "nodeId": "cn-jiangsu", "apiType": "tcping", "raw": "qq.com",
      "status": 200, "latencyMs": 33,
      "body": {"ok": true} }                      // body 兼容 JSON 对象或字符串
  ]
}
```

前端内置中间件集成示例（Nitro server route，每转发一次调用一次）：

```ts
// server/routes/middleware/[...slug].get.ts 处理完上游响应后：
await $fetch(COLLECTOR + '/report', {
  method: 'POST',
  headers: { authorization: 'Bearer ' + REPORT_TOKEN },
  body: {
    instance: 'frontend-ssr',
    stats: [{ nodeId: backendID, apiType, total: 1, errors: isErr ? 1 : 0, latencySumMs: ms, latencyMaxMs: ms }],
    // 拨测类（tcping/udping/speed）再带 probes: [{ ... }]
  },
}).catch(() => {}) // 上报失败不影响转发主链路
```

**语义约束（防双算）**：

- 只上报第一方观测（自己转发的请求），不得转播从别处收到的数据
- stats 是**增量**，收集器按累加入库；上报为 at-most-once（失败丢弃并记日志），不重试——重试会导致重复累加
- 单次报文上限 500 条 stats / 500 条 probes / 4MB
- 收集中心自身不要配 `report-url` 指向自己

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
```

## 配置项（setting.json）

原版全部键位不变（port / http-timeout-seconds / rate-limit / ws-port / remote-config-url / remote-ignore-config / cors / trusted-proxies / api-base-url / ip-location-api / api-keys / ws-keys），新增：

| 键 | 缺省 | 说明 |
| --- | --- | --- |
| `database` | sqlite boce.db | 见上文 |
| `admin-token` | 空 | /admin/* 鉴权，空=不鉴权 |
| `data-retention-days` | 30 | 0=永久保留 |
| `stats-flush-seconds` | 30 | 统计落库间隔 |
| `report-url` | 空 | 上报目标收集器地址；空 = 本实例不上报（仅作收集中心）。**收集中心不要指向自己**，否则本地+上报双份 |
| `report-token` | 空 | /report 鉴权 token（服务端校验与客户端携带共用）；空回退 `admin-token`，都空=开放 |
| `report-interval-seconds` | 15 | 主动上报间隔 |
| `report-probes` | true | 是否随报文携带拨测明细（false = 只报统计） |

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

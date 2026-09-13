# ipw-boce middleware-go

重现 [ipw-cn/middleware-go](../ipw-cn) 拨测中间件，技术栈换成 **GIN + GORM**，支持 **SQLite / MySQL / PostgreSQL** 三种数据库，并在重现基础上增加：统计聚合与拨测数据持久化（两者数据均来自节点上报）、节点在线数据持久化、按节点的远端配置托管（本服务作为节点 `remote-config-url` 的提供方）。

## 功能

### 重现自原版

| 功能 | 说明 |
| --- | --- |
| HTTP 转发 | `/{prefix}/{backendID}/{apiType}/{raw...}`，前缀兼容 `/v1/` 与 `/middleware/`；apiType：whois / dns / location / ssl / asn / dnssec / detail / tcping / speed |
| 节点池 | `api-base-url` 三栈（DualStack/IPv4/IPv6）+ `ip-location-api` 纯数组；location/asn 走后者，其余走前者。现改由控制台维护（见下方"节点池控制台托管"），setting.json 的这两个键仅在库为空时兜底。一个节点可**同时归属两个池**，转发时按 apiType 选池 |
| WS 通道 | 独立端口（默认 8092，`ws-port=0` 关闭），路径 `/ws`；节点 `"ws": true` 时拨测请求走 WS 下发/回传；注册 key 校验（ws-keys）、心跳 20s、空闲 75s 剔除、status 统计上报 |
| 配置体系 | setting.json + 环境变量 + `remote-config-url` 远端拉取（远端 > 环境变量 > 本地；`remote-ignore-config` 免覆盖；api-keys/ws-keys 永不被远端覆盖） |
| 限流 | 单 IP 每分钟固定窗口，默认 120 次，`0` 关闭；超限 429。**只挂转发路由 `/v1` 与 `/middleware`**（公开口）；/report（节点上报）、/admin、/remote-config、/console/sla、健康检查均不限流 |
| CORS | 逗号分隔域名白名单，空 = 允许所有 |
| trusted-proxies | 逗号分隔 IP/CIDR；配置后仅信任这些来源的 X-Forwarded-For |
| api-keys | HTTP 转发注入 `Authorization: Bearer <token>` |

### 新增

| 功能 | 说明 |
| --- | --- |
| 拨测数据持久化 | `probe_results` 表：detail/ssl/dns/tcping/speed 的拨测明细，含 raw/status/latency/body(截断64KB)/error/source/origin，由节点上报写入 |
| 统计聚合 | `request_stats` 表按 `分钟 × 节点 × apiType` 累加，数据唯一来源是节点上报（见下方上报协议）；所有 apiType 都计入 |
| 节点在线持久化 | `nodes` 表在线快照（注册/断开/心跳刷新）+ `node_events` 在线/离线历史；进程重启自动清离线快照防僵尸记录 |
| 远端配置托管 | `GET /remote-config/:nodeId`（global 为底、节点配置逐键覆盖）；`/admin/node-configs/*` CRUD 维护。**节点侧 `access-token` / `report-token` 属受保护凭据，远端下发不会覆盖**（节点硬编码名单，托管配置里写了也不生效，refresh 应答的 `protectedIgnored` 会列出被跳过的键）——这两项只能在节点本地用 ENV / setting.json 设置 |
| 节点上报 | 统计与拨测明细由节点自己上报：连着 WS 走 WS `report` 消息，没 WS 的走 `POST /report`；中间件不在转发路径上计数，`probe_results.origin` 记录上报方 |
| 管理 API | `/admin/*`，配置 `admin-token` 后需 `Authorization: Bearer` |
| 数据保留期 | `data-retention-days`（默认30，0=永久），每小时清理过期拨测/统计/事件 |
| 节点池控制台托管 | 上游节点改由数据库 `node_defs` 维护（见 node_defs.go）：管理员在控制台「配置分发 → 上游节点池」增删改，保存即热更新全局池，无需改 setting.json 重启；库为空时才回退 setting.json。同一节点可同时归属 api 与 location 两池（`pool` 逗号分隔，如 `api,location`）；停用则该节点退出全部池。已接入（WS 在线/在线快照）但库里无配置的节点会在控制台以下拉框提示补录 |
| 节点 OTA 升级 | 控制台建任务下发（`ota_tasks` 表，见 ota.go），节点下载新二进制→校验→原子替换→重启（交接逻辑同 middleware-go/ota.go：预检→替换→优雅停机→拉起→健康检查，失败回滚 .old）。通道同运行时配置：WS `ota` 消息优先、HTTP 回退节点 `POST /v1/ota`；下载源二选一——`url` 直发或 `version`+资产基址（节点按平台自动匹配 `lemonipw-{goos}-{goarch}` 资产，可选 sha256 强校验；GitHub 官方基址且节点配了 `gh-proxy` 时自动走代理）。节点重启必然断开 WS，最终结果以**重连注册上报的新版本号**为准（WS 注册钩子即时判定 + 兜底轮询 + 15 分钟超时收敛）；节点能力清单新增 `ota`，明确不支持的节点下发前即拒绝。节点本地可配 `node-ota=false`（env `NODE_OTA`）禁用 OTA（只读容器等），节点会拒绝指令并回传原因；该开关也可在控制台「节点配置」里热改（运行时 PATCH 即时生效，无需重启） |
| 运行时配置持久化提醒 | 节点侧配置优先级 远端 > ENV > 本地 setting.json，运行时 patch 只改节点内存（persist 也只写本地文件），重启后 ENV/本地文件会顶掉改动。patch 响应带 `unpersistedKeys`（未进托管远端配置的键）与 `persistHint` 提醒；控制台「同步到托管配置」一键把改动合并进该节点的托管配置（`POST /admin/node-configs/:nodeId/merge`），重启后由 remote-config-url 自动恢复 |

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

### 注册与能力清单（capabilities）

节点连上中间件后先发 `register`，中间件据此落库在线快照（`nodes` 表）：

```json
{ "type": "register", "data": {
  "nodeId": "cn-jiangsu",
  "key": "...",                                       // 配了 ws-keys[nodeId] 时必填
  "version": "v1.4.2",                                // 节点版本号（老版本节点不发）
  "capabilities": ["probe","report","config","ota"]   // 支持的管理能力（老版本节点不发）
} }
```

`version` 由控制台「节点状态」页展示，用于判断哪些节点需要更新；OTA 任务也以
**重连注册上报的新版本号**作为成败判定依据（见下文 OTA）。新版节点支持经控制台下发 OTA
（下载→校验→替换→重启），但部署方式受限时（无写权限 / 只读容器）升级会失败并回滚，
此时仍按传统方式人工升级。

`capabilities` 决定中间件**能否给这个节点下发管理指令**（运行时配置 `config`、OTA 升级 `ota`）。
之所以需要它：老版本节点的消息循环没有对应分支，收到后**静默忽略**——既不执行也不回错，
中间件只能干等到超时，最后甩一句 `ws config timeout`，管理员看着"节点明明在线"完全无从下手。

| 上报情况 | 中间件行为 |
| --- | --- |
| 清单含该能力 | 正常下发对应指令（当前用到 `config` 运行时配置、`ota` 升级两种） |
| 清单明确**不含**该能力 | 该能力确实没有（如精简构建）：**立刻拒绝**，不发起指令、不等超时 |
| 清单为空（未上报） | 老版本节点，能力**未知**：仍尝试下发；超时后主动探活，区分"节点活着但静默忽略（程序过旧）"与"连接已僵死" |

纯 HTTP 节点没有 WS 连接，中间件探活 `GET /` 时一并取回同名 `capabilities` 字段，判定规则同上。

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
GET    /admin/nodes                         节点在线快照（含 version：节点上报的版本号；admin only）
GET    /admin/nodes/brief                   节点池简表（登录即可：enabled 节点 + 在线/版本，脱敏无上游地址；任务表单勾选源）
GET    /admin/nodes/:nodeId/events?limit=   节点在线/离线历史
GET    /admin/probes?node=&type=&since=&limit=  拨测记录（倒序）
GET    /admin/stats/summary?hours=24        按 apiType / 节点聚合
GET    /admin/stats/timeseries?hours=24     按分钟时间序列
# —— 上游节点池（数据库托管，取代 setting.json 静态节点池；见 node_defs.go）——
GET    /admin/node-defs                     节点定义列表（含停用）
POST   /admin/node-defs                     新增（{nodeId,label,url,ws,pool|pools,stack,enabled,sortOrder}）
                                            · pool 可多选："api" / "location" / "api,location"（也可用 pools:["api","location"]）
                                            · 双归属节点：location/asn 请求走 location 池，其余走 api 池；stack 仅在归属 api 时有意义
PUT    /admin/node-defs/:id                 更新（按主键 id；未传字段保持原值）
DELETE /admin/node-defs/:id                 删除
GET    /admin/node-defs/online              已接入但库里无配置的节点（控制台补录下拉框数据源）

# —— 节点运行时配置（直连节点进程，见 admin_node_config.go）——
#    与「托管配置」不同：托管配置是节点启动时拉的远端配置，这里是节点当前生效的值
GET    /admin/nodes/:nodeId/config          读取节点当前生效配置（凭据键回显 ***）
POST   /admin/nodes/:nodeId/config          下发（{action:"patch"|"refresh",config:{...},persist:bool}）
                                            · patch 只提交改动项；凭据类键（access-token/node-key/report-token）自动剔除
                                            · 通道：WS 在线走 WS，否则回退 HTTP（凭据取 api-keys，未配置则不回退）
                                            · 响应含 applied / unknown / restartRequired / ignoredKeys
                                            · 另含 remoteProtectedKeys（远端永不覆盖的凭据键）与
                                              protectedIgnored（本次 refresh 中被保护跳过的键）
                                            · ws-url 热更新：节点侧控制器按新地址多退少补，无需重启
                                            · node-id / node-key / port / cors / ipdb /
                                              report-interval-seconds / trusted-proxies / access-token
                                              需重启才生效，节点只如实回报 restartRequired，**不会自行重启**
                                            · 节点能力清单明确不含 config → 立刻 502 拒绝；
                                              config 在旧版节点就已存在，故"未上报清单"的老节点不受影响
                                            · patch 响应另含 unpersistedKeys / persistHint：
                                              未进托管远端配置的键 + 持久化提醒（远端 > ENV > 本地，
                                              重启后 ENV/本地文件会顶掉内存改动，建议同步到托管配置）

# —— 节点 OTA 升级（见 ota.go；能力标识 ota，老版本节点下发前即拒绝）——
POST   /admin/nodes/:nodeId/ota             下发升级任务（{version?, url?, sha256?}，version 与 url 二选一）
                                            · version：节点按平台自动匹配 release 资产（基址取配置
                                              ota-asset-base，缺省 github.com/nomdn/ipw-cn/releases/download）
                                            · url：直发下载地址；sha256 可选（hex64，兼容 "sha256:" 前缀）
                                            · 通道：WS 优先，回退 HTTP（节点 access-token，未配置则不回退）
                                            · 响应含 task + hint；节点重启断开 WS 属预期，最终结果以
                                              重连注册上报的新版本号为准（15 分钟未观测到即判超时失败）
GET    /admin/ota-tasks?limit=              任务列表（倒序；status: dispatched/success/failed，stage 为节点回报的进度）

GET    /admin/node-configs                  托管配置列表
GET    /admin/node-configs/:nodeId          读取配置原文
PUT    /admin/node-configs/:nodeId          写入（body=JSON 对象；nodeId=global 即全局）
POST   /admin/node-configs/:nodeId/merge    运行时配置改动合并进该节点托管配置（body={config:{...}}；
                                            恒写节点级配置、不允许 global；配合上面的持久化提醒用）
DELETE /admin/node-configs/:nodeId          删除

# —— 控制台用户（仅 admin，见 users.go；登录态见 auth.go）——
POST   /admin/login                        登录（users 表校验，签 JWT）
GET    /admin/users                        用户列表（不回显口令）
POST   /admin/users                        新增（{username,password,role?,email?}）
PATCH  /admin/users/:id                    改 {email?,role?,enabled?}（保留≥1 个启用 admin）
PATCH  /admin/users/:id/password           重置口令（{password}）
DELETE /admin/users/:id                    删除（禁删自己；级联删其任务与站内信）
```

登录用户存 DB `users` 表（bcrypt 口令哈希），分 `admin`/`user` 两角色；admin 才能访问用户管理、节点管理（节点池/运行时配置/OTA/托管配置）与全站统计。
普通用户可用：拨测工具与一键拨测（明细页"业务拨测"可见**自己发起的**记录）、自己的 SLA 任务全套、
节点简表（`/admin/nodes/brief`，任务表单"指定节点"勾选数据源，脱敏不含上游地址）、个人资料（邮箱 + Webhook 通知）、
我的用量（`/admin/me/usage`，自己任务与自己拨测的聚合）、站内信。
普通用户配额护栏：`user-task-limit`（缺省 20，0=不限）限任务数、`user-min-interval`（缺省 0=跟随全局 10s）限最小间隔；
带邮箱的账号需先验证邮箱才能建任务（既有门控）。
首次启动 users 表为空时，由 `admin-user`/`admin-password` 自动 seed 首个 admin（未配口令则 seed `admin/admin`，请及时改）。
**三轨鉴权**（`auth.go`）：① 静态 `admin-token`（向后兼容，等价 admin 身份）；② JWT（需 `jwt-secret`，`POST /admin/login` 签发，账号由用户管理页维护）；③ 个人 API Token（`ipt_` 前缀，库中存 bcrypt 哈希，供程序化访问，吊销即失效）。`/admin/*` 与 `/api/v1` 共用同一鉴权中间件，`adminOnly` 再过滤出 admin 专属接口。未配 `admin-token` 且未启用 JWT 时，管理面不鉴权（仅限内网/联调）。

```
# —— 定时拨测任务（SLA 数据源，见 probe_task.go / sla.go）——
GET    /admin/tasks?tag=                    任务列表（含 ownerUsername；?tag= 按标签子串过滤）
POST   /admin/tasks                         新建（自动把当前登录 JWT 用户记为 owner；带 notifyRecover/quietHours/tags）
PUT    /admin/tasks/:id                     更新（不改 owner；不改 enabled——启停走下方专用端点）
PATCH  /admin/tasks/:id/enabled             启停
DELETE /admin/tasks/:id                     删除
GET    /admin/tasks/:id/sla?hours=24        某任务整窗 SLA 聚合
GET    /admin/tasks/:id/series?hours=24&node=  时序曲线（?node= 只返回该节点曲线，多节点对比用）
GET    /admin/tasks/meta                    可选拨测类型/间隔元信息
POST   /admin/tasks/share                   批量分享：body {ids:[], token?} —— 多选任务共用一个令牌；
                                            token 缺省随机 6 位 hex、可自定义（3~32 位小写字母/数字/连字符）
DELETE /admin/tasks/share                   批量关闭分享：body {ids:[]}
GET    /admin/tasks/:id/share               查看某任务当前分享状态

# —— 公开状态页（免登录，见 public_status.go）——
GET    /s/:token                            只读 Vue 状态页（30s 自刷新；前端路由，SPA fallback 由托管侧提供）
GET    /api/public/status/:token?hours=24   同数据的 JSON：{hours, tasks:[...]}（60 次/分/IP 防枚举限流，超限 429）

# —— 站内信（本人通知，铃铛；见 notices.go）——
GET    /admin/notices                       本人通知列表 {list,unread}
GET    /admin/notices/unread                未读数
POST   /admin/notices/read                  {ids?:[]} 标记已读（缺省全已读）
DELETE /admin/notices                       清空本人通知

# —— 个人资料扩展（本人；见 profile.go）——
PATCH  /admin/me                            改本人资料（{email?, webhookUrl?, webhookType?}，字段可选只更新传入项；
                                            email 变更会重置验证态；webhookType: generic/wecom/feishu）
POST   /admin/me/webhook/test               向自配 webhook 推一条测试消息（返回投递结果）
GET    /admin/me/usage?hours=24             我的用量：自己任务的 sched 样本 + 自己发起的 biz 拨测，
                                            按 apiType 聚合（total/up/down/avgMs）
POST   /admin/me/token                      生成个人 API Token（ipt_ 前缀；明文仅本次返回，库里只存 bcrypt 哈希；
                                            重复生成使旧 token 立即失效）
DELETE /admin/me/token                      吊销个人 API Token（旧 token 立即 401）
```

### REST 语法糖层（/api/v1，见 rest.go）

供个人 API Token 等程序化访问的规范接口：资源名词复数、**裸资源 + HTTP 状态码**（无信封）、
错误统一 `{"error":{"code","message"}}`；鉴权与可见性同 `/admin`（三轨），不限流。

> **完整接口文档 → [`ipw-cn/docs/guide/public-api.md`](../ipw-cn/docs/guide/public-api.md)**
> （文档站「参考 → 公开 API（/api/v1）」）。含：取 Token 流程与三轨鉴权、通用约定（错误码/分页/可见性/限流）、
> 7 个端点的参数与响应示例、任务对象逐字段说明、免登录公开状态接口、排错对照表。

端点速览：

```
GET /api/v1/tasks?page=&pageSize=&tag=           任务列表 {items,total,page,pageSize}（全字段，id 倒序）
GET /api/v1/tasks/:id                            任务详情（非本人 403，不存在 404）
GET /api/v1/tasks/:id/sla?hours=24               顶层汇总 + byNode 明细
GET /api/v1/probes?node=&type=&source=&since=&limit=&offset=
                                                 拨测明细（精简无 body）
POST /api/v1/probes                              一键拨测（同步聚合）：body {apiType,raw,query?,nodes?}
GET /api/v1/nodes                                节点简表（enabled，脱敏）
GET /api/v1/usage?hours=24                       用量 {hours,total,up,down,invalid,byType[]}
```

```bash
curl -H "Authorization: Bearer ipt_xxxx" https://<collector>/api/v1/tasks
```

- **任务所有者 & 掉线/恢复告警**：任务创建者记为 `owner`。当某 SLA 任务连续达到 `alert.downThreshold` 轮整组 down，通知该 owner：
  - **邮件 + 站内信 + Webhook 三路同时发**（互不回退）：owner 有邮箱且 SMTP 可用 → 发邮件；owner 启用即落站内信（铃铛可见）；
    owner 在个人资料配了 Webhook → 按 webhookType（generic / 企业微信·钉钉 / 飞书）推送；
  - **恢复通知**：任务勾选 `notifyRecover` 时，本次故障恢复的那一轮补一封"已恢复"（三路同 down）；
  - **免打扰时段**：任务 `quietHours`（如 "23:00-07:00"，服务器本地时区，支持跨零点）内，站内信照发、邮件与 Webhook 抑制；
  - 任务无归属（静态 token 建的旧任务）→ 不通知。
- **公开状态页**：**令牌即"分享组"**——批量把多选任务绑到同一令牌（`POST /admin/tasks/share`，
  body `{ids:[], token?}`）：token 缺省随机 6 位 hex，可**自定义**（3~32 位小写字母/数字/连字符，
  同一令牌的任务在状态页同页展示）。
  页面：前端 Vue 路由 `/s/<token>`（免登录，30s 自刷新）；数据 JSON `GET /api/public/status/:token`
  返回 `{hours, tasks:[...]}`（每任务：可用率/延迟聚合/节点表/延迟曲线；任务可按 `hideTarget` 隐藏目标）。
  关闭分享：`DELETE /admin/tasks/share` body `{ids}`。防枚举：token JSON 端点限流 60 次/分/IP。
- **任务标签**：任务可打逗号分隔标签（`tags`），列表 `?tag=` 子串过滤（/admin/tasks 与 /api/v1/tasks 皆可）。
- **服务节点掉线监控**（见 nodeHealth.go）：监控配置池（api-base-url 三栈 + ip-location-api）里全部节点。
  - HTTP 版节点（非 ws）：每 1 小时 GET 该节点 `url`（health 接口就在根路径、无追加路径）探活；连续 2 次失败判 down（约 2h）。
  - WS 版节点（ws:true）：靠心跳（middleware 每 20s ping、空闲 75s 剔除），连续 3 轮(约 3 分钟)不在线判 down。
  - 仅对"本进程内曾在线"的节点告警（冷启动未连上/从未探活成功不报，避免误报）；down 翻转通知一次、恢复复位后可再报。
  - 投递：发给**所有启用 admin**，每人**邮件+站内信同时发**（kind=`node_down`）。
  - **OTA 在途豁免**：节点有在途 OTA 任务（dispatched）时的掉线不告警（计划内重启必然断连一次），
    offline 事件照记可追溯；任务失败终结且节点仍未恢复上线时补报（见 ota.go）。
- **删除用户会级联删除其创建的任务与该用户的站内信**；同时始终保留至少 1 个启用 admin（禁删自己、不能降级/禁用最后一个 admin）。

## SLA 判定口径

调度器每 1s 扫描启用中的任务，按各自 `interval`（最小 10s）对**在线**节点下发采样，结果落 `probe_results`（`source=sched`）。SLA 的所有指标都从这些样本现算：

- **只认 `source=sched`**：节点上报的 ws/http 样本与手动「一键拨测」（`source=biz`）**不进 SLA**，但可在拨测明细页按分类查看。
- **不信任链路的 HTTP 状态**：`detail` / `ssl` 链路对上游业务总是返回 200，判定改为读取存储的响应体实时解析——`detail`/`ssl` 解析双栈的 `https_status_code` / `http_status_code`；`dns` 记录数组非空即 up（NXDOMAIN 即 down）；`tcping` / `speed` 取 `is_reachable`。叠加任务的判定配置：`expectStatus`（期望状态码）、`bothProtocols`（两种协议都要通）、`requireAllStacks`（所有栈都要通）、`certExpiredDown`（证书过期即 down）。
- **延迟只用可达（up）样本**，且取**节点实测延迟**（`detail`/`ssl` 取栈 `total_time`、`tcping` 取 `avg_rtt`、`speed` 取 `total_time`），不含控制台 → 节点的链路耗时。
- **可用率** = up / 样本数；**达标率** = (up − slow) / 样本数，`slowMs` 为任务的慢阈值。
- **判定在读取时计算**（写入只存原始 body），因此改判定配置立即对历史样本生效，无需回填重算。
- 离线节点不参与本轮采样，也不计入分母；「整组 down」要求本轮至少存在 1 个有效样本（避免全体离线被误判为服务故障）。

## 浏览器控制台（web/）

技术栈 Vue3 + Vite + vue-router + Pinia + ECharts + [`@yunyoujun/ak-ui`](https://github.com/YunYouJun/ak-ui)（明日方舟风格）。独立于服务端构建，开发态**直连后端 origin**（跨域请求，后端已开 CORS，未配 dev proxy）。

| 页面 | 路由 | 权限 | 内容 |
| --- | --- | --- | --- |
| 统计大盘 | `/` | 全部（按角色分流） | admin：全站请求统计（KPI + 趋势 + 按接口/节点分布）；普通用户：本人任务的可用率与延迟大盘 |
| 节点状态 | `/nodes` | admin | 节点卡片墙（在线/版本/远端地址/首见）；上下线事件历史；OTA 任务列表与下发对话框（按版本或直发 URL，可选 sha256） |
| 一键拨测 | `/probe` | 全部 | 选类型 / 目标 / 节点实时批量拨测，结果按类型结构化渲染 |
| 拨测明细 | `/records` | 全部 | 按「定时拨测 / 业务拨测」分类查询（普通用户只见自己发起的） |
| SLA 监控 | `/sla` | 全部（需验证邮箱） | 任务卡片（可用率 / 延迟曲线 / 失败轮次标记 / 节点对比 / 逐节点明细）、任务 CRUD、批量分享 |
| 配置分发 | `/config` | admin | 上游节点池（数据库托管）、节点运行时配置读写、托管配置编辑 |
| 用户管理 | `/users` | admin | 用户增删改、启停、重置口令 |
| 个人资料 | `/profile` | 全部 | 邮箱验证、改口令、Webhook 通知、个人 API Token、我的用量 |
| 登录 / 注册 | `/login` | 公开 | JWT 登录；开启 JWT 后支持邮箱验证码自助注册 |
| 公开状态页 | `/s/:token` | 公开 | 免登录只读 SLA 分享页（30s 自刷新） |

- **权限边界**：前端 `meta.role` 只控制菜单显隐与路由跳转，真正的权限收紧在后端 `adminOnly` 中间件，不要依赖前端做安全边界。
- **实时推送**：SLA 页经 `WS /console/sla?hours=&token=` 接收实时帧，断线按 1s→15s 指数退避重连，重连后补拉全量；切换窗口重建连接。
- **构建 / 开发**：

  ```bash
  cd web
  pnpm install
  pnpm dev        # 开发：127.0.0.1:5173
  pnpm build      # 产物 web/dist，由任意静态托管提供
  ```

  后端地址由构建期变量 `VITE_API_BASE` 注入，缺省 `http://127.0.0.1:8091`（`WS_BASE` 由其自动推导 ws/wss）。
- **SPA 回退**：公开状态页 `/s/:token` 是前端路由，托管侧需配置 history fallback（未命中路径回退 `index.html`）。

## 配置项（setting.json）

原版全部键位不变（port / http-timeout-seconds / rate-limit / ws-port / remote-config-url / remote-ignore-config / cors / trusted-proxies / api-base-url / ip-location-api / api-keys / ws-keys），新增：

> **节点池已迁到数据库**：`api-base-url` / `ip-location-api` 现为**迁移兜底**——两个池分别判定，某池在 `node_defs` 表里一条记录都没有时才回退到 setting.json；一旦在控制台录入过该池的节点，就完全以数据库为准（该池节点被全部停用即为空池，属管理员明确意图）。新建部署可直接在控制台「上游节点池」录入，不必再写这两个键。`api-keys`（转发鉴权）仍在配置文件，按 backendID 匹配控制台里的节点 ID。

| 键 | 缺省 | 说明 |
| --- | --- | --- |
| `database` | sqlite boce.db | 见上文 |
| `admin-token` | 空 | /admin/* 鉴权，空=不鉴权 |
| `data-retention-days` | 30 | 0=永久保留 |
| `report-token` | 空 | /report 鉴权 token（节点上报时携带）；空回退 `admin-token`，都空=开放 |
| `jwt-secret` | 空 | JWT 签名密钥（HS256）；**空 = 禁用 JWT 登录/自助注册**，只能用静态 `admin-token` |
| `jwt-expiry-seconds` | 86400 | JWT 有效期（秒） |
| `admin-user` / `admin-password` | admin / 空 | 首个 admin 的 seed 账号与口令：`users` 表为空时按此创建；`admin-password` 留空则 seed `admin/admin`（**请及时改**）。同时作为 JWT 登录的兜底校验源 |
| `user-task-limit` | 20 | 普通用户可建任务数上限，0=不限 |
| `user-min-interval` | 0 | 普通用户建任务的最小间隔（秒），0=跟随全局最小值（10s） |
| `smtp` | host 空=禁用 | SMTP 发信配置（host/user/password/from/fromName/port/ssl/startTLS/insecure/timeoutSec）。发信对象按任务 owner 动态解析 |
| `alert` | enabled+threshold3 | 掉线告警策略（enabled/to/downThreshold）。某 SLA 任务连续 N 轮判定整组 down 时，通知其「所有者」（见 alert.go）：邮件+站内信同时发（有邮箱走邮件；站内信恒发）。`to` 字段已不再作为收件兜底（保留兼容）。节点掉线监控参数为固定值（见 nodeHealth.go），无需配置 |
| `ota-asset-base` | GitHub Releases | 节点 OTA 按版本下发时的资产基址（节点拼 `{base}/{tag}/lemonipw-{goos}-{goarch}`；自建镜像源时改成自己的地址） |

上述键均可由环境变量覆盖（`DATABASE_DRIVER` / `DATABASE_DSN` / `DATABASE` / `ADMIN_TOKEN` / `DATA_RETENTION_DAYS` / `REPORT_TOKEN` / `JWT_SECRET` / `JWT_EXPIRY_SECONDS` / `ADMIN_USER` / `ADMIN_PASSWORD` / `USER_TASK_LIMIT` / `USER_MIN_INTERVAL` / `SMTP` / `ALERT`，以及原版那一组 `PORT` / `HTTP_TIMEOUT` / `RATE_LIMIT` / `WS_PORT` / `CORS` / `TRUSTED_PROXIES` / `REMOTE_CONFIG_URL` / `REMOTE_IGNORE_CONFIG` / `API_BASE_URLS` / `IP_LOCATION_APIS` / `API_KEYS` / `WS_KEYS`）。

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
- 中间件**自身**的 OTA 自更新未迁移（决策：中间件不含自更新）；取而代之的是**控制台对节点的 OTA 下发**（见 ota.go 两侧实现）：升级时机由管理员掌握，节点只执行单次任务，不再有原版"定期检查 GitHub 自动更新"的常驻循环
- 原版节点池为空时启动失败；本服务允许空池运行（仅作 WS 枢纽/配置托管），改为告警
- 修复原版一处 bug：原版 `main()` 用 `PORT = os.Getenv("PORT")` 覆盖配置值，setting.json 的 `port` 永不生效；本版正确实现 env > setting.json > 8080

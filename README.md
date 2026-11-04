# 1IPW-BOCE（积分版）

按 [nomdn/ipw-boce](https://github.com/nomdn/ipw-boce) 的协议与鉴权体系重写的拨测中心，内置积分系统与积分商城。后端 Go（Gin + GORM），前端 Vue 3 + NutUI，界面为 ChatGPT 风格（左侧会话栏 + 居中对话式拨测）。

## 运行

```bash
# 前端
cd web && npm install && npm run build          # 产物 web/dist，由后端托管

# 后端（仓库根目录）
cp setting.json.example setting.json             # 至少填 jwt-secret、admin-password
go build -o boce . && ./boce                     # http :8080，节点 WS :8092/ws
```

开发时 `cd web && npm run dev`（5173，代理到 8080，`BOCE_API` 可改目标）。

仓库布局：根目录是后端 Go 代码（`package main`，CI 在根目录构建）；`web/` 是前端；`agent/` 是捐赠节点的一键安装 agent（独立 Go 模块）；`scripts/mocknode` 是联调用的模拟节点。
`go test ./...` 覆盖鉴权与错误体、积分幂等、并发扣减、签到、兑换码、卡密分配、Webhook 发货与退款。

## 与原版保持一致的部分

- **配置键名**：`setting.json` 与环境变量同原版（`port`、`ws-port`、`api-base-url`、`ip-location-api`、`api-keys`、`ws-keys`、`database`、`admin-token`、`jwt-secret`、`report-token`、`smtp`、`alert`……），新增 `web-dir`。
- **鉴权三轨**：静态 `admin-token`（uid=0、admin）、`POST /admin/login` 签发的 HS256 JWT（claims `{sub,uid,username,role,iat,exp}`）、个人 Token `ipt_` + 40 hex（bcrypt 存储，可吊销）。角色 `admin` / `user`，user 只见自己的任务与数据。
- **错误体**：`/admin/*` 等内部接口为 `{"statusCode":N,"statusMessage":"..."}`；`/api/v1/*` 与公开接口为 `{"error":{"code","message"}}`；转发上游状态码与 body 原样透传，仅网络错误返回 502 `Backend unreachable`。
- **节点协议**：WS 信封 `{type,nodeId,ts,data}`（register / probe / probe_result / report / ping / pong / status），注册 key 校验、20s 心跳、75s 空闲剔除；`POST /report` 上报协议 v1（按分钟 × 节点 × apiType 累加）；中心主动拨测带 `scheduler:true` / `X-Scheduler-Probe: 1`。
- **其余接口**：`/v1/*`、`/middleware/*` 转发与限流，`/remote-config[/:nodeId]`，`/admin/*` 节点池 / 统计 / 明细 / 任务 / SLA / 分享 / 用户 / 资料 / 站内信，`/api/v1/*` 语法糖层，`/api/public/status/:token`，`/console/sla` 浏览器推送。SLA 判定口径（双栈 body 解析、改配置重算历史）同原版。

- **节点运维**（与原版协议一致，WS 优先、HTTP 回退，按节点上报的 capabilities 做能力门）：
  - 运行时配置 `GET/POST /admin/nodes/:nodeId/config`（凭据键自动剔除，未进托管配置的改动会提示，可一键 `POST /admin/node-configs/:nodeId/merge`）
  - OTA `POST /admin/nodes/:nodeId/ota {version|url, sha256?}`、`GET /admin/ota-tasks`；以节点重连上报的版本判定成功，15 分钟超时；`ota-asset-base` 可配资产地址
  - 维护窗口 `/admin/maintenance`（一次性 / 每日重复，支持跨零点）：只屏蔽上下线通知，事件照记
  - 可用率 `GET /admin/nodes/uptime?days=`、`/admin/nodes/:nodeId/uptime`（含逐日）
  - CSV（UTF-8 BOM）：拨测明细、节点事件、SLA 曲线、可用率、积分流水、商城订单，均在 `…/export`

## 积分系统

| 获取 | 说明 |
|---|---|
| 每日签到 | 可开关；基础分 + 连续签到加成（可配封顶天数） |
| 捐赠节点 | 审核通过一次性奖励 + 每在线一天奖励，机房 / 家庭宽带分别定价 |
| 注册奖励 | 自助注册成功后发放 |
| 积分兑换码 | 管理员批量生成，每码可设可用次数和过期时间，同一用户同码只能用一次 |
| 管理员手动增减 | 后台「积分管理 → 账户」 |
| 外部接口发放 | `POST /api/v1/points/grant`，支持 `Idempotency-Key` |

可选消耗：普通用户每次一键拨测扣积分（默认 0 = 免费），拨测没有执行时自动退还。

所有余额变动都在一个事务里完成：守卫式扣减（`balance >= ?`）加写流水，余额不会出现负数，每笔都有 `balanceAfter` 快照。

### 商城发货方式

- **卡密自动发（cdk）**：导入卡密库存，兑换时在事务里原子分配，并立即展示给用户。
- **接口发放（webhook）**：兑换后向商品配置的 URL 发签名请求，由你的系统发货。
- **人工发货（manual）**：生成待发货订单，同时通知管理员，管理员在后台填写发货内容。

失败或取消的订单会退还积分并回补库存，同一订单只退一次。

### 开放接口（管理员身份：admin-token 或管理员的 ipt_ Token）

```
POST /api/v1/points/grant    {userId|username|email, points, remark}   Header: Idempotency-Key
POST /api/v1/points/deduct   同上；余额不足 402 insufficient_points
GET  /api/v1/points/balance?username=
GET  /api/v1/points/ledger?userId=&kind=&limit=&offset=
POST /api/v1/points/codes    {count, points, maxUses, prefix, code, expiresAt, remark}
GET  /api/v1/shop/orders/:orderNo
POST /api/v1/shop/orders/:orderNo/deliver   {content}   幂等
POST /api/v1/shop/orders/:orderNo/fail      {reason}    自动退款
```

### Webhook 发放协议

```
POST <deliveryUrl>
X-Boce-Event: order.created
X-Boce-Timestamp: <unix 秒>
X-Boce-Signature: sha256=hex(HMAC_SHA256(secret, timestamp + "." + rawBody))

{"event":"order.created","orderNo":"...","productId":1,"productName":"...","quantity":1,
 "totalPoints":100,"userId":2,"username":"alice","email":"...","remark":"...","createdAt":...,
 "callback":"/api/v1/shop/orders/<orderNo>/deliver"}
```

15 秒内返回 2xx，响应体三选一：

- `{"content":"..."}`：立即发货完成。
- `{"status":"pending"}`：之后通过回调接口完成。
- `{"status":"failed","message":"..."}`：失败并退款。

非 2xx 或超时同样按失败退款处理。签名密钥在新建商品时自动生成（`whsec_…`），只显示一次，可在后台重置；后台的「测试接口」会发送 `event=ping`。

### 捐赠节点

1. 用户在「捐赠节点」提交：网络类型（机房 / 家庭宽带）、地区、运营商、栈、接入方式（WS 推荐，家宽无需公网端口；或 HTTP + access-token）。
2. 系统分配 nodeId 和 WS Key（`nk_…`），并给出接入配置（`ws-url` / `node-id` / `node-key`，对应环境变量 `WS_URL` / `NODE_ID` / `NODE_KEY`）。地址取 `public-ws-url` 配置，未配置时按请求域名加 `ws-port` 推算，反代部署请配置它。
3. 待审核节点已经可以连上来，管理员在「捐赠审核」能看到它是否在线，但它不进节点池、不参与拨测。
4. 审核通过：写入节点池（`node_defs`，带 `donationId` / `netType`），发放一次性奖励（幂等，重复通过不会重复发）。拒绝 / 停用 / 用户撤回：移出节点池并断开连接，已发奖励不追回。
5. 每天 00:05 结算前一天（服务器本地时区）：由 `node_events` 还原在线时长，达到「在线满 N 小时」的节点按类型发放每日奖励。流水幂等键为 `donate_day:<捐赠ID>:<日期>`，重复结算或手动补结算都不会重复发。维护窗口内的离线按在线计算。启动时会自动补结算最近 3 天。

相关规则都在「积分管理 → 规则」：两类节点的一次性奖励和每日奖励、在线满几小时算一天、每人最多捐赠数。

### 一键安装节点（agent）

1. 用户在「捐赠节点」页点「生成安装命令」，得到个人「节点 Token」（`nat_…`）和命令：
   `curl -fsSL https://<boce>/agent/install.sh | sudo bash -s -- --token nat_xxx --node <节点ID>`（卸载加 `--uninstall`）。
2. 脚本安装 `boce-agent`（systemd 服务）和节点程序。agent 默认每 300 秒向 `POST /api/agent/bootstrap` 轮询：只有**审核通过**的节点才能拿到 `ws-url / node-id / node-key` 并启动节点；被撤回 / 停用后 agent 会停掉节点服务。
3. 安装包放在 `agent-dir`（默认 `agent/`，注意它是运行目录下的数据目录，不是源码目录）：`boce-agent-linux-{amd64,arm64,armv7,386}` 与 `lemonipw-linux-*`，由 `/agent/download/<文件>` 提供。bootstrap 会带上 SHA256，agent 发现变化后校验并自动升级自己和节点程序——**替换目录里的文件就是「下发更新」**。agent 源码在 `agent/`。

### 系统设置（后台「系统设置」）

- 注册开关、注册是否需要邮箱验证码、人机验证（关闭 / 图形验证码 / Cloudflare Turnstile，作用于登录与注册）、SMTP（可发测试邮件）。
- 保存后立即生效、不用重启，存在数据库里；首次的默认值取自 `setting.json`（`registration-enabled`、`smtp`）。默认要求注册验证邮箱，需先配好 SMTP，或在设置里关掉「注册需邮箱验证码」。

### 弹窗广告

后台「系统设置 → 弹窗公告」配置一条弹窗（标题、正文、顶部图片、点击跳转、弹出频率：只弹一次 / 每天一次 / 每次打开），可上传图片（png / jpg / gif / webp，≤3MB，存在数据库同级的 `uploads/`），可分别勾选在 boce 和 1IPW 显示。内容变更会生成新的版本号，已点过「我知道了」的用户会重新看到。1IPW 主站经 `GET /api/sync/popup` 拉取。

### 给 1IPW 主站用的接口（Bearer `sync-token`）

| 接口 | 说明 |
| --- | --- |
| `GET /api/sync/nodes` | 节点表（含捐赠节点、上报的能力与版本），1IPW 据此动态生成检测节点，不再在前后端写死 |
| `POST /toolbox/:nodeId/:path` | 网络查询工具箱：把 POST 拨测经 WS 转给捐赠节点执行（白名单路径，请求体 ≤8KB） |
| `GET /speedtest/:nodeId/payload`、`POST /speedtest/:nodeId/upload` | 捐赠节点的浏览器测速：数据经 WS 二进制帧中转，节点需上报 `speedtest` 能力；测到的是整条中转链路的速度，不代表节点本身带宽。反代请对 `/speedtest/` 关闭缓冲（`proxy_buffering off; proxy_request_buffering off;`） |
| `GET /api/sync/popup` | 当前要在 1IPW 显示的弹窗（没有则 `null`） |

配置项：`sync-token`、`public-base-url`、`agent-dir`。

## 安全提示

- `/remote-config` 与 `/api/public/status/:token` 无需鉴权（与原版一致）。公开状态页按 IP 限流 60 次/分钟，分享码默认只有 6 位 hex。
- `admin-token`、`report-token` 都留空时，`/report` 对所有人开放，仅适合内网。
- 没有配置 `admin-password` 时会创建 admin/admin 初始账号，首次登录后请立即修改密码。
- 积分开放接口要求管理员身份，外部系统建议使用单独管理员账号的 `ipt_` Token，方便单独吊销。

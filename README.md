# Task Game Open Protocol v0.1

Simulated-escrow HTTP API for the Task Game Open Protocol. The contract is [`api/openapi.yaml`](api/openapi.yaml) (OpenAPI 3.1). The service is a [go-zero](https://github.com/zeromicro/go-zero) API: `taskgame.api` is the goctl source, handlers and routes are generated, and the task state machine lives in `internal/engine` behind a `store.Store` interface. The shipped default `etc/taskgame.yaml` is MySQL 8.0.19+ (InnoDB, utf8mb4) with **horizontal cell sharding**: three independent databases with identical full schemas, each owning about one third of users, tasks, messages and every other table. There is no primary database holding all rows. SQLite through `modernc.org/sqlite` (no cgo) remains available for local single-process debugging by setting `Store.Driver: sqlite` explicitly.

Credits are simulated. There is no payment provider. Locked v0.1 rules:

- `competitive.settlement_rule` is `winner_take_all`
- `collaborative.on_dropout` is `replace` (the exitee gets 0 commission and 0 ability)
- collaborative `roles[]` may include `quantity` (default 1). `share_bps` is the template total and still sums to 10000. The server expands seats. Omitting `quantity` keeps the old flat role list.
- `collaborative.settlement_mode` is `per_seat` or `unified` (default). Unified stays `open` until the publisher starts a full task, then one confirm after every seat submits. Per-seat confirm pays one `role_id` and leaves the rest held. Omitting the field means unified.
- acceptance is executor submit, then publisher confirm
- `review_hours` defaults to 72; exclusive tasks auto-settle when that review window passes
- a competitive task with exactly one current submission still auto-settles at `review_deadline`. More than one does not guess a winner: `winner_select_deadline` is `review_deadline` plus `AutoSettle.WinnerSelectHours` (default 72), then the pool is refunded (`winner_select_timeout`)
- `POST /tasks/{id}/cancel` refunds held funds when nobody has submitted. A per-seat task with submissions cancels only open or filled-but-unsubmitted seats. Submitted work returns 409 (revise, confirm, or dispute)
- `POST /tasks/{id}/revise` marks a submission superseded (default `acceptance.max_revisions` is 3) and returns that work to active
- dispute still freezes. Per-seat dispute freezes only that seat's share. `POST /tasks/{id}/resolve` applies when both sides post the same outcome. `POST /tasks/{id}/arbitrate` is immediate and only for `Escrow.ArbiterUserIds` (dev user `usr_arbiter`, key `dev-arbiter-key`)
- a full refund from resolve or arbitrate closes the task as `cancelled`. A payout closes it as `settled`. `escrow.unfreeze` is recorded before the release or refund
- funding creates one task chat (`chat_id`); accept adds the executor. `GET /chats/{id}` and `GET /chats/{id}/messages` (oldest first) are member-only
- task bodies include `submissions[]` (`current` or `superseded`). Competitive confirm picks `submission_id` from that list
- open / start / seat timeouts default to 72h on the same ticker. `open_deadline`, `start_deadline`, `winner_select_deadline`, and per-seat `refund_deadline` are on the task. An accepted task with no current submission cancels at `acceptance.deadline`
- `assignee_policy` is enforced on accept. `specific` allows listed users or a pending invite; a declined invite blocks until re-invited. `GET /tasks?assignable=me` lists tasks the caller can accept. `GET /me/invites` is the inbox. Friendship does not change who can accept a task
- mutual friends: `POST /friends/requests`. Already friends, or your own pending request, is 409. If the other user already asked you, that call accepts their request (200) instead of creating a second one. Groups are personal (50 per user, name 1–32 characters, a friend is in at most one). Unfriend clears both sides. Events `friend.requested`, `friend.accepted`, `friend.declined`, `friend.removed` go to the other party's webhooks
- direct chats: `POST /chats` `{type: direct, user_id}` stores one 1:1 thread per pair. The other user must exist. Friendship is not required and is not created. Yourself is 400. An unknown user is 404. The first call inserts it and emits `chat.created`. A later call, including after unfriend, returns the same id (still 201). `Chat.type` is `task`, `direct`, or `group`. Unfriend does not remove members, so the thread stays readable and writable. Task chats stay on the task.
- direct unread: `GET /friends` adds `unread_count` and `direct_chat_id` on each friend. The count is incoming messages in that pair's direct chat after the caller's cursor. Own messages, task chats, and group chats are not counted. `POST /chats/{chatId}/read` moves only the caller's cursor and does not emit a webhook. Listing messages does not move it.
- mutating calls accept `Idempotency-Key`; the same key with a different body returns 409 `idempotency_conflict`
- webhooks are queued and delivered with `X-Signature` (hex HMAC-SHA256 of the raw body) and `X-Event-Id`
- auth is `Authorization: Bearer` (HS256 JWT) and/or `X-Api-Key`. `POST /v1/register` and `POST /v1/login` are public and issue that same JWT (`sub` is the new user id). Passwords are stored as bcrypt.

Sample development keys (see `etc/taskgame.yaml`):

| Header | User |
| --- | --- |
| `X-Api-Key: dev-publisher-key` | `usr_publisher` |
| `X-Api-Key: dev-executor-key` | `usr_executor` |
| `X-Api-Key: dev-arbiter-key` | `usr_arbiter` |

Mint a dev JWT with `make token` (subject defaults to `usr_publisher`).

## 浏览器实时信号（SSE）

`GET /v1/events/stream` 是给浏览器 UI 用的**出站信号通道**，不属于 `api/openapi.yaml` 描述的对外 JSON 协议，因此没有对应的 SDK 方法（`taskgame-client` 不消费流）。它用 `text/event-stream` 单向推送轻量提示，**不带消息正文**：客户端收到后仍用 `GET /chats/{id}/messages`、`GET /friends` 拉数据。好友页和会话页因此不再依赖 5 秒轮询来感知新消息。

- 认证沿用 `Authorization: Bearer` 或 `X-Api-Key`。`EventSource` 不能自定义请求头，所以前端用 `fetch` + `ReadableStream` 读取（`web/src/eventStream.ts`）。
- 请求必须带精确的 `Accept: text/event-stream`；go-zero 的 `rest.WithSSE()` 会清掉服务端写超时，超时中间件也会对该请求放行，所以不用改全局 `Timeout`。
- 连接建立时发 `hello`；带 `Last-Event-ID` 重连时改发 `resync`。两种帧都让前端重新拉一次，覆盖断线期间漏掉的消息；每 15 秒另发 `: ping` 注释帧保活。
- `POST /chats/{id}/messages` 在事务**提交之后**才广播 `chat.message`（只含 `chat_id` / `message_id` / `sender_id`）。幂等重放不会重复通知。它和 `chat.message.created` webhook 是两条独立链路：webhook 面向第三方 URL 且在事务内入队，SSE 面向当前登录用户的浏览器。
- 前端保留 45 秒兜底轮询，防止信号丢失或流连不上；不可见标签页依旧不发请求。
- 订阅表本身在进程内存里（`internal/notify`），但默认配置已开启 `Scale.PubSub: redis`：跨进程信号走 Redis 广播，多实例下也能实时推送。Redis 由 `docker compose up -d` 一并启动；未启动时 API 会在启动阶段直接失败（不静默退化成单机）。客户端仍保留 45 秒兜底轮询。反向代理需禁用响应缓冲（服务端已发 `X-Accel-Buffering: no`）。

## 展示层：全局未读角标与桌面通知

只靠 SSE，用户停在首页或任务详情页时仍然看不到新消息。为此前端在 `App.tsx` 挂了两个**全局**消费者，与具体页面无关：

- **未读角标**：`web/src/useUnread.ts` 汇总 `GET /v1/friends` 的 `unread_count`，`TopBar` 上的铃铛在任何页面显示总数（>99 显示 `99+`），点击跳转好友页。SSE 信号、`hello`/`resync`、标已读（同标签事件 + 跨标签 `BroadcastChannel`）、切回可见、45 秒兜底轮询都会触发重算。**口径与好友页一致：只统计好友的一对一会话**，任务会话、群聊、非好友直聊不计入（服务端只在 Friend 上暴露 `unread_count`）。
- **桌面通知**：`web/src/notifications.ts` 直接消费 `chat.message` 信号，因此**非好友的一对一会话也能弹**。只有「别人发的、权限已授予、偏好已开启、且页面不可见或当前不在该会话」时才弹；`hello`/`resync`、自己发的消息、正在看的会话都不弹。通知只写「新消息」，点击后 `window.focus()` 并跳到 `#/chat/{id}`，正文由会话页照常拉取。同一会话用 `tag` 合并，同一 `message_id` 去重。
- 权限申请必须在用户手势里：顶栏账号菜单或「用户信息」页的开关按钮调用 `Notification.requestPermission()`，偏好存在 `localStorage`（`taskgame.web.desktopNotifications`）。浏览器拒绝或不支持时，角标仍然可用。
- 展示层是纯前端，**没有新增后端接口或协议字段**，`api/openapi.yaml` 与 SDK 不变。
- 边界：页面完全关闭后收不到通知（那需要 Web Push / Service Worker + VAPID）；当前实现不弹浏览器窗口内的 toast，也不做声音提示。

## 水平三库单元化（默认，方案 A）

默认 `etc/taskgame.yaml` 是 **3 个彼此独立的 MySQL 实例**（`127.0.0.1:3307` / `3308` / `3309`），每个实例一个 `taskgame` 库，**三套表结构完全相同**。每个库保存约 **1/3** 的 `users`、`tasks`、`messages` 及其余全部业务表；**不存在保存全量数据的主库**。

另外默认开启了 `Scale.PubSub: redis`（Redis 在 `127.0.0.1:6379`，由 `docker compose up -d` 与三个库一起启动），只用于跨 API 进程的 SSE 信号广播——**Redis 里不存任何业务数据**。多实例时每个进程复制一份配置，**只改 `Port`**；`Scale.InstanceID` 留空即可，进程启动时会自动生成唯一 id。若手写固定值，必须保证每个进程不同，否则对方的信号会被当成“自己发的回声”丢掉。其余（三个 DSN、Redis 地址与频道）保持一致。

```
请求 ──► Auth 中间件（user_id，或 URL 里的 taskId / chatId / userId）
           │  FNV-1a % 3
           ├──► cell 0 (3307)   ~1/3 users / tasks / messages / ...
           ├──► cell 1 (3308)   ~1/3 ...
           └──► cell 2 (3309)   ~1/3 ...
                     │
          单库事务（无 2PC）；无主键的全局读 scatter-gather 合并
```

- **路由**：`internal/store/cell`。认证请求按 `user_id` 定位；`/v1/tasks/{id}`、`/v1/chats/{id}`、`/v1/users/{id}` 按 URL 里的主键定位，所以同一个任务/会话的所有参与者都会落到同一个库。
- **写**：`InTx` 只在一个库里开事务。事务内 `Ensure`、好友关系等跨单元写是幂等或可交换的尽力而为操作，**不使用 2PC**。
- **读**：市场列表、按 email 登录、webhook 投递扫描等没有单一主键的读遍历三个库并合并；好友卡片昵称、积分按 `user_id` 回表补齐。
- **单元化的含义**：一个用户的账号、其发布的任务、其会话与消息、其好友视角都在自己的库里。**单个用户的数据不会拆到多个库**——因此一个超级用户创建大量任务时，这些任务会集中在它所在的库；均匀分布体现在**用户维度**，海量用户下三个库自然各约 1/3。
- **扩展**：`Store.Cells` 是**有序列表**，下标就是单元编号。加/减单元属于数据迁移（按 `user_id` 重算归属并搬迁该单元的数据），不能只改配置完成；当前原型没有在线迁移工具。
- **边界**：这是可运行的单元化原型。跨单元的余额/积分更新、好友关系双写是最终一致（`Ensure` 幂等、`UPDATE` 可重放），没有分布式事务；单条消息只有一份，落在会话所在单元。

### 按域分片（P2，可选，默认关闭）

设置 `Store.Mode: domain` 后，user / task / message 三个域各自按 `Scale.Domains.<Name>.DSNs` 路由；**未声明 DSNs 的域自动继承 `Store.Cells`**，所以第一步可以把三个域都指向原来的三库，API 行为与单元化模式一致。之后每次只给一个域声明新的 DSN 列表即可逐域切分（例如消息域先迁到 6 个专用库）。详见 `docs/per-domain-sharding-plan.md` 的阶段 2。

切换某个域的物理 DSN 前，用 `shardctl` 回填目录并搬迁历史数据（message/user/task 三域工具和 SQLite 自动化演练已交付，runbook 见 `docs/physical-shard-drill.md`）。物理分置后跨域写入默认为最终一致，可开启 `Store.StrictTx` 让它直接失败；资金类操作有持久化 saga 补偿，迁移窗口有冻结写护栏，在线读路径有目录缓存/副本与 `/metrics`。真机 MySQL 脚本 `scripts/drill-mysql.sh` 已写好，但作者环境缺少 Docker/MySQL/Redis，尚未实测。逐项状态见 `docs/remaining-work-backlog.md`。

## 消息分片（旧模式，非默认）

下面是 PR #38 的**消息级**分片原型，只在 `Store.Shards > 1` 时启用；默认配置已改为上面的三库单元化。原说明保留如下，便于对照。**注意：下文提到的 `docker-compose.3instances.yml` 与 `etc/mysql-init.d` 已随默认配置切换而删除；要重跑旧模式，按下面的 YAML 片段自行准备 1 个主库 + N 个片库。**

`Store.Shards: 2` + `ShardDriver: mysql` 是“一个主库 + 两个片库”：`taskgame` 存账号、任务、账本、好友、会话元数据，`taskgame_shard_0` / `taskgame_shard_1` 各存约一半聊天消息（按会话 ID）。

`Store.Shards > 1` 时，消息本体不再写入主库，而是按**会话聚合根**路由到 N 个独立数据库。`Store.Shards` 为 0 或 1 时仍走原来的单库路径，行为完全一致。**片库驱动与主库驱动互相独立**：主库可以是 SQLite 或 MySQL，片库可以是本机 SQLite 文件，也可以是一组网络 MySQL 数据库。

**A. 本机文件分片（`ShardDriver: sqlite`）**，想要零依赖的纯本地调试时，复制配置改成：

```yaml
Store:
  Driver: sqlite
  DSN: "file:data/taskgame.db"        # 保留原有 DSN 与其 pragma
  Shards: 2
  ShardDSN: "file:data/shard-%d.db"   # 模板，%d 替换为片序号
```

**B. 网络数据库分片（`ShardDriver: mysql`）**，`ShardDSNs` 按片序号给出**每片一个 DSN**，可以指向不同主机，所有 API 进程都能连到同一批片库：

```yaml
Store:
  Driver: mysql
  DSN: "root:123456@tcp(127.0.0.1:3306)/taskgame?charset=utf8mb4"
  Shards: 2
  ShardDriver: mysql
  # db-shard-0 / db-shard-1 只是示意主机名，需能被 DNS 或 /etc/hosts 解析；
  # 单机演示请直接写 127.0.0.1:3306，与默认配置一致
  ShardDSNs:
    - "root:123456@tcp(db-shard-0:3306)/taskgame_shard_0?charset=utf8mb4"
    - "root:123456@tcp(db-shard-1:3306)/taskgame_shard_1?charset=utf8mb4"
```

默认 `etc/taskgame.yaml` 就是这个 B 形态，只是两个 DSN 都指向 `127.0.0.1:3306` 上的不同库（`docker compose up -d` 会用 `etc/mysql-init.d` 建好两个片库；已有数据卷需要 `docker compose down -v` 重建才会执行初始化）。`ShardDSN` 模板在 MySQL 下也可用（同一主机的不同库），但只有列表能表达跨主机拓扑。片库 DSN 会补上 `utf8mb4` / `clientFoundRows`，`messages` 表由服务启动时自建，**库本身需预先存在**。

**C. 一个库一个 MySQL 实例**：`Shards: N` 时共需 **N + 1 个实例**（1 个主库实例 + N 个片库实例），每个实例只持有一个 schema。同一台机器上用**不同端口**就能起多个独立的 mysqld，连接写法与跨机器完全一致：

```bash
docker compose -f docker-compose.3instances.yml -p taskgame-3inst up -d   # 3307 主 / 3308 片0 / 3309 片1
cp etc/taskgame.yaml /tmp/taskgame-3inst.yaml
# 把 /tmp/taskgame-3inst.yaml 里 3 个 DSN 的端口改成：主库 3307、片 0 为 3308、片 1 为 3309
CGO_ENABLED=0 go run . -f /tmp/taskgame-3inst.yaml
```

改完后主库 DSN 指 `3307/taskgame`，`ShardDSNs` 依次指 `3308/taskgame_shard_0`、`3309/taskgame_shard_1`。**每个后端进程都连全部 N+1 个实例**（`openStore` 开 1 个主库连接池 + N 个片库连接池），不是 A 连片 0、B 连片 1 这种按进程分工——因为任意请求都可能路由到任意片。所有进程必须使用**同一份、同顺序**的 `ShardDSNs`。实测：三个容器 hostname 各不相同；主库实例只有 `chat_shards` / `message_outbox`（`messages` 表空），两个片实例各只有 `messages` 表且分别持有不同会话的消息，片内 `chat_seq` 独立自增。

- **路由**：`internal/store/shard/router.go`。FNV-1a 纯函数哈希，直聊用有序对 `pair_low#pair_high`、其它会话用 `chat_id`，所有进程与重启后都算出同一个片。建会话时把 `chat_id → shard` 写进主库 `chat_shards` 目录表（与 `chats` 行同一事务）。
- **写入**：`PostMessage` 在事务内只往主库 `message_outbox` 写一行；事务**提交后**由 `sqlstore.DB.flush` 投影到片库，按 `message_id` 幂等。回滚不会在片里留下消息，且发消息返回前投影已完成，`ListMessages` 立刻可读。崩溃后未投影的行由启动时 `Recover` 补投。
- **序号**：每个片库有自己的 `chat_seq` 自增列，只保证**会话内有序**；`chat_reads.last_read_rowid` 存的就是它。全局全序从来不需要，跨片序号重复不影响未读判定。
- **读取**：`ListMessages` / `MessageRowID` / `LatestMessageRow` / `CountUnread` 先查目录定位片再查；`ListDirectUnreads` 在主库取会话与游标、再逐片查未读数（不再跨片 join）。
- **未改动**：`store.Store` / `Repos` 接口、引擎、handler、OpenAPI、SDK、前端、SSE 与展示层全部不变，API 语义与响应字段保持一致。
- **边界**：这是**原型**，只分片了消息本体。`chats`、`chat_reads`、幂等键、`webhooks`、任务账本仍在主库；默认路径下投影仍由写入方进程在事务提交后本地执行（崩溃靠启动时 `Recover` 补投），没有 2PC。SSE 信号总线现已在默认配置里开启（`Scale.PubSub: redis`，只广播信号、不迁数据）；共享租约投影与 SQL 目录仍为该旧模式的可选子项，默认关闭（见「P1+ 多数据库生产化能力」）。`emit()` 仍在事务内查主库订阅、`idempotency_keys` 仍按 `user_id` 建主键；给幂等键加上片维度仍属后续工作。

### 调整分片数量（扩容 / 缩容 / 换实例）

**先记住一条**：`chat_shards` 目录表才是权威路由。`Router` 的 `hash % N` **只在建会话时算一次**并写进目录表，之后所有读写都查目录表。因此**加片不需要搬老数据，减片必须搬**。

| 变更 | 是否搬数据 | 操作 |
|---|---|---|
| 加片 N → N+1 | **不需要** | 起新实例/建库 → `ShardDSNs` **末尾追加**一个 DSN → `Shards` +1 → 所有进程统一切换 |
| 换实例（N 不变） | 只搬该片 | 把该片 `messages` 导到新实例 → 只改列表中**对应下标**的 DSN → 目录表不动 |
| 减片 N → N-1 | 需要，**当前无工具** | 见下 |
| 关闭分片（`Shards<=1`） | 需要，**当前无工具** | 片库消息不会自动回灌主库 `messages` |

**扩容步骤（2 → 3）**：

1. 新起一个 MySQL 实例（例如 `3310`），`CREATE DATABASE taskgame_shard_2`。服务启动时只自建 `messages` 表，**不建库**。
2. 改配置：`Shards: 3`，`ShardDSNs` 在**末尾**追加新 DSN。前面两项的顺序与内容**绝对不能变**——列表下标就是片序号，重排等于把老数据指到别的库。
3. **所有后端进程一起切到这份新配置**，不要滚动混部：老进程的 `Set` 只有 2 个连接池，一旦它处理目录表里 `shard=2` 的会话，`shard.Set.check` 会报 `index 2 out of range`。建议短暂停服切换。
4. 重启后 `Recover` 按 outbox 行内记录的**旧 `shard` 值**补投未投影消息，不受新 N 影响。

`TestShardScaleOutPreservesExistingChats` 实证了这条：2 片下建的 8 个会话切成 3 片后，目录行不变、消息全部可读；新会话开始落到片 2，且片 2 里没有老会话的消息。注意**老会话永远留在原片**，扩容只把新增会话分散到新片，不会均衡已有数据；要均衡得主动迁移老会话。

**为什么减片麻烦**：目录表里指向被删片的行不会自动消失，`message_outbox.shard` 也存着旧片号，`Recover` 会越界；更麻烦的是 `chat_seq` 是**片内自增**，消息搬到另一片后序号重排，而 `chat_reads.last_read_rowid` 存的正是这个序号，已读位置会错乱。所以缩容需要专门的迁移程序：搬 `messages` → 按新下标 `UPDATE chat_shards` → 修 outbox → 重算/补偿已读游标，并在切换期间停写。当前原型**没有**这套工具，不建议直接减 N；测试里验证了不搬数据就缩回 2 片会直接读失败。

**只换机器/端口**（风险最低）：`Shards` 不变，用 `mysqldump` 或物理备份把该片 `messages` 搬到新实例，只改 `ShardDSNs` 对应下标的 DSN，目录表、`chat_seq`、已读游标全部不动。

## P1+ 多数据库生产化能力（默认只开 Redis 总线，其余按需开启）

默认 `etc/taskgame.yaml` 已包含 `Scale.Enabled: true` + `PubSub: redis`，**默认装配跨进程 SSE 信号总线**，Redis 由 `docker compose up -d` 一起启动，是默认部署的必需依赖（启动时 `PING` 校验，不通则直接失败）。其它子项（`Projection` / `Directory` / `Domains` / `Archive` / `Search` / `Compensation`）默认仍关闭，因为它们需要额外数据库或外部服务，且三库单元化共置模式下并非必需；它们都已实现，不再是“未完成的骨架”。若显式设 `Scale.Enabled=false`，`scale.New` 返回 `nil`，`ServiceContext.Notifier` 退回进程内 `*notify.Broker`，这是旧的单进程行为。**没有搬走任何一张表，没有改动 `store.Store` / `Repos` 接口、引擎、handler、OpenAPI、SDK 与前端，API 响应不变**（`Scale.Directory.Enabled` 会额外创建控制面小表，但不迁移任何业务表）。

| 阶段 | 新增位置 | 已实现 | 仍需注意 |
|---|---|---|---|
| P1 | `internal/notify/*`、`internal/projection/*` | `Fanout` + `RedisTransport` 跨进程 SSE 扇出；`Lease`（Redis SET NX / 进程内）+ `Bus`（Redis pub/sub / 进程内）+ `Worker`：`Recover` 每行先认领共享租约，提交后广播唤醒 hint，定时兜底 | NATS / Kafka 传输未做；Redis pub/sub 是 at-most-once，浏览器靠 45s 轮询补漏 |
| P2 | `internal/plane/*`、`internal/store/domain/*`、`internal/compensation/*`、`internal/metrics/*` | `SQLDirectory`（含缓存与 ReplicaDSN）、`SQLUnique`、`SnowflakeIDGenerator`、三域 `DomainStore`、`AssignInTx`、`SQLFence`、资金 saga、`/metrics`；三域物理搬迁工具与 SQLite 演练已交付 | 真机 MySQL 演练脚本未实测；持续 CDC 双写未做，迁移窗口采用冻结写 |
| P3 | `internal/archive/*`、`internal/search/*`、`internal/webhook/*` | 归档批次执行与 checksum 校验、ES/OpenSearch 索引 + 可重放 CDC、cursor epoch、每会话最新页在线、webhook delivery body 外置与 `emit()` 去放大 | avatar_ref / payload_ref 大字段迁移未做；复杂任务过滤仍回退 store |
| 装配 | `internal/scale/runtime.go`、`config.ScaleConf`、`svc` | `scale.Runtime` 统一生命周期，`Close` 一并释放；后台 worker 驱动投影、CDC、补偿、调度与归档 | 告警规则属于部署侧配置，仓库只暴露 `/metrics` |

三个容易踩坑的点已经写进代码：`chat_shards` 之类**目录表才是权威路由**；**加片不搬老数据**（`ScaleOutPlan.MigratedKeys` 恒为 0，减片才必须迁移）；**没有目录行的老 key 必须按首次部署的片数（`Topology.Pinned`）做回退哈希**，否则滚动升级时会算出别的片甚至 `index out of range`。另外要记住：**分片只把数据摊薄，不会减少总量**——在线库变小靠已实现的归档冷热分离、webhook body 外置与 `emit()` 去放大；avatar/payload 大字段外置仍是后续 P3 候选。

### 用真实 Redis 跑多机 SSE（`Scale.PubSub: redis`）

`internal/notify/redis.go` 实现了 `Transport`：**发布**走带短超时的连接池（Redis 卡住也不会阻塞产生信号的请求），**订阅**走另一条关闭读写超时的连接（阻塞订阅不能被默认的 3s 读超时反复打断）。构造时 `PING` 校验，Redis 不可用会**直接启动失败**，避免多实例静默退化成互不通信的单机。`Envelope` 用 JSON 编码，坏帧／非本系统的帧直接丢弃，不会卡住接收循环。

默认 `etc/taskgame.yaml` 末尾已包含下面的 `Scale` 段（`InstanceID` 默认留空、自动生成），`docker compose up -d` 会一并启动 Redis，因此无需手工添加：

```yaml
Scale:
  Enabled: true
  PubSub: redis
  # InstanceID: api-a   # 可选；留空 = 每个进程自动生成唯一 id
  Redis:
    Addr: "127.0.0.1:6379"
    Channel: "taskgame.signals"
```

```bash
docker compose up -d    # 3 个 MySQL cell + Redis
make run                # 或 CGO_ENABLED=0 go run .
```

第二个 API 实例复制这份配置，**只改 `Port`（如 8892）**，`Scale.InstanceID` 留空即可自动生成本进程唯一 id；**指向同一个 `Scale.Redis.Addr` 和 `Scale.Redis.Channel`**。两个进程各建一条订阅，A 进程产生的信号经 Redis 广播给 B 进程的 SSE 连接。若你显式写了 `InstanceID`，务必给两个进程写不同的值：回声过滤按 `Origin == 本进程 InstanceID` 丢包，写重了会导致对方永远收不到实时消息（只剩 45s 兜底轮询）。注意 Redis pub/sub 是 **at-most-once**：进程宕机期间的信号会丢，但信号只带 id、不落库，浏览器 45s 兜底轮询会补上，因此不需要换成 Redis Stream。`Scale.PubSub: shared` 仍是进程内总线，只用于测试；默认配置的 `Store` 已是三库单元化，真多机只要所有进程都能连到同一批库和同一个 Redis 即可。

### 用共享租约投影 `message_outbox`（`Scale.Projection`）

PR #38 的 `Recover` 是「每个进程启动时全表扫 `projected=0` 并各自投影」，多实例会重复劳动。#2 把它改成协同式：

- `internal/projection` 提供 `Lease`（`RedisLease` = `SET NX PX` + Lua 比较删除；`MemoryLease` 给单机/测试）、`Bus`（`RedisBus` 用独立的 `taskgame.outbox` 频道；`MemoryBus` 给单机）、`Worker`（事件唤醒 + 定时兜底）。
- `sqlstore.DB.Recover` 对每行先 `claimOutbox` 认领租约：拿不到就跳过（别的进程在处理），投影失败立刻释放让别人重试，**成功则保留到 TTL 自动过期**——若立即释放，另一个持有旧 pending 快照的进程会重复投影同一行。
- 事务提交后、本地 `flush` 之前**异步广播** outbox id 作为加速唤醒 hint；`flush` 仍由写进程同步投影，保证「发消息返回即可读」的契约不变。
- 正确性不依赖 Redis：`shard.Set.Insert` 按 `message_id` 幂等，`projected=1` 是权威事实；租约只消除重复劳动，事件只缩短崩溃恢复延迟，丢事件由定时扫描兜底。

`Scale.Projection.Enabled=true` 时 `scale.Runtime` 装配租约/总线，`svc` 把 coordinator 注入主库并在 `StartBackground` 启动 worker；`PubSub=redis` 用真 Redis，`shared` / `local` 用进程内实现。默认全关时 `Recover` 与 PR #38 逐字节一致。

### 用 SQL 目录持久化路由（`Scale.Directory`）

`InMemoryDirectory` 是进程内的：重启即丢，多进程各持一份，「目录表是唯一权威路由」只在单进程里成立。#3 让目录真正落库：

- `internal/plane/sql_directory.go` 的 `SQLDirectory` 使用 `plane.Schema` 的三张表：`shard_directory`（`(domain, shard_key) -> shard` 权威映射）、`shard_topology`（每个域声明的片数 + DSN 快照）、`global_unique`（留给 #5）。`Migrate` 全是 `CREATE TABLE IF NOT EXISTS`，每个进程启动时重复执行也安全；`Driver` 支持 `sqlite` / `mysql`，只有 insert 幂等语法不同。
- **`Assign` 幂等且不覆盖**：SQLite `INSERT OR IGNORE`、MySQL `INSERT IGNORE`。两个进程并发或重放同一个 key，都不会把它改到别的片——这是「加片不搬老数据」在数据库层的保证。
- **`EnsureShards` 只增不减**：`ShardCount` 取 `MAX(shard)+1`。滚动升级时仍跑旧配置的进程，不会把别的进程已扩的片数缩回去；`shard_topology` 因此是跨进程共享的拓扑声明。
- 包级 `AssignInTx` 让调用方在**自己的事务**里写目录行，留给 P2 把聚合根与路由写进同一事务（现有 `chat_shards` 就是这么做的）。当前 `SQLDirectory` 自带连接池，二者是最终一致；因为 `Assign` 幂等，重试是安全的。

`Scale.Directory.Enabled=true` 时 `scale.New` 打开控制面库（`Driver` / `DSN` 省略则继承 `Store.Driver` / `Store.DSN`，即主库兼任这个极小控制面），并把三个域的片数同步进 `shard_topology`；`Runtime.Close` 释放连接池。**默认全关时仍是 `InMemoryDirectory`，PR #38 路径不变**。真多机部署要让所有进程指向同一个共享 MySQL：

```yaml
Scale:
  Directory:
    Enabled: true
    Driver: mysql
    DSN: "root:123456@tcp(mysql:3306)/taskgame_plane?charset=utf8mb4"
```

两个边界要记住：`shard_topology.dsn` 只是首次登记时的快照，权威 DSN 顺序仍来自配置里的 `Topology`（换实例改配置并滚动升级，见「调整分片数量」）；`Keys()` 只服务于缩容规划，当前是按 `(domain, shard_key)` 主键的扫描，上热路径前需补 `(domain, shard)` 索引。

`internal/scale.TODOs` 是代码里的边界清单，现已收敛到一条：avatar_ref / payload_ref 大字段迁移；README 与它保持一致。逐项完成状态见 `docs/remaining-work-backlog.md`。跑验证：

```bash
go vet ./...
go test ./...
# 默认配置只开 Scale.PubSub=redis；要试其它生产化能力，在配置副本中追加字段（见 internal/config/config.go 与 etc/taskgame.yaml）
CGO_ENABLED=0 go run .
```

## 运行

需要 Go 1.26（见 `go.mod`）、`python3`（`scripts/smoke.sh` 用来读 JSON）、`curl` 和 Docker（默认配置要连 MySQL）。不需要 C 编译器：测试用的 SQLite 驱动是 `modernc.org/sqlite`。下面的命令都在仓库根目录执行。

```bash
go mod tidy
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./...
```

`Makefile` 会导出 `CGO_ENABLED=0`（也可在命令行覆盖），所以测试和构建等价于 `make tidy`、`make test`、`make build`。

只有一份配置：`etc/taskgame.yaml`（`go run .` / `make run` 默认加载它）。它连 **3 个独立 MySQL 实例**（镜像 `mysql:8.4.11`，`root` / `123456`）：

| 端口 | 库 | 存什么 |
| --- | --- | --- |
| `3307` | `taskgame` | cell 0：约 1/3 用户及其任务、会话、消息、账本、好友、webhook |
| `3308` | `taskgame` | cell 1：另一份约 1/3，表结构完全相同 |
| `3309` | `taskgame` | cell 2：最后约 1/3 |

先启动数据库再启动服务：

```bash
docker compose up -d          # 起 3 个 MySQL 实例 + Redis
make run
# 或：CGO_ENABLED=0 go run .
```

指定别的配置文件（例如多实例的第二份配置）：

```bash
make run CONFIG=taskgame.yaml           # 等价于 go run . -f taskgame.yaml
make run GOARGS="-f taskgame.yaml"      # 直接透传完整参数
```

`-f` **不能**紧跟 `make run`：它是 make 自己的「指定 makefile」选项，会被 make 截走（报 `taskgame.yaml: No such file or directory`）。用 `CONFIG=` 或 `GOARGS=` 传。`make token` 也接受同样的变量。

每个实例的库由 `MYSQL_DATABASE` 在首次启动时创建；服务启动时只自建表，**不建库**。端口刻意用 3307/3308/3309，不会碰本机已有的 3306。要改成一机三库，把三个 DSN 的端口都改成 3306、库名分别改成 `taskgame_cell_0/1/2` 即可。

进程监听 `http://0.0.0.0:8891`。探活是 `GET /health`（响应 `{"status":"ok"}`）。业务路径都在 `/v1` 下。三个 cell DSN 是 `root:123456@tcp(127.0.0.1:3307|3308|3309)/taskgame`，与 `docker-compose.yml` 的 root 密码一致。驱动是 `github.com/go-sql-driver/mysql`；`Open` 会补上 `clientFoundRows=true` 和 `utf8mb4`（DSN 里已经写了就保持原样）。多个进程共用同一份 DSN 列表即可。每个进程仍然跑自动结算和 webhook 投递；任务行和托管行在事务里加锁，托管的 `held`/`frozen` 按版本做比较并交换，webhook 先把行认领为 `delivering` 再 POST。接收方仍应按 `X-Event-Id` 去重：worker 在 POST 成功之后、写回 `delivered` 之前崩溃时，租约到期会再投一次。

想要零依赖的本地调试，可复制配置把 `Store.Driver` 改成 `sqlite`（单连接文件，不能两个进程共用一个文件）；这不是默认路径。

需要 MySQL 的测试（并发自动结算、并发席位超时退款、并发 webhook 投递、超过 50 条的超时扫描）在设置了 `TASKGAME_MYSQL_DSN` 时运行。账号要能 `CREATE DATABASE`。compose 里的 root 可以：

```bash
export TASKGAME_MYSQL_DSN='root:123456@tcp(127.0.0.1:3306)/taskgame?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=false'
export TASKGAME_MYSQL_REQUIRED=1
CGO_ENABLED=0 go test ./...
# 或
make test-mysql
```

没设置 DSN 时这些用例会跳过，SQLite 测试照常跑。冒烟脚本不关心驱动，服务听在 8891 即可：`bash scripts/smoke.sh`。

**MySQL 分片链路**（主库 MySQL + 每片一个 MySQL DSN）同样在设置 `TASKGAME_MYSQL_DSN` 时跑：`internal/store/shard/mysql_test.go` 验证片库的 SQL 方言、幂等投影、重启恢复与物理隔离；`internal/integration/sharded_mysql_messages_test.go` 起真实 HTTP 服务，验证 API 契约、跨片隔离、outbox 全量投影。测试会自建临时库并在结束时删除，不需要预先建 `taskgame_shard_*`。手动跑完整示例：

```bash
docker compose up -d          # 新卷会自动建 taskgame_shard_0 / taskgame_shard_1
CGO_ENABLED=0 go run .        # 默认配置即分片形态
```

**一个库一个实例**（3 个独立 MySQL 容器，各自只持一个库）：

```bash
docker compose -f docker-compose.3instances.yml -p taskgame-3inst up -d
cp etc/taskgame.yaml /tmp/taskgame-3inst.yaml
# 把 3 个 DSN 的端口改成 3307（主库）、3308（片 0）、3309（片 1）
CGO_ENABLED=0 go run . -f /tmp/taskgame-3inst.yaml
```

独立项目名 `taskgame-3inst` 不会动到默认的 3306 容器。停掉用 `docker compose -f docker-compose.3instances.yml -p taskgame-3inst down`（加 `-v` 会连数据卷一起删）。

**多个 API 实例**：三库单元化配置下，所有进程连同一组 Cell DSN（顺序也要一致），并共用同一个 Redis 地址与频道；SQLite 是单连接文件，不能给两个进程共用。端口只能靠不同配置文件区分（`conf.MustLoad` 不读环境变量）。`InstanceID` 留空时每个进程自动生成唯一 id，复制配置只改 `Port` 就够了：

```bash
docker compose up -d
CGO_ENABLED=0 go run .                                                # 实例 A: 8891
sed 's/^Port: 8891/Port: 8892/' etc/taskgame.yaml > /tmp/taskgame-8892.yaml
CGO_ENABLED=0 go run . -f /tmp/taskgame-8892.yaml                     # 实例 B: 8892
```

实测过：A 注册并签发 JWT，B 用同一个 token 能直接读；A 发消息 B 能读、B 发消息 A 能读；同一个会话按 `chat_id` 稳定路由到同一个 Cell。两个进程都会跑自动结算与 webhook 投递，靠事务锁和 `delivering` 认领避免重复处理。

**已知边界**：默认 `Scale.PubSub: redis` 时，A 实例提交的消息会通过 Redis 广播给挂在 B 实例上的 SSE 连接，实时刷新。Redis pub/sub 是 at-most-once，宕机期间的信号会丢，前端 45s 兜底轮询会补上；只有把 `PubSub` 改成 `local`/`shared` 时才会退回“收不到另一进程信号”。

**两个前端服务，各连一个后端**：前端是静态资源、本身无状态，起几个 Vite 进程不改变架构；真正决定隔离性的是浏览器 **origin**（协议 + host + 端口）。`settings.ts` 把 `baseUrl` 存在 `localStorage`，且 `loadSettings()` 里已有的 localStorage 会**覆盖** `VITE_TASKGAME_BASE_URL`；`readSync.ts` / `avatarUtils.ts` 的 `BroadcastChannel` 也按 origin 广播。所以在**同一个 origin 开两个标签页模拟两个用户**是不成立的：两个窗口共用 localStorage，后改的服务地址会覆盖先改的，已读/头像事件还会互相串扰。要用两个不同 origin：

```bash
cd web && npm run dev -- --port 5137                                                    # 前端 A → 8891
cd web && VITE_TASKGAME_BASE_URL=http://127.0.0.1:8892 npm run dev -- --port 5138      # 前端 B → 8892
```

`5137`、`5138`、`4173`、`5173`–`5185` 都在后端 CORS 白名单内。环境变量只在 `loadSettings()` 作 fallback，新 origin 的 localStorage 是空的，所以各自生效；若之前已在该端口存过设置，`VITE_TASKGAME_BASE_URL` 会被 localStorage 覆盖，需要在「设置」页手动改服务地址。生产环境没这个问题——A、B 是不同设备上的浏览器，localStorage 天然不共享；只有单机模拟才需要靠不同 origin 隔离。

开发用的静态密钥在 `etc/taskgame.yaml` 的 `Auth.ApiKeys`：

| Header | User |
| --- | --- |
| `X-Api-Key: dev-publisher-key` | `usr_publisher` |
| `X-Api-Key: dev-executor-key` | `usr_executor` |
| `X-Api-Key: dev-arbiter-key` | `usr_arbiter` |

额外用户用 HS256 JWT，密钥是同一个文件里的 `Auth.JwtSecret`（至少 16 个字符）。`sub` 就是用户 id：

```bash
make token
# 指定 subject。USER_ID 缺省是 usr_publisher
make token USER_ID=usr_executor
# 或
CGO_ENABLED=0 go run ./cmd/token -f etc/taskgame.yaml -user usr_executor
```

请求头写成 `Authorization: Bearer <token>`。冒烟脚本会自己为每轮新的执行者签发 JWT，所以正在跑的进程必须使用同一份 `Auth.JwtSecret`。

邮箱注册是同一套 JWT（仍是 v0.1 的附加接口）。`POST /v1/register` 和 `POST /v1/login` 不需要鉴权，密码以 bcrypt 保存，不会回明文。`GET /v1/me` 需要 Bearer 或 `X-Api-Key`，返回 `id`、`email`、`nickname`、`is_arbiter`。`nickname` 是显示名，不是登录名，也不要求唯一；可以不填，已有用户为空字符串。`PATCH /v1/me` 的 body 是 `{ "nickname": "…" }`，去掉首尾空白后 1–32 个字符，空值和纯空白是 400。它不改邮箱、密码，也不刷新 token。注册 body 可以带可选的 `nickname`。`usr_publisher` / `usr_executor` 的开发密钥保持可用。令牌有效期是 `Auth.TokenTTL`（缺省 24h）。

```bash
curl -s -X POST http://127.0.0.1:8891/v1/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"publisher@example.com","password":"password123"}'
```

另开一个终端，对已经在听的服务跑三种任务（独占、竞争 `winner_take_all`、协作 `on_dropout=replace`）：

```bash
bash scripts/smoke.sh
# 或
make smoke
```

`scripts/smoke.sh` 默认请求 `http://127.0.0.1:8891`。可用环境变量覆盖：

| 变量 | 默认 | 作用 |
| --- | --- | --- |
| `BASE` | `http://127.0.0.1:8891` | 服务地址 |
| `PUB_KEY` | `dev-publisher-key` | 发布者 `X-Api-Key` |
| `EXE_KEY` | `dev-executor-key` | 独占路径的执行者 `X-Api-Key` |
| `CONFIG` | `etc/taskgame.yaml` | 给额外执行者签 JWT；须与服务的 `Auth.JwtSecret` 一致 |

三条路径：

1. **exclusive**：创建 → 注资 → 接受 → 提交 → 确认 → `settled`。同时检查幂等重放、积分、webhook 和 chat。
2. **competitive**（`settlement_rule=winner_take_all`）：两个执行者都接受并提交。不带 `submission_id` 的确认返回 400，任务保持 `in_review`，托管不动。任务体的 `submissions[]` 按 `created_at` 列出每一份（`current` 或 `superseded`）。冒烟从这份列表里取赢家的 id 再确认。只有赢家拿到全部托管，任务 `settled`，输家能力分为 0。多份提交不会在 `review_deadline` 被猜成赢家；再过 `WinnerSelectHours`（默认 72）仍未选定就整笔退回（`winner_select_timeout`）。
3. **collaborative**（`on_dropout=replace`，默认 `settlement_mode=unified`）：两个席位的 `share_bps` 使用接口已经要求的合计 10000（这里是 6000 与 4000，不带 `quantity`）。坐满后任务仍是 `open`，提交会被拒绝，直到发布者 `start`。开始后一人退出：佣金 0、能力分 0，该席位回到 `open`，另一席位仍是 `filled`，任务保持 `active`。替补入席后两席都提交，确认才按该 bps 结算（500 积分分成 300 与 200，退出者拿不到 release）。只提交一席时确认失败。另一条用一个角色 `quantity=4`、`share_bps=10000`（角色合计，不是每席）。服务端展开为 `writer_1`…`writer_4`，每席 2500。四席坐满、`start`、四席都提交后，500 积分各结算 125。第三条是 `per_seat`：一席提交并按 `role_id` 确认后只拨付该席，托管留下另一席的份额；第二席再提交、确认后任务才 `settled`。

72 小时自动结算不会替多份提交的竞争任务猜赢家。冒烟不等待 72 小时。同一条规则由单元测试拨钟覆盖（`review_hours` 设为 1，只为越过截止时间，不改变规则）：

```bash
CGO_ENABLED=0 go test ./internal/engine -run 'TestAutoSettleCompetitiveDoesNotPickWinner|TestAutoSettleExclusive'
```

配置项在 `etc/taskgame.yaml`：

- `Auth.JwtSecret`：HS256 密钥，至少 16 个字符。`make token`、注册和登录共用它
- `Auth.TokenTTL`：注册和登录签发的 JWT 有效期，缺省 24h
- `Auth.ApiKeys`：静态 `X-Api-Key` → 用户 id
- `Store.Driver`：`mysql`（默认，连接池）或 `sqlite`（本地调试，单连接）
- `Store.Cells`：**默认 3 个** cell DSN（有序，下标即单元编号）。三个 DSN 可以是不同实例（默认 3307/3308/3309），也可以是同一实例的不同库名。设置后忽略 `Store.DSN` 与 `Store.Shards`
- `Store.DSN`：单库模式的主库 DSN（不设 `Cells` 时生效）
- `Store.Shards`：旧的消息分片原型，`> 1` 时启用；`Store.ShardDriver`、`Store.ShardDSNs`、`Store.ShardDSN`（含 `%d` 模板）见「消息分片（旧模式）」
- `Escrow.InitialBalance`：用户第一次出现时记入的模拟积分（开发配置是 1000000）
- `Webhook.Mode`：`log` 只记录签名，不发 HTTP；`post` 才真正 POST
- `AutoSettle`：到 `review_deadline` 后自动结算。竞争任务如果有多份提交，不会猜赢家，保持 `in_review`
- `Scale.PubSub`：**默认 `redis`**，跨进程 SSE 信号总线；`Scale.InstanceID` 留空则每进程自动生成唯一 id（手写则必须互不相同），`Scale.Redis.Addr` / `Channel` 全部署一致。Redis 由 compose 启动，未启动则 API 启动失败。其它 Scale 子项默认关闭

错误响应是 JSON：`{"code":"...","message":"..."}`。

## Horizontal cells (default, plan A)

The shipped `etc/taskgame.yaml` opens **three independent MySQL instances** (`127.0.0.1:3307` / `3308` / `3309`), each with one `taskgame` database and the **same full schema**. Each cell owns about **one third** of `users`, `tasks`, `messages` and every other business table. **No primary database holds all rows.** It also enables `Scale.PubSub: redis` on `127.0.0.1:6379` (started by the same compose file) for cross-process SSE signals only — Redis holds no business data. For multiple API processes, copy the config and change only `Port`; leave `Scale.InstanceID` empty so each process generates a unique id. If you set it explicitly, every process must use a different value — a duplicate id makes the echo filter drop the other process's signals.

- **Routing** (`internal/store/cell`): an authenticated request is routed by `user_id`; `/v1/tasks/{id}`, `/v1/chats/{id}` and `/v1/users/{id}` are routed by the id in the URL, so every participant of a task or chat lands on the same cell.
- **Writes**: `InTx` opens a transaction in exactly one cell. Cross-cell work inside a transaction (`Ensure`, friendships) is idempotent or commutative best-effort; there is no 2PC.
- **Reads**: market listing, login by email and webhook delivery scans fan out over all cells and merge; friend-card nickname and scores are re-read from the owner cell.
- **What unit-based means**: a user's account, the tasks they publish, their chats/messages and their friend view live in their own cell. **One user's data is never split across cells**, so a single power user's many tasks concentrate in that user's cell; even distribution is over **users**, and with many users the three cells naturally hold about 1/3 each.
- **Resizing**: `Store.Cells` is an ordered list (index = cell id). Adding or removing a cell is a data migration (recompute `user_id` ownership and move that cell's rows); the prototype has no online migration tool.
- **Boundary**: this is a runnable unit-based prototype. Cross-cell balance/score updates and the two-sided friend view are eventually consistent (idempotent `Ensure`, replayable `UPDATE`), not distributed transactions.

## Message sharding (legacy, not the default)

The PR #38 message-level prototype is kept for comparison and only activates when `Store.Shards > 1`. Its original description follows. **The `docker-compose.3instances.yml` and `etc/mysql-init.d` files it mentions were removed with the switch to cells; to rerun the legacy mode, create one primary plus N shard schemas yourself from the YAML below.**

With `Store.Shards: 2` and `ShardDriver: mysql`, one MySQL server holds `taskgame` (accounts, tasks, ledger, friends, chat metadata) plus `taskgame_shard_0` / `taskgame_shard_1` for about half of the chat messages each, routed by chat id.

With `Store.Shards > 1`, message bodies leave the primary database and are routed by the **chat aggregate root** to N independent databases. `0` or `1` keeps the original single-database behaviour. The shard driver is independent of the primary driver.

- `ShardDriver: sqlite`: `ShardDSN: "file:data/shard-%d.db"` expands into one local file per shard (single-process local debugging only).
- `ShardDriver: mysql`: `ShardDSNs` gives **one DSN per shard, in shard order**, and may point at different hosts so every API process reaches the same shard databases over the network. This is the default; both DSNs point at `127.0.0.1:3306` on different schemas. The service creates the `messages` table but not the databases themselves.
- Routing (FNV-1a, ordered pair for direct chats), `chat_shards` directory, transactional `message_outbox`, post-commit projection and startup `Recover` are unchanged. Only the `messages` table is sharded. In the base prototype projection is performed locally by the writing process, and SSE fan-out is process-local only when `Scale` is disabled. The shipped config enables `Scale.PubSub: redis` for the cross-process signal bus; the optional P1/P2 skeleton additionally offers a shared-lease outbox projection worker and a SQL-backed routing directory (`shard_directory` / `shard_topology`) shared by all processes. Neither path uses 2PC.

MySQL-backed shard tests (`internal/store/shard/mysql_test.go`, `internal/integration/sharded_mysql_messages_test.go`) run when `TASKGAME_MYSQL_DSN` is set and skip otherwise; they create and drop their own temporary databases.

**Resizing the shard set**: `chat_shards` is the authoritative route. The `hash % N` router runs **only once, when a chat is created**, and the result is stored in the directory; every later read and write looks it up. So **adding a shard needs no data migration, removing one does**.

| Change | Move data? | Action |
|---|---|---|
| Add a shard N → N+1 | **No** | Start a new instance/create the database → **append** one DSN to `ShardDSNs` → set `Shards: N+1` → switch all processes together |
| Replace an instance (N unchanged) | Only that shard | Move its `messages` to the new instance → change only the DSN at that **index** → leave the directory alone |
| Remove a shard N → N-1 | Yes, **no tooling yet** | See below |
| Disable sharding (`Shards<=1`) | Yes, **no tooling yet** | Shard rows are not copied back into the primary `messages` table |

To scale 2 → 3: create the new database first (the service only creates the `messages` table, never the schema), append its DSN at the **end** of `ShardDSNs` — the list index *is* the shard number, so never reorder existing entries — and switch **all** API processes to the new config at once. A rolling upgrade is unsafe: an old process has only two shard pools and fails with `index 2 out of range` when it serves a chat whose directory row says shard 2. `Recover` replays unprojected outbox rows using the `shard` value stored in each row, unaffected by the new N. `TestShardScaleOutPreservesExistingChats` verifies that eight chats created with two shards are still readable after switching to three, their directory rows are unchanged, new chats start landing on shard 2, and shard 2 contains no pre-scale-out messages. Note that **existing chats never move**: scaling out only spreads new chats, so it does not rebalance existing load.

Shrinking is harder: directory rows still point at the removed shard, `message_outbox.shard` holds the old index, and `chat_seq` is **per-shard auto-increment** — moving rows renumbers them while `chat_reads.last_read_rowid` stores exactly that sequence, corrupting read cursors. A real shrink needs a migration that moves `messages`, rewrites `chat_shards` and outbox rows, and repairs cursors while writes are paused. The prototype has no such tool; the test confirms that shrinking back without migration fails. **Changing only a host/port at a fixed N** is the safest resize: move that shard's `messages`, edit the one DSN at its index, and leave the directory, sequences and cursors untouched.

**One MySQL instance per database**: with `Shards: N` you need **N + 1 instances** (one primary plus one per shard), each holding a single schema. Multiple independent mysqld processes can run on one machine on **different ports**; the DSN syntax is identical to a cross-host setup:

```bash
docker compose -f docker-compose.3instances.yml -p taskgame-3inst up -d   # 3307 primary / 3308 shard0 / 3309 shard1
cp etc/taskgame.yaml /tmp/taskgame-3inst.yaml
# repoint the three DSNs to 3307 (primary), 3308 (shard 0), 3309 (shard 1)
CGO_ENABLED=0 go run . -f /tmp/taskgame-3inst.yaml
```

After that edit the primary DSN points at `3307/taskgame` and `ShardDSNs` at `3308/taskgame_shard_0` and `3309/taskgame_shard_1`. **Every API process connects to all N + 1 instances** (`openStore` opens one primary pool plus N shard pools); processes are not split across shards, since any request may route to any shard. All processes must use the same `ShardDSNs` list in the same order. Verified: the three containers have distinct hostnames; the primary instance holds `chat_shards` / `message_outbox` and an empty `messages` table, while each shard instance holds only `messages` for its own chats with an independent `chat_seq`.

**Multiple API instances**: in the default three-cell setup, every process connects to the same Cell DSNs (same order) and the same Redis address/channel; SQLite is a single-connection file and cannot be shared. Ports are set per config file (`conf.MustLoad` does not read environment variables). Leave `InstanceID` empty so each process generates a unique id — copying the config then only needs a different `Port`:

```bash
docker compose up -d
CGO_ENABLED=0 go run .                                                # instance A: 8891
sed 's/^Port: 8891/Port: 8892/' etc/taskgame.yaml > /tmp/taskgame-8892.yaml
CGO_ENABLED=0 go run . -f /tmp/taskgame-8892.yaml                     # instance B: 8892
```

Verified: a JWT issued by A works on B; messages posted on A are readable on B and vice versa; a chat routes to the same cell from both processes. Auto-settle and webhook delivery run on both processes safely (transaction locks and `delivering` claims). With the default `Scale.PubSub: redis`, a message committed on A is broadcast over Redis to the SSE stream attached to B, so the peer's panel refreshes immediately. Redis pub/sub is at-most-once — signals lost while a process is down are repaired by the browser's 45s fallback poll. Cross-process push only degrades if `PubSub` is set to `local`/`shared`, or if two processes reuse the same `InstanceID`.

**Two frontends, one backend each**: the frontend is stateless static assets, so the number of Vite processes does not change the architecture; what matters is the browser **origin** (scheme + host + port). `settings.ts` stores `baseUrl` in `localStorage`, and existing localStorage **overrides** `VITE_TASKGAME_BASE_URL` in `loadSettings()`. `readSync.ts` / `avatarUtils.ts` broadcast over `BroadcastChannel`, also scoped to one origin. Two tabs on a **single origin** therefore cannot model two users: they share localStorage (the last edited base URL wins) and their read/avatar events cross-contaminate. Use two distinct origins:

```bash
cd web && npm run dev -- --port 5137                                                    # frontend A → 8891
cd web && VITE_TASKGAME_BASE_URL=http://127.0.0.1:8892 npm run dev -- --port 5138      # frontend B → 8892
```

Ports `5137`, `5138`, `4173` and `5173`–`5185` are all in the CORS allowlist. The env var is only a fallback in `loadSettings()`, and a fresh origin has empty localStorage, so each one takes effect; if settings were saved earlier on that port, localStorage wins and the service URL must be changed in the Settings page. Production has no such problem: A and B are browsers on different devices with separate localStorage; distinct origins are only needed when simulating both users on one machine.

## Run

Requires Go 1.26 (see `go.mod`), `python3` (the smoke script reads JSON with it), `curl`, and Docker (the default config needs MySQL). No C compiler: the SQLite driver used by the tests is `modernc.org/sqlite`. Run the commands below from the repository root.

```bash
go mod tidy
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./...
```

The `Makefile` exports `CGO_ENABLED=0` (override it from the environment or the command line), so test and build are `make tidy`, `make test`, and `make build`.

There is one config: `etc/taskgame.yaml`, loaded by default by `go run .` and `make run`. It connects to **three independent MySQL instances** (image `mysql:8.4.11`, `root` / `123456`):

| Port | Database | Holds |
| --- | --- | --- |
| `3307` | `taskgame` | cell 0: about 1/3 of users plus their tasks, chats, messages, ledger, friends, webhooks |
| `3308` | `taskgame` | cell 1: another ~1/3, identical schema |
| `3309` | `taskgame` | cell 2: the last ~1/3 |

Start the databases and Redis first, then the service. Redis is required at startup because `Scale.PubSub: redis` PINGs it:

```bash
docker compose up -d          # three independent MySQL cells + Redis
make run
# or: CGO_ENABLED=0 go run .
```

To use another config file (for example a second API instance), pass it through make:

```bash
make run CONFIG=taskgame.yaml           # same as go run . -f taskgame.yaml
make run GOARGS="-f taskgame.yaml"      # pass the full flag through
```

Do **not** write `make run -f taskgame.yaml`: make consumes `-f` as its own "use this makefile" option and looks for `taskgame.yaml` as a makefile. `make token` accepts `CONFIG=` / `GOARGS=` too.

`MYSQL_DATABASE` creates each cell's database on first boot; the service creates tables but **not databases**. The ports are deliberately 3307/3308/3309 so an existing local MySQL on 3306 is untouched. To run all three cells on one server, point all three DSNs at 3306 and use `taskgame_cell_0/1/2` as schema names.

The process listens on `http://0.0.0.0:8891`. Liveness is `GET /health` (`{"status":"ok"}`). Protocol routes are under `/v1`. The three cell DSNs are `root:123456@tcp(127.0.0.1:3307|3308|3309)/taskgame`, matching `docker-compose.yml`. The driver is `github.com/go-sql-driver/mysql`. `Open` sets `clientFoundRows=true` and `utf8mb4` when the DSN does not already say so. Several processes can share the same DSN list. Each process still runs auto-settle and webhook delivery. Task and escrow rows are locked inside the transaction, escrow `held`/`frozen` updates compare-and-swap on a version, and a webhook row is claimed as `delivering` before the POST. Receivers should still dedupe on `X-Event-Id`: if a worker crashes after the POST and before it marks the row delivered, the lease expires and another worker sends the event again.

For zero-dependency local debugging, copy the config and set `Store.Driver: sqlite` (a single-connection file; never point two processes at one file). That is not the default path.

Tests that need MySQL (concurrent auto-settle, concurrent per-seat timeout refund, concurrent webhook delivery, and the timeout sweep past 50 rows) run when `TASKGAME_MYSQL_DSN` is set. The account must be allowed to `CREATE DATABASE`. The compose root user can:

```bash
export TASKGAME_MYSQL_DSN='root:123456@tcp(127.0.0.1:3306)/taskgame?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=false'
export TASKGAME_MYSQL_REQUIRED=1
CGO_ENABLED=0 go test ./...
# or
make test-mysql
```

Without the DSN those cases skip and the SQLite tests still run. Smoke does not care which driver is behind port 8891: `bash scripts/smoke.sh`.

Development keys live in `etc/taskgame.yaml` under `Auth.ApiKeys`:

| Header | User |
| --- | --- |
| `X-Api-Key: dev-publisher-key` | `usr_publisher` |
| `X-Api-Key: dev-executor-key` | `usr_executor` |
| `X-Api-Key: dev-arbiter-key` | `usr_arbiter` |

Mint an HS256 JWT for any other user with the same file's `Auth.JwtSecret` (at least 16 characters). `sub` is the user id:

```bash
make token
# USER_ID defaults to usr_publisher
make token USER_ID=usr_executor
# or
CGO_ENABLED=0 go run ./cmd/token -f etc/taskgame.yaml -user usr_executor
```

Send `Authorization: Bearer <token>`. The smoke script mints a fresh JWT per extra executor, so the running process must be using that same `Auth.JwtSecret`.

Email registration is the same JWT, added without a version bump. `POST /v1/register` and `POST /v1/login` are public. Passwords are stored as bcrypt and are not returned. `GET /v1/me` requires Bearer or `X-Api-Key` and returns `id`, `email`, `nickname`, and `is_arbiter`. `nickname` is a display name, not a unique login handle. Omit it and the field stays an empty string. `PATCH /v1/me` body is `{ "nickname": "…" }`, 1–32 characters after trim. Empty or whitespace-only is 400. It does not change email, password, or the access token. Register may include an optional `nickname`. The `usr_publisher` / `usr_executor` development keys keep working. Token lifetime is `Auth.TokenTTL` (default 24h).

```bash
curl -s -X POST http://127.0.0.1:8891/v1/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"publisher@example.com","password":"password123"}'
```

In another shell, exercise all three kinds against the listening server (exclusive, competitive `winner_take_all`, collaborative `on_dropout=replace`):

```bash
bash scripts/smoke.sh
# or
make smoke
```

`scripts/smoke.sh` calls `http://127.0.0.1:8891` unless you override:

| Variable | Default | Role |
| --- | --- | --- |
| `BASE` | `http://127.0.0.1:8891` | Server URL |
| `PUB_KEY` | `dev-publisher-key` | Publisher `X-Api-Key` |
| `EXE_KEY` | `dev-executor-key` | Executor `X-Api-Key` for the exclusive path |
| `CONFIG` | `etc/taskgame.yaml` | Signs JWTs; must share `Auth.JwtSecret` with the server |

The three paths:

1. **exclusive**: create → fund → accept → submit → confirm → `settled`, plus the idempotency replay, scores, webhook, and chat calls.
2. **competitive** (`settlement_rule=winner_take_all`): two executors accept and submit. Confirm without `submission_id` returns 400 and leaves the task `in_review` with the escrow untouched. `submissions[]` on the task lists each delivery (`current` or `superseded`) in `created_at` order. Smoke reads the winner's id from that list. Confirm with that `submission_id` pays the whole escrow to the winner and leaves the loser at ability 0. More than one current submission is not auto-picked at `review_deadline`; `WinnerSelectHours` later (default 72) refunds the pool (`winner_select_timeout`).
3. **collaborative** (`on_dropout=replace`, default `settlement_mode=unified`): two seats use the share the API already requires (`share_bps` sums to 10000; this run uses 6000 and 4000 and omits `quantity`). After both seats are filled the task stays `open` and submit is rejected until the publisher calls `start`. After start, one participant drops out: 0 commission, 0 ability, that seat returns to `open`, the other seat stays `filled`, and the task stays `active`. The replacement takes the seat. Confirm fails until both current seats have submitted, then settlement follows those basis points (500 credits split 300 / 200; the exitee gets no release). A second create sends one role with `quantity=4` and `share_bps=10000` (the template total, not per seat). The server stores `writer_1`…`writer_4` at 2500 each. After all four seats are filled, `start`, and all four submit, 500 credits settle as 125 each. A third create uses `per_seat`: confirming one submitted `role_id` releases only that share and leaves the other share held; the task settles after the second seat submits and is confirmed.

The 72h auto-settle path does not pick a winner for a competitive task that has more than one submission. The smoke script does not sleep 72h. Cancel, revise, mutual resolve, arbitrate, chat history, `assignable=me`, and specific-policy accept are in the same smoke run. Open, start, seat, and winner-select timeouts are covered by moving the clock (`TestTimeouts`). The single-submission and exclusive auto-settle rules:

```bash
CGO_ENABLED=0 go test ./internal/engine -run 'TestAutoSettleCompetitiveDoesNotPickWinner|TestAutoSettleExclusive'
```

`etc/taskgame.yaml` holds:

- `Auth.JwtSecret`: HS256 secret, at least 16 characters. Shared by `make token`, register, and login
- `Auth.TokenTTL`: lifetime of register and login JWTs, default 24h
- `Auth.ApiKeys`: static `X-Api-Key` to user id
- `Store.Driver`: `mysql` (default, pooled) or `sqlite` (local debugging, one connection)
- `Store.Cells`: **three** cell DSNs by default, ordered (index = cell id). DSNs may point at different instances (3307/3308/3309) or at different schemas on one server. When set, `Store.DSN` and `Store.Shards` are ignored
- `Store.DSN`: single-database DSN (used only when `Cells` is empty)
- `Store.Shards`: legacy message-level prototype; `> 1` enables it. See "Message sharding (legacy)" for `Store.ShardDriver`, `Store.ShardDSNs` and `Store.ShardDSN`
- `Escrow.InitialBalance`: simulated credits granted the first time a user is seen (1000000 in the dev file)
- `Webhook.Mode`: `log` records the signature and does not POST; `post` delivers HTTP
- `AutoSettle`: settles when `review_deadline` passes. A competitive task with more than one submission stays `in_review` until `winner_select_deadline`, then refunds. `OpenTimeout`, `StartTimeout`, and `SeatTimeout` default to 72h
- `Scale.PubSub`: **`redis` by default**, the cross-process SSE signal bus. Leave `Scale.InstanceID` empty to auto-generate a unique id per process; if set explicitly it must be unique per API process. `Scale.Redis.Addr` / `Channel` must be identical across the deployment. Redis is started by compose and is required at startup; other Scale sub-systems stay off
- `Escrow.ArbiterUserIds`: users who may `POST /v1/tasks/{taskId}/arbitrate`. Dev config lists `usr_arbiter` (`make token USER_ID=usr_arbiter` or `X-Api-Key: dev-arbiter-key`)

Error bodies are JSON: `{"code":"...","message":"..."}`.

## goctl

运行时依赖是 `go.mod` 里的 go-zero v1.10.3。`taskgame.api` 是 goctl 的输入，与 OpenAPI 对齐。生成命令（与本仓库用来铺目录的 goctl 1.8.4 一致）：

```bash
go install github.com/zeromicro/go-zero/tools/goctl@v1.8.4
goctl api go -api taskgame.api -dir . -style goZero
```

`make generate` 会调用同一条命令。goctl 会重写：

- `internal/types/types.go`
- `internal/handler/**`（路由在 `internal/handler/routes.go`）
- `internal/logic/**` 里尚不存在的文件

这些文件是手写的，重新生成后不要覆盖：

- `taskgame.go`：统一错误处理、启动后台的自动结算和 webhook 投递（文件已存在时 goctl 会跳过它）
- `internal/config`、`internal/svc`、`internal/middleware`
- `internal/logic` 里已经接到 `internal/engine` 的方法体
- `internal/handler/task/submitTaskHandler.go`：提交体用 `encoding/json` 解码，因为 go-zero 的 mapper 填不了 `map[string]interface{}` 的 `payload`
- 创建任务、webhook、会话、消息，以及 `POST /v1/register` 的 handler 返回 **201**（`httpx.WriteJsonCtx`），其余成功响应是 200

The runtime dependency is go-zero v1.10.3 from `go.mod`. `taskgame.api` is the goctl input and matches the OpenAPI document. Generate with the same goctl 1.8.4 used to lay out this tree:

```bash
go install github.com/zeromicro/go-zero/tools/goctl@v1.8.4
goctl api go -api taskgame.api -dir . -style goZero
```

`make generate` runs that command. goctl rewrites:

- `internal/types/types.go`
- `internal/handler/**` (routes in `internal/handler/routes.go`)
- `internal/logic/**` files that do not exist yet

Do not overwrite these after a regen:

- `taskgame.go`: shared error handler, plus the auto-settle and webhook workers (goctl skips this file when it already exists)
- `internal/config`, `internal/svc`, `internal/middleware`
- `internal/logic` method bodies that call `internal/engine`
- `internal/handler/task/submitTaskHandler.go`: the body is decoded with `encoding/json` because go-zero's mapper cannot fill a `map[string]interface{}` payload
- create task, webhook, chat, and message handlers, plus `POST /v1/register`, return **201** via `httpx.WriteJsonCtx`; other successes return 200

## TypeScript 客户端

协议客户端在 [`sdk/ts`](sdk/ts)，npm 包名 `taskgame-client`（还没发布到 registry）。路径和类型以 [`api/openapi.yaml`](api/openapi.yaml) 为准，另外调用进程探活 `GET /health`。中英接入说明（基址、`X-Api-Key` / Bearer、独占 happy path、竞争确认要 `submission_id`、协作 `on_dropout=replace`、webhook 原始 body 的 HMAC-SHA256 十六进制）在 [`docs/client-ts.md`](docs/client-ts.md)。可运行示例 [`sdk/ts/examples/exclusive.mjs`](sdk/ts/examples/exclusive.mjs) 与 `scripts/smoke.sh` 的独占路径一致。

```bash
cd sdk/ts
npm install
npm test
npm run build
# 服务已在听（make run）时：
npm run example:exclusive
```

`baseUrl` 用 `http://127.0.0.1:8891`，或以 `/v1` 结尾的 OpenAPI 服务器 URL。鉴权用 `apiKey(...)`（`X-Api-Key`）或 `bearer(...)`（`Authorization: Bearer`）。写操作把 `idempotencyKey` 写成 `Idempotency-Key`。

## Web 界面

发布者 / 执行者演示页在 [`web`](web)（Vite + React，中文界面，依赖本地 `sdk/ts`）。`make run` 之后：

```bash
cd web && npm install && npm run dev
```

打开 `http://localhost:5137` 即可看到公开首页（未登录也能按独占、竞争、协作浏览任务，并可点关键字分类筛选）；注册/登录后可进入完整界面。创建任务时可设置 `keywords` 数组，用于首页按关键字分类；还可设置 `attributes`（位置、坐标 latitude/longitude、时限紧急度、重复周期、完成者条件标签），首页据此展示与筛选。`GET /v1/tasks` 支持服务端筛选：`q`（标题/描述/关键字全文检索）、`keyword`、`urgency`、`recurrence`、`executor_requirement`、`min_amount`/`max_amount`、`latitude`/`longitude`+`radius_km`（Haversine 距离，作用于全部任务）以及 `sort`（recent/amount_asc/amount_desc/deadline_asc/distance_asc），并返回 `total` 命中数——筛选在 SQL 里完成，不受 100 条分页上限影响。首页顶部有全文搜索框，所有筛选条件写入 URL（`#/home?q=...&keyword=...`），刷新和分享链接都能还原。报酬额始终取 `payment.amount`。用法、localStorage 键和三种任务的点法见 [`web/README.md`](web/README.md)。进程为本地 Vite 源（`localhost` / `127.0.0.1` / `[::1]` 的 5137、4173 与 5173–5185）加了 CORS 预检，业务路由不变。

## TypeScript client

The protocol client is [`sdk/ts`](sdk/ts), npm package `taskgame-client` (not published to the registry yet). Paths and types follow [`api/openapi.yaml`](api/openapi.yaml), plus process liveness `GET /health`. The Chinese and English guide (base URL, `X-Api-Key` / bearer, the exclusive happy path, competitive `submission_id`, collaborative `on_dropout=replace`, hex HMAC-SHA256 of the raw webhook body) is [`docs/client-ts.md`](docs/client-ts.md). The runnable sample [`sdk/ts/examples/exclusive.mjs`](sdk/ts/examples/exclusive.mjs) follows the exclusive path in `scripts/smoke.sh`.

```bash
cd sdk/ts
npm install
npm test
npm run build
# with the API already listening (make run):
npm run example:exclusive
```

`baseUrl` is `http://127.0.0.1:8891`, or the OpenAPI server URL ending in `/v1`. Auth is `apiKey(...)` (`X-Api-Key`) or `bearer(...)` (`Authorization: Bearer`). Mutating calls send `idempotencyKey` as `Idempotency-Key`.

## Web UI

The publisher / executor demo is [`web`](web) (Vite + React, Chinese UI, local `sdk/ts`). With `make run` already listening:

```bash
cd web && npm install && npm run dev
```

Open `http://localhost:5137` for the public homepage: visitors can browse exclusive, competitive, and collaborative tasks before signing in. Keys, localStorage, and the three task flows are in [`web/README.md`](web/README.md). The process adds a CORS preflight for local Vite origins (`localhost`, `127.0.0.1`, `[::1]` on 5137, 4173 and 5173–5185). Handlers stay the same.

## 布局 / Layout

```
taskgame.api                 goctl API 描述 / goctl API spec
taskgame.go                  进程入口 / process entry
etc/taskgame.yaml            配置 / config
api/openapi.yaml             OpenAPI 3.1 合同 / contract
cmd/token                    开发用 HS256 JWT / dev JWT
internal/handler             HTTP 适配（goctl） / HTTP adapters
internal/logic               鉴权用户 → engine / caller id → engine
internal/svc                 依赖注入 / wiring
internal/middleware          Bearer JWT 与 X-Api-Key
internal/engine              状态机、托管账本、幂等 / FSM, escrow, idempotency
internal/store               Store 接口；sqlite 与 mysql / sqlite and mysql
internal/webhook             签名与投递 / sign and deliver
internal/model               持久化记录 / persistence records
scripts/smoke.sh             三种任务的端到端冒烟 / smoke for all three kinds
sdk/ts                       TypeScript 客户端 taskgame-client / TS client
docs/client-ts.md            客户端接入说明（中英） / client guide (zh + en)
web/                         浏览器演示（Vite + React） / player demo UI
```

MySQL 实现同一个 `store.Store`，handler 不用改。JSON 和时间在两个驱动里都是文本。SQLite 用隐式 `rowid` 排消息，MySQL 用 `messages.seq`。

The MySQL store implements the same `store.Store`. Handlers stay put. JSON and timestamps are text on both drivers. SQLite orders messages by the implicit `rowid`. MySQL uses `messages.seq`.

## OpenAPI 路径 → handler

服务器 URL 在 OpenAPI 里是 `https://api.example.com/v1`，所以下面的路径已经带上 `/v1`。`GET /health` 只给进程探活，不在协议里。

The OpenAPI server URL is `https://api.example.com/v1`, so the paths below include `/v1`. `GET /health` is process liveness and is not part of the protocol.

| Method | Path | operationId | Handler | Logic |
| --- | --- | --- | --- | --- |
| GET | `/health` | — | `handler/health.HealthHandler` | `logic/health.Health` |
| GET | `/v1/tasks` | `listTasks` | `handler/task.ListTasksHandler` | `logic/task.ListTasks` |
| POST | `/v1/tasks` | `createTask` | `handler/task.CreateTaskHandler` | `logic/task.CreateTask` |
| GET | `/v1/tasks/:taskId` | `getTask` | `handler/task.GetTaskHandler` | `logic/task.GetTask` |
| POST | `/v1/tasks/:taskId/fund` | `fundTask` | `handler/task.FundTaskHandler` | `logic/task.FundTask` |
| POST | `/v1/tasks/:taskId/accept` | `acceptTask` | `handler/task.AcceptTaskHandler` | `logic/task.AcceptTask` |
| POST | `/v1/tasks/:taskId/submit` | `submitTask` | `handler/task.SubmitTaskHandler` | `logic/task.SubmitTask` |
| POST | `/v1/tasks/:taskId/confirm` | `confirmTask` | `handler/task.ConfirmTaskHandler` | `logic/task.ConfirmTask` |
| POST | `/v1/tasks/:taskId/dispute` | `disputeTask` | `handler/task.DisputeTaskHandler` | `logic/task.DisputeTask` |
| POST | `/v1/tasks/:taskId/dropout` | `dropoutTask` | `handler/task.DropoutTaskHandler` | `logic/task.DropoutTask` |
| POST | `/v1/webhooks` | `createWebhook` | `handler/webhook.CreateWebhookHandler` | `logic/webhook.CreateWebhook` |
| POST | `/v1/chats` | `createChat` | `handler/chat.CreateChatHandler` | `logic/chat.CreateChat` |
| POST | `/v1/chats/:chatId/messages` | `postMessage` | `handler/chat.PostMessageHandler` | `logic/chat.PostMessage` |
| GET | `/v1/users/:userId/scores` | `getUserScores` | `handler/user.GetUserScoresHandler` | `logic/user.GetUserScores` |
| GET | `/v1/me` | `me` | `handler/user.GetMeHandler` | `logic/user.GetMe` |
| PATCH | `/v1/me` | `updateMe` | `handler/user.UpdateMeHandler` | `logic/user.UpdateMe` |

创建类接口（task、webhook、chat、message、新建好友分组）返回 201。新建好友申请在状态为 `pending` 时返回 201；如果直接接受了对方已有的申请，返回 200。其余成功返回 200。除 `/health`、`GET /v1/tasks`、`POST /v1/register`、`POST /v1/login` 外都要 Bearer JWT 或 `X-Api-Key`；`GET /v1/tasks?assignable=me` 仍需鉴权。

Creates (task, webhook, chat, message, friend group) return 201. A new friend request returns 201 while `pending`. Accepting the other side's existing request on that same route returns 200. Other successes return 200. Every route except `/health`, `GET /v1/tasks`, `POST /v1/register`, and `POST /v1/login` requires a bearer JWT or `X-Api-Key`; `GET /v1/tasks?assignable=me` still requires auth.

### 好友 / Friends

好友是双向关系，存在 `friendships` 的一行里（`user_low < user_high`）。分组是个人文件夹，对方看不到。上限：每人 50 个分组、500 个好友、100 条待处理的发出申请。分组名去掉首尾空白后 1–32 个字符，同一用户下不能重复。`sort_order` 范围是 -1000000 到 1000000。

重复申请：已经是好友，或自己已经发出待处理申请，返回 409，不插入第二行。如果对方已经有一条指向自己的待处理申请，`POST /friends/requests` 会接受那一条并返回 200 `status=accepted`，而不是再开一条。拒绝、撤回、解除好友之后可以重新申请。删除分组不会解除好友。解除好友会清掉双方对这一对的分组成员。

好友卡片有 `user_id`、`nickname`、`integrity`、`ability`、`since`、`unread_count`、`direct_chat_id`，不含邮箱、余额和密码。`nickname` 可以和别人相同，没设置时是空字符串。申请上的 `from_nickname` / `to_nickname` 也是这样。`unread_count` 是对方在这一对的一对一会话里发来、调用者还没标已读的条数。自己发的消息不算。还没有一对一会话时 `unread_count` 是 0，`direct_chat_id` 是 null。任务会话和多人会话不计入。`POST /v1/chats/{chatId}/read` 只推进调用者自己的已读位置，不发 webhook。`GET` 消息历史不会标已读。省略 `message_id` 会标到当前最新一条；带上 `message_id` 就停在那一条，不会往后。同一个 `Idempotency-Key` 会重放第一次的响应，不会把之后新到的消息标成已读。Webhook `friend.requested`（发给接收方）、`friend.accepted` 和 `friend.declined`（发给申请方）、`friend.removed`（发给被解除的一方，`data.user_id` 是操作者）走现有的用户订阅和 HMAC 投递，不按任务过滤。撤回申请不发 webhook。

一对一会话：`POST /v1/chats`，body 是 `{ "type": "direct", "user_id": "usr_…" }`。对方必须已经存在。不要求是好友，这次调用也不会加好友。不能和自己开（400）。不存在的用户是 404。同一对用户只存一行，两边再调都返回同一个 `id`，HTTP 仍是 201，不会再插入。解除好友之后再调，还是原来那条。第一次创建时给双方发 `chat.created`。响应里的 `type` 用来区分：`task` 是任务会话（`GET /chats?task_id=` 里全是这种），`direct` 是一对一，`group` 是原来的 `member_ids` 自由会话（每次调用新开一行）。解除好友不会把人移出已有会话，历史还能读，也还能发消息。任务会话仍由注资创建，从任务详情进入。

| Method | Path | operationId |
| --- | --- | --- |
| GET | `/v1/friends` | `listFriends` |
| POST | `/v1/friends/requests` | `createFriendRequest` |
| GET | `/v1/friend-requests?direction=incoming\|outgoing` | `listFriendRequests` |
| POST | `/v1/friend-requests/:requestId/accept` | `acceptFriendRequest` |
| POST | `/v1/friend-requests/:requestId/decline` | `declineFriendRequest` |
| DELETE | `/v1/friend-requests/:requestId` | `cancelFriendRequest` |
| DELETE | `/v1/friends/:userId` | `unfriend` |
| GET/POST | `/v1/friend-groups` | `listFriendGroups` / `createFriendGroup` |
| PATCH/DELETE | `/v1/friend-groups/:groupId` | `updateFriendGroup` / `deleteFriendGroup` |
| PUT | `/v1/friends/:userId/group` | `setFriendGroup` |

Friendship is one row per pair (`user_low < user_high`). Groups are personal folders. Caps: 50 groups, 500 friends, and 100 pending outgoing requests per user. Names are 1–32 characters after trim and unique per owner. `sort_order` is from -1000000 to 1000000.

Duplicate rule: already friends, or a pending request you already sent, is 409 with no second row. If the other user already has a pending request toward you, `POST /friends/requests` accepts that request and returns 200 `status=accepted`. Decline, cancel, and unfriend all allow a later request. Deleting a group leaves the friends in place, ungrouped. Unfriend clears both users' membership for that pair.

A friend card is `user_id`, `nickname`, `integrity`, `ability`, `since`, `unread_count`, and `direct_chat_id`. No email, balance, or password. `nickname` may be shared and is an empty string when unset. Friend requests expose the same names as `from_nickname` and `to_nickname`. `unread_count` is how many messages the other person sent in that pair's direct chat after the caller's read cursor. The caller's own messages are not counted. Before a direct chat exists, `unread_count` is 0 and `direct_chat_id` is null. Task chats and group chats are not included. `POST /v1/chats/{chatId}/read` moves only the caller's cursor and does not emit a webhook. Listing messages does not mark them read. Omit `message_id` to cover the newest message; set it to stop there. The same `Idempotency-Key` replays the first response and does not cover messages that arrived later. `friend.requested` goes to the recipient. `friend.accepted` and `friend.declined` go to the requester. `friend.removed` goes to the other person (`data.user_id` is the actor). Delivery is the existing owner-scoped HMAC webhook, not task-scoped. Cancel does not emit an event.

# 物理切分演练（按域）

这是 `docs/per-domain-sharding-plan.md` 第 7 节和第 4 阶段的可执行 runbook。它覆盖 message、user、task 三个域的物理搬迁，每次只切一个域，不改任何 API 语义。工具在 `cmd/shardctl`，库实现在 `internal/shardmigrate`。

状态：SQLite 自动化演练与生产化护栏（冻结写、资金 saga、控制面缓存/副本、`/metrics`、游标世代）均已落地；真机 MySQL + 双进程回归脚本已编写但未在作者环境实测，详见第 9 节。逐项完成状态见 `docs/remaining-work-backlog.md`。

## 1. 演练目标

- 给一个已经运行的老系统（没有目录行、三库共置）回填权威目录；
- 给某个域加一个**第四物理库**，只让新聚合根按新拓扑落过去；
- message 域搬 chat 及其消息、阅读游标、legacy `chat_shards` 行；
- user 域搬用户、余额/积分、积分事件、好友、分组、幂等记录；
- task 域搬任务、投递、托管账本与流水、邀请；
- 校验行数与内容，原子翻转目录，删源，验证读写切流；
- 整套动作可原路回滚。

## 2. 与逻辑分域的区别

阶段 2 的“逐域切流”只是改配置里的 DSN 列表，数据在原库；本演练做的是**真的把行复制到另一台库**，并改写 `shard_directory`。目录的 `Assign` 永不覆盖，所以改写只能走专门的维护接口 `SQLDirectory.ReassignBatch`，它带 CAS 条件（当前片号不匹配就整批回滚），避免过期脚本抢走已迁移的 key。

## 3. 前置条件

- 控制面库：`Scale.Directory.DSN`，含 `shard_directory` / `shard_topology`。
- 旧库 3 个（示例 `:3307/:3308/:3309`），新库 1 个（示例 `:3310`），每个都是**完整 schema**。
- 所有 API 进程最终必须拿到同一份有序 DSN 列表；滚动升级期间新旧配置并存，目录行是唯一权威。
- 选一个低峰维护窗口。切换目录到删源之间会有短暂的双份数据，但目录已指向新库，读不会丢失。

## 4. 自动化演练（无外部依赖）

```bash
go test ./internal/shardmigrate/... -run TestPhysicalCutoverDrill -v
```

这个测试用四个 SQLite 文件模拟四个物理库，覆盖：目录回填 → `Topology.AddShard` → Copy → Verify → 幂等重试 Copy → Cutover → Cleanup → 重启 API → 读历史消息、读游标、查直聊、写新消息 → Rollback → 再次读。它也覆盖了回填时目录与实际数据不一致的早期发现。

user / task 域各自的端到端演练：

```bash
go test ./internal/shardmigrate/... -run 'Test(User|Task)DomainPhysicalCutover' -v
```

覆盖用户余额/昵称/积分/积分事件、好友双写与分组、收到的好友请求、幂等记录；以及任务、投递、托管账本与流水、邀请。两者都验证了切流后的读、写和完整回滚。

## 5. 真机 MySQL 步骤

下面的命令用 `shardctl`；`-driver mysql`，DSN 与 `etc/taskgame.yaml` 中的写法一致。

### 5.1 准备新库

```bash
# 新库建完整 schema（CREATE TABLE IF NOT EXISTS + 所有 additive migration）
shardctl init -driver mysql -dsns "<new-dsn>"
```

### 5.2 回填目录

```bash
shardctl backfill -driver mysql \
  -control "<control-dsn>" \
  -dsns "<dsn-0>,<dsn-1>,<dsn-2>" \
  -domains user,task,message
```

逐库扫 `users` / `tasks` / `chats`，把主键（直聊额外写 `low#high`）钉在实际所在的片。重复运行安全；如果发现某 key 在库中的位置与已有目录行冲突，会打印 `MISMATCH` 并以非零码退出，**不会**覆盖目录。

```bash
shardctl status -driver mysql -control "<control-dsn>"
```

### 5.3 扩容并滚动重启

1. 在配置里给 `Scale.Domains.Message.DSNs` 追加第 4 个 DSN，`Shards: 4`；user/task 保持 3 个。
2. 每个 API 进程重启后，`wireDirectory` 会把新片写进 `shard_topology`（`EnsureShards` 只插入，不缩容）。
3. 用 `status` 确认所有进程看到相同的拓扑。此时新 key 会按 4 片哈希，老 key 仍由目录行钉在原片。

### 5.4 选择并搬迁 chat

```bash
shardctl plan -driver mysql -dsns "<dsn-0>,<dsn-1>,<dsn-2>" -from 0 -chats "chat_a,chat_b"
shardctl copy -driver mysql -source "<dsn-0>" -target "<dsn-3>" -to 3 -chats "chat_a,chat_b"
shardctl verify -driver mysql -source "<dsn-0>" -target "<dsn-3>" -to 3 -chats "chat_a,chat_b"
```

`copy` 会搬 `chats`、`messages`、`chat_reads`、`chat_shards`：

- **消息顺序**：MySQL 的 `messages.seq` 和 SQLite 的 `rowid` 都是自增值，复制后必然变化。`copy` 按源顺序插入，记录 old→new 映射，并用它重写 `chat_reads.last_read_rowid`。`verify` 会再次独立推导映射并比对，映射错位会直接失败。
- **幂等**：目标已存在同 id 行时会比对内容；一致则跳过，不一致则报错。中断后可直接重试。
- **目录行**：`chats.id` 和直聊的 `low#high` 两个 key 必须一起翻，这一点由 `plan`/`cutover` 保证。

### 5.5 切流与清理

```bash
shardctl cutover -driver mysql \
  -control "<control-dsn>" \
  -source "<dsn-0>" -target "<dsn-3>" \
  -from 0 -to 3 -chats "chat_a,chat_b"
```

它在一个控制面事务里 CAS 改写所有目录行，随后删除源库的 chat 数据。想先观察再删源，加 `-skip-cleanup`，稍后单独执行 `cleanup`。

验证清单：

- `status` 里 message 域 shard 3 出现对应 key；
- 用两个不同用户（API key）查同一会话，读到相同的历史消息；
- 阅读游标未变（未读数与迁移前一致）；
- 迁移后发一条新消息，用 SQL 确认它落在 `<dsn-3>` 的 `messages` 表；
- `FindDirect` 仍能找到该会话（跨片扫描路径仍有效）。

### 5.6 回滚

```bash
shardctl rollback -driver mysql \
  -control "<control-dsn>" \
  -source "<dsn-0>" -target "<dsn-3>" \
  -from 0 -to 3 -chats "chat_a,chat_b"
```

从新库反向 copy 回旧库 → verify → CAS 把目录翻回 0 → 删除新库行。目录 CAS 保证重复执行不会把别的 key 带回去。

## 6. user / task 域搬迁

这两个域与 message 域用同一套命令，只是加 `-entity=user|task`，并用 `-keys` 传聚合根 id：

```bash
# 1) 规划（确认 key 在源库存在）
shardctl plan -driver mysql -entity user -dsns "<dsn-0>,<dsn-1>,<dsn-2>" -from 0 -keys "usr_a,usr_b"

# 2) 复制 + 校验（源行保留）
shardctl copy -driver mysql -entity user -source "<dsn-0>" -target "<dsn-3>" -to 3 -keys "usr_a,usr_b"
shardctl verify -driver mysql -entity user -source "<dsn-0>" -target "<dsn-3>" -to 3 -keys "usr_a,usr_b"

# 3) 切目录 + 清源
shardctl cutover -driver mysql -entity user -control "<control-dsn>" \
  -source "<dsn-0>" -target "<dsn-3>" -from 0 -to 3 -keys "usr_a,usr_b"

# 4) 回滚（同样是 copy back → verify → 翻目录 → 清目标）
shardctl rollback -driver mysql -entity user -control "<control-dsn>" \
  -source "<dsn-0>" -target "<dsn-3>" -from 0 -to 3 -keys "usr_a,usr_b"
```

把 `-entity` 换成 `task`、`-keys` 换成 task id 即完成任务域搬迁。

### 6.1 迁移单元

| 域 | 目录键 | 一起搬迁的表 |
|---|---|---|
| user | `user_id` | users（含头像与余额）、score_events、friend_requests（发送方所有）、friendships（双写）、friend_groups、friend_group_members、idempotency_keys |
| task | `task_id` | tasks、submissions、escrow_accounts、escrow_entries（随所属托管账户）、invites |

**不随任务搬迁的表**：`webhooks` 按 owner id 路由，`webhook_deliveries` 按 delivery id 路由，两者都不由 task_id 决定，搬迁后仍通过 scatter 读到达。

### 6.2 与 message 域的三个关键差异

1. **没有自增序号**：user/task 域的表都以 varchar 主键定位，没有 `messages.seq` 那样需要重映射的序号。复制按列原样搬运，不经过 model 结构体重新序列化，因此时间戳、JSON、BLOB 字节级保真。
2. **好友关系是双写的**：`friendships` 在双方各自的 user 分片都有一行。搬迁时复制到新片，但**不删源行**——那一行可能是对端用户在本片的唯一本地视图。源库里 A 与 C（都留在旧片）的那一行同样保留。幂等复制保证重复执行不会制造重复主键。
3. **目录键就是主键**：user 域 `user_id`、task 域 `task_id`（任务创建时会显式写目录行），因此 `PlanEntity` 只需确认主表存在该行。`Resolver.Place` 的修复确保预置目录行不会被忽略。

### 6.3 事务边界的现实

共置部署下，“发布任务扣发布者余额”是一次本地事务。user 与 task 分置到不同物理库后，这类跨 user/task 操作会退化为最终一致（非严格模式），或返回 `ErrCrossShard`（严格模式）。**资金相关操作建议保持 user/task 共置**，先只切 message 域；确需切分时启用 `Scale.Compensation.Enabled`，由 `internal/compensation` 的持久化 saga 与后台 reconciler 处理单边账（方案文档第 6 节）。补偿链路已有自动化测试，真机 MySQL 上的崩溃重放尚未实测。

同理，新任务创建时若请求带路由 key（发布者 user id），`createPlacement` 仍按 pinned 片数定位，新物理片主要承接迁移过来的任务，而不是自动接收所有新任务。

### 6.4 验证清单

- `status` 中 user/task 域 shard 3 出现对应 key；
- `Users().Get` 的余额、昵称、积分与迁移前一致，`ListScoreEvents` 完整；
- `ListFriendEdges` 双向都能补到对端昵称；收件人的 incoming 好友请求仍可见；
- 幂等记录 `Idem().Get` 命中；
- `Tasks().Get`、投递列表、托管账户与流水、邀请列表完整；
- 迁移后写入（改余额/改任务状态）落在新的物理库；
- 回滚后目录、数据、路由读全部恢复。

## 7. 生产化护栏（已实现）

- **冻结写入**：`plane.SQLFence` + `shardctl fence-lock/unlock/list` 可在迁移窗口冻结选中 key 或整个域；domain 存储写入前检查，命中返回 `plane.ErrFenced`。无需 CDC 也能保证窗口内无并发写（代价是冻结期间这些 key 不可写）。持续双写场景仍需 CDC。
- **跨 user/task 事务补偿**：`internal/compensation` 的持久化 saga + 后台 reconciler，FundTask 在扣款前记录 intent，崩溃后按 escrow 权威金额重放或退款；通过 `Scale.Compensation.Enabled` 开启（需 `Scale.Directory.Enabled`）。
- **控制面高可用**：`SQLDirectory` 增加读缓存与可选 `Scale.Directory.ReplicaDSN`（读走副本、故障自动回落主库）；目录行与业务行在共置时可经 `AssignInTx` 同事务写入。
- **监控**：`/metrics` 暴露目录命中率、控制面 QPS、跨域事务数、冻结写、补偿次数。
- **auto_increment 与游标（仅 message 域）**：`last_read_rowid` 已重写；外部保存的 rowid/seq 由 `plane.CursorEpoch` + `shardctl cursor-bump` 失效，客户端传 legacy bare-offset 时按 epoch 0 兼容，传 v2 `v2|<epoch>|<offset>` 时遇到世代变化自动从 offset 0 重新拉取。
- **部分索引/生成列**：MySQL `chats.direct_pair`、`friend_requests.pending_pair` 是生成列，copy 不显式写它们；SQLite 无此列。两边 schema 差异已在工具中处理。

## 8. 本次演练已经验证的事实

- 目录回填能让“迁移前没目录”的老 key 全部被 `ResolveExisting` 命中；
- `Resolver.Place` 已修复为返回**目录中的权威片号**（此前会返回调用方传入的片号，可能把数据写到与目录不同的库）；
- `ReassignBatch` 的 CAS + 单事务能挡住过期脚本的重放；
- message 域加片后，老消息仍可读、新消息落新库、跨片 `FindDirect` 正常；
- 阅读游标在消息序号重建后仍然正确；
- user 域搬迁后余额、积分、好友、分组、幂等记录与收件人视角全部正确，双写好友行未被误删；
- task 域搬迁后任务、投递、托管账本与流水、邀请完整，写入落新片；
- 三个域的完整回滚都能让目录、数据、路由读回到迁移前状态。

## 9. 真机 MySQL + 双进程演练（未实测项）

`scripts/drill-mysql.sh` 是真机演练的可执行脚本：拉起第 4 个 MySQL cell（3310）+ 控制面库，跑 `shardctl init/backfill/plan/copy/verify/cutover`，然后以新拓扑重启 8891/8892 两个 API 进程验证跨进程 SSE 与历史消息，最后 rollback。它覆盖运行手册第 5 节的全部步骤。

**状态：脚本已编写并通过 `bash -n` 语法校验，但未在真实 MySQL 环境执行。** 撰写环境没有 Docker / MySQL / Redis（端口 3307-3311、6379 均未监听，也没有相应可执行文件），因此真机行为——尤其是 MySQL 生成列、`DATETIME` 解析、`INSERT IGNORE` 并发语义和双进程 Redis pub/sub——**尚未实测**。测试等价物是 SQLite 自动化演练（`go test ./internal/shardmigrate/...`），它验证了除 MySQL 方言差异外的全部逻辑。

在有 Docker 的机器上执行：

```bash
bash scripts/drill-mysql.sh           # 全量演练，结束自动清理
bash scripts/drill-mysql.sh --keep    # 保留容器与进程，便于人工核对
```

脚本phase 2 会打印提示；真机上需把创建出的 chat id 传给 phase 3/5 的 `shardctl -chats <id>`（这部分保留为显式手工步骤，避免自动选取错误的会话）。跨进程 SSE 订阅 `/v1/events/stream` 的断言同样保留为人工核对项。

## 10. 配套运维命令

除切分主流程外，`shardctl` 还提供以下生产化操作：

```bash
# 迁移窗口冻结/解冻写入，key 可用 * 表示整个域
shardctl fence-lock   -control "<control-dsn>" -domain message -key "chat_a"
shardctl fence-unlock -control "<control-dsn>" -domain message -key "chat_a"
shardctl fence-list   -control "<control-dsn>"

# 消息游标世代：物理切流后让客户端旧 cursor 失效
shardctl cursor-bump -control "<control-dsn>" -chats "chat_a,chat_b"

# webhook delivery body 外置：一份正文 + N 个 body_ref
shardctl externalize-bodies -source "<dsn>" -blob-dir "/var/lib/taskgame/blobs"

# 冷归档与恢复：把旧行搬到 blob 冷库，按 checksum 校验
shardctl archive-run -source "<dsn>" -blob-dir "/var/lib/taskgame/cold" -table messages

# archive-run 会打印 ref；恢复时把该 ref 传回
shardctl archive-restore -source "<dsn>" -blob-dir "/var/lib/taskgame/cold" \
  -table messages -ref "<archive-ref>"
```

`externalize-bodies` 只处理 webhook delivery body；头像与 submission payload 的 `*_ref` 迁移仍未做，保留在 `internal/scale.TODOs`。

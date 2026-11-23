# 物理切分演练（按域，message 域优先）

这是 `docs/per-domain-sharding-plan.md` 第 7 节和第 4 阶段的可执行 runbook。它只搬**一个域**（message）里选中的 chat，不碰 user/task 域，也不改任何 API 语义。工具在 `cmd/shardctl`，库实现在 `internal/shardmigrate`。

## 1. 演练目标

- 给一个已经运行的老系统（没有目录行、三库共置）回填权威目录；
- 给 message 域加一个**第四物理库**，只让新会话落过去；
- 把选中的旧 chat 及其消息、阅读游标、legacy `chat_shards` 行搬到新库；
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

这个测试用四个 SQLite 文件模拟四个物理库，覆盖：目录回填 → `Topology.AddShard` → Copy → Verify → 幂等重试 Copy → Cutover → Cleanup → 重启 API → 读历史消息、读游标、查直聊、写新消息 → Rollback → 再次读。它也覆盖了回填时目录与实际数据不一致的早期发现（见第 9 节）。

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

## 6. 真正上生产还需要什么

- **user/task 域的物理迁移**：`Backfill` 已支持扫这两域的目录，但 `shardctl` 的 copy/verify 只实现 message 域（chats 及其关联表）。资金/任务账本搬迁要单独设计。
- **冻结写入或 CDC**：当前演练假设维护窗口内选中 chat 没有并发写入。持续写入场景需要先接 CDC 或双写。
- **控制面高可用**：SQLDirectory 当前单 DSN；生产至少要主从。
- **auto_increment 与游标**：迁移后的 `last_read_rowid` 已重写，但任何外部保存的 rowid/seq（如客户端游标）都需要同步失效。
- **部分索引/生成列**：MySQL `chats.direct_pair` 是生成列，copy 不显式写它；SQLite 无此列。两边 schema 差异已在 `copy` 中处理。

## 7. 本次演练已经验证的事实

- 目录回填能让“迁移前没目录”的老 key 全部被 `ResolveExisting` 命中；
- `Resolver.Place` 已修复为返回**目录中的权威片号**（此前会返回调用方传入的片号，可能把数据写到与目录不同的库）；
- `ReassignBatch` 的 CAS + 单事务能挡住过期脚本的重放；
- message 域加片后，老消息仍可读、新消息落新库、跨片 `FindDirect` 正常；
- 阅读游标在消息序号重建后仍然正确；
- 完整回滚后目录、数据、路由读全部回到迁移前状态。

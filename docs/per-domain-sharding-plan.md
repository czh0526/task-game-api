# 按域分片改造方案（P2）

状态：阶段 0/1/2、全局身份与 ID、事务护栏、控制面高可用、规模骨架与可观测性均已实现并接线；默认运行时仍为 cell 模式，`Store.Mode: domain` 可按需切换。阶段 4 的**物理切分演练**工具、SQLite 自动化演练与真机 MySQL cutover/rollback/SSE 均已完成，详见 `docs/physical-shard-drill.md` 第 9 节。

`internal/scale.TODOs` 当前仅保留 avatar_ref / payload_ref 大字段迁移；逐项完成状态与验收证据见 `docs/remaining-work-backlog.md`。

本文只讨论 persistence 层的改造，不改任何 API 语义、事件格式或前端。

---

## 1. 现状与目标

### 现状（main 分支，cell 单元化）

- 配置只有一份 `Store.Cells: [DB0, DB1, DB2]`，三个库各跑全 schema。
- `cell.Open` 建一个 `cell.Store`，包三个完整的 `store.Store`。
- 只有 `cell.Router`（FNV-1a % 3）。请求按 `user_id` 或 URL 里的聚合根 id 选中一个 cell，该请求的全部表读写都在这个库。
- 好处：同一用户的所有数据在同一个库，一次 `InTx` 就是一次本地事务，无 2PC。
- 代价：扩容只能整体搬迁用户单元；消息量增长最快，却不能单独扩容。

### 目标（按域）

每个聚合根域拥有**独立的 DSN 列表、独立的路由键、独立的目录映射**：

| 域 | 路由键 | 典型数据 |
|---|---|---|
| `user` | `user_id` | users、user_avatars、score_events、friends、friend_groups、friend_group_members、friend_requests |
| `task` | `task_id`（或发布者 `user_id`） | tasks、submissions、escrow_accounts、escrow_entries、webhooks、deliveries、invites |
| `message` | `ChatKey`：直聊为 `OrderedPair(a,b)` 即 `low#high`，其它为 `chat_id` | chats、messages、chat_reads |

三域的 DSN 列表**可以相同也可以不同**——这是这套设计的全部意义所在。

---

## 2. 核心策略：先逻辑分域、物理共置，再按需切分

这是本方案最重要的决定，也是控制风险的关键。

**第一步只做逻辑分域。** 配置上把 `user` / `task` / `message` 三个域的 DSN 列表都填成现有的 `[DB0, DB1, DB2]`，并且三个域的路由键都仍从 `user_id` 派生。此时：

- 每个域各自解析出 `Placement`，但解析结果恰好落在同一个库；
- 同一用户的数据仍然共置，现有事务全部保持本地事务；
- API 行为、数据位置、测试断言全部不变。

**第二步在确实出现瓶颈时，只切一个域。** 例如消息成为热点，把 `message` 域的 DSN 列表改成独立的 `[M0..M5]`，并让消息域按 `ChatKey` 路由。此时：

- 消息数据迁移到消息域专用库；
- 涉及消息 + 其它域的事务才需要按第 6 节拆分；
- user / task 两个域完全不动。

这样把"域拆分"和"跨域事务"两笔成本解耦：前者先拿到目录权威、独立路由、独立配置的好处，后者只在真正需要时才付。直接全切会让每个事务都变成跨域问题，那是 P2 至今没接线的原因。

---

## 3. 已经就绪的基础设施（不要重造）

现有代码已经把骨架搭好了，改造主要是接线：

| 组件 | 位置 | 现状 |
|---|---|---|
| `Domain`、`Topology`、`Placement` | `internal/plane/directory.go`、`topology.go` | 完整实现。`Topology.Register/AddShard/DSN/Pinned`，`map[Domain][]ShardRef` 就是"每域一份 DSN 列表" |
| `Resolver` | `internal/plane/resolver.go` | 完整。`Resolve`（新 key，按当前片数并写目录）、`ResolveExisting`（老 key，按 pinned 片数，不写目录）、`Place`（显式片号） |
| `SQLDirectory` | `internal/plane/sql_directory.go` | 完整。持久化 `shard_directory` / `shard_topology`，`Assign` 幂等且永不覆盖 |
| `AssignInTx` | `internal/plane/sql_directory.go:180` | 已预留：在调用方事务里写目录行，让业务行与目录行同事务 |
| `Schema` | `internal/plane/control.go:19` | 已含 `shard_directory`、`shard_topology`、`global_unique` 三张表 |
| `UniqueIndex` / `InMemoryUnique` | `internal/plane/control.go` | 接口 + 内存实现；`global_unique` 的 SQL 实现待做 |
| `IDGenerator` / `LocalIDGenerator` | `internal/plane/control.go` | 接口 + 本地实现；雪花实现待做 |
| `DomainRouter` | `internal/store/shard/domains.go` | `Resolve`、`ResolveChat`、`ScaleOut` 已实现，`ChatKey` 已保证直聊对称 |
| `TxScope` / `ErrCrossShard` | `internal/store/shard/domains.go:59` | 已实现。同域跨片直接报错，不静默跨库 |

需要新增/替换的只有：`DomainStore` 实现、`scale` 接线、配置字段、`SQLUnique`、`SnowflakeIDGenerator`。

---

## 4. 改造步骤

### 阶段 0：配置与控制面（无行为变化）

1. `ScaleConf.Domains`（`internal/config/config.go:126`）从只有 `UserShards/TaskShards` 数量，扩展为每域 DSN 列表：

   ```yaml
   Scale:
     Enabled: true
     PubSub: redis
     Directory:
       Enabled: true
       Driver: mysql
       DSN: "root:123456@tcp(127.0.0.1:3307)/taskgame_control?..."
     Domains:
       User:
         Shards: 3
         DSNs: ["...3307...", "...3308...", "...3309..."]
       Task:
         Shards: 3
         DSNs: ["...3307...", "...3308...", "...3309..."]
       Message:
         Shards: 3
         DSNs: ["...3307...", "...3308...", "...3309..."]
   ```

2. `registerDomains`（`internal/scale/runtime.go:179`）改为读这些 DSN，删掉 `skeleton://` 占位符。Message 不再从旧的 `Store.Shards/ShardDSNs` 取值。

3. 打开 `Scale.Directory.Enabled`，让 `wireDirectory` 用 `OpenSQLDirectory` 把目录落到独立控制面库（**不要复用业务库**，目录必须小而可缓存）。确认 `EnsureShards` 在启动时把三域片数写进 `shard_topology`。

4. 保留 `cell.Store` 作为默认 `Store` 实现，新增一个开关（例如 `Store.Mode: cell|domain`）选择走哪条链路。默认仍是 `cell`，保证 PR 可独立回滚。

验收：`go test ./...` 全绿；用默认配置启动，行为与当前完全一致；控制面库出现三域各 3 条 `shard_topology` 行。

### 阶段 1：域仓储骨架（仍由 cell 承载数据）

1. 新建 `internal/store/domain` 包，实现 `store.Store` 接口，内部持有三个 `DomainStore`（每个域一个 DSN→`store.Store` 的连接池映射）和一个 `*shard.DomainRouter`。

2. 把 `store.Repos` 的 10 个 repo 按第 1 节的域归属拆开。`domainStore.InTx` 的逻辑：

   - 从 ctx 或请求 hint 解析出本次事务的**主域**和 key；
   - `Resolver.Resolve/ResolveExisting` 得到 `Placement`；
   - `TxScope.Pin(placement)` 校验（同域跨片立即返回 `ErrCrossShard`）；
   - 只打开主域对应 DSN 的连接和事务，把该域的 repos 交给 `fn`；
   - 其它域的 repo 通过 overlay 提供**本域内只读**或显式 scatter 读（沿用 `cell/overlay*.go` 的成熟做法）。

3. 所有创建类操作在写业务行的**同一个事务**里调用 `plane.AssignInTx` 写目录行；读取类操作走 `Resolver.ResolveExisting`，目录命中则直接拿 DSN，未命中按 pinned 片数算哈希（兼容迁移前的老数据）。

验收：新增 `domain` 包的单元与集成测试；在共置配置下重跑 `cell_test.go` 的全部断言（三库同 schema、约 1/3 分布、跨库读），结果应一致。

**阶段 1 实现状态**：`internal/store/domain` 已完成 `store.Store` 接口、每域 DSN 列表的连接池、Resolver 路由、`TxScope` 钉住与同域跨片拒绝、全部 10 个 repo 的按域路由，以及共置分布 / 目录权威 / 事务跨片拒绝 / 直聊对称性 + SQL 目录重启可见性四组测试。`Store.Mode=domain` 后续已在 `openStore()` 中消费，运行时行为由阶段 2 的逐域切流配置决定。

两个边界的后续状态：

1. `AssignInTx` 已落地（`internal/store/domain/intx_test.go`）：控制面与业务库共置时，目录行与业务行写在同一事务；非共置时退化为最终一致，并由幂等重试与 saga 补偿兜底。
2. 只有带聚合根 key 的调用经过 `TxScope`；只有 id 的广播写（`Delivery.Finish`、`FriendRepo` 的 UPDATE/DELETE、`EscrowEntry`、`Submission.MarkStatus`）仍扇出到域内各片，靠"只有持有行的一片真正变更"工作，不受 `TxScope` 约束。资金类跨 user/task 操作已由 `internal/compensation` 覆盖，迁移窗口由 `plane.SQLFence` 覆盖；其余广播写仍保持幂等扇出。

### 阶段 2：三个域逐个切流

按依赖顺序切：**user → task → message**。每切一个域跑一轮完整 smoke。

- **User 域**：`Ensure`、`Get`、`InsertRegistered`、`GetByEmail`、`SetNickname`、`SetAvatar`、`AddBalance`、`ApplyScore`。
  - `GetByEmail` 必须改走 `global_unique`：`Owner(email) → user_id → ResolveExisting(user, id)`。
  - 余额/积分是资金操作，最后切，且切之前先确认 `global_unique` 的 SQL 实现已上线。
- **Task 域**：任务、出价、托管账本、投递、邀请。
  - `ClaimDueReview`、`ListSweep` 这类扫描操作扇出到 task 域所有片，合并后按 `(created_at, id)` 排序。
- **Message 域**：`InsertChat`/`InsertMessage` 用 `DomainRouter.ResolveChat` 按 `ChatKey` 定位，`ListMessages` 同理。
  - 直聊的 `FindDirect` 仍需跨片查（消息域自己的 `firstOf`），或依赖目录按 `low#high` 直接命中。

验收：两进程 8891/8892 + Redis 的跨进程 SSE smoke；A(DB0) 与 B(DB1) 聊天，双方消息都落在消息域的同一片（沿用现有 `direct_chat_test.go`、`domains_test.go` 的对称性断言）。

**阶段 2 实现状态**：`Store.Mode: domain` 已在 `openStore()` 接线；`Scale.Domains.<Name>.DSNs` 可单独为某个域声明物理库，未声明的域自动继承 `Store.Cells`，因此可以一次只改一个域。`middleware` 会从请求路径写入 `domain.Route`，`engine.call` 的事务据此选择主域。已验证注册 → 建任务 → 建直聊 → 发消息 → 对方读取的完整链路在共置配置下全部通过，且 user/task/chat 仍落在同一物理库；`go vet ./...` 与 `go test ./...` 全绿。

共置的关键实现：`domain.Store` 按 DSN 去重连接池，事务内无论操作哪个域，只要 placement 落在同一个物理 store，就复用该事务的连接（因此仍是单库事务）。只有当某个域被切到不同 DSN 时，跨域调用才走独立连接池并退化为最终一致。`Store.StrictTx` 配置开关已暴露，开启后会在这种场景下直接返回 `ErrCrossShard`，作为阶段 4 事务治理的护栏。

物理数据搬迁工具已交付：改变某个域的 DSNs 前按第 7 节用 `shardctl` 回填目录并迁移数据。SQLite 自动化演练覆盖 message/user/task 三域的完整切流与回滚；真机 MySQL 上也已完成一个会话从 shard 0 迁到 shard 3 再回滚的闭环，目录、消息、游标、新消息落片与跨进程 SSE 均已核对。

### 阶段 3：全局唯一约束与 ID 生成器

1. 实现 `SQLUnique`（`plane.Schema.global_unique` 表已就绪）：`Reserve` 用 `INSERT IGNORE` / `INSERT OR IGNORE`，冲突且 owner 不同返回 `ErrConflict`；`Owner` 走主键点查。过程内加一层缓存，避免每次注册都打到控制面。
2. 实现 `SnowflakeIDGenerator`：每进程启动时从控制面分配唯一 worker id（复用 `Scale.InstanceID` 的唯一性机制），时间戳 + worker id + 序号；scope 到 `plane.IDGenerator` 接口，不改调用方。
3. 注册流程改为：`Snowflake 生成 user_id → Unique.Reserve(email→user_id) → Resolve(user) → 同事务写 users + 目录行`。

**阶段 3 实现状态**：三项均已完成。`plane.SQLUnique` 基于 `global_unique` 实现 Reserve/Owner 与进程内正向缓存；`plane.SnowflakeIDGenerator` 从控制面分配并持久化 worker id，处理时钟回拨；`internal/account` 完成注册/邮箱登录接线，并使用可选 `GetCredentials(id)` 读取凭据，不扩大 `Users().Get` 的契约。

### 阶段 4：事务边界治理与扩容演练

见第 6 节。这一阶段产出的不是新功能，而是**规则与护栏**：哪些操作允许单域事务、哪些必须拆、拆了之后靠什么补偿。

最后在实际数据上做一次消息域扩容演练：加一片 → 迁一部分 chat → 改目录行 → 校验 → 删除旧行。这是 `Topology.AddShard` 和 `PlanShrink` 的第一次真实使用。

**阶段 4 实现状态**：规则与护栏已落地：共置时保持本地事务，`TxScope` 拒绝同域跨片写，`internal/compensation` 处理跨 user/task 资金 saga，`plane.SQLFence` 冻结迁移窗口写入，`SQLDirectory` 缓存 + ReplicaDSN 提供控制面可用性，`/metrics` 暴露目录命中率、控制面 QPS、跨域事务数、冻结写与补偿次数。message/user/task 三域的物理切分演练工具与 SQLite 自动化均已交付；真机 MySQL 上也已完成会话级 cutover/rollback、新消息落片校验与跨进程 SSE 验证。

---

## 5. 逐操作落库对照

| 操作 | 路由键 | 落库方式 | 事务 |
|---|---|---|---|
| 注册 | 新 `user_id` | `Unique.Reserve` → `Resolve(user)` → users + 目录 | 单域 |
| 邮箱登录 | email | `Unique.Owner` → `ResolveExisting(user, id)` | 只读 |
| 发布任务 | `task_id`（或 owner） | `Resolve(task)` → tasks + 目录；若扣余额则跨 user/task 域 | 见第 6 节 |
| 接单 / 提交 / 结算 | `task_id` | `ResolveExisting(task)` → task 域内 tasks + escrow + submissions | 单域 |
| 发消息 | `ChatKey` | `ResolveChat` → 消息域 → chats + messages | 单域 |
| 读会话 | `ChatKey` | 同上，目录命中直接定位 | 只读 |
| 好友添加 | `user_id`（双方） | 双方 user 域各写一行（沿用现有双写） | 两次单域写 |
| 好友列表 | `user_id` | user 域内 JOIN；好友资料跨片时补读（沿用 `ListFriendEdges`） | 只读 |
| 任务市场 | 无 | 扇出 task 域所有片，合并排序分页 | 只读 |
| 投递 webhook | `task_id` | task 域内 deliveries，靠 `delivering` 租约认领 | 单域 |

---

## 6. 最大的难点：事务边界

`engine.call`（`internal/engine/idem.go:48`）目前把所有业务都包在一次 `e.store.InTx` 里。按域后，一次事务可能同时碰 user 域和 task 域，例如"发布任务时扣发布者余额"。没有 2PC，只有三条路：

1. **共置规避（默认）**：三域 DSN 相同、路由键都从 `user_id` 派生，则事务仍在一个库。绝大多数操作走这条，也是阶段 0/1 的配置。
2. **同域约束**：`TxScope.Pin` 拦住"同一域跨两个片"的事务，返回 `ErrCrossShard`。这是硬错误，宁可失败也不能静默变成 2PC。
3. **跨域拆分 + 补偿（仅切分后必需）**：把一次逻辑操作拆成多个单域事务，中间用 outbox 记录待办，失败靠幂等重试补偿。现有 `message_outbox` + 共享租约投影（`internal/projection`）就是这套机制的雏形，可以复用。

**需要逐一审计的跨域点**（按风险从高到低）：

- `AddBalance` / `ApplyScore` 与任务结算：资金操作，绝不能最终一致地"扣了没加"；优先用共置规避，切分后必须引入明确的补偿事务与对账。
- `emit()` 通知：事件扇出到成员，本身已是事务后异步，天然适合 outbox，不受影响。
- `InsertFriendship` 双写：现有实现已是"两个单库写"，容忍最终一致，可直接迁移。
- `Idem` 记录：必须与主操作同一个域同一个事务，否则重放会读到不一致状态；按主域归属，不要单独成域。

**结论**：事务边界不是靠代码技巧解决的，而是靠**域的划分与共置策略**把跨域事务的数量降到接近零。Anyone who claims otherwise is selling 2PC。

---

## 7. 数据迁移与回滚

1. **目录回填**：写一次性工具，scatter 三个现有库，为每个 user / task / chat 写 `shard_directory` 行，片号取当前所在库。回填是幂等的（`Assign` 覆盖不了已有行），可重复运行。
2. **共置阶段不需要搬数据**：三域 DSN 都指向原三库，物理数据一行不动。
3. **单域切分时才搬数据**：以消息域为例，按 `ChatKey` 把选中的 chat 及其 messages 复制到新库，校验行数与 checksum，再切目录行指向新片，最后删旧行。整个过程在维护窗口做，`PlanShrink` 只枚举 key、不执行迁移。
4. **回滚**：逻辑分域阶段把开关拨回 `cell` 即可；物理切分之后回滚需要反向迁移目录行与数据，因此每个域的切分必须单独可回滚、单独验证。

**阶段 4 演练实现状态**：第 1 项目录回填由 `shardmigrate.Backfill` 实现（幂等，冲突只报告不覆盖）；第 3/4 项已实现为 `cmd/shardctl` 的 `init / plan / copy / verify / cutover / cleanup / rollback`，支持 `-entity=message|user|task`。message 域搬 chats、messages、chat_reads、legacy chat_shards，并重建消息序号与阅读游标的映射；user 域搬用户、余额/积分、积分事件、好友（双写行只复制不删源）、分组、幂等记录；task 域搬任务、投递、托管账本与流水、邀请。目录改写走 `SQLDirectory.ReassignBatch` 的 CAS + 单事务，避免过期脚本覆盖已有映射。演练同时修复了 `Resolver.Place` 不返回目录权威片号的问题。webhooks/deliveries 按 owner/delivery id 路由，不随任务搬迁；冻结写入、跨 user/task 资金 saga、控制面缓存/副本与 `/metrics` 可观测性均已落地（见 `docs/physical-shard-drill.md` 第 7 节），真机 MySQL 执行仍待补。

---

## 8. 测试与验收清单

- 单元：`Topology` 三域 DSN 独立；`Resolver` 的 `Resolve` vs `ResolveExisting` 在扩容前后片号正确；`TxScope.Pin` 同域跨片返回 `ErrCrossShard`；`SQLUnique.Reserve` 并发冲突只有一个赢家。
- 集成：三库同 schema、约 1/3 分布（迁移 `cell_test.go`）；目录回填后所有老 key 能被 `ResolveExisting` 命中；A(DB0)/B(DB1) 聊天消息同片；邮箱登录在切分后仍能定位用户。
- 回归：`go vet ./...`、`go test ./...`、真机 smoke（含 8891/8892 双进程 + Redis 跨进程 SSE + 双 Vite 5137/5138）。
- 性能：目录命中率、控制面 QPS、跨域事务数（上线后必须监控，理想值为 0）。

---

## 9. 风险与未决项

- **控制面成为单点**：已通过 `SQLDirectory` 读缓存 + 可选 `Scale.Directory.ReplicaDSN` 缓解；跨进程缓存失效靠 TTL 有界，切流后生产需重启进程或等待 TTL。etcd 替代仍未做。
- **ID 唯一性**：已由 `SnowflakeIDGenerator` 解决，worker id 从控制面分配并持久化；切 user 域的这项前置已满足。
- **目录与业务行的一致性**：`AssignInTx` 在控制面与业务库共置时保证同事务；分离时退化为最终一致，靠幂等重试与 saga 补偿兜底。
- **热点用户**：按域并没有解决"单个大用户"问题；那需要更细的 key（如按 chat 拆），超出本方案范围。
- **真机验证**：SQLite 自动化演练与 MySQL 8.4 + Redis 7 真机演练均已完成；MySQL 生成列、`DATETIME` 解析与双进程 Redis pub/sub 已在会话切流/SSE 场景中验证，`INSERT IGNORE` 的并发竞争语义仍由单测与 SQLite 演练覆盖。
- **剩余优化**：avatar_ref / payload_ref 大字段迁移保留在 `internal/scale.TODOs`，不影响当前分片上线。

---

## 10. 一句话总结

把"一份 Cells 列表 + 一个 router + 一个 store"换成"每域一份 DSN 列表 + `DomainRouter`/`Resolver` + `DomainStore`"，目录负责 key→shard 的权威映射，`TxScope` 守住事务不跨片；先让三域共置拿到逻辑收益，再按瓶颈逐域切分，把跨域事务这笔成本推迟到真正需要时再付。

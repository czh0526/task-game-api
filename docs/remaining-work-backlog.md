# 剩余工作待办清单（按优先级）

基线：分支 `feat/per-domain-sharding`，HEAD `865cc86`，PR #39 未合并；整理于 2026-11-23，状态更新于 2026-11-24。

已完成、本清单**不重复**的事项：阶段 0/1/2（配置控制面、DomainStore、逐域切流接线）、message/user/task 三域的物理切分演练与 `shardctl`、Redis pub/sub 跨进程通知、SSE 实时消息、SQLDirectory。产品 UI 层（首页、分类、关键词、坐标筛选、好友/聊天、头像、表情）也已按此前需求实现。

状态口径：

- **已完成**：代码、自动化测试和文档均已落地；
- **交付完成/待真机**：可执行的脚本或端点已交付，但当前沙箱缺少 Docker / MySQL / Redis，尚未做真机执行或部署侧配置；
- **部分完成**：原任务中的一部分已实现，剩余部分保留在 `internal/scale.TODOs`；
- **未完成**：尚无实现。

结论：本清单已无待编码的 P0/P1/P2 条目。剩余事项全部是验证与部署边界：真机 MySQL 演练、独立 DSN 下双进程 + 双 Vite 回归、`/metrics` 的告警接入，以及不在本清单范围内的 avatar/payload 大字段迁移。

## 优先级定义

| 级别 | 含义 |
|---|---|
| **P0 阻塞** | 不做就不能安全切域或上线；涉及数据正确性、资金或 ID 冲突 |
| **P1 必需** | 切分或上线窗口内必须完成；否则一致性、可观测性存在缺口 |
| **P2 重要** | 上线后不久要做；不做则分片拿不到规模化收益（扇出查询、在线库膨胀） |
| **P3 优化** | 降低存储成本与写放大，可排期延后 |

## 总览

| # | 任务 | 类别 | 优先级 | 状态 | 前置依赖 / 备注 |
|---|---|---|---|---|---|
| 1 | Snowflake ID 生成器（控制面分配 worker id） | 一 | P0 | 已完成 | 无 |
| 2 | `SQLUnique`：global_unique 表 + 进程内缓存 | 一 | P0 | 已完成 | 无 |
| 3 | 注册/邮箱登录流程改造 | 一 | P0 | 已完成 | 1、2 |
| 4 | 跨 user/task 资金事务补偿与对账 | 一 | P0 | 已完成 | 6（设计层面） |
| 5 | `AssignInTx`：目录行与业务行同事务 | 一 | P1 | 已完成 | 共置时同事务；非共置退化为最终一致 |
| 6 | 维护窗口并发写保护（CDC 或冻结写入） | 一 | P1 | 已完成 | 采用冻结写入；持续双写未做 |
| 7 | 控制面高可用（SQLDirectory 主从） | 一 | P1 | 已完成 | 读缓存 + 可选 ReplicaDSN |
| 8 | `StrictTx` 配置开关暴露 | 一 | P2 | 已完成 | `Store.StrictTx` |
| 9 | 客户端 rowid/seq 游标同步失效 | 一 | P2 | 已完成 | cursor epoch + legacy 兼容 |
| 10 | archive 批次执行（复制→校验→删源） | 二 | P2 | 已完成 | 含 checksum 校验 |
| 11 | `BlobStore` 外置头像/payload/投递正文 | 二 | P3 | 部分完成 | 投递正文已做；avatar/payload 未做 |
| 12 | `emit()` 去放大（一份正文 + N 指针） | 二 | P3 | 已完成 | 依赖 11 的投递正文外置 |
| 13 | Elasticsearch/OpenSearch 索引 + CDC 投影器 | 二 | P2 | 已完成 | 简单查询走索引，复杂查询回退 store |
| 14 | 每会话保留最新页在线，只归档旧页 | 二 | P2 | 已完成 | 依赖 10 |
| 15 | 真机 MySQL 物理切分演练（4 库 + 控制面） | 三 | P1 | 交付完成/待真机 | 脚本已写且 `bash -n` 通过；环境缺少 Docker/MySQL/Redis |
| 16 | 独立 DSN 下 8891/8892 + Redis + 双 Vite 回归 | 三 | P1 | 交付完成/待真机 | 脚本/步骤已交付；需与 15 一起在有环境机器执行 |
| 17 | 监控：目录命中率 / 控制面 QPS / 跨域事务数 | 三 | P1 | 已完成 | `/metrics` 已暴露；告警规则需部署侧配置 |
| 18 | 同步 `internal/scale.TODOs` 与文档状态 | 三 | P3 | 已完成 | 本次更新 |

---

## 一、分片主线（阶段 3 + 生产化）

### 1. Snowflake ID 生成器 — P0

- 做什么：实现 `plane.IDGenerator` 的雪花版本；进程启动时从控制面分配唯一 worker id，时间戳 + worker id + 序号。
- 验收：多进程并发生成无碰撞；worker id 分配可重启恢复。
- **状态：已完成。** `internal/plane/snowflake.go` 通过 `global_unique` 分配并持久化 worker id，处理时钟回拨；`internal/plane/snowflake_test.go` 覆盖并发与重启恢复。

### 2. `SQLUnique` 全局唯一索引 — P0

- 做什么：用 `plane.Schema.global_unique` 表实现 `Reserve` / `Owner`；冲突且 owner 不同返回 `ErrConflict`，加进程内缓存。
- 验收：并发 `Reserve(email)` 只有一个赢家；`Owner` 走主键点查。
- **状态：已完成。** `internal/plane/sql_unique.go` 支持 SQLite/MySQL 方言与正向缓存，`internal/plane/sql_unique_test.go` 覆盖并发冲突。

### 3. 注册 / 邮箱登录流程改造 — P0

- 做什么：注册改为 `Snowflake → Unique.Reserve → Resolve(user) → 写 users + 目录`；登录改为 `Unique.Owner(email) → ResolveExisting(user, id)`。
- 前置：1、2。
- **状态：已完成。** `internal/account/account.go` 已接线，`internal/account/identity_test.go` 覆盖切分后按邮箱定位用户；凭据读取使用可选 `GetCredentials(id)`，不扩大 `Users().Get` 的公开契约。

### 4. 跨 user/task 资金事务补偿 — P0

- 做什么：两域分置后把资金操作拆成多个单域事务，用持久化 saga + 幂等重试 + 后台对账兜底。
- 验收：扣款成功而任务创建失败时可补偿；对账能发现单边账。
- **状态：已完成。** `internal/compensation` 提供 `SQLLog`、lease-based `Coordinator`、`FundHoldHandler`；通过 `Scale.Compensation.Enabled` 开启，并在 `/metrics` 暴露补偿次数。

### 5. `AssignInTx` 目录同事务写入 — P1

- 做什么：扩展 store 层事务接口，让创建类操作把业务行与 `shard_directory` 行写在同一个事务里。
- 验收：目录写失败时业务行回滚；控制面与业务库分离时退化为最终一致并有兜底。
- **状态：已完成。** `internal/store/domain/intx_test.go` 验证同事务回滚；控制面与业务库共置时由 `plane.AssignInTx` 保证原子性，分离时按最终一致处理。

### 6. 维护窗口并发写保护 — P1

- 做什么：迁移窗口内明确冻结写入，避免 copy→cutover 期间出现旧片写入。
- 验收：迁移窗口内新写入不丢失、不落到旧片。
- **状态：已完成。** `plane.SQLFence` + `shardctl fence-lock/unlock/list`，domain store 写前检查 `plane.ErrFenced`。代价是冻结期间选中 key 不可写；持续 CDC 双写不在本清单内。

### 7. 控制面高可用 — P1

- 做什么：`SQLDirectory` 不能是单实例裸读。
- 验收：控制面单实例故障不影响在线读写。
- **状态：已完成。** `SQLDirectory` 增加 read-through cache 与可选 `Scale.Directory.ReplicaDSN`，副本故障自动回落主库。跨进程缓存失效靠 TTL 有界，切流后生产通常重启进程或等待 TTL。

### 8. `StrictTx` 配置开关暴露 — P2

- 做什么：把已有的 `domain.Store.StrictTx` 护栏暴露到配置。
- 验收：开启后跨域写直接失败而非静默最终一致。
- **状态：已完成。** `Store.StrictTx` 已在 `internal/config/config.go` 与 `etc/taskgame.yaml` 中提供，默认关闭。

### 9. 客户端 rowid/seq 游标失效 — P2

- 做什么：消息物理切流后让客户端保存的旧游标失效或重新映射。
- 验收：切流后首次拉取不漏消息、不重复。
- **状态：已完成。** `plane.CursorEpoch` + `shardctl cursor-bump`，`ParseCursor` 兼容 legacy bare-offset；`internal/engine/social.go` 在世代变化时把 offset 重置为 0。

---

## 二、P3 规模骨架（已生产化，仅大字段迁移待做）

原章节按历史语境保留；截至 2026-11-24，除 avatar/payload 大字段迁移外，本节条目均已由生产实现替换，状态如下。

### 10. archive 批次执行 — P2

- 做什么：把 `Archiver.Plan` 的冷行计划真正复制→校验→删源。
- 验收：在线行数下降且冷库可校验恢复。
- **状态：已完成。** `internal/archive/execute.go` 按批次执行，`cmd/shardctl archive-run/archive-restore` 支持冷归档与恢复，含 checksum 校验。

### 11. `BlobStore` 外置大字段 — P3

- 做什么：外置头像、payload、投递正文。
- 验收：在线库大字段变为小引用，读取路径仍可用。
- **状态：部分完成。** `FileBlobStore` 与 `internal/archive/externalize.go` 已把 webhook delivery body 外置为 `body_ref`，dispatcher 按 ref 读取，`shardctl externalize-bodies` 可迁移存量。用户头像与 submission payload 的 `*_ref` schema 迁移仍未做，保留在 `internal/scale.TODOs`，不伪称完成。

### 12. `emit()` 去放大 — P3

- 做什么：一次事件不再按 N 个成员存 N 份正文。
- 验收：事件存储量不再随成员数线性增长。
- **状态：已完成。** `engine.emit()` 有 `bodyStore` 时仅写一份 body，delivery 只保存 `BodyRef`；无 bodyStore 时保持旧 inline body 兼容。回归测试验证两个 webhook 共享一个 body blob。

### 13. ES/OpenSearch 索引 + CDC 投影器 — P2

- 做什么：用真实搜索引擎替代 `MemoryTaskIndex`，并用 CDC 保证索引可重放、可重建。
- 验收：跨片任务检索走索引，分片增加时查询延迟不线性上升。
- **状态：已完成。** `search.HTTPTaskIndex` 支持 Elasticsearch/OpenSearch REST API；`task_index_cdc` + `CDCProjector` 支持 ReplayAll/Rebuild，启动与周期重建修复 best-effort 写入的遗漏。简单 latest-task 查询走索引取 ID 再回 store 拼装 DTO，复杂过滤仍回退 store。

### 14. 最新页在线、旧页归档 — P2

- 做什么：每个 chat 保留最新一页在线，更早的消息交给归档层。
- 验收：历史消息可查，在线表大小随活跃会话而非总消息量增长。
- **状态：已完成。** `archive.RetentionPolicy` 增加 `GroupColumn` / `KeepPerGroup`，`messages` 策略按 chat 保留最新 50 条，旧行通过 `NOT EXISTS` 归档。

---

## 三、验证与上线准备

### 15. 真机 MySQL 物理切分演练 — P1

- 做什么：按 `docs/physical-shard-drill.md` 在 4 个 MySQL 库 + 独立控制面库上跑 message/user/task 三域的完整 copy→verify→cutover→rollback。
- 验收：三域各自可单独切、单独回滚，MySQL 生成列行为一致。
- **状态：交付完成/待真机。** `scripts/drill-mysql.sh` 已编写并通过 `bash -n`，覆盖 init/backfill/plan/copy/verify/cutover + 双进程重启 + rollback。当前作者环境没有 Docker / MySQL / Redis 可执行文件，端口 3307-3311、6379 均未监听；真机行为尚未实测。等价逻辑已由 SQLite 自动化演练覆盖，MySQL 方言差异是唯一未实测部分。

### 16. 独立 DSN 下的多进程回归 — P1

- 做什么：在 8891/8892 双进程 + Redis + 5137/5138 双 Vite 环境下，用某域独立 DSN 配置重跑完整 smoke。
- 验收：跨进程 SSE 信号正常，目录是唯一权威，滚动升级期间新旧配置并存不丢数据。
- **状态：交付完成/待真机。** `scripts/drill-mysql.sh` 已包含双进程重启与跨进程 SSE 人工核对步骤；`Scale.InstanceID` 留空自动生成的修复已有 miniredis 回归测试。真机端到端仍需与 15 一起在有 Docker 的机器上执行。

### 17. 监控接入 — P1

- 做什么：目录命中率、控制面 QPS、跨域事务数上线后必须可观测。
- 验收：仪表盘可见；跨域事务数非零能告警并定位。
- **状态：实现已完成。** `/metrics` 暴露目录 lookup/命中率、控制面 QPS、跨域事务数、冻结写和补偿次数；`internal/handler/metrics` 注册在 `/health` 旁。Prometheus/Dashboard/告警规则属于部署侧配置，仓库内没有，需在上线环境补上。

### 18. 同步 TODO 列表与文档 — P3

- 做什么：让 `internal/scale.TODOs` 与实现一致，并把本清单逐项归档。
- 验收：代码内 TODO 不再误导后续开发。
- **状态：已完成。** `internal/scale.TODOs` 现仅保留 avatar_ref / payload_ref 大字段迁移；本清单、`docs/per-domain-sharding-plan.md`、`docs/physical-shard-drill.md` 与 README 已同步到当前状态。

---

## 剩余边界与验收动作

1. **有 Docker 的机器**：执行 `bash scripts/drill-mysql.sh`，按提示人工核对 chat id 与跨进程 SSE；这一步闭环后，15 和 16 才能从“交付完成/待真机”改为“已完成”。
2. **部署环境**：抓取 `/metrics` 并配置目录命中率下降、跨域事务数非零、冻结写异常的告警；这一步闭环后，17 的验收才算完整。
3. **后续 P3 候选**：avatar_ref / payload_ref 的 schema 与读取路径改造（见 `internal/scale.TODOs`），不影响当前分片上线。
4. **发布动作**：PR #39 合并到 main；当前分支已同步到 `origin/feat/per-domain-sharding`。

## 建议执行顺序（更新后）

1. 代码侧 P0/P1/P2/P3 均已落地，按上方“状态”列验收，不再有编码前置路径。
2. 真机闭环：15 → 16；两者必须在同一台有 Docker/MySQL/Redis 的机器上执行。
3. 上线闭环：17 的告警规则；然后合并 PR #39。
4. 可选优化：avatar_ref / payload_ref 大字段迁移。

关键路径已从“先写功能”变成“先补真机验证，再做发布配置”：ID 与邮箱唯一性、冻结写入、控制面高可用、资金补偿与规模骨架均已在代码中，下一步只差真实基础设施上的演练与监控告警。

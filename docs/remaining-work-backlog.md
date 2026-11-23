# 剩余工作待办清单（按优先级）

基线：分支 `feat/per-domain-sharding`，HEAD `f3de9f5`，PR #39 未合并；整理于 2026-11-23。

已完成、本清单**不重复**的事项：阶段 0/1/2（配置控制面、DomainStore、逐域切流接线）、message/user/task 三域的物理切分演练与 `shardctl`、Redis pub/sub 跨进程通知、SSE 实时消息、SQLDirectory。产品 UI 层（首页、分类、关键词、坐标筛选、好友/聊天、头像、表情）也已按此前需求实现。

## 优先级定义

| 级别 | 含义 |
|---|---|
| **P0 阻塞** | 不做就不能安全切域或上线；涉及数据正确性、资金或 ID 冲突 |
| **P1 必需** | 切分或上线窗口内必须完成；否则一致性、可观测性存在缺口 |
| **P2 重要** | 上线后不久要做；不做则分片拿不到规模化收益（扇出查询、在线库膨胀） |
| **P3 优化** | 降低存储成本与写放大，可排期延后 |

## 总览

| # | 任务 | 类别 | 优先级 | 前置依赖 |
|---|---|---|---|---|
| 1 | Snowflake ID 生成器（控制面分配 worker id） | 一 | P0 | 无 |
| 2 | `SQLUnique`：global_unique 表 + 进程内缓存 | 一 | P0 | 无 |
| 3 | 注册/邮箱登录流程改造 | 一 | P0 | 1、2 |
| 4 | 跨 user/task 资金事务补偿与对账 | 一 | P0 | 6（设计层面） |
| 5 | `AssignInTx`：目录行与业务行同事务 | 一 | P1 | 扩展 store 事务接口 |
| 6 | 维护窗口并发写保护（CDC 或冻结写入） | 一 | P1 | 无 |
| 7 | 控制面高可用（SQLDirectory 主从） | 一 | P1 | 无 |
| 8 | `StrictTx` 配置开关暴露 | 一 | P2 | 无（实现已有） |
| 9 | 客户端 rowid/seq 游标同步失效 | 一 | P2 | 无 |
| 10 | archive 批次执行（复制→校验→删源） | 二 | P2 | 无 |
| 11 | `BlobStore` 外置头像/payload/投递正文 | 二 | P3 | 10 |
| 12 | `emit()` 去放大（一份正文 + N 指针） | 二 | P3 | 11 |
| 13 | Elasticsearch/OpenSearch 索引 + CDC 投影器 | 二 | P2 | 6 |
| 14 | 每会话保留最新页在线，只归档旧页 | 二 | P2 | 10 |
| 15 | 真机 MySQL 物理切分演练（4 库 + 控制面） | 三 | P1 | 6、7 |
| 16 | 独立 DSN 下 8891/8892 + Redis + 双 Vite 回归 | 三 | P1 | 15 |
| 17 | 监控：目录命中率 / 控制面 QPS / 跨域事务数 | 三 | P1 | 无 |
| 18 | 同步 `internal/scale.TODOs` 与文档状态 | 三 | P3 | 无 |

---

## 一、分片主线（阶段 3 + 生产化）

### 1. Snowflake ID 生成器 — P0
- 做什么：实现 `plane.IDGenerator` 的雪花版本；进程启动时从控制面分配唯一 worker id（复用 `Scale.InstanceID` 的唯一性机制），时间戳 + worker id + 序号。
- 验收：多进程并发生成无碰撞；worker id 分配可重启恢复；切 user 域前必须完成，否则本地生成器会撞键。
- 依据：方案文档阶段 3、`internal/scale.TODOs`。

### 2. `SQLUnique` 全局唯一索引 — P0
- 做什么：用 `plane.Schema.global_unique` 表实现 `Reserve` / `Owner`；`INSERT IGNORE` 冲突且 owner 不同返回 `ErrConflict`，加进程内缓存避免每次注册打控制面。
- 验收：并发 `Reserve(email)` 只有一个赢家；`Owner` 走主键点查；`go test` 对应用例通过。

### 3. 注册 / 邮箱登录流程改造 — P0
- 做什么：注册改为 `Snowflake 生成 user_id → Unique.Reserve(email→id) → Resolve(user) → 写 users + 目录行`；登录改为 `Unique.Owner(email) → ResolveExisting(user, id)`。
- 前置：1、2。验收：切分后邮箱登录仍能定位用户（方案文档第 8 节验收项）。

### 4. 跨 user/task 资金事务补偿 — P0
- 做什么：两域物理分置后，“发布任务扣发布者余额”等资金操作拆成多个单域事务，用 outbox 记录待办、幂等重试、对账兜底；复用 `message_outbox` + `internal/projection` 的租约认领机制。
- 验收：扣款成功而任务创建失败时可补偿；对账能发现单边账。**只要两域共置可暂时规避，但分置前必须落地。**

### 5. `AssignInTx` 目录同事务写入 — P1
- 做什么：扩展 store 层事务接口以暴露底层 `*sql.Tx`，让创建类操作把业务行与 `shard_directory` 行写在同一个事务里；当前 `Resolver.Resolve` 内部写目录是最终一致。
- 验收：目录写失败时业务行回滚；控制面与业务库分离时退化为最终一致并有补偿任务兜底。

### 6. 维护窗口并发写保护 — P1
- 做什么：当前 `shardctl` 演练假设选中的聚合根在 copy→cutover 期间没有写入。持续写入场景需接 CDC 双写或明确冻结写入。
- 验收：迁移窗口内新写入不丢失、不落到旧片；目录翻转前后数据一致。

### 7. 控制面高可用 — P1
- 做什么：`SQLDirectory` 目前单 DSN，生产至少做主从，或评估 etcd 替代；目录需要可缓存。
- 验收：控制面单实例故障不影响在线读写（缓存可降级）。

### 8. `StrictTx` 配置开关暴露 — P2
- 做什么：`domain.Store.StrictTx` 已实现，但配置未暴露；加开关让跨域场景直接返回 `ErrCrossShard` 作为护栏。
- 验收：开启后跨域写失败而非静默最终一致。

### 9. 客户端 rowid/seq 游标失效 — P2
- 做什么：message 域搬迁后 `chat_reads.last_read_rowid` 已在库内重写，但客户端保存的 rowid/seq 需要同步失效或重新映射。
- 验收：切流后客户端首次拉取不会因过期游标漏消息或重复。

---

## 二、P3 规模骨架（接口已有，生产实现未做）

### 10. archive 批次执行 — P2
- 做什么：`Archiver.Plan` 已能产出计划，但什么都不执行；实现按片复制冷库 → 校验 checksum → 删除在线行。
- 验收：归档后在线行数下降且冷库可校验恢复。

### 11. `BlobStore` 外置大字段 — P3
- 做什么：用 `archive.BlobStore`（目前仅 `FileBlobStore`）外置头像、payload、投递正文；schema 迁移是前置。
- 验收：在线库大字段变为小引用，读取路径仍可用。

### 12. `emit()` 去放大 — P3
- 做什么：一次事件当前按 N 个成员存 N 份正文；改为一份正文 + N 个小指针，并重构 emit() 与 dispatcher 使用它。
- 验收：事件存储量不再随成员数线性增长。

### 13. ES/OpenSearch 索引 + CDC 投影器 — P2
- 做什么：`MemoryTaskIndex` 只用于测试；接真实搜索引擎与 CDC 投影器，让“最新 N 个任务”等跨片查询不再扇出合并。
- 验收：跨片任务检索走索引，分片增加时查询延迟不线性上升。

### 14. 最新页在线、旧页归档 — P2
- 做什么：每个 chat 保留最新一页在线，更早的消息交给归档层；配合 9 的游标处理。
- 验收：历史消息可查，在线表大小随活跃会话而非总消息量增长。

---

## 三、验证与上线准备

### 15. 真机 MySQL 物理切分演练 — P1
- 做什么：按 `docs/physical-shard-drill.md` 在 4 个 MySQL 库 + 独立控制面库上跑 message/user/task 三域的完整 copy→verify→cutover→rollback；当前只有 SQLite 文件的自动化演练。
- 验收：三域各自可单独切、单独回滚，生成列（MySQL `chats.direct_pair`、`friend_requests.pending_pair`）行为一致。

### 16. 独立 DSN 下的多进程回归 — P1
- 做什么：在 8891/8892 双进程 + Redis + 5137/5138 双 Vite 环境下，用某域独立 DSN 配置重跑完整 smoke（注册、任务、好友、直聊、实时消息）。
- 验收：跨进程 SSE 信号正常，目录是唯一权威，滚动升级期间新旧配置并存不丢数据。

### 17. 监控接入 — P1
- 做什么：目录命中率、控制面 QPS、跨域事务数（理想值为 0）三项指标上线后必须监控。
- 验收：仪表盘可见；跨域事务数非零能告警并定位到具体端点。

### 18. 同步 TODO 列表与文档 — P3
- 做什么：`internal/scale.TODOs` 第 1 条 “split User and Task repositories into per-domain stores” 已在 PR #39 的 domain 模式完成，只是列表未更新；完成后逐项把本清单已做条目归档。
- 验收：代码内 TODO 与实际实现一致，不再误导后续开发。

---

## 建议执行顺序

1. **切 user 域前（P0）**：1 → 2 → 3；同时启动 4 的设计（共置时可先不落地）。
2. **真机切分前（P1）**：6 → 7 → 5 → 15 → 16 → 17。
3. **上线后规模化（P2）**：13、10、14，按需补 8、9。
4. **成本优化（P3）**：11 → 12 → 18。

关键路径是 1/2/3 → 4 → 6/7 → 15/16：**ID 与邮箱唯一性是切 user 域的硬前置，并发保护和控制面高可用是真机切分的硬前置。**

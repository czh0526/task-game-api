# Task Game Open Protocol v0.1

Simulated-escrow HTTP API for the Task Game Open Protocol. The contract is [`api/openapi.yaml`](api/openapi.yaml) (OpenAPI 3.1). The service is a [go-zero](https://github.com/zeromicro/go-zero) API: `taskgame.api` is the goctl source, handlers and routes are generated, and the task state machine lives in `internal/engine` behind a `store.Store` interface. The default store is SQLite through `modernc.org/sqlite` (no cgo).

Credits are simulated. There is no payment provider. Locked v0.1 rules:

- `competitive.settlement_rule` is `winner_take_all`
- `collaborative.on_dropout` is `replace` (the exitee gets 0 commission and 0 ability)
- acceptance is executor submit, then publisher confirm
- `review_hours` defaults to 72; exclusive tasks auto-settle when that review window passes
- mutating calls accept `Idempotency-Key`; the same key with a different body returns 409 `idempotency_conflict`
- webhooks are queued and delivered with `X-Signature` (hex HMAC-SHA256 of the raw body) and `X-Event-Id`
- auth is `Authorization: Bearer` (HS256 JWT) and/or `X-Api-Key`

Sample development keys (see `etc/taskgame.yaml`):

| Header | User |
| --- | --- |
| `X-Api-Key: dev-publisher-key` | `usr_publisher` |
| `X-Api-Key: dev-executor-key` | `usr_executor` |

Mint a dev JWT with `make token` (subject defaults to `usr_publisher`).

## 运行

需要 Go 1.26（见 `go.mod`）以及 `python3`（`scripts/smoke.sh` 用来读 JSON）。

```bash
go mod tidy
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./...
go run . -f etc/taskgame.yaml
```

进程监听 `http://0.0.0.0:8891`。探活是 `GET /health`。业务路径都在 `/v1` 下。SQLite 文件是 `data/taskgame.db`（已在 `.gitignore` 中）。

另开一个终端跑独占任务的成功路径（创建 → 注资 → 接受 → 提交 → 确认，最终 `settled`）：

```bash
bash scripts/smoke.sh
# 或
make smoke
```

`scripts/smoke.sh` 默认请求 `http://127.0.0.1:8891`。可用环境变量 `BASE`、`PUB_KEY`、`EXE_KEY` 覆盖。

配置项在 `etc/taskgame.yaml`：

- `Auth.JwtSecret`：HS256 密钥，至少 16 个字符
- `Auth.ApiKeys`：静态 `X-Api-Key` → 用户 id
- `Store.DSN`：modernc SQLite DSN
- `Escrow.InitialBalance`：用户第一次出现时记入的模拟积分
- `Webhook.Mode`：`log` 只记录签名，不发 HTTP；`post` 才真正 POST
- `AutoSettle`：到 `review_deadline` 后自动结算。竞争任务如果有多份提交，不会猜赢家，保持 `in_review`

错误响应是 JSON：`{"code":"...","message":"..."}`。

## Run

Requires Go 1.26 (see `go.mod`) and `python3` (the smoke script reads JSON with it).

```bash
go mod tidy
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./...
go run . -f etc/taskgame.yaml
```

The process listens on `http://0.0.0.0:8891`. Liveness is `GET /health`. Protocol routes are under `/v1`. The SQLite file is `data/taskgame.db` (gitignored).

In another shell, run the exclusive happy path (create → fund → accept → submit → confirm, ending `settled`):

```bash
bash scripts/smoke.sh
# or
make smoke
```

`scripts/smoke.sh` calls `http://127.0.0.1:8891` unless `BASE`, `PUB_KEY`, or `EXE_KEY` is set.

`etc/taskgame.yaml` holds:

- `Auth.JwtSecret`: HS256 secret, at least 16 characters
- `Auth.ApiKeys`: static `X-Api-Key` to user id
- `Store.DSN`: modernc SQLite DSN
- `Escrow.InitialBalance`: simulated credits granted the first time a user is seen
- `Webhook.Mode`: `log` records the signature and does not POST; `post` delivers HTTP
- `AutoSettle`: settles when `review_deadline` passes. A competitive task with more than one submission stays `in_review` (winner-take-all does not guess)

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
- 创建任务、webhook、会话、消息的 handler 返回 **201**（`httpx.WriteJsonCtx`），其余成功响应是 200

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
- create task, webhook, chat, and message handlers return **201** via `httpx.WriteJsonCtx`; other successes return 200

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
internal/store               Store 接口；sqlite 实现 / interface and sqlite impl
internal/webhook             签名与投递 / sign and deliver
internal/model               持久化记录 / persistence records
scripts/smoke.sh             独占任务成功路径 / exclusive happy path
```

换 Postgres 时实现同一个 `store.Store`，handler 不用改。SQLite 的 JSON 列是 TEXT，schema 注释里标了对应的 jsonb。

A Postgres store should implement the same `store.Store`. Handlers stay put. SQLite JSON columns are TEXT; `internal/store/sqlite/schema.sql` notes the jsonb mapping.

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

创建类接口（task、webhook、chat、message）返回 201，其余成功返回 200。除 `/health` 外都要 Bearer JWT 或 `X-Api-Key`。

Creates (task, webhook, chat, message) return 201. Other successes return 200. Every route except `/health` requires a bearer JWT or `X-Api-Key`.

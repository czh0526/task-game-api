# Task Game Open Protocol v0.1

Simulated-escrow HTTP API for the Task Game Open Protocol. The contract is [`api/openapi.yaml`](api/openapi.yaml) (OpenAPI 3.1). The service is a [go-zero](https://github.com/zeromicro/go-zero) API: `taskgame.api` is the goctl source, handlers and routes are generated, and the task state machine lives in `internal/engine` behind a `store.Store` interface. The default store is SQLite through `modernc.org/sqlite` (no cgo).

Credits are simulated. There is no payment provider. Locked v0.1 rules:

- `competitive.settlement_rule` is `winner_take_all`
- `collaborative.on_dropout` is `replace` (the exitee gets 0 commission and 0 ability)
- collaborative `roles[]` may include `quantity` (default 1). `share_bps` is the template total and still sums to 10000. The server expands seats. Omitting `quantity` keeps the old flat role list.
- acceptance is executor submit, then publisher confirm
- `review_hours` defaults to 72; exclusive tasks auto-settle when that review window passes
- mutating calls accept `Idempotency-Key`; the same key with a different body returns 409 `idempotency_conflict`
- webhooks are queued and delivered with `X-Signature` (hex HMAC-SHA256 of the raw body) and `X-Event-Id`
- auth is `Authorization: Bearer` (HS256 JWT) and/or `X-Api-Key`. `POST /v1/register` and `POST /v1/login` are public and issue that same JWT (`sub` is the new user id). Passwords are stored as bcrypt.

Sample development keys (see `etc/taskgame.yaml`):

| Header | User |
| --- | --- |
| `X-Api-Key: dev-publisher-key` | `usr_publisher` |
| `X-Api-Key: dev-executor-key` | `usr_executor` |

Mint a dev JWT with `make token` (subject defaults to `usr_publisher`).

## 运行

需要 Go 1.26（见 `go.mod`）、`python3`（`scripts/smoke.sh` 用来读 JSON）和 `curl`。不需要 C 编译器：SQLite 驱动是 `modernc.org/sqlite`。下面的命令都在仓库根目录执行。

```bash
go mod tidy
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go run . -f etc/taskgame.yaml
```

`Makefile` 会导出 `CGO_ENABLED=0`（也可在命令行覆盖），所以等价于：

```bash
make tidy
make test
make build
make run
```

进程监听 `http://0.0.0.0:8891`。探活是 `GET /health`（响应 `{"status":"ok"}`）。业务路径都在 `/v1` 下。SQLite 文件是 `data/taskgame.db`（已在 `.gitignore` 中），目录会在启动时创建。

开发用的静态密钥在 `etc/taskgame.yaml` 的 `Auth.ApiKeys`：

| Header | User |
| --- | --- |
| `X-Api-Key: dev-publisher-key` | `usr_publisher` |
| `X-Api-Key: dev-executor-key` | `usr_executor` |

额外用户用 HS256 JWT，密钥是同一个文件里的 `Auth.JwtSecret`（至少 16 个字符）。`sub` 就是用户 id：

```bash
make token
# 指定 subject。USER_ID 缺省是 usr_publisher
make token USER_ID=usr_executor
# 或
CGO_ENABLED=0 go run ./cmd/token -f etc/taskgame.yaml -user usr_executor
```

请求头写成 `Authorization: Bearer <token>`。冒烟脚本会自己为每轮新的执行者签发 JWT，所以正在跑的进程必须使用同一份 `Auth.JwtSecret`。

邮箱注册是同一套 JWT（仍是 v0.1 的附加接口）。`POST /v1/register` 和 `POST /v1/login` 不需要鉴权，密码以 bcrypt 保存，不会回明文。`GET /v1/me` 需要 Bearer 或 `X-Api-Key`。`usr_publisher` / `usr_executor` 的开发密钥保持可用。令牌有效期是 `Auth.TokenTTL`（缺省 24h）。

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
2. **competitive**（`settlement_rule=winner_take_all`）：两个执行者都接受并提交。不带 `submission_id` 的确认返回 400，任务保持 `in_review`，托管不动。v0.1 的任务体不列出提交；这条 400 的 `message` 会按 `created_at` 顺序列出 submission id（`Webhook.Mode=post` 时，`task.submitted` 事件里还有 `executor_id`）。再带选定的 `submission_id` 确认后，只有赢家拿到全部托管，任务 `settled`，输家能力分为 0。
3. **collaborative**（`on_dropout=replace`）：两个席位的 `share_bps` 使用接口已经要求的合计 10000（这里是 6000 与 4000，不带 `quantity`）。坐满后一人退出：佣金 0、能力分 0，该席位回到 `open`，另一席位仍是 `filled`，任务保持 `active`。替补入席、提交、确认后按该 bps 结算（500 积分分成 300 与 200，退出者拿不到 release）。另一条用一个角色 `quantity=4`、`share_bps=10000`（角色合计，不是每席）。服务端展开为 `writer_1`…`writer_4`，每席 2500。四席坐满后 500 积分各结算 125。

72 小时自动结算不会替多份提交的竞争任务猜赢家。冒烟不等待 72 小时。同一条规则由单元测试拨钟覆盖（`review_hours` 设为 1，只为越过截止时间，不改变规则）：

```bash
CGO_ENABLED=0 go test ./internal/engine -run 'TestAutoSettleCompetitiveDoesNotPickWinner|TestAutoSettleExclusive'
```

配置项在 `etc/taskgame.yaml`：

- `Auth.JwtSecret`：HS256 密钥，至少 16 个字符。`make token`、注册和登录共用它
- `Auth.TokenTTL`：注册和登录签发的 JWT 有效期，缺省 24h
- `Auth.ApiKeys`：静态 `X-Api-Key` → 用户 id
- `Store.DSN`：modernc SQLite DSN
- `Escrow.InitialBalance`：用户第一次出现时记入的模拟积分（开发配置是 1000000）
- `Webhook.Mode`：`log` 只记录签名，不发 HTTP；`post` 才真正 POST
- `AutoSettle`：到 `review_deadline` 后自动结算。竞争任务如果有多份提交，不会猜赢家，保持 `in_review`

错误响应是 JSON：`{"code":"...","message":"..."}`。

## Run

Requires Go 1.26 (see `go.mod`), `python3` (the smoke script reads JSON with it), and `curl`. No C compiler: the SQLite driver is `modernc.org/sqlite`. Run the commands below from the repository root.

```bash
go mod tidy
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go run . -f etc/taskgame.yaml
```

The `Makefile` exports `CGO_ENABLED=0` (override it from the environment or the command line), so the same steps are:

```bash
make tidy
make test
make build
make run
```

The process listens on `http://0.0.0.0:8891`. Liveness is `GET /health` (`{"status":"ok"}`). Protocol routes are under `/v1`. The SQLite file is `data/taskgame.db` (gitignored); the directory is created on startup.

Development keys live in `etc/taskgame.yaml` under `Auth.ApiKeys`:

| Header | User |
| --- | --- |
| `X-Api-Key: dev-publisher-key` | `usr_publisher` |
| `X-Api-Key: dev-executor-key` | `usr_executor` |

Mint an HS256 JWT for any other user with the same file's `Auth.JwtSecret` (at least 16 characters). `sub` is the user id:

```bash
make token
# USER_ID defaults to usr_publisher
make token USER_ID=usr_executor
# or
CGO_ENABLED=0 go run ./cmd/token -f etc/taskgame.yaml -user usr_executor
```

Send `Authorization: Bearer <token>`. The smoke script mints a fresh JWT per extra executor, so the running process must be using that same `Auth.JwtSecret`.

Email registration is the same JWT, added without a version bump. `POST /v1/register` and `POST /v1/login` are public. Passwords are stored as bcrypt and are not returned. `GET /v1/me` requires Bearer or `X-Api-Key`. The `usr_publisher` / `usr_executor` development keys keep working. Token lifetime is `Auth.TokenTTL` (default 24h).

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
2. **competitive** (`settlement_rule=winner_take_all`): two executors accept and submit. Confirm without `submission_id` returns 400 and leaves the task `in_review` with the escrow untouched. v0.1 task bodies do not list submissions; that 400 `message` names the submission ids in `created_at` order (`task.submitted` webhooks also include `executor_id` when `Webhook.Mode=post`). Confirm with the chosen `submission_id` pays the whole escrow to that winner, settles the task, and leaves the loser at ability 0.
3. **collaborative** (`on_dropout=replace`): two seats use the share the API already requires (`share_bps` sums to 10000; this run uses 6000 and 4000 and omits `quantity`). After both seats are filled, one participant drops out: 0 commission, 0 ability, that seat returns to `open`, the other seat stays `filled`, and the task stays `active`. The replacement takes the seat, the work is submitted and confirmed, and settlement follows those basis points (500 credits split 300 / 200; the exitee gets no release). A second create sends one role with `quantity=4` and `share_bps=10000` (the template total, not per seat). The server stores `writer_1`…`writer_4` at 2500 each. After all four seats are filled, 500 credits settle as 125 each.

The 72h auto-settle path does not pick a winner for a competitive task that has more than one submission. The smoke script does not sleep 72h. The same rule is covered by moving the clock in a unit test (`review_hours` is 1 only so the deadline can pass; the rule is unchanged):

```bash
CGO_ENABLED=0 go test ./internal/engine -run 'TestAutoSettleCompetitiveDoesNotPickWinner|TestAutoSettleExclusive'
```

`etc/taskgame.yaml` holds:

- `Auth.JwtSecret`: HS256 secret, at least 16 characters. Shared by `make token`, register, and login
- `Auth.TokenTTL`: lifetime of register and login JWTs, default 24h
- `Auth.ApiKeys`: static `X-Api-Key` to user id
- `Store.DSN`: modernc SQLite DSN
- `Escrow.InitialBalance`: simulated credits granted the first time a user is seen (1000000 in the dev file)
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

打开 `http://localhost:5173`。用法、localStorage 键和三种任务的点法见 [`web/README.md`](web/README.md)。进程为本地 Vite 源（`localhost` / `127.0.0.1` / `[::1]` 的 4173 与 5173–5185）加了 CORS 预检，业务路由不变。

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

Open `http://localhost:5173`. Keys, localStorage, and the three task flows are in [`web/README.md`](web/README.md). The process adds a CORS preflight for local Vite origins (`localhost`, `127.0.0.1`, `[::1]` on 4173 and 5173–5185). Handlers stay the same.

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
scripts/smoke.sh             三种任务的端到端冒烟 / smoke for all three kinds
sdk/ts                       TypeScript 客户端 taskgame-client / TS client
docs/client-ts.md            客户端接入说明（中英） / client guide (zh + en)
web/                         浏览器演示（Vite + React） / player demo UI
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

# Task Game Open Protocol v0.1

Simulated-escrow HTTP API for the Task Game Open Protocol. The contract is [`api/openapi.yaml`](api/openapi.yaml) (OpenAPI 3.1). The service is a [go-zero](https://github.com/zeromicro/go-zero) API: `taskgame.api` is the goctl source, handlers and routes are generated, and the task state machine lives in `internal/engine` behind a `store.Store` interface. The default store is SQLite through `modernc.org/sqlite` (no cgo).

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
- direct chats: `POST /chats` `{type: direct, user_id}` stores one thread per friend pair. Both people must already be friends. The first call inserts it and emits `chat.created`. A later call returns the same id (still 201). `Chat.type` is `task`, `direct`, or `group`. Unfriend does not remove members, so the thread stays readable and writable. Opening it again requires friendship and returns the original id. Task chats stay on the task.
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
- `Store.DSN`: modernc SQLite DSN
- `Escrow.InitialBalance`: simulated credits granted the first time a user is seen (1000000 in the dev file)
- `Webhook.Mode`: `log` records the signature and does not POST; `post` delivers HTTP
- `AutoSettle`: settles when `review_deadline` passes. A competitive task with more than one submission stays `in_review` until `winner_select_deadline`, then refunds. `OpenTimeout`, `StartTimeout`, and `SeatTimeout` default to 72h
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

创建类接口（task、webhook、chat、message、新建好友分组）返回 201。新建好友申请在状态为 `pending` 时返回 201；如果直接接受了对方已有的申请，返回 200。其余成功返回 200。除 `/health`、`POST /v1/register`、`POST /v1/login` 外都要 Bearer JWT 或 `X-Api-Key`。

Creates (task, webhook, chat, message, friend group) return 201. A new friend request returns 201 while `pending`. Accepting the other side's existing request on that same route returns 200. Other successes return 200. Every route except `/health`, `POST /v1/register`, and `POST /v1/login` requires a bearer JWT or `X-Api-Key`.

### 好友 / Friends

好友是双向关系，存在 `friendships` 的一行里（`user_low < user_high`）。分组是个人文件夹，对方看不到。上限：每人 50 个分组、500 个好友、100 条待处理的发出申请。分组名去掉首尾空白后 1–32 个字符，同一用户下不能重复。`sort_order` 范围是 -1000000 到 1000000。

重复申请：已经是好友，或自己已经发出待处理申请，返回 409，不插入第二行。如果对方已经有一条指向自己的待处理申请，`POST /friends/requests` 会接受那一条并返回 200 `status=accepted`，而不是再开一条。拒绝、撤回、解除好友之后可以重新申请。删除分组不会解除好友。解除好友会清掉双方对这一对的分组成员。

好友卡片有 `user_id`、`integrity`、`ability`、`since`、`unread_count`、`direct_chat_id`，不含邮箱、余额和密码。`unread_count` 是对方在这一对的一对一会话里发来、调用者还没标已读的条数。自己发的消息不算。还没有一对一会话时 `unread_count` 是 0，`direct_chat_id` 是 null。任务会话和多人会话不计入。`POST /v1/chats/{chatId}/read` 只推进调用者自己的已读位置，不发 webhook。`GET` 消息历史不会标已读。省略 `message_id` 会标到当前最新一条；带上 `message_id` 就停在那一条，不会往后。同一个 `Idempotency-Key` 会重放第一次的响应，不会把之后新到的消息标成已读。Webhook `friend.requested`（发给接收方）、`friend.accepted` 和 `friend.declined`（发给申请方）、`friend.removed`（发给被解除的一方，`data.user_id` 是操作者）走现有的用户订阅和 HMAC 投递，不按任务过滤。撤回申请不发 webhook。

一对一会话：`POST /v1/chats`，body 是 `{ "type": "direct", "user_id": "usr_…" }`。双方必须已经是好友，待处理申请不行。同一对用户只存一行，两边再调都返回同一个 `id`，HTTP 仍是 201，不会再插入。第一次创建时给双方发 `chat.created`。响应里的 `type` 用来区分：`task` 是任务会话（`GET /chats?task_id=` 里全是这种），`direct` 是好友一对一，`group` 是原来的 `member_ids` 自由会话（每次调用新开一行）。解除好友不会把人移出已有会话，历史还能读，也还能发消息。解除之后再调 `POST` 会 403；重新成为好友后再调，还是原来那条。任务会话仍由注资创建，从任务详情进入。

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

A friend card is `user_id`, `integrity`, `ability`, `since`, `unread_count`, and `direct_chat_id`. No email, balance, or password. `unread_count` is how many messages the other person sent in that pair's direct chat after the caller's read cursor. The caller's own messages are not counted. Before a direct chat exists, `unread_count` is 0 and `direct_chat_id` is null. Task chats and group chats are not included. `POST /v1/chats/{chatId}/read` moves only the caller's cursor and does not emit a webhook. Listing messages does not mark them read. Omit `message_id` to cover the newest message; set it to stop there. The same `Idempotency-Key` replays the first response and does not cover messages that arrived later. `friend.requested` goes to the recipient. `friend.accepted` and `friend.declined` go to the requester. `friend.removed` goes to the other person (`data.user_id` is the actor). Delivery is the existing owner-scoped HMAC webhook, not task-scoped. Cancel does not emit an event.

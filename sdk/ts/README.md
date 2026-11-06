# taskgame-client

TypeScript client for the [Task Game Open Protocol](../../api/openapi.yaml) v0.1 HTTP API. The package is not published to the npm registry yet. Install it from this directory after `npm run build`.

The contract is [`api/openapi.yaml`](../../api/openapi.yaml). This client calls those operations and `GET /health` (process liveness, outside the protocol document). Integration steps in Chinese and English: [`docs/client-ts.md`](../../docs/client-ts.md).

## Install

```bash
cd sdk/ts
npm install
npm test
npm run build
```

From another Node project, after the build:

```bash
npm install /path/to/task-game-api/sdk/ts
```

Node.js 18 or newer. The client uses global `fetch`.

## Call the dev server

Start the API from the repo root (`make run`). It listens on `http://127.0.0.1:8891`. Pass that origin, or the OpenAPI server URL ending in `/v1`. `health()` calls `/health` on the origin. Every other method calls `/v1/...`.

Auth is one header. `apiKey(...)` sends `X-Api-Key`. `bearer(...)` sends `Authorization: Bearer`. Development keys in `etc/taskgame.yaml`:

| Header | User |
| --- | --- |
| `X-Api-Key: dev-publisher-key` | `usr_publisher` |
| `X-Api-Key: dev-executor-key` | `usr_executor` |

Mint a JWT with `make token` (`sub` is the user id) and pass the raw token to `bearer`. `register` and `login` are public and return that same HS256 token (`access_token`, `sub` is `user.id`). They do not send credentials already set on the client. `me()` reads the caller. Passwords are 8 to 72 bytes; the server stores bcrypt.

```ts
import { TaskGameClient, bearer } from "taskgame-client";

const anon = new TaskGameClient({ baseUrl: "http://127.0.0.1:8891" });
const session = await anon.register({
  email: "publisher@example.com",
  password: "password123",
});
const publisher = anon.withAuth(bearer(session.access_token));
const me = await publisher.me();
```

`login({ email, password })` returns the same `AuthSession`. `apiKey()` is unchanged.

```ts
import { TaskGameClient, apiKey, isTaskGameError } from "taskgame-client";

const publisher = new TaskGameClient({
  baseUrl: "http://127.0.0.1:8891",
  auth: apiKey("dev-publisher-key"),
});

const deadline = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString();
try {
  const task = await publisher.createTask(
    {
      kind: "exclusive",
      title: "Translate release notes",
      description: "Translate the v0.1 notes from Chinese to English.",
      payment: { mode: "one_shot", amount: 500 },
      acceptance: {
        deadline,
        quality: "Meets the written acceptance bar",
        review_hours: 72,
      },
      assignee_policy: { type: "open" },
    },
    { idempotencyKey: "create-1" },
  );
  console.log(task.id, task.status);
} catch (error) {
  if (isTaskGameError(error)) {
    console.error(error.status, error.code, error.message);
  }
  throw error;
}
```

`idempotencyKey` is sent as `Idempotency-Key` on the operations that declare it. The same key with the same request replays the stored response. The same key with a different request throws `IdempotencyConflictError` (HTTP 409, code `idempotency_conflict`).

Runnable exclusive path (matches `scripts/smoke.sh`): `npm run example:exclusive`.

Competitive confirm with more than one submission needs `submission_id`. Collaborative `on_dropout` is `replace` (the exitee gets 0 commission and 0 ability, and the seat returns to `open`). A collaborative role may set `quantity`; `share_bps` is that role's total and still sums to 10000 across roles. The server expands the seats. Webhook `X-Signature` is hex HMAC-SHA256 of the raw body: `verifyWebhookSignature`. Details and Chinese text are in [`docs/client-ts.md`](../../docs/client-ts.md).

## 安装

```bash
cd sdk/ts
npm install
npm test
npm run build
```

构建之后，其他 Node 项目可以 `npm install /path/to/task-game-api/sdk/ts`。需要 Node.js 18+，客户端使用全局 `fetch`。

## 调用开发服务

在仓库根目录 `make run`。进程地址是 `http://127.0.0.1:8891`。`baseUrl` 可以是这个 origin，也可以是以 `/v1` 结尾的 OpenAPI 服务器 URL。`health()` 请求 origin 上的 `/health`，其余方法请求 `/v1/...`。

鉴权只带一个头。`apiKey(...)` 发送 `X-Api-Key`，`bearer(...)` 发送 `Authorization: Bearer`。`etc/taskgame.yaml` 里的开发密钥：

| Header | 用户 |
| --- | --- |
| `X-Api-Key: dev-publisher-key` | `usr_publisher` |
| `X-Api-Key: dev-executor-key` | `usr_executor` |

`make token` 签发 JWT（`sub` 是用户 id），把原始 token 交给 `bearer`。`register` 和 `login` 是公开接口，返回同样的 HS256 token（`access_token`，`sub` 为 `user.id`），不会带上客户端里已经配置的凭证。`me()` 读取当前调用方。密码 8 到 72 字节，服务端只存 bcrypt。

```ts
import { TaskGameClient, bearer } from "taskgame-client";

const anon = new TaskGameClient({ baseUrl: "http://127.0.0.1:8891" });
const session = await anon.register({
  email: "publisher@example.com",
  password: "password123",
});
const publisher = anon.withAuth(bearer(session.access_token));
const me = await publisher.me();
```

已有账号用 `login({ email, password })`，返回同一个 `AuthSession`。`apiKey()` 保持不变。

写操作用 `idempotencyKey` 发送 `Idempotency-Key`。同一把 key、同一请求会重放已保存的响应。同一把 key、不同请求抛出 `IdempotencyConflictError`（HTTP 409，`idempotency_conflict`）。

可运行的独占路径与 `scripts/smoke.sh` 一致：`npm run example:exclusive`。竞争任务有多份提交时，确认必须带 `submission_id`。协作任务的 `on_dropout` 是 `replace`（退出者佣金 0、能力分 0，席位回到 `open`）。协作角色可以带 `quantity`；`share_bps` 是该角色合计，各角色相加仍是 10000，席位由服务端展开。Webhook 的 `X-Signature` 是原始 body 的 HMAC-SHA256 十六进制，用 `verifyWebhookSignature` 校验。中英步骤见 [`docs/client-ts.md`](../../docs/client-ts.md)。

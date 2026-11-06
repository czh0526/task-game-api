# Task Game TypeScript client

The HTTP contract is [`api/openapi.yaml`](../api/openapi.yaml) (OpenAPI 3.1, `info.version` 0.1.0). The client in [`sdk/ts`](../sdk/ts) (`taskgame-client`) calls the operations in that file. `GET /health` is process liveness from the go-zero server and is not an OpenAPI path.

Package layout, `npm test`, and a short snippet are in [`sdk/ts/README.md`](../sdk/ts/README.md). The runnable exclusive script is [`sdk/ts/examples/exclusive.mjs`](../sdk/ts/examples/exclusive.mjs). The competitive and collaborative samples below are typechecked in [`sdk/ts/examples/snippets.ts`](../sdk/ts/examples/snippets.ts).

Start the API from the repository root before calling it:

```bash
make run
# listens on http://127.0.0.1:8891
```

Development keys (`etc/taskgame.yaml`):

| Header | User |
| --- | --- |
| `X-Api-Key: dev-publisher-key` | `usr_publisher` |
| `X-Api-Key: dev-executor-key` | `usr_executor` |

`make token` (or `make token USER_ID=usr_executor`) prints an HS256 JWT. `sub` is the user id.

---

## 中文

### 基址

开发服务的进程地址是 `http://127.0.0.1:8891`。协议路径在 `/v1` 下，与 OpenAPI 的 server URL `https://api.example.com/v1` 一致。

`baseUrl` 两种写法都可以：

- `http://127.0.0.1:8891` — 进程 origin
- `http://127.0.0.1:8891/v1` — OpenAPI server URL

客户端会把末尾的 `/v1` 当成协议前缀。`health()` 请求 `{origin}/health`。其余方法请求 `{origin}/v1/...`。注册、创建任务、webhook、会话、消息成功时是 **201**，其他成功是 **200**。除 `/health`、`POST /v1/register`、`POST /v1/login` 外都要鉴权。

### 鉴权

只配置一种。服务端如果同时收到两个头，会只认 `X-Api-Key`。客户端在构造时拒绝同时传入两套凭证，避免打到意外的用户。

```ts
import { TaskGameClient, apiKey, bearer } from "taskgame-client";

const publisher = new TaskGameClient({
  baseUrl: process.env.BASE ?? "http://127.0.0.1:8891",
  auth: apiKey(process.env.PUB_KEY ?? "dev-publisher-key"),
});

// 另一名用户。withAuth 共用同一个 baseUrl。
const executor = publisher.withAuth(apiKey(process.env.EXE_KEY ?? "dev-executor-key"));

// JWT。make token 打印的是原始 token，不要自己再加一层 Bearer 以外的东西。
const asJwt = new TaskGameClient({
  baseUrl: "http://127.0.0.1:8891",
  auth: bearer(process.env.TOKEN ?? ""),
});
```

`apiKey` 发送 `X-Api-Key`。`bearer` 发送 `Authorization: Bearer <jwt>`。`bearer` 也可以是返回 token 的函数。

### 注册和登录

`register` 和 `login` 是公开接口，不会带上客户端里已经配置的 `apiKey` 或 `bearer`。密码至少 8 个字符、最长 72 字节。服务只存 bcrypt，响应里没有密码。成功时 `access_token` 是 HS256 JWT，`sub` 就是 `user.id`，和 `make token` 的 claims 一样（`sub`、`iat`、`exp`）。`expires_in` 是秒。重复邮箱是 **409** `conflict`。邮箱或密码不合法是 **400** `invalid_argument`。登录时邮箱不存在和密码错误都是 **401** `unauthorized`，文案相同。

`me()` 要鉴权，返回 `{id, email}`。开发密钥用户的 `email` 是空字符串。对这三个方法传入 `idempotencyKey` 会抛出 `TypeError`。

```ts
import { TaskGameClient, bearer } from "taskgame-client";

const anon = new TaskGameClient({ baseUrl: "http://127.0.0.1:8891" });
const session = await anon.register({
  email: "publisher@example.com",
  password: "password123",
});
const publisher = anon.withAuth(bearer(session.access_token));
const me = await publisher.me();
// me.id === session.user.id
```

已有账号用 `anon.login({ email, password })`，返回同一形状。开发密钥 `apiKey()` 仍然可用，`usr_publisher` / `usr_executor` 不用注册。

### 幂等

OpenAPI 在写操作上声明了请求头 `Idempotency-Key`，`GET /v1/tasks` 的参数表里也有这个头。传入 `idempotencyKey` 就会带上。创建、注资、接受、提交、确认、争议、退出、webhook、会话和消息会保存这把 key：同一用户、同一把 key、同一请求会重放已保存的响应；同一把 key 配了不同的请求则返回 **409** `idempotency_conflict`，客户端抛出 `IdempotencyConflictError`。`GET /v1/tasks` 会收下这个头，当前服务不用它做重放。

`getTask`、`getUserScores`、`me`、`register`、`login`、`health` 的 OpenAPI（或探活）没有这个头。对它们传入 `idempotencyKey` 会抛出 `TypeError`。

错误体一律是 JSON：`{"code":"...","message":"..."}`。常见 `code` 有对应的错误类（都继承 `TaskGameError`）：

| HTTP | code | 类 |
| --- | --- | --- |
| 400 | `invalid_argument` | `InvalidArgumentError` |
| 401 | `unauthorized` | `UnauthorizedError` |
| 403 | `forbidden` | `ForbiddenError` |
| 404 | `not_found` | `NotFoundError` |
| 409 | `conflict` | `ConflictError` |
| 409 | `failed_precondition` | `FailedPreconditionError` |
| 409 | `insufficient_credits` | `InsufficientCreditsError` |
| 409 | `idempotency_conflict` | `IdempotencyConflictError` |
| 503 | `unavailable` | `UnavailableError` |

### 独占 happy path

与 `scripts/smoke.sh` 的独占路径相同：创建 → 同一把幂等 key 重放 → 注资 → 接受 → 提交 → 确认 → `settled`，然后读积分、登记 webhook、建会话。金额是模拟积分。`review_hours` 用协议默认的 72。可运行脚本 `sdk/ts/examples/exclusive.mjs`（`npm run example:exclusive`）停在建会话，与冒烟一致。下面多了一次 `postMessage`，用来演示发消息。

```ts
import { TaskGameClient, apiKey } from "taskgame-client";

const publisher = new TaskGameClient({
  baseUrl: "http://127.0.0.1:8891",
  auth: apiKey("dev-publisher-key"),
});
const executor = publisher.withAuth(apiKey("dev-executor-key"));

const deadline = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString();
const body = {
  kind: "exclusive" as const,
  title: "Translate release notes",
  description: "Translate the v0.1 notes from Chinese to English.",
  payment: { mode: "one_shot" as const, amount: 500 },
  acceptance: {
    deadline,
    quality: "Meets the written acceptance bar",
    review_hours: 72,
  },
  assignee_policy: { type: "open" as const },
};

const idem = `example-excl-${Date.now()}`;
const created = await publisher.createTask(body, { idempotencyKey: idem });
const replay = await publisher.createTask(body, { idempotencyKey: idem });
// replay.id === created.id，状态仍是 draft

await publisher.fundTask(created.id, { idempotencyKey: `fund-${idem}` }); // open
await executor.acceptTask(created.id, {}); // active
await executor.submitTask(created.id, { payload: { body: "translated" }, note: "done" }); // in_review
await publisher.confirmTask(created.id, {}); // settled

const task = await publisher.getTask(created.id);
const scores = await publisher.getUserScores("usr_executor");
const hook = await publisher.createWebhook({
  url: "https://example.com/hooks",
  secret: "webhook-secret-16",
  events: ["task.settled"],
});
const chat = await publisher.createChat({ member_ids: ["usr_executor"] });
await publisher.postMessage(chat.id, { type: "text", text: "shipped" });
```

独占确认不需要 `submission_id`（冒烟脚本发送 `{}`）。`task.status` 在确认后是 `settled`。创建会话时服务会把调用者并进 `member_ids`，所以发布者可以接着发消息。

### 竞争：确认必须带 submission_id

`competitive.settlement_rule` 固定为 `winner_take_all`。两个执行者都接受并提交之后，任务在 `in_review`。v0.1 的任务体不列出提交。

不带 `submission_id` 的确认返回 **400** `invalid_argument`。`message` 以 `submission_id is required when multiple submissions exist: ` 开头，后面是按 `created_at` 排列的 submission id（逗号分隔）。任务保持 `in_review`，托管不动。`task.submitted` 事件里也有 `submission_id` 和 `executor_id`（`Webhook.Mode=post` 时才会真正 POST）。

选定一份提交再确认：全部托管给该赢家，任务 `settled`，输家能力分为 0。`submissionIdsFromConfirmError` 只解析上面那句 400，其他错误返回 `undefined`。

```ts
import { submissionIdsFromConfirmError } from "taskgame-client";

let ids: string[] | undefined;
try {
  await publisher.confirmTask(taskId, {});
} catch (error) {
  ids = submissionIdsFromConfirmError(error);
  if (!ids) throw error;
}
// ids 的顺序就是 created_at。调用方自己选赢家，冒烟脚本选后提交的那一份。
const settled = await publisher.confirmTask(taskId, { submission_id: ids[1] });
```

多份提交时，72 小时自动结算不会猜赢家，任务留在 `in_review`。

### 协作：退出即 replace

`collaborative.on_dropout` 固定为 `replace`。各席位 `share_bps` 合计必须是 10000。冒烟用 6000 与 4000：500 积分结算为 300 与 200。

两席都坐满后，一人 `dropoutTask`：退出者佣金 0、能力分 0（积分记录原因 `collaborative_dropout`），该席位回到 `open`，另一席位仍是 `filled`，任务保持 `active`。替补用同一个 `role_id` 再 `acceptTask`。之后提交、确认，按仍在席的 `share_bps` 结算，退出者拿不到 release。

```ts
await publisher.createTask({
  kind: "collaborative",
  title: "Write the launch page",
  description: "Design and copy seats. Dropout reopens the seat and pays nothing.",
  payment: { mode: "one_shot", amount: 500 },
  acceptance: {
    deadline,
    quality: "Meets the written acceptance bar",
    review_hours: 72,
  },
  assignee_policy: { type: "open" },
  collaborative: {
    on_dropout: "replace",
    roles: [
      { id: "design", name: "Design", share_bps: 6000 },
      { id: "copy", name: "Copy", share_bps: 4000 },
    ],
  },
});

await exitee.acceptTask(taskId, { role_id: "design" });
await stayer.acceptTask(taskId, { role_id: "copy" });
const dropped = await exitee.dropoutTask(taskId);
// dropped.status === "active"
// design 席位 status === "open"，assignee 已清空
await replacement.acceptTask(taskId, { role_id: "design" });
```

### Webhook 签名

登记时带上 `secret`（至少 16 个字符）和 `events`。投递是 HTTP POST，请求头：

- `X-Signature`：对**原始 body 字节**做 HMAC-SHA256，再编码成小写十六进制。密钥是注册时的 `secret`。
- `X-Event-Id`：事件 id，供接收方去重。正文里的 `id` 与它相同。

先拿原始 body 校验，再 `JSON.parse`。重新 `JSON.stringify` 会改变字节，签名对不上。`Webhook.Mode=log` 时服务只记录签名，不发 HTTP。

```ts
import { createHmac, timingSafeEqual } from "node:crypto";

function signWebhook(secret: string, rawBody: Buffer): string {
  return createHmac("sha256", secret).update(rawBody).digest("hex");
}

function verifyNode(secret: string, rawBody: Buffer, header: string): boolean {
  const expected = Buffer.from(signWebhook(secret, rawBody), "utf8");
  const actual = Buffer.from(header.trim().toLowerCase(), "utf8");
  if (expected.length !== actual.length) return false;
  return timingSafeEqual(expected, actual);
}
```

SDK 用 Web Crypto 做同一运算（浏览器和 Node 都可以）：

```ts
import { parseWebhookEnvelope, verifyWebhookSignature } from "taskgame-client";

const rawBody = await request.text(); // 必须是原始字节对应的字符串，或直接传 Uint8Array
const header = request.headers.get("X-Signature") ?? "";
if (!(await verifyWebhookSignature(secret, rawBody, header))) {
  throw new Error("bad signature");
}
const event = parseWebhookEnvelope(rawBody);
// event.id 与 X-Event-Id 相同；event.type 是 OpenAPI 的 WebhookEventType
```

---

## English

### Base URL

The dev process origin is `http://127.0.0.1:8891`. Protocol routes live under `/v1`, matching the OpenAPI server URL `https://api.example.com/v1`.

`baseUrl` accepts either form:

- `http://127.0.0.1:8891` — process origin
- `http://127.0.0.1:8891/v1` — OpenAPI server URL

A trailing `/v1` is the protocol prefix. `health()` calls `{origin}/health`. Every other method calls `{origin}/v1/...`. Register, task, webhook, chat, and message creates return **201**. Other successes return **200**. Every route except `/health`, `POST /v1/register`, and `POST /v1/login` requires auth.

### Auth

Configure one credential. If both headers arrive, the server authenticates `X-Api-Key` and ignores the bearer token. The client rejects a constructor that sets both, so the call cannot land on the other user by accident.

```ts
import { TaskGameClient, apiKey, bearer } from "taskgame-client";

const publisher = new TaskGameClient({
  baseUrl: process.env.BASE ?? "http://127.0.0.1:8891",
  auth: apiKey(process.env.PUB_KEY ?? "dev-publisher-key"),
});

// Another user. withAuth keeps the same baseUrl.
const executor = publisher.withAuth(apiKey(process.env.EXE_KEY ?? "dev-executor-key"));

// JWT. `make token` prints the raw token.
const asJwt = new TaskGameClient({
  baseUrl: "http://127.0.0.1:8891",
  auth: bearer(process.env.TOKEN ?? ""),
});
```

`apiKey` sends `X-Api-Key`. `bearer` sends `Authorization: Bearer <jwt>`. `bearer` also accepts a function that returns the token.

### Register and login

`register` and `login` are public. They do not send an `apiKey` or `bearer` already configured on the client. The password is 8 to 72 bytes. The server stores bcrypt and does not return the password. `access_token` is an HS256 JWT whose `sub` is `user.id`, the same claims as `make token` (`sub`, `iat`, `exp`). `expires_in` is seconds. A duplicate email is **409** `conflict`. An invalid email or password is **400** `invalid_argument`. Login uses one **401** `unauthorized` message for an unknown email and a wrong password.

`me()` requires auth and returns `{id, email}`. Development API-key users have an empty `email`. Passing `idempotencyKey` to these three methods throws `TypeError`.

```ts
import { TaskGameClient, bearer } from "taskgame-client";

const anon = new TaskGameClient({ baseUrl: "http://127.0.0.1:8891" });
const session = await anon.register({
  email: "publisher@example.com",
  password: "password123",
});
const publisher = anon.withAuth(bearer(session.access_token));
const me = await publisher.me();
// me.id === session.user.id
```

`anon.login({ email, password })` returns the same shape for an existing account. `apiKey()` still works. `usr_publisher` and `usr_executor` do not need to register.

### Idempotency

OpenAPI declares `Idempotency-Key` on the mutating operations, and the parameter list for `GET /v1/tasks` includes it too. Pass `idempotencyKey` and the client sets that header. Create, fund, accept, submit, confirm, dispute, dropout, webhook, chat, and message store the key: the same user, key, and request replays the stored response; the same key with a different request returns **409** `idempotency_conflict`, and the client throws `IdempotencyConflictError`. `GET /v1/tasks` accepts the header. The current server does not use it for replay.

`getTask`, `getUserScores`, `me`, `register`, `login`, and `health` do not define the header. Passing `idempotencyKey` to them throws `TypeError`.

Error bodies are JSON: `{"code":"...","message":"..."}`. Common codes have classes (each extends `TaskGameError`):

| HTTP | code | Class |
| --- | --- | --- |
| 400 | `invalid_argument` | `InvalidArgumentError` |
| 401 | `unauthorized` | `UnauthorizedError` |
| 403 | `forbidden` | `ForbiddenError` |
| 404 | `not_found` | `NotFoundError` |
| 409 | `conflict` | `ConflictError` |
| 409 | `failed_precondition` | `FailedPreconditionError` |
| 409 | `insufficient_credits` | `InsufficientCreditsError` |
| 409 | `idempotency_conflict` | `IdempotencyConflictError` |
| 503 | `unavailable` | `UnavailableError` |

### Exclusive happy path

Same steps as the exclusive section of `scripts/smoke.sh`: create → replay with the same idempotency key → fund → accept → submit → confirm → `settled`, then scores, webhook, and chat. Amounts are simulated credits. `review_hours` is the protocol default, 72. The runnable script `sdk/ts/examples/exclusive.mjs` (`npm run example:exclusive`) stops after creating the chat, matching smoke. The sample below adds one `postMessage` call so sending a message is visible.

```ts
import { TaskGameClient, apiKey } from "taskgame-client";

const publisher = new TaskGameClient({
  baseUrl: "http://127.0.0.1:8891",
  auth: apiKey("dev-publisher-key"),
});
const executor = publisher.withAuth(apiKey("dev-executor-key"));

const deadline = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString();
const body = {
  kind: "exclusive" as const,
  title: "Translate release notes",
  description: "Translate the v0.1 notes from Chinese to English.",
  payment: { mode: "one_shot" as const, amount: 500 },
  acceptance: {
    deadline,
    quality: "Meets the written acceptance bar",
    review_hours: 72,
  },
  assignee_policy: { type: "open" as const },
};

const idem = `example-excl-${Date.now()}`;
const created = await publisher.createTask(body, { idempotencyKey: idem });
const replay = await publisher.createTask(body, { idempotencyKey: idem });
// replay.id === created.id, and the task is still draft

await publisher.fundTask(created.id, { idempotencyKey: `fund-${idem}` }); // open
await executor.acceptTask(created.id, {}); // active
await executor.submitTask(created.id, { payload: { body: "translated" }, note: "done" }); // in_review
await publisher.confirmTask(created.id, {}); // settled

const task = await publisher.getTask(created.id);
const scores = await publisher.getUserScores("usr_executor");
const hook = await publisher.createWebhook({
  url: "https://example.com/hooks",
  secret: "webhook-secret-16",
  events: ["task.settled"],
});
const chat = await publisher.createChat({ member_ids: ["usr_executor"] });
await publisher.postMessage(chat.id, { type: "text", text: "shipped" });
```

Exclusive confirm does not need `submission_id` (the smoke script sends `{}`). After confirm, `task.status` is `settled`. Creating a chat adds the caller to `member_ids`, so the publisher can post the message.

### Competitive confirm needs submission_id

`competitive.settlement_rule` is `winner_take_all`. After two executors accept and submit, the task is `in_review`. v0.1 task bodies do not list submissions.

Confirm without `submission_id` returns **400** `invalid_argument`. `message` starts with `submission_id is required when multiple submissions exist: ` and then lists submission ids in `created_at` order, separated by commas. The task stays `in_review` and the escrow stays where it is. `task.submitted` payloads also include `submission_id` and `executor_id` when `Webhook.Mode=post` actually delivers HTTP.

Confirm again with the chosen `submission_id`. That winner receives the whole escrow, the task becomes `settled`, and the loser stays at ability 0. `submissionIdsFromConfirmError` reads only that 400 sentence. Any other failure returns `undefined`.

```ts
import { submissionIdsFromConfirmError } from "taskgame-client";

let ids: string[] | undefined;
try {
  await publisher.confirmTask(taskId, {});
} catch (error) {
  ids = submissionIdsFromConfirmError(error);
  if (!ids) throw error;
}
// ids are in created_at order. The caller picks the winner.
// The smoke script picks the submission that was created second.
const settled = await publisher.confirmTask(taskId, { submission_id: ids[1] });
```

Auto-settle after the review window does not guess a winner when more than one submission exists. The task stays `in_review`.

### Collaborative dropout is replace

`collaborative.on_dropout` is `replace`. `share_bps` across seats must sum to 10000. The smoke script uses 6000 and 4000, which settles 500 credits as 300 and 200.

After both seats are filled, `dropoutTask` from one participant pays that person 0 commission and writes ability 0 (score reason `collaborative_dropout`). That seat returns to `open`, the other seat stays `filled`, and the task stays `active`. The replacement calls `acceptTask` with the same `role_id`. Submit and confirm then pay the seats that are still filled. The exitee gets no release.

```ts
await publisher.createTask({
  kind: "collaborative",
  title: "Write the launch page",
  description: "Design and copy seats. Dropout reopens the seat and pays nothing.",
  payment: { mode: "one_shot", amount: 500 },
  acceptance: {
    deadline,
    quality: "Meets the written acceptance bar",
    review_hours: 72,
  },
  assignee_policy: { type: "open" },
  collaborative: {
    on_dropout: "replace",
    roles: [
      { id: "design", name: "Design", share_bps: 6000 },
      { id: "copy", name: "Copy", share_bps: 4000 },
    ],
  },
});

await exitee.acceptTask(taskId, { role_id: "design" });
await stayer.acceptTask(taskId, { role_id: "copy" });
const dropped = await exitee.dropoutTask(taskId);
// dropped.status === "active"
// the design seat status is "open" and its assignee is cleared
await replacement.acceptTask(taskId, { role_id: "design" });
```

### Webhook signatures

Registration sends `secret` (at least 16 characters) and `events`. Delivery is an HTTP POST with:

- `X-Signature`: lowercase hex HMAC-SHA256 of the **raw body bytes**, keyed with the registered secret.
- `X-Event-Id`: event id for receiver-side dedupe. The JSON `id` is the same value.

Verify the raw body before `JSON.parse`. Running the body through `JSON.stringify` again changes the bytes and the signature fails. `Webhook.Mode=log` records the signature and does not POST.

```ts
import { createHmac, timingSafeEqual } from "node:crypto";

function signWebhook(secret: string, rawBody: Buffer): string {
  return createHmac("sha256", secret).update(rawBody).digest("hex");
}

function verifyNode(secret: string, rawBody: Buffer, header: string): boolean {
  const expected = Buffer.from(signWebhook(secret, rawBody), "utf8");
  const actual = Buffer.from(header.trim().toLowerCase(), "utf8");
  if (expected.length !== actual.length) return false;
  return timingSafeEqual(expected, actual);
}
```

The SDK computes the same MAC with Web Crypto (Node and browsers):

```ts
import { parseWebhookEnvelope, verifyWebhookSignature } from "taskgame-client";

const rawBody = await request.text(); // exact body string, or pass the Uint8Array
const header = request.headers.get("X-Signature") ?? "";
if (!(await verifyWebhookSignature(secret, rawBody, header))) {
  throw new Error("bad signature");
}
const event = parseWebhookEnvelope(rawBody);
// event.id matches X-Event-Id. event.type is the OpenAPI WebhookEventType.
```

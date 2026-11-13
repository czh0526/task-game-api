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
| `X-Api-Key: dev-arbiter-key` | `usr_arbiter` |

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

`me()` 要鉴权，返回 `{id, email, nickname}`。开发密钥用户的 `email` 是空字符串。`nickname` 是显示名，不是登录名，也不要求唯一；还没设置时是空字符串。`updateMe({ nickname })` 对应 `PATCH /v1/me`，只改昵称，不改邮箱和密码，也不刷新 token。空昵称、纯空白、控制字符、以及超过 32 个字符是 **400**。`register` 可以带上可选的 `nickname`，不带就以后再设。对 `register`、`login`、`me`、`updateMe` 传入 `idempotencyKey` 会抛出 `TypeError`。

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
const named = await publisher.updateMe({ nickname: "阿宁" });
// named.nickname === "阿宁"
```

已有账号用 `anon.login({ email, password })`，返回同一形状。开发密钥 `apiKey()` 仍然可用，`usr_publisher` / `usr_executor` 不用注册。

### 幂等

OpenAPI 在写操作上声明了请求头 `Idempotency-Key`，`GET /v1/tasks` 的参数表里也有这个头。传入 `idempotencyKey` 就会带上。创建、注资、接受、提交、确认、争议、退出、webhook、会话和消息会保存这把 key：同一用户、同一把 key、同一请求会重放已保存的响应；同一把 key 配了不同的请求则返回 **409** `idempotency_conflict`，客户端抛出 `IdempotencyConflictError`。`GET /v1/tasks` 会收下这个头，当前服务不用它做重放。

`getTask`、`getUserScores`、`me`、`updateMe`、`register`、`login`、`health` 的 OpenAPI（或探活）没有这个头。对它们传入 `idempotencyKey` 会抛出 `TypeError`。

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

### 协作：结算方式与退出

`collaborative.on_dropout` 固定为 `replace`。各席位 `share_bps` 合计必须是 10000。冒烟用 6000 与 4000：500 积分结算为 300 与 200。v0.1 的协作任务使用 `payment.mode = one_shot`。

`settlement_mode` 在创建时选定，省略则为 `unified`（含这个字段出现之前已经存下的任务）。

**统一结算 `unified`（默认）。** 执行者在 `open` 时入席，任务仍是 `open`，此时不能提交。席位坐满后，发布者调用 `startTask`，状态变为 `active`。之后每一席都要提交。`confirmTask` 在还有空席、或有席位没提交时失败，错误会列出 `open:` 或 `missing:` 的席位 id。全部提交后一次确认，按各席 `share_bps` 分完整个托管。开始前退出，任务保持 `open`。开始后退出仍是 replace：该席回到 `open`，任务保持 `active`，确认继续被挡住，直到补位并且当前每一席都已提交。退出者佣金 0、能力分 0（原因 `collaborative_dropout`）。

**分别结算 `per_seat`。** 任一席位入席后任务即可变为 `active`，该席可以单独提交。`confirmTask({ role_id })` 只拨付这一席占原始托管的份额，其余继续 `held`。空席不会被支付；对 `open` 席位调用确认会把该份额 `refund` 给发布者。每一席都是 `settled` 或 `refunded` 之后，任务才是 `settled`。多席同时等待确认时必须带 `role_id`。执行者的能力分和完整度在该席拨付时记入；发布者的完整度在任务最终 `settled` 时记一次。v0.1 不会在只提交了部分席位时自动结算；全部非终态席位都提交并且复核窗口结束，才会自动把这些席位一起拨付。分别结算不要调用 `startTask`。

统一结算开始时会发 `task.started`。按席拨付沿用 `task.confirmed` 和 `escrow.release`。空席退回沿用 `escrow.refund`。

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
    settlement_mode: "unified",
    roles: [
      { id: "design", name: "Design", share_bps: 6000 },
      { id: "copy", name: "Copy", share_bps: 4000 },
    ],
  },
});

await exitee.acceptTask(taskId, { role_id: "design" });
await stayer.acceptTask(taskId, { role_id: "copy" });
await publisher.startTask(taskId);
const dropped = await exitee.dropoutTask(taskId);
// dropped.status === "active"
// design 席位 status === "open"，assignee 已清空
await replacement.acceptTask(taskId, { role_id: "design" });
await replacement.submitTask(taskId, { payload: { page: "design" } });
await stayer.submitTask(taskId, { payload: { page: "copy" } });
await publisher.confirmTask(taskId, {});
```

分别结算只确认已经提交的那一席：

```ts
await publisher.createTask({
  kind: "collaborative",
  title: "Write the launch page",
  description: "Design can settle before copy is filled.",
  payment: { mode: "one_shot", amount: 500 },
  acceptance: {
    deadline,
    quality: "Meets the written acceptance bar",
    review_hours: 72,
  },
  assignee_policy: { type: "open" },
  collaborative: {
    on_dropout: "replace",
    settlement_mode: "per_seat",
    roles: [
      { id: "design", name: "Design", share_bps: 6000 },
      { id: "copy", name: "Copy", share_bps: 4000 },
    ],
  },
});
await design.acceptTask(taskId, { role_id: "design" });
await design.submitTask(taskId, { payload: { page: "design" } });
const partial = await publisher.confirmTask(taskId, { role_id: "design" });
// partial.status === "active"，托管仍持有文案席的份额
// 空着的文案席：await publisher.confirmTask(taskId, { role_id: "copy" }) 退回该份额
```

### 角色人数 quantity

创建时 `roles[]` 的每一项是角色模板。`quantity` 省略或为 1 时，和以前的扁平席位一样：`share_bps` 就是这一席，`id` 就是 `accept` 用的 `role_id`。各模板的 `share_bps` 相加必须是 10000。人数不乘进这个合计。

`quantity` 大于 1 时，`share_bps` 仍是这一角色的合计。服务端拆成 `quantity` 个席位：每席先拿 `floor(share_bps / quantity)`，余数从最后几席起每人再加 1，使各席之和等于该角色的 `share_bps`。席位 id 是 `{id}_1` … `{id}_N`（从 1 起）。`accept` 和退出补位用席位 id。

任务响应里的 `roles` 是席位。每席的 `share_bps` 是拆开之后的数字，`quantity` 不会出现。`template_id` 是创建时的模板 id。把响应里的席位原样再拿去创建也合法：每一席省略 `quantity`，`share_bps` 已经是席位份额，合计仍是 10000。

协议仍是 0.1.0。旧的扁平 `roles[]` 不用改。

```ts
await publisher.createTask({
  kind: "collaborative",
  title: "Write the launch page",
  description: "Four writer seats from one role template.",
  payment: { mode: "one_shot", amount: 500 },
  acceptance: {
    deadline,
    quality: "Meets the written acceptance bar",
    review_hours: 72,
  },
  assignee_policy: { type: "open" },
  collaborative: {
    on_dropout: "replace",
    roles: [{ id: "writer", name: "Writer", quantity: 4, share_bps: 10000 }],
  },
});
// seats writer_1 … writer_4, each share_bps 2500, template_id "writer"
await client.acceptTask(taskId, { role_id: "writer_1" });
```

### 好友

好友是双向的。`listFriends()` 只返回调用者自己的分组和未分组好友，字段是 `user_id`、`nickname`、`integrity`、`ability`、`since`、`unread_count`、`direct_chat_id`，没有邮箱。`nickname` 是显示名，可以重复，没设置时为空字符串。`unread_count` 只算这一对的一对一会话里、对方发来且还没 `markChatRead` 的消息。自己发的不算。任务会话和 `member_ids` 会话不加到这个数字上。没有一对一会话时是 0，`direct_chat_id` 为 null。分组是个人文件夹：一个人最多在一个分组里。每人最多 50 个分组、500 个好友、100 条待处理的发出申请。分组名去掉首尾空白后 1–32 个字符，同一用户不能重复。

已经是好友，或自己已经发出待处理申请，是 409。如果对方已经有一条指向你的待处理申请，`createFriendRequest` 会接受那一条（HTTP 200，`status` 为 `accepted`），不会再插入一行。新申请是 201。解除好友之后可以重新申请。删除分组不会解除好友。

`friend.requested` 发给接收方，`friend.accepted` 和 `friend.declined` 发给申请方，`friend.removed` 发给另一方。投递仍是用户自己的 webhook 订阅和原来的 HMAC。撤回申请不发事件。

一对一会话用 `createChat({ type: "direct", user_id })`。对方必须已经存在，不要求是好友，这次调用也不会加好友。不能和自己开（400）。不存在的用户是 404。同一对用户只有一条，再次调用仍是 201，`id` 不变，解除好友之后再调也是原来那条。响应的 `type` 是 `direct`。任务会话的 `type` 是 `task`，`GET /chats?task_id=` 列出来的都是它。`member_ids` 那种自由会话是 `group`，每次调用新开一行。解除好友之后，已有会话还能 `getChat`、`listMessages` 和 `postMessage`。第一次创建会给双方发 `chat.created`。

```ts
const pending = await client.createFriendRequest(
  { user_id: "usr_executor" },
  { idempotencyKey: "friend-1" },
);
await client.acceptFriendRequest(pending.id);
const folder = await client.createFriendGroup({ name: "同事" });
await client.setFriendGroup("usr_executor", { group_id: folder.id });
const mine = await client.listFriends();
const chat = await client.createChat({ type: "direct", user_id: "usr_executor" });
await client.postMessage(chat.id, { type: "text", text: "你好" });
// 接收方好友卡片的 unread_count 变为 1。发送方自己的未读数不变。
// 接收方打开会话后 markChatRead(chat.id)，再 listFriends() 就是 0。
await client.setFriendGroup("usr_executor", { group_id: null });
await client.unfriend("usr_executor");
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

`me()` requires auth and returns `{id, email, nickname}`. Development API-key users have an empty `email`. `nickname` is a display name, not a login handle, and it does not have to be unique. It is an empty string until set. `updateMe({ nickname })` is `PATCH /v1/me`. It changes only the nickname: not the email, not the password, and not the access token. An empty nickname, whitespace, a control character, or more than 32 characters is **400**. `register` may include an optional `nickname`; omit it and set one later. Passing `idempotencyKey` to `register`, `login`, `me`, or `updateMe` throws `TypeError`.

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
const named = await publisher.updateMe({ nickname: "阿宁" });
// named.nickname === "阿宁"
```

`anon.login({ email, password })` returns the same shape for an existing account. `apiKey()` still works. `usr_publisher` and `usr_executor` do not need to register.

### Idempotency

OpenAPI declares `Idempotency-Key` on the mutating operations, and the parameter list for `GET /v1/tasks` includes it too. Pass `idempotencyKey` and the client sets that header. Create, fund, accept, submit, confirm, dispute, dropout, webhook, chat, and message store the key: the same user, key, and request replays the stored response; the same key with a different request returns **409** `idempotency_conflict`, and the client throws `IdempotencyConflictError`. `GET /v1/tasks` accepts the header. The current server does not use it for replay.

`getTask`, `getUserScores`, `me`, `updateMe`, `register`, `login`, and `health` do not define the header. Passing `idempotencyKey` to them throws `TypeError`.

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

### Collaborative settlement and dropout

`collaborative.on_dropout` is `replace`. `share_bps` across seats must sum to 10000. The smoke script uses 6000 and 4000, which settles 500 credits as 300 and 200. Collaborative tasks in v0.1 use `payment.mode = one_shot`.

`settlement_mode` is chosen at create time. Omitting it means `unified`, including tasks stored before the field existed.

**`unified` (default).** Executors fill seats while the task is `open`. Submit is rejected until the publisher calls `startTask`, which requires every seat to be filled and moves the task to `active`. `confirmTask` fails while any seat is open or has not submitted. The error lists those seat ids after `open:` or `missing:`. One confirm then splits the whole pool by seat `share_bps`. Dropout before start leaves the task `open`. Dropout after start still reopens that seat and the task stays `active`; confirm stays blocked until the seat is filled and every current seat has submitted. The exitee gets 0 commission and ability 0 (score reason `collaborative_dropout`).

**`per_seat`.** The task becomes `active` when the first seat is filled, and that seat can submit alone. `confirmTask({ role_id })` releases only that seat's share of the original pool and leaves the rest held. An open seat is not paid; confirming it refunds that share to the publisher. The task is `settled` only when every seat is `settled` or `refunded`. If more than one seat is awaiting confirmation, `role_id` is required. The executor's ability and integrity are recorded when that seat is paid. The publisher's integrity is recorded once, when the task becomes `settled`. v0.1 does not auto-settle a per-seat task until every non-terminal seat has submitted and the review window has passed. Do not call `startTask` for `per_seat`.

Starting a unified task emits `task.started`. A per-seat payout reuses `task.confirmed` and `escrow.release`. Refunding an open seat reuses `escrow.refund`.

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
    settlement_mode: "unified",
    roles: [
      { id: "design", name: "Design", share_bps: 6000 },
      { id: "copy", name: "Copy", share_bps: 4000 },
    ],
  },
});

await exitee.acceptTask(taskId, { role_id: "design" });
await stayer.acceptTask(taskId, { role_id: "copy" });
await publisher.startTask(taskId);
const dropped = await exitee.dropoutTask(taskId);
// dropped.status === "active"
// the design seat status is "open" and its assignee is cleared
await replacement.acceptTask(taskId, { role_id: "design" });
await replacement.submitTask(taskId, { payload: { page: "design" } });
await stayer.submitTask(taskId, { payload: { page: "copy" } });
await publisher.confirmTask(taskId, {});
```

Per-seat confirm pays only the submitted seat:

```ts
await publisher.createTask({
  kind: "collaborative",
  title: "Write the launch page",
  description: "Design can settle before copy is filled.",
  payment: { mode: "one_shot", amount: 500 },
  acceptance: {
    deadline,
    quality: "Meets the written acceptance bar",
    review_hours: 72,
  },
  assignee_policy: { type: "open" },
  collaborative: {
    on_dropout: "replace",
    settlement_mode: "per_seat",
    roles: [
      { id: "design", name: "Design", share_bps: 6000 },
      { id: "copy", name: "Copy", share_bps: 4000 },
    ],
  },
});
await design.acceptTask(taskId, { role_id: "design" });
await design.submitTask(taskId, { payload: { page: "design" } });
const partial = await publisher.confirmTask(taskId, { role_id: "design" });
// partial.status === "active", and escrow still holds the copy seat's share.
// An empty copy seat: await publisher.confirmTask(taskId, { role_id: "copy" }) refunds that share.
```

### Role quantity

Each `roles[]` entry on create is a template. Omit `quantity`, or send 1, and it behaves like the old flat seat: `share_bps` is that seat and `id` is the `role_id` passed to `accept`. Template `share_bps` values must still sum to 10000. Quantity does not multiply that sum.

When `quantity` is greater than 1, `share_bps` is still that role's total. The server stores `quantity` seats. Each seat gets `floor(share_bps / quantity)`, and the last `share_bps mod quantity` seats each get one extra basis point so the seats sum to the template total. Seat ids are `{id}_1` … `{id}_N` (1-based). `accept` and dropout use the seat id.

A task response lists seats. `share_bps` there is the split figure, `quantity` is absent, and `template_id` is the template id from create. Sending those seats back on a later create is valid: each one omits `quantity`, the seat shares already sum to 10000.

The protocol version stays 0.1.0. Old flat `roles[]` payloads do not need a change.

```ts
await publisher.createTask({
  kind: "collaborative",
  title: "Write the launch page",
  description: "Four writer seats from one role template.",
  payment: { mode: "one_shot", amount: 500 },
  acceptance: {
    deadline,
    quality: "Meets the written acceptance bar",
    review_hours: 72,
  },
  assignee_policy: { type: "open" },
  collaborative: {
    on_dropout: "replace",
    roles: [{ id: "writer", name: "Writer", quantity: 4, share_bps: 10000 }],
  },
});
// seats writer_1 … writer_4, each share_bps 2500, template_id "writer"
await client.acceptTask(taskId, { role_id: "writer_1" });
```

### Friends

Friendship is mutual. `listFriends()` returns only the caller's groups and ungrouped friends, with `user_id`, `nickname`, `integrity`, `ability`, `since`, `unread_count`, and `direct_chat_id`. There is no email. `nickname` is a display name, it may be shared, and it is an empty string when unset. `unread_count` counts messages the other person sent in that pair's direct chat and that this caller has not passed to `markChatRead`. The caller's own messages are not counted. Task chats and `member_ids` chats do not add to it. It is 0, and `direct_chat_id` is null, until a direct chat exists. A friend sits in at most one of the owner's groups. Caps are 50 groups, 500 friends, and 100 pending outgoing requests per user. A group name is 1–32 characters after trim and unique for that owner.

Already friends, or a pending request you already sent, is 409. If the other user already has a pending request toward you, `createFriendRequest` accepts that request (HTTP 200, `status` `accepted`) and does not insert another row. A new request is 201. Unfriend allows a later request. Deleting a group does not unfriend anyone.

`friend.requested` is delivered to the recipient. `friend.accepted` and `friend.declined` go to the requester. `friend.removed` goes to the other person. Delivery uses that user's existing webhook subscription and the same HMAC. Cancelling a request does not emit an event.

A 1:1 thread is `createChat({ type: "direct", user_id })`. The other user must exist. Friendship is not required, and the call does not create one. Yourself is 400. An unknown user is 404. The pair has one chat. A later call is still 201 and the same `id`, including after unfriend. The response `type` is `direct`. A task chat is `type: "task"` and is what `listChats(taskId)` returns. A `member_ids` chat is `type: "group"` and each call inserts a new row. After unfriend, `getChat`, `listMessages`, and `postMessage` still work for that thread. The first insert emits `chat.created` to both members.

```ts
const pending = await client.createFriendRequest(
  { user_id: "usr_executor" },
  { idempotencyKey: "friend-1" },
);
await client.acceptFriendRequest(pending.id);
const folder = await client.createFriendGroup({ name: "同事" });
await client.setFriendGroup("usr_executor", { group_id: folder.id });
const mine = await client.listFriends();
const chat = await client.createChat({ type: "direct", user_id: "usr_executor" });
await client.postMessage(chat.id, { type: "text", text: "hello" });
// The recipient's friend card unread_count becomes 1. The sender's own count stays 0.
// The recipient calls markChatRead(chat.id), then listFriends() shows 0.
await client.setFriendGroup("usr_executor", { group_id: null });
await client.unfriend("usr_executor");
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

## Workflow closure

These calls are additive on v0.1. New task fields are optional for older readers. `submissions` lists `current` and `superseded` deliveries so a competitive confirm can pass `submission_id` from the task, not from an error string.

```ts
await client.cancelTask(taskId, { reason: "no longer needed" });
await client.reviseTask(taskId, { reason: "please fix the intro" });
await client.resolveTask(taskId, { outcome: "refund_publisher" });
await client.arbitrateTask(taskId, { outcome: "pay_executor", reason: "delivery matches the brief" });
const chatId = (await client.getTask(taskId)).chat_id;
if (chatId) {
  await client.listMessages(chatId, { limit: 50 });
}
await client.listTasks({ assignable: "me" });
await client.listMyInvites({ status: "pending" });
```

`me().is_arbiter` is true for `usr_arbiter` in the dev config (`X-Api-Key: dev-arbiter-key` or `make token USER_ID=usr_arbiter`). Per-seat disputes freeze only that seat (`role_id`). A task-level refund closes as `cancelled`. A payout closes as `settled`. Message pages are oldest-first. New webhook events: `task.revision_requested`, `task.resolve_proposed`, `task.arbitrated`, `task.invited`, `task.invite_declined`, `escrow.unfreeze`. Timeouts reuse `task.cancelled` with `data.reason` of `open_timeout`, `start_timeout`, or `winner_select_timeout`. A per-seat open-seat refund emits `escrow.refund` with `reason` `seat_timeout`.

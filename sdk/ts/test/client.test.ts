import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  ConflictError,
  FailedPreconditionError,
  ForbiddenError,
  IdempotencyConflictError,
  InsufficientCreditsError,
  InvalidArgumentError,
  NotFoundError,
  TaskGameClient,
  TaskGameError,
  UnauthorizedError,
  UnavailableError,
  apiKey,
  bearer,
  createIdempotencyKey,
  isIdempotencyConflict,
  isTaskGameError,
  submissionIdsFromConfirmError,
} from "../dist/index.js";

interface Captured {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: string;
}

function jsonResponse(status: number, body: unknown): Pick<Response, "ok" | "status" | "text"> {
  const text = JSON.stringify(body);
  return {
    ok: status >= 200 && status < 300,
    status,
    text: () => Promise.resolve(text),
  };
}

function captureFetch(status: number, body: unknown, calls: Captured[]) {
  return (url: string, init: { method: string; headers: Record<string, string>; body?: string }) => {
    calls.push({ url, method: init.method, headers: init.headers, body: init.body });
    return Promise.resolve(jsonResponse(status, body));
  };
}

const taskCreate = {
  kind: "exclusive" as const,
  title: "Translate release notes",
  description: "Translate the v0.1 notes from Chinese to English.",
  payment: { mode: "one_shot" as const, amount: 500 },
  acceptance: {
    deadline: "2026-12-01T00:00:00Z",
    quality: "Meets the written acceptance bar",
    review_hours: 72,
  },
  assignee_policy: { type: "open" as const },
};

const task = {
  ...taskCreate,
  id: "tsk_1",
  status: "draft" as const,
  publisher_id: "usr_publisher",
  executor_ids: [] as string[],
  created_at: "2026-11-06T00:00:00.000000000Z",
  updated_at: "2026-11-06T00:00:00.000000000Z",
};

describe("TaskGameClient URLs and auth", () => {
  it("accepts an origin or an OpenAPI server URL and keeps /health off /v1", async () => {
    for (const baseUrl of [
      "http://127.0.0.1:8891",
      "http://127.0.0.1:8891/",
      "http://127.0.0.1:8891/v1",
      "https://api.example.com/v1/",
    ]) {
      const calls: Captured[] = [];
      const client = new TaskGameClient({
        baseUrl,
        apiKey: "dev-publisher-key",
        fetch: captureFetch(200, { status: "ok" }, calls),
      });
      await client.health();
      const origin = baseUrl.startsWith("https") ? "https://api.example.com" : "http://127.0.0.1:8891";
      assert.equal(calls[0]?.url, `${origin}/health`);
      assert.equal(calls[0]?.method, "GET");
      assert.equal(calls[0]?.headers["X-Api-Key"], undefined);
      assert.equal(calls[0]?.body, undefined);
      assert.equal(client.origin, origin);
      assert.equal(client.apiBase, `${origin}/v1`);
    }
  });

  it("sends X-Api-Key and does not send Authorization", async () => {
    const calls: Captured[] = [];
    const client = new TaskGameClient({
      baseUrl: "http://127.0.0.1:8891",
      auth: apiKey("dev-publisher-key"),
      fetch: captureFetch(200, task, calls),
    });
    const got = await client.getTask("tsk_1");
    assert.equal(got.id, "tsk_1");
    assert.equal(calls[0]?.url, "http://127.0.0.1:8891/v1/tasks/tsk_1");
    assert.equal(calls[0]?.headers["X-Api-Key"], "dev-publisher-key");
    assert.equal(calls[0]?.headers.Authorization, undefined);
  });

  it("sends Authorization Bearer and preserves an existing scheme", async () => {
    const calls: Captured[] = [];
    const client = new TaskGameClient({
      baseUrl: "http://127.0.0.1:8891",
      bearerToken: async () => "jwt-token",
      fetch: captureFetch(200, { user_id: "usr_executor", integrity: 1, ability: 10 }, calls),
    });
    await client.getUserScores("usr_executor");
    assert.equal(calls[0]?.headers.Authorization, "Bearer jwt-token");
    assert.equal(calls[0]?.headers["X-Api-Key"], undefined);

    const prefixed = client.withAuth(bearer("Bearer already"));
    await prefixed.getUserScores("usr/executor");
    assert.equal(calls[1]?.headers.Authorization, "Bearer already");
    assert.equal(calls[1]?.url, "http://127.0.0.1:8891/v1/users/usr%2Fexecutor/scores");
  });

  it("rejects configuring both credentials", () => {
    assert.throws(
      () => new TaskGameClient({ baseUrl: "http://127.0.0.1:8891", apiKey: "k", bearerToken: "t" }),
      TypeError,
    );
    assert.throws(
      () => new TaskGameClient({ baseUrl: "http://127.0.0.1:8891", auth: apiKey("k"), apiKey: "k" }),
      TypeError,
    );
    assert.throws(() => new TaskGameClient({ baseUrl: "ftp://127.0.0.1:8891" }), TypeError);
    assert.throws(() => new TaskGameClient({ baseUrl: "http://127.0.0.1:8891/v1/tasks" }), TypeError);
  });
});

describe("protocol calls", () => {
  it("creates, replays, and follows the exclusive mutating calls", async () => {
    const calls: Captured[] = [];
    const client = new TaskGameClient({
      baseUrl: "http://127.0.0.1:8891",
      apiKey: "dev-publisher-key",
      fetch: (url, init) => {
        calls.push({ url, method: init.method, headers: init.headers, body: init.body });
        const status = url.endsWith("/tasks") && init.method === "POST" ? 201 : 200;
        return Promise.resolve(jsonResponse(status, { ...task, status: "settled" }));
      },
    });
    const idem = createIdempotencyKey("example-excl");
    await client.createTask(taskCreate, { idempotencyKey: idem });
    await client.createTask(taskCreate, { idempotencyKey: idem });
    await client.fundTask("tsk_1", { idempotencyKey: `fund-${idem}` });
    await client.acceptTask("tsk_1", {});
    await client.submitTask("tsk_1", { payload: { body: "translated" }, note: "done" });
    await client.confirmTask("tsk_1", {});
    await client.listTasks({ status: "open", kind: "exclusive", limit: 20, cursor: "c 1" }, { idempotencyKey: "list-1" });

    assert.equal(calls[0]?.method, "POST");
    assert.equal(calls[0]?.url, "http://127.0.0.1:8891/v1/tasks");
    assert.equal(calls[0]?.headers["Idempotency-Key"], idem);
    assert.equal(calls[0]?.headers["Content-Type"], "application/json");
    assert.deepEqual(JSON.parse(calls[0]?.body ?? ""), taskCreate);
    assert.equal(calls[1]?.headers["Idempotency-Key"], idem);
    assert.equal(calls[1]?.body, calls[0]?.body);
    assert.equal(calls[2]?.url, "http://127.0.0.1:8891/v1/tasks/tsk_1/fund");
    assert.equal(calls[2]?.body, undefined);
    assert.equal(calls[2]?.headers["Idempotency-Key"], `fund-${idem}`);
    assert.equal(calls[3]?.body, "{}");
    assert.deepEqual(JSON.parse(calls[4]?.body ?? ""), { payload: { body: "translated" }, note: "done" });
    assert.equal(calls[5]?.body, "{}");
    const listUrl = new URL(calls[6]?.url ?? "");
    assert.equal(listUrl.pathname, "/v1/tasks");
    assert.equal(listUrl.searchParams.get("status"), "open");
    assert.equal(listUrl.searchParams.get("kind"), "exclusive");
    assert.equal(listUrl.searchParams.get("limit"), "20");
    assert.equal(listUrl.searchParams.get("cursor"), "c 1");
    assert.equal(calls[6]?.headers["Idempotency-Key"], "list-1");
    assert.equal(calls[6]?.method, "GET");
  });

  it("sends submission_id, dispute reason, dropout, webhook, chat, and message", async () => {
    const calls: Captured[] = [];
    const client = new TaskGameClient({
      baseUrl: "http://127.0.0.1:8891/v1",
      apiKey: "dev-publisher-key",
      fetch: (url, init) => {
        calls.push({ url, method: init.method, headers: init.headers, body: init.body });
        const created = url.endsWith("/webhooks") || url.endsWith("/chats") || url.endsWith("/messages");
        return Promise.resolve(
          jsonResponse(created ? 201 : 200, {
            ...task,
            id: "whk_1",
            url: "https://example.com/hooks",
            secret: "webhook-secret-16",
            events: ["task.settled"],
            created_at: task.created_at,
            member_ids: ["usr_executor"],
            chat_id: "cht_1",
            sender_id: "usr_publisher",
            type: "text",
            text: "hello",
          }),
        );
      },
    });
    await client.confirmTask("tsk_1", { submission_id: "sub_winner" });
    await client.disputeTask("tsk_1", { reason: "quality" });
    await client.dropoutTask("tsk_1");
    await client.acceptTask("tsk_1", { role_id: "design" });
    await client.createWebhook({
      url: "https://example.com/hooks",
      secret: "webhook-secret-16",
      events: ["task.settled"],
    });
    await client.createChat({ member_ids: ["usr_executor"] });
    await client.postMessage("cht_1", { type: "text", text: "hello" });

    assert.deepEqual(JSON.parse(calls[0]?.body ?? ""), { submission_id: "sub_winner" });
    assert.deepEqual(JSON.parse(calls[1]?.body ?? ""), { reason: "quality" });
    assert.equal(calls[2]?.url, "http://127.0.0.1:8891/v1/tasks/tsk_1/dropout");
    assert.equal(calls[2]?.body, "{}");
    assert.deepEqual(JSON.parse(calls[3]?.body ?? ""), { role_id: "design" });
    assert.equal(calls[4]?.url, "http://127.0.0.1:8891/v1/webhooks");
    assert.equal(calls[5]?.url, "http://127.0.0.1:8891/v1/chats");
    assert.equal(calls[6]?.url, "http://127.0.0.1:8891/v1/chats/cht_1/messages");
  });

  it("refuses Idempotency-Key on operations that do not declare it", async () => {
    const client = new TaskGameClient({
      baseUrl: "http://127.0.0.1:8891",
      apiKey: "dev-publisher-key",
      fetch: () => Promise.resolve(jsonResponse(200, task)),
    });
    await assert.rejects(() => client.getTask("tsk_1", { idempotencyKey: "nope" }), TypeError);
    await assert.rejects(() => client.getUserScores("usr_executor", { idempotencyKey: "nope" }), TypeError);
    assert.throws(() => client.listTasks({ limit: 0 }), TypeError);
    assert.throws(() => client.getTask(""), TypeError);
  });
});

describe("typed errors", () => {
  const cases: Array<{ status: number; code: string; ctor: typeof TaskGameError }> = [
    { status: 400, code: "invalid_argument", ctor: InvalidArgumentError },
    { status: 401, code: "unauthorized", ctor: UnauthorizedError },
    { status: 403, code: "forbidden", ctor: ForbiddenError },
    { status: 404, code: "not_found", ctor: NotFoundError },
    { status: 409, code: "conflict", ctor: ConflictError },
    { status: 409, code: "failed_precondition", ctor: FailedPreconditionError },
    { status: 409, code: "insufficient_credits", ctor: InsufficientCreditsError },
    { status: 409, code: "idempotency_conflict", ctor: IdempotencyConflictError },
    { status: 503, code: "unavailable", ctor: UnavailableError },
  ];

  for (const item of cases) {
    it(`maps ${item.status} ${item.code}`, async () => {
      const client = new TaskGameClient({
        baseUrl: "http://127.0.0.1:8891",
        apiKey: "dev-publisher-key",
        fetch: () =>
          Promise.resolve(
            jsonResponse(item.status, { code: item.code, message: `${item.code} happened` }),
          ),
      });
      await assert.rejects(() => client.fundTask("tsk_1"), (error: unknown) => {
        assert.ok(error instanceof item.ctor);
        assert.ok(isTaskGameError(error));
        assert.equal(error.status, item.status);
        assert.equal(error.code, item.code);
        assert.equal(error.message, `${item.code} happened`);
        if (item.code === "idempotency_conflict") assert.equal(isIdempotencyConflict(error), true);
        return true;
      });
    });
  }

  it("keeps an unclassified body on TaskGameError", async () => {
    const client = new TaskGameClient({
      baseUrl: "http://127.0.0.1:8891",
      apiKey: "k",
      fetch: () => Promise.resolve({ ok: false, status: 502, text: () => Promise.resolve("bad gateway") }),
    });
    await assert.rejects(() => client.health(), (error: unknown) => {
      assert.ok(error instanceof TaskGameError);
      assert.equal(error.constructor, TaskGameError);
      assert.equal(error.code, "http_error");
      assert.equal(error.status, 502);
      return true;
    });
  });

  it("reads competitive submission ids from the confirm 400", () => {
    const error = new InvalidArgumentError(
      400,
      "invalid_argument",
      "submission_id is required when multiple submissions exist: sub_loser, sub_winner",
      {},
    );
    assert.deepEqual(submissionIdsFromConfirmError(error), ["sub_loser", "sub_winner"]);
    const other = new InvalidArgumentError(400, "invalid_argument", "submission_id does not match a submission on this task", {});
    assert.equal(submissionIdsFromConfirmError(other), undefined);
    assert.equal(submissionIdsFromConfirmError(new Error("nope")), undefined);
  });
});

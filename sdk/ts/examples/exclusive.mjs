// Exclusive happy path against a running server. Same steps as scripts/smoke.sh
// (create → idempotent replay → fund → accept → submit → confirm → settled,
// then scores, webhook, and chat).
//
//   cd sdk/ts
//   npm run example:exclusive
//
// The API must already be listening (make run). Defaults match scripts/smoke.sh:
//   BASE=http://127.0.0.1:8891
//   PUB_KEY=dev-publisher-key
//   EXE_KEY=dev-executor-key

import { TaskGameClient, apiKey, isTaskGameError } from "../dist/index.js";

const baseUrl = process.env.BASE ?? "http://127.0.0.1:8891";
const publisher = new TaskGameClient({
  baseUrl,
  auth: apiKey(process.env.PUB_KEY ?? "dev-publisher-key"),
});
const executor = publisher.withAuth(apiKey(process.env.EXE_KEY ?? "dev-executor-key"));

const deadline = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString().replace(/\.\d{3}Z$/, "Z");
/** @type {import("../dist/index.js").TaskCreate} */
const body = {
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
};

try {
  const health = await publisher.health();
  if (health.status !== "ok") {
    throw new Error(`health status ${health.status}`);
  }

  const idem = `example-excl-${Date.now()}`;
  const created = await publisher.createTask(body, { idempotencyKey: idem });
  const replay = await publisher.createTask(body, { idempotencyKey: idem });
  if (replay.id !== created.id) {
    throw new Error(`idempotency replay returned ${replay.id}`);
  }

  await publisher.fundTask(created.id, { idempotencyKey: `fund-${idem}` });
  await executor.acceptTask(created.id, {});
  await executor.submitTask(created.id, { payload: { body: "translated" }, note: "done" });
  await publisher.confirmTask(created.id, {});

  const task = await publisher.getTask(created.id);
  if (task.status !== "settled") {
    throw new Error(`expected settled, got ${task.status}`);
  }

  const scores = await publisher.getUserScores("usr_executor");
  const hook = await publisher.createWebhook({
    url: "https://example.com/hooks",
    secret: "webhook-secret-16",
    events: ["task.settled"],
  });
  const chat = await publisher.createChat({ member_ids: ["usr_executor"] });

  console.log(
    JSON.stringify(
      {
        task_id: task.id,
        status: task.status,
        held: task.escrow?.held ?? null,
        executor_ability: scores.ability,
        executor_integrity: scores.integrity,
        webhook_id: hook.id,
        chat_id: chat.id,
      },
      null,
      2,
    ),
  );
} catch (error) {
  if (isTaskGameError(error)) {
    console.error(`${error.status} ${error.code}: ${error.message}`);
    process.exit(1);
  }
  throw error;
}

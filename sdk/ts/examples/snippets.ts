/**
 * Typechecked samples for docs/client-ts.md.
 * These functions are not a live script. examples/exclusive.mjs calls a server.
 */
import {
  TaskGameClient,
  apiKey,
  bearer,
  isTaskGameError,
  submissionIdsFromConfirmError,
  verifyWebhookSignature,
  parseWebhookEnvelope,
  type TaskCreate,
} from "../dist/index.js";

export function clients(publisherJwt: string, executorJwt: string): { publisher: TaskGameClient; executor: TaskGameClient } {
  const publisher = new TaskGameClient({
    baseUrl: "http://127.0.0.1:8891",
    auth: bearer(publisherJwt),
  });
  return { publisher, executor: publisher.withAuth(bearer(executorJwt)) };
}

export function exclusiveBody(deadline: string): TaskCreate {
  return {
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
}

export async function confirmCompetitive(publisher: TaskGameClient, taskId: string, chosenSubmissionId: string) {
  try {
    await publisher.confirmTask(taskId, {});
  } catch (error) {
    const ids = submissionIdsFromConfirmError(error);
    if (!ids) throw error;
    if (!ids.includes(chosenSubmissionId)) {
      throw new Error(`submission ${chosenSubmissionId} is not in ${ids.join(", ")}`);
    }
  }
  return publisher.confirmTask(taskId, { submission_id: chosenSubmissionId });
}

export function collaborativeBody(deadline: string): TaskCreate {
  return {
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
  };
}

export async function replaceDroppedSeat(
  taskId: string,
  exitee: TaskGameClient,
  stayer: TaskGameClient,
  replacement: TaskGameClient,
) {
  await exitee.acceptTask(taskId, { role_id: "design" });
  await stayer.acceptTask(taskId, { role_id: "copy" });
  const dropped = await exitee.dropoutTask(taskId);
  const design = dropped.collaborative?.roles?.find((role) => role.id === "design");
  if (design?.status !== "open") {
    throw new Error("design seat should be open after dropout");
  }
  return replacement.acceptTask(taskId, { role_id: "design" });
}

export async function readWebhook(secret: string, rawBody: string, signature: string | null) {
  const ok = await verifyWebhookSignature(secret, rawBody, signature ?? "");
  if (!ok) return undefined;
  return parseWebhookEnvelope(rawBody);
}

export function explainAuth(apiKeyValue: string): TaskGameClient {
  return new TaskGameClient({
    baseUrl: "http://127.0.0.1:8891/v1",
    auth: apiKey(apiKeyValue),
  });
}

export function errorCode(error: unknown): string | undefined {
  if (!isTaskGameError(error)) return undefined;
  return `${error.status} ${error.code}`;
}

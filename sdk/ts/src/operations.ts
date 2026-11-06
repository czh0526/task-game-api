/**
 * operationId values in api/openapi.yaml. sdk/ts/test/openapi.test.ts fails
 * when this list and the document diverge.
 */
export const protocolOperations = [
  "register",
  "login",
  "me",
  "listTasks",
  "createTask",
  "getTask",
  "fundTask",
  "acceptTask",
  "submitTask",
  "confirmTask",
  "disputeTask",
  "dropoutTask",
  "createWebhook",
  "createChat",
  "postMessage",
  "getUserScores",
] as const;

export type ProtocolOperation = (typeof protocolOperations)[number];

/** `info.version` in api/openapi.yaml. */
export const PROTOCOL_VERSION = "0.1.0";

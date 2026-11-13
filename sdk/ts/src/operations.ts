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
  "startTask",
  "submitTask",
  "confirmTask",
  "disputeTask",
  "cancelTask",
  "reviseTask",
  "resolveTask",
  "arbitrateTask",
  "listTaskInvites",
  "createTaskInvites",
  "dropoutTask",
  "createWebhook",
  "listMyInvites",
  "declineInvite",
  "listChats",
  "createChat",
  "getChat",
  "listMessages",
  "postMessage",
  "markChatRead",
  "getUserScores",
  "listFriends",
  "createFriendRequest",
  "unfriend",
  "setFriendGroup",
  "listFriendRequests",
  "acceptFriendRequest",
  "declineFriendRequest",
  "cancelFriendRequest",
  "listFriendGroups",
  "createFriendGroup",
  "updateFriendGroup",
  "deleteFriendGroup",
] as const;

export type ProtocolOperation = (typeof protocolOperations)[number];

/** `info.version` in api/openapi.yaml. */
export const PROTOCOL_VERSION = "0.1.0";

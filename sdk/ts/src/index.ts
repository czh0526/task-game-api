export { apiKey, authorizationHeader, bearer } from "./auth.js";
export type { Auth, BearerToken } from "./auth.js";

export {
  TaskGameClient,
  createIdempotencyKey,
  resolveServiceUrls,
} from "./client.js";
export type { CallOptions, FetchInit, FetchLike, TaskGameClientOptions } from "./client.js";

export {
  ConflictError,
  FailedPreconditionError,
  ForbiddenError,
  IdempotencyConflictError,
  InsufficientCreditsError,
  InvalidArgumentError,
  MULTIPLE_SUBMISSIONS_PREFIX,
  NotFoundError,
  TaskGameError,
  UnauthorizedError,
  UnavailableError,
  isIdempotencyConflict,
  isTaskGameError,
  submissionIdsFromConfirmError,
} from "./errors.js";

export { PROTOCOL_VERSION, protocolOperations } from "./operations.js";
export type { ProtocolOperation } from "./operations.js";

export {
  WEBHOOK_EVENT_ID_HEADER,
  WEBHOOK_SIGNATURE_HEADER,
  parseWebhookEnvelope,
  verifyWebhookSignature,
  webhookSignature,
} from "./webhook.js";

export type {
  AcceptTaskBody,
  Acceptance,
  AssigneePolicy,
  AssigneePolicyType,
  Chat,
  ChatCreate,
  CollaborativeRole,
  CollaborativeSettings,
  CompetitiveSettings,
  ConfirmTaskBody,
  DisputeTaskBody,
  DropoutRule,
  EscrowAccount,
  EscrowEntry,
  EscrowEntryType,
  AuthSession,
  HealthStatus,
  ListTasksQuery,
  Message,
  MessageCreate,
  MessageType,
  Milestone,
  MilestoneStatus,
  MoneyAmount,
  Payment,
  PaymentCondition,
  PasswordCredentials,
  PaymentMode,
  RoleStatus,
  ScoreChange,
  SettlementMode,
  SettlementRule,
  SubmissionCreate,
  Task,
  TaskCreate,
  TaskKind,
  TaskList,
  TaskStatus,
  UserScores,
  UserSummary,
  Webhook,
  WebhookCreate,
  WebhookEnvelope,
  WebhookEventType,
} from "./types.js";

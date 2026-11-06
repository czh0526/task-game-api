/**
 * Shapes from api/openapi.yaml (Task Game Open Protocol 0.1.0).
 * Field names match the JSON the go-zero server reads and writes.
 */

/** Simulated credits in the smallest unit. The wire format is a JSON number. */
export type MoneyAmount = number;

export type TaskKind = "exclusive" | "competitive" | "collaborative";

export type TaskStatus =
  | "draft"
  | "open"
  | "active"
  | "in_review"
  | "disputed"
  | "settled"
  | "cancelled";

export type PaymentMode = "one_shot" | "milestone";

export type AssigneePolicyType = "specific" | "criteria" | "open";

export type EscrowEntryType = "hold" | "release" | "refund" | "freeze";

export type MilestoneStatus = "pending" | "submitted" | "released" | "disputed";

export type RoleStatus = "open" | "filled" | "dropped";

export type SettlementRule = "winner_take_all";

export type DropoutRule = "replace";

export type PaymentCondition = "on_accept" | "on_milestone_pass" | "on_final_confirm";

export type MessageType = "text" | "attachment_ref";

export type WebhookEventType =
  | "task.funded"
  | "task.accepted"
  | "task.submitted"
  | "task.in_review"
  | "task.confirmed"
  | "task.auto_settled"
  | "task.disputed"
  | "task.settled"
  | "task.cancelled"
  | "task.executor_replaced"
  | "escrow.hold"
  | "escrow.release"
  | "escrow.refund"
  | "chat.message.created"
  | "score.updated";

export interface Milestone {
  id: string;
  amount: MoneyAmount;
  condition: string;
  status?: MilestoneStatus;
}

export interface AssigneePolicy {
  type: AssigneePolicyType;
  user_ids?: string[];
  min_ability?: number;
}

export interface CollaborativeRole {
  id: string;
  name: string;
  /** Basis points of the bounty for this seat when it is still active at settle. */
  share_bps: number;
  assignee_user_id?: string | null;
  status?: RoleStatus;
}

export interface Payment {
  mode: PaymentMode;
  amount: MoneyAmount;
  milestones?: Milestone[];
  conditions?: PaymentCondition[];
}

export interface Acceptance {
  /** RFC3339 timestamp. The server requires it to be in the future. */
  deadline: string;
  quality: string;
  constraints?: string[];
  /** Defaults to 72 on the server when omitted or 0. */
  review_hours?: number;
}

export interface CompetitiveSettings {
  slots: number;
  settlement_rule?: SettlementRule;
}

export interface CollaborativeSettings {
  roles: CollaborativeRole[];
  on_dropout?: DropoutRule;
}

export interface TaskCreate {
  kind: TaskKind;
  title: string;
  description: string;
  payment: Payment;
  acceptance: Acceptance;
  assignee_policy: AssigneePolicy;
  competitive?: CompetitiveSettings;
  collaborative?: CollaborativeSettings;
}

export interface EscrowEntry {
  id: string;
  type: EscrowEntryType;
  amount: MoneyAmount;
  to_user_id?: string;
  created_at: string;
}

export interface EscrowAccount {
  id?: string;
  held?: MoneyAmount;
  currency?: string;
  entries?: EscrowEntry[];
}

export interface Task extends TaskCreate {
  id: string;
  status: TaskStatus;
  publisher_id: string;
  executor_ids?: string[];
  winning_submission_id?: string | null;
  review_deadline?: string | null;
  escrow?: EscrowAccount;
  created_at: string;
  updated_at: string;
}

export interface TaskList {
  items: Task[];
  next_cursor?: string | null;
}

export interface ListTasksQuery {
  status?: TaskStatus;
  kind?: TaskKind;
  /** Integer from 1 to 100. The server default is 20. */
  limit?: number;
  cursor?: string;
}

export interface AcceptTaskBody {
  /** Required when the task is collaborative. Names the role seat. */
  role_id?: string;
}

export interface SubmissionCreate {
  payload: Record<string, unknown>;
  milestone_id?: string;
  note?: string;
}

export interface ConfirmTaskBody {
  /**
   * Required for competitive tasks once more than one submission exists.
   * Selects the winner under winner_take_all.
   */
  submission_id?: string;
  milestone_id?: string;
}

export interface DisputeTaskBody {
  reason: string;
}

export interface WebhookCreate {
  url: string;
  /** At least 16 characters. */
  secret: string;
  events: WebhookEventType[];
}

export interface Webhook extends WebhookCreate {
  id: string;
  created_at: string;
}

/** Body delivered as the webhook POST. `data` is event-specific. */
export interface WebhookEnvelope<T = Record<string, unknown>> {
  id: string;
  type: WebhookEventType;
  created_at: string;
  data: T;
}

export interface ChatCreate {
  member_ids: string[];
}

export interface Chat {
  id: string;
  task_id?: string | null;
  member_ids: string[];
}

export interface MessageCreate {
  type: MessageType;
  text?: string;
  attachment_ref?: string;
}

export interface Message extends MessageCreate {
  id: string;
  chat_id: string;
  sender_id: string;
  created_at: string;
}

export interface ScoreChange {
  delta_integrity?: number;
  delta_ability?: number;
  reason?: string;
  task_id?: string;
  created_at?: string;
}

export interface UserScores {
  user_id: string;
  integrity: number;
  ability: number;
  recent?: ScoreChange[];
}

/** `GET /health` is process liveness. It is not an OpenAPI operation. */
export interface HealthStatus {
  status: string;
}

/** Body of `POST /v1/register` and `POST /v1/login`. Password is 8 to 72 bytes. */
export interface PasswordCredentials {
  email: string;
  password: string;
}

/** Caller summary from register, login, and `GET /v1/me`. Email is empty for API-key users. */
export interface UserSummary {
  id: string;
  email: string;
}

/**
 * Access token from register or login.
 * `access_token` is an HS256 JWT whose `sub` is `user.id` (same claims as `make token`).
 */
export interface AuthSession {
  access_token: string;
  expires_in: number;
  user: UserSummary;
}

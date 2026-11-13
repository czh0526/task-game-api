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

export type EscrowEntryType = "hold" | "release" | "refund" | "freeze" | "unfreeze";

export type MilestoneStatus = "pending" | "submitted" | "released" | "disputed";

export type RoleStatus = "open" | "filled" | "dropped" | "submitted" | "settled" | "refunded" | "disputed";

export type SubmissionStatus = "current" | "superseded";

export type ResolutionOutcome = "pay_executor" | "refund_publisher" | "split";

export type InviteStatus = "pending" | "accepted" | "declined";

export type SettlementRule = "winner_take_all";

/** Collaborative create-time settlement. Omitted means `unified`. */
export type SettlementMode = "per_seat" | "unified";

export type DropoutRule = "replace";

export type PaymentCondition = "on_accept" | "on_milestone_pass" | "on_final_confirm";

export type MessageType = "text" | "attachment_ref";

export type WebhookEventType =
  | "task.funded"
  | "task.accepted"
  | "task.started"
  | "task.submitted"
  | "task.in_review"
  | "task.confirmed"
  | "task.auto_settled"
  | "task.disputed"
  | "task.settled"
  | "task.cancelled"
  | "task.executor_replaced"
  | "task.revision_requested"
  | "task.resolve_proposed"
  | "task.arbitrated"
  | "task.invited"
  | "task.invite_declined"
  | "escrow.hold"
  | "escrow.release"
  | "escrow.refund"
  | "escrow.unfreeze"
  | "chat.created"
  | "chat.message.created"
  | "score.updated"
  | "friend.requested"
  | "friend.accepted"
  | "friend.declined"
  | "friend.removed";

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
  /**
   * Template id on create. Seat id on a task.
   * Omitted quantity, or quantity 1, keeps this id.
   * quantity N becomes `{id}_1` .. `{id}_N` (1-based). accept and dropout use the seat id.
   */
  id: string;
  name: string;
  /**
   * Create input only. Seats to materialize. Omitted means 1.
   * Not returned on task seats, and it does not multiply share_bps.
   */
  quantity?: number;
  /**
   * On create, the template total. Template totals must sum to 10000.
   * The server gives each seat floor(share_bps / quantity), then adds 1 to each of the
   * last (share_bps mod quantity) seats.
   * On a task, this is that seat's share after the split. Settlement uses the seat shares.
   */
  share_bps: number;
  /** Server-assigned on materialized seats. The template id from create. */
  template_id?: string;
  assignee_user_id?: string | null;
  /**
   * Server-assigned. `submitted` is awaiting confirm.
   * `settled` was paid. `refunded` returned that share to the publisher.
   * Dropout replace sets the seat back to `open`.
   */
  status?: RoleStatus;
  /** When an open per_seat seat auto-refunds. */
  refund_deadline?: string | null;
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
  /** Defaults to 3. How many revise calls are allowed. */
  max_revisions?: number;
}

export interface CompetitiveSettings {
  slots: number;
  settlement_rule?: SettlementRule;
}

export interface CollaborativeSettings {
  /**
   * Create: role templates. Response: materialized seats.
   * share_bps is the template total on the way in and the seat share on the way out.
   * With quantity omitted or 1, those are the same number.
   */
  roles: CollaborativeRole[];
  on_dropout?: DropoutRule;
  /**
   * Chosen at create time. Omitted means `unified`, including tasks stored before the field existed.
   * `unified` fills seats while open, then the publisher calls `startTask`, then one confirm splits the pool.
   * `per_seat` lets each filled seat submit and settle on its own.
   */
  settlement_mode?: SettlementMode;
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
  frozen?: MoneyAmount;
  currency?: string;
  entries?: EscrowEntry[];
}

export interface Submission {
  id: string;
  executor_id: string;
  role_id?: string | null;
  milestone_id?: string | null;
  payload: Record<string, unknown>;
  note?: string;
  status: SubmissionStatus;
  created_at: string;
}

export interface SplitLine {
  user_id: string;
  amount?: MoneyAmount;
  share_bps?: number;
}

export interface ResolveProposal {
  user_id: string;
  side: "publisher" | "executor";
  outcome: ResolutionOutcome;
  reason?: string;
  role_id?: string;
  submission_id?: string;
  splits?: SplitLine[];
  created_at: string;
}

export interface Task extends TaskCreate {
  id: string;
  status: TaskStatus;
  publisher_id: string;
  executor_ids?: string[];
  winning_submission_id?: string | null;
  review_deadline?: string | null;
  open_deadline?: string | null;
  start_deadline?: string | null;
  winner_select_deadline?: string | null;
  chat_id?: string | null;
  cancel_reason?: string | null;
  cancel_code?: string | null;
  cancelled_at?: string | null;
  dispute_role_id?: string | null;
  resolve_proposals?: ResolveProposal[];
  submissions?: Submission[];
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
  /** `me` returns tasks the caller can accept right now. */
  assignable?: "me";
}

export interface AcceptTaskBody {
  /**
   * Required when the task is collaborative.
   * The materialized seat id (`design`, or `writer_1` when quantity is greater than 1).
   */
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
  /**
   * Seat id when `settlement_mode` is `per_seat`.
   * Confirms that submitted seat, or refunds that open seat.
   */
  role_id?: string;
}

export interface DisputeTaskBody {
  reason: string;
  /** per_seat: the submitted seat to freeze. Required when more than one seat is submitted. */
  role_id?: string;
}

export interface CancelTaskBody {
  reason?: string;
}

export interface ReviseTaskBody {
  reason: string;
  role_id?: string;
  submission_id?: string;
}

export interface ResolutionBody {
  outcome: ResolutionOutcome;
  reason?: string;
  role_id?: string;
  submission_id?: string;
  splits?: SplitLine[];
}

export interface InviteUsersBody {
  user_ids: string[];
}

export interface Invite {
  id: string;
  task_id: string;
  user_id: string;
  status: InviteStatus;
  created_at: string;
  updated_at: string;
}

export interface InviteList {
  items: Invite[];
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

export type ChatType = "task" | "direct" | "group";

/** Free-form chat. Each call inserts a new row. Optional task_id reuses the task thread. */
export interface GroupChatCreate {
  type?: "group";
  member_ids: string[];
  task_id?: string;
  user_id?: never;
}

/**
 * One 1:1 thread per friend pair. The first call creates it. A later call returns the same chat.
 * Both users must already be friends.
 */
export interface DirectChatCreate {
  type: "direct";
  user_id: string;
  member_ids?: never;
  task_id?: never;
}

export type ChatCreate = GroupChatCreate | DirectChatCreate;

export interface Chat {
  id: string;
  /** task is bound to a task. direct is the friend pair. group is a free-form thread. */
  type: ChatType;
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

export interface ChatList {
  items: Chat[];
}

/** Body of `POST /v1/chats/{chatId}/read`. Omit `message_id` to cover the newest message. */
export interface MarkChatReadBody {
  message_id?: string;
}

/** The caller's read cursor after `POST /v1/chats/{chatId}/read`. */
export interface ChatRead {
  chat_id: string;
  /** Messages from other members still after the cursor. Own messages are not counted. */
  unread_count: number;
  /** Newest message the cursor covers. Null when the chat has no messages. */
  read_through_message_id?: string | null;
}

export interface MessageList {
  items: Message[];
  next_cursor?: string | null;
}

export interface ListMessagesQuery {
  limit?: number;
  cursor?: string;
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
  /** Display name. Empty or omitted when unset. No email. */
  nickname?: string;
  integrity: number;
  ability: number;
  recent?: ScoreChange[];
}

/** `GET /health` is process liveness. It is not an OpenAPI operation. */
export interface HealthStatus {
  status: string;
}

/** Body of `POST /v1/login`, and the email plus password of `POST /v1/register`. Password is 8 to 72 bytes. */
export interface PasswordCredentials {
  email: string;
  password: string;
}

/** Body of `POST /v1/register`. `nickname` is an optional display name, not a login handle. */
export interface RegisterBody extends PasswordCredentials {
  /** Omit to leave the display name unset. 1–32 characters after trim. Not unique. */
  nickname?: string;
}

/** Body of `PATCH /v1/me`. Does not change email or password. */
export interface ProfileUpdate {
  /** Display name. Trimmed. 1–32 characters. Not unique. */
  nickname: string;
}

/** Caller summary from register, login, and `GET /v1/me`. Email is empty for API-key users. */
export interface UserSummary {
  id: string;
  email: string;
  /** Display name. Empty or omitted until the user sets one. Not unique. */
  nickname?: string;
  is_arbiter?: boolean;
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

export type FriendRequestStatus = "pending" | "accepted" | "declined" | "cancelled";

export type FriendRequestDirection = "incoming" | "outgoing";

/**
 * Public friend card. Same scores as `GET /users/{userId}/scores`. No email.
 * `unread_count` is only the pair's direct chat.
 */
export interface Friend {
  user_id: string;
  /** Display name. Empty or omitted when unset. Not unique. No email. */
  nickname?: string;
  integrity: number;
  ability: number;
  /** When the friendship was accepted. */
  since: string;
  /**
   * Incoming messages in the 1:1 direct chat that this caller has not marked read.
   * Own messages are not counted. 0 when there is no direct chat, or when every
   * incoming message is covered. Task chats and group chats are not included.
   */
  unread_count: number;
  /** The pair's direct chat. Null until the first direct `createChat`. */
  direct_chat_id?: string | null;
}

export interface FriendGroup {
  id: string;
  name: string;
  sort_order: number;
}

export interface FriendGroupWithFriends extends FriendGroup {
  friends: Friend[];
}

/** Owner-only. `groups` includes empty folders. `ungrouped` is the null group_id bucket. */
export interface FriendList {
  groups: FriendGroupWithFriends[];
  ungrouped: Friend[];
}

export interface FriendRequest {
  id: string;
  from_user_id: string;
  to_user_id: string;
  /** Display name of `from_user_id`. Empty or omitted when unset. */
  from_nickname?: string;
  /** Display name of `to_user_id`. Empty or omitted when unset. */
  to_nickname?: string;
  status: FriendRequestStatus;
  created_at: string;
  updated_at: string;
}

export interface FriendRequestList {
  items: FriendRequest[];
}

export interface FriendGroupList {
  items: FriendGroup[];
}

export interface RemovedFriend {
  user_id: string;
}

export interface FriendPlacement {
  user_id: string;
  group_id?: string | null;
}

export interface CreateFriendRequestBody {
  user_id: string;
}

export interface CreateFriendGroupBody {
  name: string;
}

export interface PatchFriendGroupBody {
  name?: string;
  sort_order?: number;
}

/** `group_id` null, or omitted, moves the friend to the ungrouped bucket. */
export interface SetFriendGroupBody {
  group_id?: string | null;
}

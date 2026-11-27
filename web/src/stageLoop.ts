/** Milestone submit / confirm / revise choices. The server still decides the outcome. */

export interface StageMilestone {
  id: string;
  amount: number;
  condition: string;
  status?: string;
}

export interface StageSubmission {
  id: string;
  status: string;
  milestone_id?: string | null;
}

export interface StageTask {
  kind: string;
  status: string;
  payment: {
    mode: string;
    amount: number;
    milestones?: StageMilestone[];
  };
  submissions?: StageSubmission[];
}

export interface ConfirmBody {
  submission_id?: string;
  milestone_id?: string;
}

export interface SeatInviteTask {
  kind: string;
  status: string;
  publisher_id: string;
}

export interface InviteTask {
  kind: string;
  status: string;
  publisher_id: string;
  executor_ids?: string[];
  competitive?: { slots?: number };
  assignee_policy?: { type?: string; user_ids?: string[] };
}

/**
 * The assignee list can be replaced only by the publisher, on a specific-policy
 * task that is still open. Collaborative tasks use per-seat invites instead.
 * Mirrors the server's SetAssignees guard.
 */
export function canEditAssignees(me: string | undefined, task: InviteTask): boolean {
  return (
    !!me &&
    me === task.publisher_id &&
    task.kind !== "collaborative" &&
    task.status === "open" &&
    task.assignee_policy?.type === "specific"
  );
}

/**
 * Mirrors the server's hasInviteCapacity: the invite action exists only while
 * a slot is actually free. Exclusive has one slot; collaborative seats are
 * invited individually and never use the task-wide invite action.
 */
export function canInviteTask(me: string | undefined, task: InviteTask): boolean {
  if (!me || me !== task.publisher_id) return false;
  if (task.kind === "collaborative") return false;
  const taken = (task.executor_ids ?? []).length;
  if (task.kind === "exclusive") return task.status === "open" && taken === 0;
  if (task.kind === "competitive") {
    if (task.status !== "open" && task.status !== "active") return false;
    return taken < (task.competitive?.slots ?? 0);
  }
  return false;
}

/** A publisher may invite someone only into a still-open seat of a live task. */
export function canInviteSeat(me: string | undefined, task: SeatInviteTask, roleStatus: string | undefined): boolean {
  return (
    !!me &&
    me === task.publisher_id &&
    task.kind === "collaborative" &&
    (task.status === "open" || task.status === "active") &&
    (roleStatus ?? "open") === "open"
  );
}

/** A publisher may remove an empty seat of a live task. */
export function canRemoveSeat(me: string | undefined, task: SeatInviteTask, roleStatus: string | undefined, assignee?: string | null): boolean {
  return (
    !!me &&
    me === task.publisher_id &&
    task.kind === "collaborative" &&
    (task.status === "open" || task.status === "active") &&
    ((roleStatus ?? "open") === "open" || (roleStatus ?? "open") === "dropped") &&
    !assignee
  );
}

/** Fields needed to mirror the server's CancelTask guards without SDK types. */
export interface CancelTaskView {
  status: string;
  publisher_id: string;
  kind?: string;
  dispute_role_id?: string | null;
  submissions?: Array<{ status?: string }>;
  collaborative?: {
    settlement_mode?: string;
    roles?: Array<{ status?: string }>;
  };
  escrow?: { frozen?: number };
}

function cancelFrozenReason(task: CancelTaskView): string | null {
  if ((task.escrow?.frozen ?? 0) > 0 && !task.dispute_role_id) {
    return "托管资金已冻结，请走协商或裁定";
  }
  return null;
}

/**
 * Mirrors engine.CancelTask. A plain task cannot be cancelled once any work
 * has been submitted -- including a submission the publisher sent back for
 * revision, because it stays on file as superseded. Per-seat collaboration may
 * still refund unfinished seats. Returns null only if the server should accept.
 */
export function cancelReason(task: CancelTaskView, me: string | undefined): string | null {
  if (!me || me !== task.publisher_id) return "只有发布者可以取消";
  if (task.status === "draft" || task.status === "open") return cancelFrozenReason(task);
  if (task.status === "active") {
    const perSeat = task.kind === "collaborative" && task.collaborative?.settlement_mode === "per_seat";
    if (!perSeat) {
      if ((task.submissions ?? []).length > 0) {
        return "已经有完成通知，不能取消；请发起结算或请对方返工";
      }
      return cancelFrozenReason(task);
    }
    const refundable = (task.collaborative?.roles ?? []).some((role) => {
      const status = role.status ?? "open";
      return status === "open" || status === "dropped" || status === "" || status === "filled";
    });
    if (!refundable) return "没有可退款的未完成席位，请按席位结算";
    return cancelFrozenReason(task);
  }
  if (task.status === "in_review" || task.status === "disputed") {
    return "请改用返工、结算、协商或裁定";
  }
  return "这个状态不能取消";
}

/** Same comma/whitespace splitting used by the invite forms. */
export function inviteUserIds(raw: string): string[] {
  return [...new Set(raw.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean))];
}

export function milestoneSubmitBlock(task: StageTask): string | null {
  if (task.payment.mode !== "milestone") return null;
  const stages = task.payment.milestones ?? [];
  if (stages.some((milestone) => milestone.status === "submitted")) {
    return "已有阶段在待结算。发起结算或请对方返工后，任务回到进行中才能通知下一阶段完成。";
  }
  if (!stages.some((milestone) => (milestone.status ?? "pending") === "pending")) {
    return "没有待完成的阶段";
  }
  if (task.status !== "active") return "分阶段任务只有进行中可以通知完成";
  return null;
}

export function milestoneConfirmBlock(task: StageTask): string | null {
  if (task.payment.mode !== "milestone" || task.status !== "in_review") return null;
  const awaiting = (task.payment.milestones ?? []).filter((milestone) => milestone.status === "submitted");
  if (awaiting.length === 0) return "没有待结算的阶段";
  return null;
}

export function milestoneNextStep(task: StageTask): string | null {
  if (task.payment.mode !== "milestone") return null;
  if (task.status === "draft" || task.status === "settled" || task.status === "cancelled" || task.status === "disputed") {
    return null;
  }
  const stages = task.payment.milestones ?? [];
  const submitted = stages.filter((milestone) => milestone.status === "submitted");
  const pending = stages.filter((milestone) => (milestone.status ?? "pending") === "pending");
  if (task.status === "in_review" && submitted[0]) {
    const stage = submitted[0];
    const lock =
      task.kind === "competitive"
        ? "这一阶段以第一份完成通知为准，结算或退回返工前不能再通知。"
        : "发起结算只拨付这一阶段，任务会回到进行中。";
    return `等待发布者结算阶段 ${stage.id}（${stage.amount} 积分）。${lock}`;
  }
  if (task.status === "active" && pending.length > 0) {
    return `等待执行者通知完成阶段：${pending.map((milestone) => milestone.id).join("、")}。同一时间只能有一个阶段处于待结算。`;
  }
  if (task.status === "open") return "等待执行者接单。注资已经包含全部分阶段金额。";
  return null;
}

export function currentSubmissionsForMilestone<T extends StageSubmission>(
  task: { payment: { mode: string }; submissions?: T[] },
  milestoneId: string | undefined,
): T[] {
  const current = (task.submissions ?? []).filter((submission) => submission.status === "current");
  if (task.payment.mode !== "milestone" || !milestoneId) return current;
  return current.filter((submission) => submission.milestone_id === milestoneId);
}

/** Body for confirm. A string is a client-side error and must not be sent. */
export function confirmTaskBody(
  task: StageTask,
  chosen: string,
  milestoneId: string,
  forcedSubmissionIds: string[] | null,
): ConfirmBody | string {
  const body: ConfirmBody = {};
  let stageId = milestoneId.trim();
  if (task.payment.mode === "milestone") {
    const awaiting = (task.payment.milestones ?? []).filter((milestone) => milestone.status === "submitted");
    if (!stageId && awaiting.length === 1) stageId = awaiting[0]?.id ?? "";
    if (!stageId || !awaiting.some((milestone) => milestone.id === stageId)) return "请选择待结算的阶段";
    body.milestone_id = stageId;
  }
  const relevant = currentSubmissionsForMilestone(task, task.payment.mode === "milestone" ? stageId : undefined);
  if (task.kind === "competitive") {
    if (relevant.length > 1) {
      if (!chosen || !relevant.some((submission) => submission.id === chosen)) return "请选择一份 submission_id";
      body.submission_id = chosen;
    } else if (relevant[0]) {
      body.submission_id = relevant[0].id;
    } else if (forcedSubmissionIds && forcedSubmissionIds.length > 0) {
      if (!chosen || !forcedSubmissionIds.includes(chosen)) return "请选择一份 submission_id";
      body.submission_id = chosen;
    }
    return body;
  }
  if (chosen && forcedSubmissionIds?.includes(chosen)) body.submission_id = chosen;
  return body;
}

/**
 * Current submission for the milestone that is in review.
 * undefined: not a milestone task, keep the caller's existing choice.
 * null: milestone task, but no single in-review submission could be chosen.
 */
export function inReviewMilestoneSubmissionId(task: StageTask, chosen: string): string | null | undefined {
  if (task.payment.mode !== "milestone") return undefined;
  const awaiting = new Set(
    (task.payment.milestones ?? [])
      .filter((milestone) => milestone.status === "submitted" || milestone.status === "disputed")
      .map((milestone) => milestone.id),
  );
  const matches = (task.submissions ?? []).filter(
    (submission) => submission.status === "current" && !!submission.milestone_id && awaiting.has(submission.milestone_id),
  );
  if (matches.length === 1) return matches[0]?.id ?? null;
  if (matches.length > 1) {
    const pick = matches.find((submission) => submission.id === chosen);
    return pick ? pick.id : null;
  }
  return null;
}

/** `{}` for one_shot. A string is a client-side error and must not be sent. */
export function submitMilestoneField(task: StageTask, selected: string): { milestone_id?: string } | string {
  if (task.payment.mode !== "milestone") return {};
  const block = milestoneSubmitBlock(task);
  if (block) return block;
  const id = selected.trim();
  const pending = (task.payment.milestones ?? []).some(
    (milestone) => milestone.id === id && (milestone.status ?? "pending") === "pending",
  );
  if (!pending) return "请选择一个待完成的阶段";
  return { milestone_id: id };
}

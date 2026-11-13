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

export function milestoneSubmitBlock(task: StageTask): string | null {
  if (task.payment.mode !== "milestone") return null;
  const stages = task.payment.milestones ?? [];
  if (stages.some((milestone) => milestone.status === "submitted")) {
    return "已有阶段在待确认。确认或请对方修改后，任务回到进行中才能提交下一阶段。";
  }
  if (!stages.some((milestone) => (milestone.status ?? "pending") === "pending")) {
    return "没有待提交的阶段";
  }
  if (task.status !== "active") return "分阶段任务只有进行中可以提交";
  return null;
}

export function milestoneConfirmBlock(task: StageTask): string | null {
  if (task.payment.mode !== "milestone" || task.status !== "in_review") return null;
  const awaiting = (task.payment.milestones ?? []).filter((milestone) => milestone.status === "submitted");
  if (awaiting.length === 0) return "没有待确认的阶段";
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
        ? "这一阶段以第一份提交为准，确认或退回修改前不能再交。"
        : "确认只拨付这一阶段，任务会回到进行中。";
    return `等待发布者确认阶段 ${stage.id}（${stage.amount} 积分）。${lock}`;
  }
  if (task.status === "active" && pending.length > 0) {
    return `等待执行者提交待完成阶段：${pending.map((milestone) => milestone.id).join("、")}。同一时间只能有一个阶段处于待确认。`;
  }
  if (task.status === "open") return "等待执行者接受。注资已经包含全部分阶段金额。";
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
    if (!stageId || !awaiting.some((milestone) => milestone.id === stageId)) return "请选择待确认的阶段";
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
  if (!pending) return "请选择一个待提交的阶段";
  return { milestone_id: id };
}

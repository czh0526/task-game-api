/**
 * Task detail page: turn (status, viewer) into a plain-language "what now".
 * Pure functions only, so the wording can be tested without rendering React.
 */

import { canInviteTask } from "./stageLoop.ts";

export type ViewerRole = "guest" | "publisher" | "executor" | "observer";

export interface TaskViewTask {
  status: string;
  kind: string;
  publisher_id: string;
  executor_ids?: string[];
  dispute_role_id?: string | null;
  collaborative?: {
    settlement_mode?: string;
    roles?: Array<{ assignee_user_id?: string | null; status?: string }>;
  };
  payment?: { mode: string };
}

export type StageTone = "neutral" | "ok" | "info" | "warn" | "bad";

/** Which side of the task is the viewer on? */
export function viewerRole(task: TaskViewTask, me: string | undefined): ViewerRole {
  if (!me) return "guest";
  if (me === task.publisher_id) return "publisher";
  if ((task.executor_ids ?? []).includes(me)) return "executor";
  if (
    (task.collaborative?.roles ?? []).some(
      (role) => role.assignee_user_id === me && (role.status === "filled" || role.status === "submitted"),
    )
  ) {
    return "executor";
  }
  return "observer";
}

/** One sentence telling the viewer whose turn it is and what happens next. */
export function viewerHint(task: TaskViewTask, me: string | undefined): string {
  const role = viewerRole(task, me);
  const unified = task.collaborative?.settlement_mode !== "per_seat";
  switch (task.status) {
    case "draft":
      return role === "publisher"
        ? "任务还在草稿中。完成注资后，执行者才能看到并接单。"
        : "发布者尚未注资。注资后任务才会对外开放。";
    case "open":
      if (role === "publisher") {
        if (task.kind === "collaborative" && unified) {
          return "席位已经坐满。点击「开始任务」，任务进入进行中。";
        }
        if (!canInviteTask(me, task)) {
          return "等待执行者接单。名额已满，暂时无法邀请新人。";
        }
        return "等待执行者接单。你可以邀请合适的人参与。";
      }
      if (role === "executor") return "任务正在招募。条件合适的话，现在就可以接单。";
      return "任务正在招募执行者。接单后即可参与。";
    case "active":
      if (role === "publisher") return "执行者正在处理。收到完成通知后由你发起结算。";
      if (role === "executor") return "任务进行中。完成后点「通知完成」，等待发布者结算。";
      return "执行者正在处理，通知完成后将进入待结算。";
    case "in_review":
      if (role === "publisher") return "执行者已通知完成，等你发起结算；认为未完成可以请对方返工。";
      if (role === "executor") return "你已通知完成，正在等待发布者发起结算，请留意结果。";
      return "执行者已通知完成，等待发布者发起结算。";
    case "disputed":
      return "任务进入争议处理。双方可以提交协商方案，裁定人可以作出裁定。";
    case "settled":
      return "任务已结算完成，托管金额已全部处理。";
    case "cancelled":
      return "任务已取消，未支付的托管已退回发布者。";
    default:
      return "查看任务信息和参与记录。";
  }
}

/** Colour cue for the status banner; matches the status pill palette. */
export function stageTone(status: string): StageTone {
  switch (status) {
    case "open":
    case "settled":
      return "ok";
    case "active":
      return "info";
    case "in_review":
      return "warn";
    case "disputed":
      return "bad";
    default:
      return "neutral";
  }
}

/** Short label for the viewer's relationship to the task. */
export function viewerLabel(role: ViewerRole): string {
  switch (role) {
    case "publisher":
      return "发布者";
    case "executor":
      return "执行者";
    case "observer":
      return "围观者";
    case "guest":
      return "未登录";
  }
}

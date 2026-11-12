import { isTaskGameError, submissionIdsFromConfirmError, type TaskKind, type TaskStatus } from "taskgame-client";

export const STATUS_LABEL: Record<TaskStatus, string> = {
  draft: "草稿",
  open: "待接受",
  active: "进行中",
  in_review: "待确认",
  disputed: "争议中",
  settled: "已结算",
  cancelled: "已取消",
};

export const KIND_LABEL: Record<TaskKind, string> = {
  exclusive: "独占",
  competitive: "竞争",
  collaborative: "协作",
};

export const ROLE_STATUS_LABEL: Record<string, string> = {
  open: "空席",
  filled: "已入席",
  dropped: "已退出",
  submitted: "已提交",
  settled: "已拨付",
  refunded: "已退回",
  disputed: "争议中",
};

export const SETTLEMENT_MODE_LABEL: Record<string, string> = {
  per_seat: "分别结算",
  unified: "统一结算",
};

export const ENTRY_LABEL: Record<string, string> = {
  hold: "托管",
  release: "拨付",
  refund: "退回",
  freeze: "冻结",
  unfreeze: "解冻",
};

export const PAYMENT_MODE_LABEL: Record<string, string> = {
  one_shot: "一次性",
  milestone: "分阶段",
};

export const MILESTONE_STATUS_LABEL: Record<string, string> = {
  pending: "待提交",
  submitted: "待确认",
  released: "已拨付",
  disputed: "争议中",
};

export function formatCredits(amount: number | null | undefined): string {
  if (amount === null || amount === undefined || Number.isNaN(amount)) return "—";
  return `${amount.toLocaleString("zh-CN")} 积分`;
}

export function formatTime(value: string | null | undefined): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

export function isAbort(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
}

export function formatError(error: unknown): string {
  if (isAbort(error)) return "";
  if (isTaskGameError(error)) {
    return `${error.status} ${error.code}：${error.message}`;
  }
  if (error instanceof Error) {
    if (error.message === "Failed to fetch" || error.message.includes("NetworkError") || error.message.includes("Load failed")) {
      return "无法连接服务。请先在仓库根目录执行 make run，并用本机 Vite 页面打开（默认 http://localhost:5173）。";
    }
    return error.message;
  }
  return "请求失败";
}

export function confirmFailure(error: unknown): { text: string; submissionIds?: string[] } {
  const submissionIds = submissionIdsFromConfirmError(error);
  if (submissionIds) {
    return {
      text: "竞争任务有多份提交。确认需要 submission_id，任务保持 in_review，托管不动。下面按接口 message 的 created_at 顺序列出。",
      submissionIds,
    };
  }
  return { text: formatError(error) };
}

export function parseIntStrict(value: string): number | null {
  const trimmed = value.trim();
  if (!/^-?\d+$/.test(trimmed)) return null;
  const number = Number(trimmed);
  if (!Number.isSafeInteger(number)) return null;
  return number;
}

export function counterparty(userId: string): string {
  if (userId === "usr_publisher") return "usr_executor";
  if (userId === "usr_executor") return "usr_publisher";
  return "usr_publisher";
}

const USER_ID = /^[A-Za-z0-9_.:@-]{1,128}$/;

export function validUserId(value: string): boolean {
  return USER_ID.test(value);
}

/** Create-form payment. Field names match the OpenAPI body. Status is never sent. */

export const MILESTONE_ID = /^[A-Za-z0-9_-]{1,64}$/;
export const MAX_MILESTONES = 20;
export const MAX_CONDITION_BYTES = 500;

export interface MilestoneDraft {
  id: string;
  amount: string;
  condition: string;
}

export interface MilestonePayload {
  id: string;
  amount: number;
  condition: string;
}

export interface PaymentPayload {
  mode: "one_shot" | "milestone";
  amount: number;
  milestones?: MilestonePayload[];
}

export type TaskKindName = "exclusive" | "competitive" | "collaborative";
export type PaymentModeName = "one_shot" | "milestone";

export function parseAmount(value: string): number | null {
  const trimmed = value.trim();
  if (!/^-?\d+$/.test(trimmed)) return null;
  const number = Number(trimmed);
  if (!Number.isSafeInteger(number)) return null;
  return number;
}

/** One stage whose amount matches the typed total, so a single stage is already balanced. */
export function defaultMilestones(amountText: string): MilestoneDraft[] {
  const amount = parseAmount(amountText);
  return [{ id: "m1", amount: amount !== null && amount >= 0 ? String(amount) : "", condition: "" }];
}

export const EXAMPLE_MILESTONES: MilestoneDraft[] = [
  { id: "draft", amount: "200", condition: "交出初稿" },
  { id: "final", amount: "300", condition: "按验收标准交终稿" },
];

export function nextMilestoneId(rows: MilestoneDraft[]): string {
  const used = new Set(rows.map((row) => row.id.trim()));
  for (let n = 1; n <= rows.length + 1; n += 1) {
    const id = `m${n}`;
    if (!used.has(id)) return id;
  }
  return `m${rows.length + 1}`;
}

export function draftedMilestoneTotal(rows: MilestoneDraft[]): { ok: true; sum: number } | { ok: false } {
  let sum = 0;
  for (const row of rows) {
    const amount = parseAmount(row.amount);
    if (amount === null || amount < 0) return { ok: false };
    if (!Number.isSafeInteger(sum + amount)) return { ok: false };
    sum += amount;
  }
  return { ok: true, sum };
}

/** Credits still unassigned. Empty when the rows already meet or exceed the total, or a row is not a number. */
export function remainderAmount(amountText: string, rows: MilestoneDraft[]): string {
  const amount = parseAmount(amountText);
  const drafted = draftedMilestoneTotal(rows);
  if (amount === null || amount < 0 || !drafted.ok || amount <= drafted.sum) return "";
  return String(amount - drafted.sum);
}

/** Integer split of the typed total. Leftover credits go to the last stages. */
export function evenMilestoneAmounts(total: number, count: number): number[] {
  if (count <= 0 || !Number.isSafeInteger(total) || total < 0) return [];
  const base = Math.floor(total / count);
  const extra = total - base * count;
  return Array.from({ length: count }, (_, index) => base + (index >= count - extra ? 1 : 0));
}

function utf8Bytes(value: string): number {
  return new TextEncoder().encode(value).length;
}

export function buildPayment(input: {
  kind: TaskKindName;
  mode: PaymentModeName;
  amountText: string;
  milestones: MilestoneDraft[];
}): PaymentPayload | string {
  const amount = parseAmount(input.amountText);
  if (amount === null || amount < 0) return "金额需要是非负整数";
  // Collaborative tasks cannot be milestone. Drop any draft rows instead of sending them.
  if (input.kind === "collaborative" || input.mode === "one_shot") {
    return { mode: "one_shot", amount };
  }
  return buildMilestones(amount, input.milestones);
}

function buildMilestones(amount: number, rows: MilestoneDraft[]): PaymentPayload | string {
  if (rows.length < 1 || rows.length > MAX_MILESTONES) return "分阶段需要 1 到 20 个阶段";
  const seen = new Set<string>();
  const milestones: MilestonePayload[] = [];
  let sum = 0;
  for (const row of rows) {
    const id = row.id.trim();
    if (!MILESTONE_ID.test(id)) return "阶段 id 需要匹配 [A-Za-z0-9_-]{1,64}";
    if (seen.has(id)) return `阶段 id 重复：${id}`;
    seen.add(id);
    const stageAmount = parseAmount(row.amount);
    if (stageAmount === null || stageAmount < 0) return "阶段金额需要是非负整数";
    if (!Number.isSafeInteger(sum + stageAmount)) return "阶段金额合计超出范围";
    sum += stageAmount;
    const condition = row.condition.trim();
    if (!condition) return "请填写阶段验收条件";
    if (utf8Bytes(condition) > MAX_CONDITION_BYTES) return "阶段条件不能超过 500 字节";
    milestones.push({ id, amount: stageAmount, condition });
  }
  if (sum !== amount) return `阶段金额合计是 ${sum}，需要等于 payment.amount ${amount}`;
  return { mode: "milestone", amount, milestones };
}

export function milestoneSumLabel(amountText: string, rows: MilestoneDraft[]): { text: string; matched: boolean } {
  const drafted = draftedMilestoneTotal(rows);
  const amount = parseAmount(amountText);
  if (!drafted.ok) return { text: "阶段金额需要是非负整数，合计才能和报酬比较。", matched: false };
  if (amount === null || amount < 0) return { text: `阶段合计 ${drafted.sum}。报酬需要是非负整数。`, matched: false };
  if (drafted.sum === amount) return { text: `阶段合计 ${drafted.sum}，与报酬 ${amount} 一致。`, matched: true };
  const gap = amount - drafted.sum;
  const direction = gap > 0 ? `还差 ${gap}` : `多了 ${-gap}`;
  return { text: `阶段合计 ${drafted.sum}，报酬 ${amount}，${direction}。`, matched: false };
}

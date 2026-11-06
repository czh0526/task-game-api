/** UI-only expansion of collaborative role templates into flat API seats. */

export const SHARE_BPS_TOTAL = 10000;
export const MAX_COLLAB_SEATS = 20;

const ROLE_ID = /^[A-Za-z0-9_-]{1,64}$/;

export interface RoleTemplate {
  name: string;
  /** Optional base id. Expanded seats are `{slug}_1` … `{slug}_{count}`. */
  slug: string;
  count: string;
}

export interface ExpandedSeat {
  id: string;
  name: string;
  share_bps: number;
}

/**
 * Split `total` across `seatCount` seats.
 * Each seat gets floor(total / n); the remainder is added 1 each to the last seats
 * so the parts sum to exactly `total`.
 */
export function splitShareBps(seatCount: number, total = SHARE_BPS_TOTAL): number[] {
  if (!Number.isInteger(seatCount) || seatCount <= 0) return [];
  const base = Math.floor(total / seatCount);
  const remainder = total - base * seatCount;
  const firstLarger = seatCount - remainder;
  return Array.from({ length: seatCount }, (_, index) => (index >= firstLarger ? base + 1 : base));
}

function parsePositiveInt(value: string): number | null {
  const trimmed = value.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  const number = Number(trimmed);
  if (!Number.isSafeInteger(number)) return null;
  return number;
}

function seatIds(base: string, count: number): string[] {
  return Array.from({ length: count }, (_, index) => `${base}_${index + 1}`);
}

function idsAvailable(ids: string[], used: Set<string>): boolean {
  return ids.every((id) => ROLE_ID.test(id) && !used.has(id));
}

interface ParsedTemplate {
  name: string;
  base: string;
  count: number;
  explicitBase: boolean;
}

function parseTemplates(templates: RoleTemplate[]): ParsedTemplate[] | string {
  if (templates.length < 1) return "请至少添加一种角色";
  if (templates.length > MAX_COLLAB_SEATS) return "角色种类不能超过 20";

  const parsed: ParsedTemplate[] = [];
  for (const template of templates) {
    const name = template.name.trim();
    if (!name) return "请填写角色名称";
    if (name.length > 80) return "角色名称不能超过 80 个字符";

    const count = parsePositiveInt(template.count);
    if (count === null || count < 1 || count > MAX_COLLAB_SEATS) return "人数需要是 1 到 20 的整数";

    const slug = template.slug.trim();
    if (slug) {
      if (!ROLE_ID.test(slug)) return "基础 id 只能包含字母、数字、下划线和连字符";
      parsed.push({ name, base: slug, count, explicitBase: true });
      continue;
    }
    if (ROLE_ID.test(name)) {
      parsed.push({ name, base: name, count, explicitBase: true });
      continue;
    }
    parsed.push({ name, base: "", count, explicitBase: false });
  }

  const total = parsed.reduce((sum, template) => sum + template.count, 0);
  if (total < 1 || total > MAX_COLLAB_SEATS) return `展开后有 ${total} 个席位，需要 1 到 20 个`;
  return parsed;
}

function allocateBase(template: ParsedTemplate, used: Set<string>, serial: { n: number }): string | { error: string } {
  if (template.explicitBase) {
    const ids = seatIds(template.base, template.count);
    for (const id of ids) {
      if (id.length > 64) return { error: "基础 id 加上序号后超过 64 个字符" };
      if (!ROLE_ID.test(id)) return { error: `展开后的 role id 无效：${id}` };
      if (used.has(id)) return { error: `role id 重复：${id}` };
    }
    return template.base;
  }

  for (let guard = 0; guard < MAX_COLLAB_SEATS + 1; guard += 1) {
    const preferred = serial.n === 1 ? "role" : `role${serial.n}`;
    serial.n += 1;
    const ids = seatIds(preferred, template.count);
    if (idsAvailable(ids, used)) return preferred;
  }
  return { error: "无法为角色生成唯一 role id" };
}

/** Expand templates in order. Share basis points are an even split of 10000. */
export function expandRoleTemplates(templates: RoleTemplate[]): ExpandedSeat[] | string {
  const parsed = parseTemplates(templates);
  if (typeof parsed === "string") return parsed;

  const total = parsed.reduce((sum, template) => sum + template.count, 0);
  const shares = splitShareBps(total);
  const used = new Set<string>();
  const serial = { n: 1 };
  const seats: ExpandedSeat[] = [];
  let shareIndex = 0;

  for (const template of parsed) {
    const allocated = allocateBase(template, used, serial);
    if (typeof allocated !== "string") return allocated.error;
    for (const id of seatIds(allocated, template.count)) {
      used.add(id);
      const share = shares[shareIndex];
      if (share === undefined) return "无法分配 share_bps";
      seats.push({ id, name: template.name, share_bps: share });
      shareIndex += 1;
    }
  }

  return seats;
}

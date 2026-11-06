import { useState } from "react";
import {
  createIdempotencyKey,
  type CollaborativeRole,
  type Task,
  type TaskCreate,
  type TaskKind,
} from "taskgame-client";
import { useApp } from "../context";
import { KIND_LABEL, formatError, parseIntStrict } from "../format";
import { href } from "../route";
import { type ExpandedSeat, type RoleTemplate, SHARE_BPS_TOTAL, expandRoleTemplates, templatesFromSeats } from "../roles";
import { PRESETS } from "../settings";
import { Button, ErrorNote, Field, KindPill, OkNote, Screen, StatusPill } from "../ui";

interface FormState {
  kind: TaskKind;
  title: string;
  description: string;
  amount: string;
  deadline: string;
  quality: string;
  reviewHours: string;
  slots: string;
  templates: RoleTemplate[];
  adjustShares: boolean;
  shareDrafts: string[];
}

const DEFAULT_TEMPLATES: RoleTemplate[] = [{ name: "设计", slug: "design", count: "4" }];

const EXAMPLE_TEMPLATES: RoleTemplate[] = [
  { name: "设计", slug: "design", count: "4" },
  { name: "文案", slug: "copy", count: "2" },
];

export function CreateTask() {
  const { client, clientError, settings } = useApp();
  const [form, setForm] = useState<FormState>(() => ({
    kind: "exclusive",
    title: "",
    description: "",
    amount: "500",
    deadline: defaultDeadlineLocal(),
    quality: "达到书面验收标准",
    reviewHours: "72",
    slots: "2",
    templates: DEFAULT_TEMPLATES.map((template) => ({ ...template })),
    adjustShares: false,
    shareDrafts: [],
  }));
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState<"create" | "fund" | null>(null);
  const [created, setCreated] = useState<Task | null>(null);

  const expanded = expandRoleTemplates(form.templates);
  const shareDrafts = shareDraftsFor(form, expanded);
  const shareSum = typeof expanded === "string" ? null : sumShareDrafts(shareDrafts);

  function patch(partial: Partial<FormState>) {
    setCreated(null);
    setForm((prev) => ({ ...prev, ...partial }));
  }

  function fillExample(kind: TaskKind) {
    const common = {
      kind,
      amount: "500",
      deadline: defaultDeadlineLocal(),
      quality: "达到书面验收标准",
      reviewHours: "72",
    };
    if (kind === "exclusive") {
      patch({
        ...common,
        title: "翻译发布说明",
        description: "把 v0.1 发布说明从中文译成英文。",
      });
      return;
    }
    if (kind === "competitive") {
      patch({
        ...common,
        title: "选定发布标题",
        description: "两名执行者各提交一份。发布者确认时选定 submission_id。结算规则是 winner_take_all。",
        slots: "2",
      });
      return;
    }
    patch({
      ...common,
      title: "撰写发布页",
      description: "4 名设计、2 名文案。退出后席位重新开放，退出者佣金和能力分都是 0。",
      templates: EXAMPLE_TEMPLATES.map((template) => ({ ...template })),
      adjustShares: false,
      shareDrafts: [],
    });
  }

  function updateTemplate(index: number, partial: Partial<RoleTemplate>) {
    setCreated(null);
    setForm((prev) => withTemplates(prev, prev.templates.map((template, i) => (i === index ? { ...template, ...partial } : template))));
  }

  function editShare(index: number, value: string) {
    setCreated(null);
    setForm((prev) => {
      const seats = expandRoleTemplates(prev.templates);
      const drafts = shareDraftsFor(prev, seats);
      if (drafts.length === 0) return prev;
      const next = [...drafts];
      next[index] = value;
      return { ...prev, adjustShares: true, shareDrafts: next };
    });
  }

  async function onCreate() {
    if (!client) return;
    const built = buildTask(form);
    if (typeof built === "string") {
      setError(built);
      return;
    }
    setPending("create");
    setError(null);
    try {
      const task = await client.createTask(built, { idempotencyKey: createIdempotencyKey("create") });
      setCreated(task);
    } catch (err) {
      setError(formatError(err));
    } finally {
      setPending(null);
    }
  }

  async function onFund() {
    if (!client || !created) return;
    setPending("fund");
    setError(null);
    try {
      const task = await client.fundTask(created.id, { idempotencyKey: createIdempotencyKey("fund") });
      setCreated(task);
    } catch (err) {
      setError(formatError(err));
    } finally {
      setPending(null);
    }
  }

  const actingAsPublisher = settings.userId === PRESETS.publisher.userId && settings.authMode === "apiKey" && settings.apiKey === PRESETS.publisher.apiKey;

  return (
    <Screen title="创建任务" lede="创建后状态是 draft。注资把 payment.amount 放进托管，状态变为 open。">
      <ErrorNote message={client ? error : clientError} />
      {!actingAsPublisher ? (
        <p className="banner warn">
          当前调用身份是 <code className="mono">{settings.userId || "未填写"}</code>
          。任务的 publisher_id 以密钥对应的用户为准。演示三条路径时，先在左侧切换到发布者开发密钥。
        </p>
      ) : null}

      <div className="tabs">
        {(["exclusive", "competitive", "collaborative"] as const).map((kind) => (
          <button key={kind} type="button" className={form.kind === kind ? "tab on" : "tab"} onClick={() => patch({ kind })}>
            {KIND_LABEL[kind]} {kind}
          </button>
        ))}
        <button type="button" className="btn ghost" onClick={() => fillExample(form.kind)}>
          填入示例
        </button>
      </div>

      <form
        className="form"
        onSubmit={(event) => {
          event.preventDefault();
          void onCreate();
        }}
      >
        <Field label="标题 title">
          <input value={form.title} maxLength={200} onChange={(event) => patch({ title: event.target.value })} required />
        </Field>
        <Field label="说明 description">
          <textarea value={form.description} rows={4} maxLength={20000} onChange={(event) => patch({ description: event.target.value })} required />
        </Field>
        <div className="form-grid">
          <Field label="金额 payment.amount" hint="模拟积分，one_shot">
            <input value={form.amount} inputMode="numeric" onChange={(event) => patch({ amount: event.target.value })} required />
          </Field>
          <Field label="验收截止 acceptance.deadline">
            <input type="datetime-local" value={form.deadline} onChange={(event) => patch({ deadline: event.target.value })} required />
          </Field>
          <Field label="复核时限 review_hours" hint="默认 72，范围 1–2160">
            <input value={form.reviewHours} inputMode="numeric" onChange={(event) => patch({ reviewHours: event.target.value })} />
          </Field>
        </div>
        <Field label="验收标准 acceptance.quality">
          <input value={form.quality} maxLength={2000} onChange={(event) => patch({ quality: event.target.value })} required />
        </Field>
        <p className="hint">
          payment.mode = <code className="mono">one_shot</code>，assignee_policy.type = <code className="mono">open</code>
        </p>

        {form.kind === "competitive" ? (
          <div className="card">
            <h2>竞争 competitive</h2>
            <Field label="名额 slots" hint="2–100。结算规则固定 winner_take_all">
              <input value={form.slots} inputMode="numeric" onChange={(event) => patch({ slots: event.target.value })} />
            </Field>
            <p className="hint">settlement_rule = winner_take_all。多名执行者都提交后，确认必须带 submission_id。</p>
          </div>
        ) : null}

        {form.kind === "collaborative" ? (
          <div className="card">
            <h2>协作 collaborative</h2>
            <p className="hint">
              on_dropout = replace。按「角色 × 人数」填写。未勾选手动调整时，请求发送角色模板：quantity 是人数，share_bps 是这一角色的合计，各角色相加为 10000。服务端展开席位。人数为 1 时席位 id 就是基础 id；大于 1 时是 <code className="mono">基础id_1</code> … <code className="mono">基础id_人数</code>。10000 在全部席位间均分，余数在最后几席。
            </p>
            {form.templates.map((template, index) => (
              <div className="role-row" key={index}>
                <Field label="显示名称 name">
                  <input
                    value={template.name}
                    maxLength={80}
                    placeholder="设计"
                    onChange={(event) => updateTemplate(index, { name: event.target.value })}
                  />
                </Field>
                <Field label="基础 id">
                  <input
                    value={template.slug}
                    maxLength={61}
                    spellCheck={false}
                    placeholder="可选，如 design"
                    onChange={(event) => updateTemplate(index, { slug: event.target.value })}
                  />
                </Field>
                <Field label="人数 count">
                  <input
                    value={template.count}
                    inputMode="numeric"
                    placeholder="4"
                    onChange={(event) => updateTemplate(index, { count: event.target.value })}
                  />
                </Field>
                <Button
                  type="button"
                  kind="ghost"
                  disabled={form.templates.length <= 1}
                  onClick={() => {
                    setCreated(null);
                    setForm((prev) => withTemplates(prev, prev.templates.filter((_, i) => i !== index)));
                  }}
                >
                  移除
                </Button>
              </div>
            ))}
            <Button
              type="button"
              kind="secondary"
              disabled={form.templates.length >= 20 || (typeof expanded !== "string" && expanded.length >= 20)}
              onClick={() => {
                setCreated(null);
                setForm((prev) => withTemplates(prev, [...prev.templates, { name: "", slug: "", count: "1" }]));
              }}
            >
              增加角色
            </Button>

            <h3>展开预览</h3>
            {typeof expanded === "string" ? (
              <p className="banner warn">{expanded}</p>
            ) : (
              <>
                <p className={shareSum === SHARE_BPS_TOTAL ? "hint" : "banner warn"}>
                  共 {expanded.length} 席，share_bps 合计 {shareSum === null ? "无效" : shareSum}
                  {shareSum === SHARE_BPS_TOTAL ? "。" : "，需要 10000。"}
                  {form.adjustShares ? " 手动调整后按逐席 roles[] 提交。" : " 创建请求只发送角色模板，由服务端展开。"}
                </p>
                <table className="seat-table">
                  <thead>
                    <tr>
                      <th>role_id</th>
                      <th>名称</th>
                      <th>share_bps</th>
                    </tr>
                  </thead>
                  <tbody>
                    {expanded.map((seat, index) => {
                      const draft = shareDrafts[index] ?? String(seat.share_bps);
                      const share = parseIntStrict(draft);
                      return (
                        <tr key={seat.id}>
                          <td>
                            <code className="mono">{seat.id}</code>
                          </td>
                          <td>{seat.name}</td>
                          <td>
                            {form.adjustShares ? (
                              <input
                                aria-label={`${seat.id} share_bps`}
                                value={draft}
                                inputMode="numeric"
                                onChange={(event) => editShare(index, event.target.value)}
                              />
                            ) : (
                              draft
                            )}{" "}
                            <span className="en">{share === null ? "—" : `${(share / 100).toFixed(2)}%`}</span>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
                <label className="check-line">
                  <input
                    type="checkbox"
                    checked={form.adjustShares}
                    onChange={() => {
                      setCreated(null);
                      setForm((prev) => {
                        if (prev.adjustShares) return { ...prev, adjustShares: false, shareDrafts: [] };
                        const seats = expandRoleTemplates(prev.templates);
                        return {
                          ...prev,
                          adjustShares: true,
                          shareDrafts: typeof seats === "string" ? [] : seats.map((seat) => String(seat.share_bps)),
                        };
                      });
                    }}
                  />
                  手动调整各席 share_bps
                </label>
              </>
            )}
          </div>
        ) : null}

        <div className="toolbar">
          <Button type="submit" kind="primary" pending={pending === "create"} disabled={!client || pending !== null}>
            创建草稿
          </Button>
        </div>
      </form>

      {created ? (
        <div className="card result">
          <div className="task-top">
            <StatusPill status={created.status} />
            <KindPill kind={created.kind} />
          </div>
          <h2>{created.title}</h2>
          <p>
            <code className="mono">{created.id}</code>
          </p>
          <OkNote message={created.status === "draft" ? "草稿已创建，可以注资。" : `当前状态 ${created.status}。`} />
          <div className="toolbar">
            <Button type="button" kind="primary" pending={pending === "fund"} disabled={created.status !== "draft" || pending !== null} onClick={() => void onFund()}>
              注资
            </Button>
            <a className="btn secondary" href={href.task(created.id)}>
              打开详情
            </a>
          </div>
        </div>
      ) : null}
    </Screen>
  );
}

function withTemplates(prev: FormState, templates: RoleTemplate[]): FormState {
  const previousKey = seatKey(prev.templates);
  const nextKey = seatKey(templates);
  const keepDrafts = prev.adjustShares && previousKey !== null && previousKey === nextKey;
  return { ...prev, templates, shareDrafts: keepDrafts ? prev.shareDrafts : [] };
}

function seatKey(templates: RoleTemplate[]): string | null {
  const seats = expandRoleTemplates(templates);
  if (typeof seats === "string") return null;
  return seats.map((seat) => seat.id).join("\n");
}

function shareDraftsFor(form: FormState, expanded: ExpandedSeat[] | string): string[] {
  if (typeof expanded === "string") return [];
  if (form.adjustShares && form.shareDrafts.length === expanded.length) return form.shareDrafts;
  return expanded.map((seat) => String(seat.share_bps));
}

function sumShareDrafts(drafts: string[]): number | null {
  let sum = 0;
  for (const draft of drafts) {
    const share = parseIntStrict(draft);
    if (share === null) return null;
    sum += share;
  }
  return sum;
}

function buildTask(form: FormState): TaskCreate | string {
  const title = form.title.trim();
  const description = form.description.trim();
  const quality = form.quality.trim();
  if (!title) return "请填写标题";
  if (!description) return "请填写说明";
  if (!quality) return "请填写验收标准";
  const amount = parseIntStrict(form.amount);
  if (amount === null || amount < 0) return "金额需要是非负整数";
  const reviewHours = parseIntStrict(form.reviewHours);
  if (reviewHours === null || reviewHours < 1 || reviewHours > 2160) return "review_hours 需要是 1 到 2160 的整数";
  let deadline: string;
  try {
    deadline = localInputToRfc3339(form.deadline);
  } catch (error) {
    return error instanceof Error ? error.message : "截止时间无效";
  }
  const acceptance = { deadline, quality, review_hours: reviewHours };
  const payment = { mode: "one_shot" as const, amount };
  const assignee_policy = { type: "open" as const };
  const common = { title, description, payment, acceptance, assignee_policy };
  if (form.kind === "exclusive") return { kind: "exclusive", ...common };
  if (form.kind === "competitive") {
    const slots = parseIntStrict(form.slots);
    if (slots === null || slots < 2 || slots > 100) return "slots 需要是 2 到 100 的整数";
    return { kind: "competitive", ...common, competitive: { slots, settlement_rule: "winner_take_all" } };
  }
  const roles = collaborativeRoles(form);
  if (typeof roles === "string") return roles;
  return { kind: "collaborative", ...common, collaborative: { on_dropout: "replace", roles } };
}

function collaborativeRoles(form: FormState): CollaborativeRole[] | string {
  const expanded = expandRoleTemplates(form.templates);
  if (typeof expanded === "string") return expanded;
  const drafts = shareDraftsFor(form, expanded);
  const shares: number[] = [];
  let sum = 0;
  for (let index = 0; index < expanded.length; index += 1) {
    const share = parseIntStrict(drafts[index] ?? "");
    if (share === null || share < 0 || share > SHARE_BPS_TOTAL) return "share_bps 需要是 0 到 10000 的整数";
    sum += share;
    shares.push(share);
  }
  if (expanded.length < 1 || expanded.length > 20) return "协作任务需要 1 到 20 个席位";
  if (sum !== SHARE_BPS_TOTAL) return `share_bps 合计是 ${sum}，需要 10000`;
  if (form.adjustShares) {
    return expanded.map((seat, index) => ({
      id: seat.id,
      name: seat.name,
      share_bps: shares[index] ?? 0,
    }));
  }
  return templatesFromSeats(expanded.map((seat, index) => ({ ...seat, share_bps: shares[index] ?? seat.share_bps })));
}

function defaultDeadlineLocal(): string {
  const date = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000);
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function localInputToRfc3339(value: string): string {
  if (!value) throw new Error("请填写验收截止时间");
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) throw new Error("截止时间无效");
  if (date.getTime() <= Date.now()) throw new Error("验收截止时间需要晚于现在");
  return date.toISOString();
}

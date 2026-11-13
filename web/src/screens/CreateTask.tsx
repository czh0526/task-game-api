import { useState } from "react";
import {
  createIdempotencyKey,
  type AssigneePolicy,
  type CollaborativeRole,
  type SettlementMode,
  type Task,
  type TaskCreate,
  type TaskKind,
} from "taskgame-client";
import { useApp } from "../context";
import { KIND_LABEL, SETTLEMENT_MODE_LABEL, formatError, parseIntStrict, validUserId } from "../format";
import {
  EXAMPLE_MILESTONES,
  MAX_MILESTONES,
  buildPayment,
  defaultMilestones,
  evenMilestoneAmounts,
  milestoneSumLabel,
  nextMilestoneId,
  remainderAmount,
} from "../paymentForm";
import type { MilestoneDraft, PaymentModeName } from "../paymentForm";
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
  policy: "open" | "specific" | "criteria";
  policyUsers: string;
  minAbility: string;
  settlementMode: SettlementMode;
  templates: RoleTemplate[];
  adjustShares: boolean;
  shareDrafts: string[];
  paymentMode: PaymentModeName;
  milestones: MilestoneDraft[];
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
    policy: "open",
    policyUsers: "",
    minAbility: "0",
    settlementMode: "unified",
    templates: DEFAULT_TEMPLATES.map((template) => ({ ...template })),
    adjustShares: false,
    shareDrafts: [],
    paymentMode: "one_shot",
    milestones: [],
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
      ...(kind !== "collaborative" && form.paymentMode === "milestone"
        ? { milestones: EXAMPLE_MILESTONES.map((row) => ({ ...row })) }
        : {}),
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
      paymentMode: "one_shot",
      title: "撰写发布页",
      description: "4 名设计、2 名文案。默认统一结算：坐满后由发布者开始，全部提交后再一次确认。退出后席位重新开放，退出者佣金和能力分都是 0。",
      settlementMode: "unified",
      templates: EXAMPLE_TEMPLATES.map((template) => ({ ...template })),
      adjustShares: false,
      shareDrafts: [],
    });
  }

  function updateTemplate(index: number, partial: Partial<RoleTemplate>) {
    setCreated(null);
    setForm((prev) => withTemplates(prev, prev.templates.map((template, i) => (i === index ? { ...template, ...partial } : template))));
  }

  function updateMilestone(index: number, partial: Partial<MilestoneDraft>) {
    setCreated(null);
    setForm((prev) => ({
      ...prev,
      milestones: prev.milestones.map((row, i) => (i === index ? { ...row, ...partial } : row)),
    }));
  }

  function removeMilestone(index: number) {
    setCreated(null);
    setForm((prev) => ({ ...prev, milestones: prev.milestones.filter((_, i) => i !== index) }));
  }

  function addMilestone() {
    setCreated(null);
    setForm((prev) => {
      if (prev.milestones.length >= MAX_MILESTONES) return prev;
      return {
        ...prev,
        milestones: [
          ...prev.milestones,
          { id: nextMilestoneId(prev.milestones), amount: remainderAmount(prev.amount, prev.milestones), condition: "" },
        ],
      };
    });
  }

  function splitMilestones() {
    const amount = parseIntStrict(form.amount);
    if (amount === null || amount < 0 || form.milestones.length === 0) {
      setError("金额需要是非负整数，才能均分到各阶段");
      return;
    }
    const parts = evenMilestoneAmounts(amount, form.milestones.length);
    setError(null);
    setCreated(null);
    setForm((prev) => ({
      ...prev,
      milestones: prev.milestones.map((row, index) => ({ ...row, amount: String(parts[index] ?? 0) })),
    }));
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
  const milestoneMode = form.kind !== "collaborative" && form.paymentMode === "milestone";
  const sumLine = milestoneSumLabel(form.amount, form.milestones);

  return (
    <Screen title="创建任务" lede="创建后状态是 draft。注资把 payment.amount 放进托管，状态变为 open。分阶段任务同样一次注资全部金额。">
      <ErrorNote message={client ? error : clientError} />
      {!actingAsPublisher ? (
        <p className="banner warn">
          当前调用身份是 <code className="mono">{settings.userId || "未填写"}</code>
          。任务的 publisher_id 以密钥对应的用户为准。演示三条路径时，先在左侧切换到发布者开发密钥。
        </p>
      ) : null}

      <div className="tabs">
        {(["exclusive", "competitive", "collaborative"] as const).map((kind) => (
          <button
            key={kind}
            type="button"
            className={form.kind === kind ? "tab on" : "tab"}
            onClick={() => patch(kind === "collaborative" ? { kind, paymentMode: "one_shot" } : { kind })}
          >
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
          <Field
            label="金额 payment.amount"
            hint={milestoneMode ? "模拟积分。这是托管总额，各阶段 amount 相加必须等于它" : "模拟积分，一次性拨付"}
          >
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
        <fieldset>
          <legend>谁可以接 assignee_policy</legend>
          {(
            [
              ["open", "开放"],
              ["criteria", "能力门槛"],
              ["specific", "指定人"],
            ] as const
          ).map(([value, label]) => (
            <label className="choice" key={value}>
              <input
                type="radio"
                name="policy"
                value={value}
                checked={form.policy === value}
                onChange={() => patch({ policy: value })}
              />
              <span>
                {label} <span className="en">{value}</span>
              </span>
            </label>
          ))}
        </fieldset>
        {form.policy === "specific" ? (
          <Field label="指定用户 user_ids" hint="用逗号或空格分隔。注资后会给他们发邀请。">
            <input
              value={form.policyUsers}
              spellCheck={false}
              placeholder="usr_executor"
              onChange={(event) => patch({ policyUsers: event.target.value })}
            />
          </Field>
        ) : null}
        {form.policy === "criteria" ? (
          <Field label="最低能力分 min_ability" hint="接任务时能力分必须达到这个数。">
            <input value={form.minAbility} inputMode="numeric" onChange={(event) => patch({ minAbility: event.target.value })} />
          </Field>
        ) : null}
        {form.kind === "collaborative" ? (
          <p className="hint">
            协作任务固定 <code className="mono">payment.mode = one_shot</code>。分阶段只用于独占和竞争。
          </p>
        ) : (
          <fieldset>
            <legend>报酬方式 payment.mode</legend>
            {(
              [
                ["one_shot", "一次性"],
                ["milestone", "分阶段"],
              ] as const
            ).map(([value, label]) => (
              <label className="choice" key={value}>
                <input
                  type="radio"
                  name="payment_mode"
                  value={value}
                  checked={form.paymentMode === value}
                  onChange={() => {
                    if (value === "milestone" && form.milestones.length === 0) {
                      patch({ paymentMode: value, milestones: defaultMilestones(form.amount) });
                      return;
                    }
                    patch({ paymentMode: value });
                  }}
                />
                <span>
                  {label} <span className="en">{value}</span>
                </span>
              </label>
            ))}
          </fieldset>
        )}

        {milestoneMode ? (
          <div className="card">
            <h2>分阶段 milestones</h2>
            <p className="hint">
              先填报酬总额，再把金额拆到 1–20 个阶段。每个阶段需要 id（<code className="mono">[A-Za-z0-9_-]</code>，任务内唯一）、amount（非负整数）和 condition（必填，最多 500 字节）。请求不带 status，服务端记为 pending。阶段合计必须等于 payment.amount。增加阶段时，还没分完的差额会填进新行。确认时只拨付该阶段，注资仍是总额这一笔。
              {form.kind === "competitive" ? " 竞争任务里，某一阶段的第一份提交进入待确认，其他人要等这一阶段确认或退回后才能再交。" : ""}
            </p>
            <p className={sumLine.matched ? "hint" : "banner warn"}>{sumLine.text}</p>
            {form.milestones.map((row, index) => (
              <div className="milestone-row" key={index}>
                <Field label="阶段 id">
                  <input
                    value={row.id}
                    maxLength={64}
                    spellCheck={false}
                    placeholder="m1"
                    onChange={(event) => updateMilestone(index, { id: event.target.value })}
                  />
                </Field>
                <Field label="金额 amount">
                  <input
                    value={row.amount}
                    inputMode="numeric"
                    onChange={(event) => updateMilestone(index, { amount: event.target.value })}
                  />
                </Field>
                <Field label="条件 condition">
                  <input
                    value={row.condition}
                    maxLength={500}
                    placeholder="这一阶段怎样算完成"
                    onChange={(event) => updateMilestone(index, { condition: event.target.value })}
                  />
                </Field>
                <Button type="button" kind="ghost" disabled={form.milestones.length <= 1} onClick={() => removeMilestone(index)}>
                  移除
                </Button>
              </div>
            ))}
            <div className="toolbar">
              <Button type="button" kind="secondary" disabled={form.milestones.length >= MAX_MILESTONES} onClick={addMilestone}>
                增加阶段
              </Button>
              <Button type="button" kind="ghost" onClick={splitMilestones}>
                按报酬均分
              </Button>
            </div>
          </div>
        ) : null}

        {form.kind === "competitive" ? (
          <div className="card">
            <h2>竞争 competitive</h2>
            <Field label="名额 slots" hint="2–100。结算规则固定 winner_take_all">
              <input value={form.slots} inputMode="numeric" onChange={(event) => patch({ slots: event.target.value })} />
            </Field>
            <p className="hint">
              settlement_rule = winner_take_all。一次性支付时，多名执行者都提交后，确认必须带 submission_id。分阶段时，每一阶段以第一份提交进入待确认，确认带 milestone_id。
            </p>
          </div>
        ) : null}

        {form.kind === "collaborative" ? (
          <div className="card">
            <h2>协作 collaborative</h2>
            <fieldset>
              <legend>结算方式 settlement_mode</legend>
              {(["unified", "per_seat"] as const).map((mode) => (
                <label className="choice" key={mode}>
                  <input
                    type="radio"
                    name="settlement_mode"
                    value={mode}
                    checked={form.settlementMode === mode}
                    onChange={() => patch({ settlementMode: mode })}
                  />
                  <span>
                    {SETTLEMENT_MODE_LABEL[mode]} <span className="en">{mode}</span>
                  </span>
                </label>
              ))}
            </fieldset>
            <p className="hint">
              {form.settlementMode === "per_seat"
                ? "分别结算：任一席位入席后即可提交。确认时带 role_id，只拨付该席份额，其余继续托管。空席确认会把该份额退回发布者。不需要 start。"
                : "统一结算（默认）：席位在 open 时坐满，发布者 start 后才开始。每一席都提交后才能一次确认。开始前退出任务仍是 open；开始后退出席位重开，任务保持 active。"}
              {" "}
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
  const payment = buildPayment({
    kind: form.kind,
    mode: form.paymentMode,
    amountText: form.amount,
    milestones: form.milestones,
  });
  if (typeof payment === "string") return payment;
  const reviewHours = parseIntStrict(form.reviewHours);
  if (reviewHours === null || reviewHours < 1 || reviewHours > 2160) return "review_hours 需要是 1 到 2160 的整数";
  let deadline: string;
  try {
    deadline = localInputToRfc3339(form.deadline);
  } catch (error) {
    return error instanceof Error ? error.message : "截止时间无效";
  }
  const acceptance = { deadline, quality, review_hours: reviewHours };
  let assignee_policy: AssigneePolicy;
  if (form.policy === "specific") {
    const userIds = form.policyUsers
      .split(/[\s,]+/)
      .map((item) => item.trim())
      .filter(Boolean);
    if (userIds.length === 0) return "指定人需要至少一个用户 id";
    if (userIds.some((id) => !validUserId(id))) return "user_ids 里有无效的用户 id";
    assignee_policy = { type: "specific", user_ids: userIds };
  } else if (form.policy === "criteria") {
    const minAbility = parseIntStrict(form.minAbility);
    if (minAbility === null || minAbility < 0) return "min_ability 需要是非负整数";
    assignee_policy = { type: "criteria", min_ability: minAbility };
  } else {
    assignee_policy = { type: "open" };
  }
  const common = { title, description, payment, acceptance, assignee_policy };
  if (form.kind === "exclusive") return { kind: "exclusive", ...common };
  if (form.kind === "competitive") {
    const slots = parseIntStrict(form.slots);
    if (slots === null || slots < 2 || slots > 100) return "slots 需要是 2 到 100 的整数";
    return { kind: "competitive", ...common, competitive: { slots, settlement_rule: "winner_take_all" } };
  }
  const roles = collaborativeRoles(form);
  if (typeof roles === "string") return roles;
  return {
    kind: "collaborative",
    ...common,
    collaborative: { on_dropout: "replace", settlement_mode: form.settlementMode, roles },
  };
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

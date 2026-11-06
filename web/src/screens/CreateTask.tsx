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
import { PRESETS } from "../settings";
import { Button, ErrorNote, Field, KindPill, OkNote, Screen, StatusPill } from "../ui";

interface RoleDraft {
  id: string;
  name: string;
  share: string;
}

interface FormState {
  kind: TaskKind;
  title: string;
  description: string;
  amount: string;
  deadline: string;
  quality: string;
  reviewHours: string;
  slots: string;
  roles: RoleDraft[];
}

const EMPTY_ROLES: RoleDraft[] = [
  { id: "design", name: "Design", share: "6000" },
  { id: "copy", name: "Copy", share: "4000" },
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
    roles: EMPTY_ROLES.map((role) => ({ ...role })),
  }));
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState<"create" | "fund" | null>(null);
  const [created, setCreated] = useState<Task | null>(null);

  const shareSum = form.roles.reduce((sum, role) => sum + (parseIntStrict(role.share) ?? 0), 0);

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
      description: "设计席和文案席。退出后席位重新开放，退出者佣金和能力分都是 0。",
      roles: EMPTY_ROLES.map((role) => ({ ...role })),
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
            <p className="hint">on_dropout = replace。各席 share_bps 合计必须是 10000。当前合计 {shareSum}。</p>
            {form.roles.map((role, index) => (
              <div className="role-row" key={index}>
                <Field label="role id">
                  <input
                    value={role.id}
                    maxLength={64}
                    spellCheck={false}
                    onChange={(event) => updateRole(setForm, index, { id: event.target.value })}
                  />
                </Field>
                <Field label="名称 name">
                  <input value={role.name} maxLength={80} onChange={(event) => updateRole(setForm, index, { name: event.target.value })} />
                </Field>
                <Field label="share_bps">
                  <input value={role.share} inputMode="numeric" onChange={(event) => updateRole(setForm, index, { share: event.target.value })} />
                </Field>
                <Button
                  type="button"
                  kind="ghost"
                  disabled={form.roles.length <= 1}
                  onClick={() => setForm((prev) => ({ ...prev, roles: prev.roles.filter((_, i) => i !== index) }))}
                >
                  移除
                </Button>
              </div>
            ))}
            <Button
              type="button"
              kind="secondary"
              disabled={form.roles.length >= 20}
              onClick={() => setForm((prev) => ({ ...prev, roles: [...prev.roles, { id: "", name: "", share: "" }] }))}
            >
              增加席位
            </Button>
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

function updateRole(setForm: (updater: (prev: FormState) => FormState) => void, index: number, partial: Partial<RoleDraft>) {
  setForm((prev) => ({
    ...prev,
    roles: prev.roles.map((role, i) => (i === index ? { ...role, ...partial } : role)),
  }));
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
  const roles: CollaborativeRole[] = [];
  const seen = new Set<string>();
  let sum = 0;
  for (const role of form.roles) {
    const id = role.id.trim();
    const name = role.name.trim();
    const share = parseIntStrict(role.share);
    if (!/^[A-Za-z0-9_-]{1,64}$/.test(id)) return "role id 只能包含字母、数字、下划线和连字符";
    if (seen.has(id)) return `role id 重复：${id}`;
    seen.add(id);
    if (!name) return "请填写席位名称";
    if (share === null || share < 0 || share > 10000) return "share_bps 需要是 0 到 10000 的整数";
    sum += share;
    roles.push({ id, name, share_bps: share });
  }
  if (roles.length < 1 || roles.length > 20) return "协作任务需要 1 到 20 个席位";
  if (sum !== 10000) return `share_bps 合计是 ${sum}，需要 10000`;
  return { kind: "collaborative", ...common, collaborative: { on_dropout: "replace", roles } };
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

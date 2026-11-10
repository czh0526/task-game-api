import { useEffect, useState } from "react";
import { createIdempotencyKey, type Task } from "taskgame-client";
import { useApp } from "../context";
import {
  ENTRY_LABEL,
  PAYMENT_MODE_LABEL,
  ROLE_STATUS_LABEL,
  SETTLEMENT_MODE_LABEL,
  confirmFailure,
  formatCredits,
  formatError,
  formatTime,
  isAbort,
} from "../format";
import { href } from "../route";
import { ActionRow, Button, ErrorNote, Field, IdText, KindPill, Screen, StatusPill } from "../ui";

export function TaskDetail({ id }: { id: string }) {
  const { client, clientError, settings } = useApp();
  const [task, setTask] = useState<Task | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const [submissionIds, setSubmissionIds] = useState<string[] | null>(null);
  const [chosen, setChosen] = useState("");
  const [roleId, setRoleId] = useState("");
  const [confirmSeat, setConfirmSeat] = useState("");
  const [payloadText, setPayloadText] = useState('{\n  "body": ""\n}');
  const [note, setNote] = useState("");
  const [milestoneId, setMilestoneId] = useState("");
  const [reason, setReason] = useState("");

  useEffect(() => {
    if (!client) return;
    const ac = new AbortController();
    setLoading(true);
    setError(null);
    client
      .getTask(id, { signal: ac.signal })
      .then((next) => {
        if (!ac.signal.aborted) setTask(next);
      })
      .catch((err: unknown) => {
        if (isAbort(err)) return;
        setError(formatError(err));
      })
      .finally(() => {
        if (!ac.signal.aborted) setLoading(false);
      });
    return () => ac.abort();
  }, [client, id]);

  useEffect(() => {
    if (!task) return;
    const open = (task.collaborative?.roles ?? []).filter((role) => (role.status ?? "open") === "open");
    setRoleId((prev) => (open.some((role) => role.id === prev) ? prev : (open[0]?.id ?? "")));
    const payable = (task.collaborative?.roles ?? []).filter((role) => {
      const status = role.status ?? "open";
      return status === "submitted" || status === "open";
    });
    setConfirmSeat((prev) => {
      if (payable.some((role) => role.id === prev)) return prev;
      return payable.find((role) => role.status === "submitted")?.id ?? payable[0]?.id ?? "";
    });
  }, [task]);

  async function run(name: string, fn: () => Promise<Task>) {
    setPending(name);
    setError(null);
    try {
      const next = await fn();
      setTask(next);
      if (name === "confirm") setSubmissionIds(null);
    } catch (err) {
      if (name === "confirm") {
        const failure = confirmFailure(err);
        if (failure.submissionIds) {
          setSubmissionIds(failure.submissionIds);
          setChosen((prev) => (failure.submissionIds?.includes(prev) ? prev : (failure.submissionIds?.[0] ?? "")));
          setError(failure.text);
          if (client) {
            try {
              setTask(await client.getTask(id));
            } catch {
              // keep the task we already showed
            }
          }
          return;
        }
      }
      setError(formatError(err));
    } finally {
      setPending(null);
    }
  }

  if (!client) {
    return (
      <Screen title="任务详情">
        <ErrorNote message={clientError} />
      </Screen>
    );
  }
  if (loading && !task) {
    return (
      <Screen title="任务详情">
        <p className="empty">正在读取 {id} …</p>
      </Screen>
    );
  }
  if (!task) {
    return (
      <Screen title="任务详情">
        <ErrorNote message={error ?? "没有读到任务"} />
        <a className="btn secondary" href={href.tasks}>
          返回列表
        </a>
      </Screen>
    );
  }

  const me = settings.userId;
  const mode = settlementMode(task);
  const openRoles = (task.collaborative?.roles ?? []).filter((role) => (role.status ?? "open") === "open");
  const confirmSeats = (task.collaborative?.roles ?? []).filter((role) => {
    const status = role.status ?? "open";
    return status === "submitted" || status === "open";
  });
  const selectedConfirm = confirmSeats.find((role) => role.id === confirmSeat) ?? confirmSeats[0];
  const mySeat = (task.collaborative?.roles ?? []).find(
    (role) => role.assignee_user_id === me && (role.status === "filled" || role.status === "submitted"),
  );

  return (
    <Screen title={task.title} lede={task.description}>
      <div className="task-top">
        <StatusPill status={task.status} />
        <KindPill kind={task.kind} />
        <IdText value={task.id} />
        <a className="btn ghost" href={href.tasks}>
          返回列表
        </a>
      </div>
      <ErrorNote message={error} />

      <div className="facts">
        <Fact label="发布者 publisher_id" value={task.publisher_id} />
        <Fact label="执行者 executor_ids" value={(task.executor_ids ?? []).join(", ") || "—"} />
        <Fact label="报酬" value={`${formatCredits(task.payment.amount)} · ${PAYMENT_MODE_LABEL[task.payment.mode] ?? task.payment.mode}`} />
        <Fact label="验收截止" value={formatTime(task.acceptance.deadline)} raw={task.acceptance.deadline} />
        <Fact label="验收标准" value={task.acceptance.quality} />
        <Fact label="review_hours" value={String(task.acceptance.review_hours ?? "—")} />
        <Fact label="复核截止 review_deadline" value={formatTime(task.review_deadline)} raw={task.review_deadline ?? undefined} />
        <Fact label="胜出提交 winning_submission_id" value={task.winning_submission_id ?? "—"} />
        {task.kind === "competitive" ? (
          <Fact
            label="竞争"
            value={`${task.competitive?.slots ?? "—"} 个名额 · ${task.competitive?.settlement_rule ?? "winner_take_all"}`}
          />
        ) : null}
        {task.kind === "collaborative" ? (
          <Fact
            label="结算方式"
            value={`${SETTLEMENT_MODE_LABEL[mode ?? "unified"] ?? mode} · ${mode ?? "unified"}`}
          />
        ) : null}
        {task.kind === "collaborative" ? <Fact label="退出规则" value={task.collaborative?.on_dropout ?? "replace"} /> : null}
      </div>

      <section className="card">
        <h2>托管 escrow</h2>
        {task.escrow ? (
          <>
            <p>
              持有 {formatCredits(task.escrow.held)} · {task.escrow.currency || "credits"} · <IdText value={task.escrow.id} />
            </p>
            {(task.escrow.entries ?? []).length === 0 ? <p className="hint">还没有流水。</p> : null}
            <table>
              <thead>
                <tr>
                  <th>类型</th>
                  <th>金额</th>
                  <th>对象 to_user_id</th>
                  <th>时间</th>
                </tr>
              </thead>
              <tbody>
                {(task.escrow.entries ?? []).map((entry) => (
                  <tr key={entry.id}>
                    <td>
                      {ENTRY_LABEL[entry.type] ?? entry.type} <span className="en">{entry.type}</span>
                    </td>
                    <td>{formatCredits(entry.amount)}</td>
                    <td>{entry.to_user_id ? <IdText value={entry.to_user_id} /> : "—"}</td>
                    <td title={entry.created_at}>{formatTime(entry.created_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </>
        ) : (
          <p className="hint">尚未注资，没有托管账户。</p>
        )}
      </section>

      {task.collaborative ? (
        <section className="card">
          <h2>席位 roles</h2>
          <p className="hint">
            表里是服务端展开后的席位。share_bps 是这一席结算用的基点。接受时用席位 id。
            {mode === "per_seat"
              ? " 分别结算：已提交的席位可以单独确认拨付；空席确认会把该份额退回发布者。全部席位拨付或退回后任务才 settled。"
              : " 统一结算：坐满后由发布者开始。每一席都提交、且没有空席时才能一次确认。开始后退出会重开该席，任务保持 active。"}
          </p>
          <table>
            <thead>
              <tr>
                <th>role_id</th>
                <th>名称</th>
                <th>share_bps</th>
                <th>template_id</th>
                <th>状态</th>
                <th>assignee_user_id</th>
              </tr>
            </thead>
            <tbody>
              {task.collaborative.roles.map((role) => (
                <tr key={role.id} className={role.assignee_user_id === me ? "mine-row" : undefined}>
                  <td>
                    <IdText value={role.id} />
                  </td>
                  <td>{role.name}</td>
                  <td>
                    {role.share_bps} <span className="en">{(role.share_bps / 100).toFixed(2)}%</span>
                  </td>
                  <td>{role.template_id ? <IdText value={role.template_id} /> : "—"}</td>
                  <td>{ROLE_STATUS_LABEL[role.status ?? "open"] ?? role.status} <span className="en">{role.status ?? "open"}</span></td>
                  <td>{role.assignee_user_id ? <IdText value={role.assignee_user_id} /> : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      ) : null}

      {(task.payment.milestones ?? []).length > 0 ? (
        <section className="card">
          <h2>里程碑</h2>
          <ul className="plain">
            {(task.payment.milestones ?? []).map((milestone) => (
              <li key={milestone.id}>
                <IdText value={milestone.id} /> {formatCredits(milestone.amount)} · {milestone.condition} · {milestone.status ?? "pending"}
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      <section className="card">
        <h2>操作</h2>
        <p className="hint">
          按钮按设置里的用户 id <code className="mono">{me || "未填写"}</code> 和任务状态显示。服务端仍以密钥对应的用户做校验。
        </p>
        <div className="actions">
          <ActionRow title="注资 fund" reason={fundReason(task, me)}>
            <Button
              type="button"
              kind="primary"
              pending={pending === "fund"}
              disabled={fundReason(task, me) !== null || pending !== null}
              onClick={() => void run("fund", () => client.fundTask(task.id, { idempotencyKey: createIdempotencyKey("fund") }))}
            >
              注资
            </Button>
          </ActionRow>

          {task.kind === "collaborative" ? (
            <ActionRow title="开始 start" reason={startReason(task, me)}>
              <Button
                type="button"
                kind="primary"
                pending={pending === "start"}
                disabled={startReason(task, me) !== null || pending !== null}
                onClick={() => void run("start", () => client.startTask(task.id, { idempotencyKey: createIdempotencyKey("start") }))}
              >
                开始任务
              </Button>
            </ActionRow>
          ) : null}

          <ActionRow title="确认 confirm" reason={confirmReason(task, me)}>
            <Button
              type="button"
              kind="primary"
              pending={pending === "confirm"}
              disabled={confirmReason(task, me) !== null || pending !== null}
              onClick={() => {
                if (mode === "per_seat") {
                  if (!selectedConfirm) {
                    setError("请选择席位");
                    return;
                  }
                  if ((selectedConfirm.status ?? "open") === "open") {
                    if (!window.confirm(`退回 ${selectedConfirm.id} 的份额给发布者？这一席不会支付执行者。`)) return;
                  }
                  void run("confirm", () =>
                    client.confirmTask(
                      task.id,
                      { role_id: selectedConfirm.id },
                      { idempotencyKey: createIdempotencyKey("confirm") },
                    ),
                  );
                  return;
                }
                void run("confirm", () => {
                  if (submissionIds && submissionIds.length > 0 && !chosen) {
                    throw new Error("请选择一份 submission_id");
                  }
                  const body: { submission_id?: string; milestone_id?: string } = {};
                  if (chosen && (task.kind === "competitive" || submissionIds)) body.submission_id = chosen;
                  if (task.payment.mode === "milestone" && milestoneId.trim()) body.milestone_id = milestoneId.trim();
                  return client.confirmTask(task.id, body, { idempotencyKey: createIdempotencyKey("confirm") });
                });
              }}
            >
              {mode === "per_seat"
                ? (selectedConfirm?.status ?? "open") === "open"
                  ? "退回该席份额"
                  : "确认拨付该席"
                : submissionIds
                  ? "按所选提交确认"
                  : "确认结算"}
            </Button>
          </ActionRow>
          {mode === "per_seat" ? (
            <Field label="确认席位 role_id" hint="已提交的席位拨付给执行者。空席把该份额退回发布者。">
              <select value={selectedConfirm?.id ?? ""} onChange={(event) => setConfirmSeat(event.target.value)}>
                {confirmSeats.length === 0 ? <option value="">没有可确认的席位</option> : null}
                {confirmSeats.map((role) => (
                  <option key={role.id} value={role.id}>
                    {role.id} · {ROLE_STATUS_LABEL[role.status ?? "open"] ?? role.status} · {role.share_bps} bps
                  </option>
                ))}
              </select>
            </Field>
          ) : null}
          {task.kind === "competitive" && task.status === "in_review" ? (
            <div className="picker">
              <p className="hint">
                多份提交时，不带 submission_id 的确认会返回 400。页面用 submissionIdsFromConfirmError 解析 message，请选一份再确认。
              </p>
              {submissionIds ? (
                <fieldset>
                  <legend>submission_id（created_at 顺序）</legend>
                  {submissionIds.map((submissionId, index) => (
                    <label key={submissionId} className="choice">
                      <input
                        type="radio"
                        name="submission"
                        value={submissionId}
                        checked={chosen === submissionId}
                        onChange={() => setChosen(submissionId)}
                      />
                      <span>第 {index + 1} 份</span>
                      <code className="mono">{submissionId}</code>
                    </label>
                  ))}
                </fieldset>
              ) : (
                <p className="hint">还没有提交列表。点「确认结算」；如果只有一份提交，会直接结算。</p>
              )}
            </div>
          ) : null}

          <ActionRow title="争议 dispute" reason={disputeReason(task, me)}>
            <Button
              type="button"
              kind="danger"
              pending={pending === "dispute"}
              disabled={disputeReason(task, me) !== null || pending !== null}
              onClick={() => {
                const text = reason.trim();
                if (!text) {
                  setError("请填写争议原因");
                  return;
                }
                void run("dispute", () =>
                  client.disputeTask(task.id, { reason: text }, { idempotencyKey: createIdempotencyKey("dispute") }),
                );
              }}
            >
              发起争议
            </Button>
          </ActionRow>
          <Field label="争议原因 reason">
            <textarea value={reason} rows={2} maxLength={2000} onChange={(event) => setReason(event.target.value)} />
          </Field>

          <ActionRow title="接受 accept" reason={acceptReason(task, me, roleId)}>
            <Button
              type="button"
              kind="primary"
              pending={pending === "accept"}
              disabled={acceptReason(task, me, roleId) !== null || pending !== null}
              onClick={() =>
                void run("accept", () =>
                  client.acceptTask(
                    task.id,
                    task.kind === "collaborative" ? { role_id: roleId } : {},
                    { idempotencyKey: createIdempotencyKey("accept") },
                  ),
                )
              }
            >
              接受
            </Button>
          </ActionRow>
          {task.kind === "collaborative" ? (
            <Field label="入席 role_id" hint="选展开后的席位 id。人数为 1 时就是角色 id。">
              <select value={roleId} onChange={(event) => setRoleId(event.target.value)}>
                {openRoles.length === 0 ? <option value="">没有空席</option> : null}
                {openRoles.map((role) => (
                  <option key={role.id} value={role.id}>
                    {role.id} · {role.name} · {role.share_bps} bps
                  </option>
                ))}
              </select>
            </Field>
          ) : null}

          <ActionRow title="提交 submit" reason={submitReason(task, me)}>
            <Button
              type="button"
              kind="primary"
              pending={pending === "submit"}
              disabled={submitReason(task, me) !== null || pending !== null}
              onClick={() => {
                let payload: Record<string, unknown>;
                try {
                  const parsed = JSON.parse(payloadText) as unknown;
                  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
                    throw new Error("payload 必须是 JSON 对象");
                  }
                  payload = parsed as Record<string, unknown>;
                } catch (err) {
                  setError(err instanceof SyntaxError ? "payload 不是合法 JSON" : formatError(err));
                  return;
                }
                void run("submit", () =>
                  client.submitTask(
                    task.id,
                    {
                      payload,
                      note: note.trim() || undefined,
                      milestone_id: task.payment.mode === "milestone" && milestoneId.trim() ? milestoneId.trim() : undefined,
                    },
                    { idempotencyKey: createIdempotencyKey("submit") },
                  ),
                );
              }}
            >
              提交
            </Button>
          </ActionRow>
          <Field label="payload" hint="JSON 对象">
            <textarea className="mono" rows={5} value={payloadText} onChange={(event) => setPayloadText(event.target.value)} spellCheck={false} />
          </Field>
          <Field label="备注 note">
            <input value={note} maxLength={2000} onChange={(event) => setNote(event.target.value)} />
          </Field>
          {task.payment.mode === "milestone" ? (
            <Field label="milestone_id" hint="里程碑支付时提交和确认需要">
              <input value={milestoneId} spellCheck={false} onChange={(event) => setMilestoneId(event.target.value)} />
            </Field>
          ) : null}

          <ActionRow title="退出 dropout" reason={dropoutReason(task, me)}>
            <Button
              type="button"
              kind="danger"
              pending={pending === "dropout"}
              disabled={dropoutReason(task, me) !== null || pending !== null}
              onClick={() => {
                if (!window.confirm("退出后该席位回到 open，佣金和能力分记为 0。继续？")) return;
                void run("dropout", () => client.dropoutTask(task.id, { idempotencyKey: createIdempotencyKey("dropout") }));
              }}
            >
              退出席位
            </Button>
          </ActionRow>
          {mySeat ? <p className="hint">你占着 {mySeat.id}。退出后同一 role_id 可以再被接受，用来补位。</p> : null}
        </div>
        {(task.kind === "competitive" || task.kind === "collaborative") && (
          <p className="hint">
            开发配置只有一把执行者密钥 dev-executor-key（usr_executor）。第二个执行者可以在仓库根目录执行{" "}
            <code className="mono">make token USER_ID=usr_extra</code>
            ，把左侧鉴权改成 Bearer，用户 id 填 usr_extra，再来接受或提交。
          </p>
        )}
      </section>
    </Screen>
  );
}

function Fact({ label, value, raw }: { label: string; value: string; raw?: string }) {
  return (
    <div>
      <span className="fact-label">{label}</span>
      <strong title={raw}>{value}</strong>
    </div>
  );
}

function fundReason(task: Task, me: string): string | null {
  if (me !== task.publisher_id) return "只有发布者可以注资";
  if (task.status !== "draft") return "只有草稿可以注资";
  return null;
}

function startReason(task: Task, me: string): string | null {
  const mode = settlementMode(task);
  if (mode === null) return "只有统一结算的协作任务需要开始";
  if (mode === "per_seat") return "分别结算不需要开始，入席后即可提交";
  if (me !== task.publisher_id) return "只有发布者可以开始";
  if (task.status === "active" || task.status === "in_review") return "任务已经开始";
  if (task.status !== "open") return "只有 open 可以开始";
  const open = (task.collaborative?.roles ?? []).filter((role) => (role.status ?? "open") === "open");
  if (open.length > 0) return "坐满后才能开始";
  return null;
}

function confirmReason(task: Task, me: string): string | null {
  if (me !== task.publisher_id) return "只有发布者可以确认";
  const mode = settlementMode(task);
  if (mode === "per_seat") {
    if (task.status !== "open" && task.status !== "active" && task.status !== "in_review") return "当前状态不能按席确认";
    const roles = task.collaborative?.roles ?? [];
    const waiting = roles.some((role) => (role.status ?? "open") === "submitted" || (role.status ?? "open") === "open");
    if (!waiting) return "没有可确认或退回的席位";
    return null;
  }
  if (mode === "unified") {
    if (task.status === "open") return "统一结算要先开始，并等全部席位提交";
    if (task.status === "active") return "还有席位未提交或未坐满，确认会失败";
  }
  if (task.status !== "in_review") return "只有 in_review 可以确认";
  return null;
}

function disputeReason(task: Task, me: string): string | null {
  if (me !== task.publisher_id) return "这里由发布者发起争议";
  if (task.status !== "in_review") return "只有 in_review 可以争议";
  return null;
}

function acceptReason(task: Task, me: string, roleId: string): string | null {
  if (!me) return "请在左侧填写当前用户 id";
  if (me === task.publisher_id) return "发布者不能接受自己的任务";
  if (task.kind === "collaborative") {
    if (
      (task.collaborative?.roles ?? []).some(
        (role) => role.assignee_user_id === me && (role.status === "filled" || role.status === "submitted"),
      )
    ) {
      return "你已经占了一个席位";
    }
    if (task.status !== "open" && task.status !== "active") return "当前状态不能接受";
    if (!roleId) return "没有可接受的空席";
    return null;
  }
  if ((task.executor_ids ?? []).includes(me)) return "你已经接受过";
  if (task.kind === "exclusive") {
    if (task.status !== "open") return "独占任务只有 open 可以接受";
    return null;
  }
  if (task.status !== "open" && task.status !== "active") return "当前状态不能接受";
  const slots = task.competitive?.slots ?? 0;
  if ((task.executor_ids ?? []).length >= slots) return "竞争名额已满";
  return null;
}

function submitReason(task: Task, me: string): string | null {
  if (!me) return "请在左侧填写当前用户 id";
  if (task.kind === "collaborative") {
    const mine = (task.collaborative?.roles ?? []).find((role) => role.assignee_user_id === me);
    if (!mine) return "只有当前席位的执行者可以提交";
    if (mine.status === "submitted") return "你已经提交过";
    if (mine.status !== "filled") return "你没有可提交的席位";
    if (settlementMode(task) === "unified" && task.status === "open") return "统一结算要等发布者开始";
    if (task.status !== "active" && task.status !== "in_review") return "任务需要处于 active";
    return null;
  }
  if (!(task.executor_ids ?? []).includes(me)) return "只有执行者可以提交";
  if (task.kind === "competitive") {
    if (task.status !== "active" && task.status !== "in_review") return "竞争任务在 active 或 in_review 时提交";
    return null;
  }
  if (task.status !== "active") return "任务需要处于 active";
  return null;
}

function dropoutReason(task: Task, me: string): string | null {
  if (task.kind !== "collaborative") return "只有协作任务可以退出";
  if (task.status !== "open" && task.status !== "active") return "只有 open 或 active 时可以退出";
  const submitted = (task.collaborative?.roles ?? []).find((role) => role.assignee_user_id === me && role.status === "submitted");
  if (submitted) return "已提交的席位不能退出";
  const seat = (task.collaborative?.roles ?? []).find((role) => role.assignee_user_id === me && role.status === "filled");
  if (!seat) return "你没有已入席的 role";
  return null;
}

function settlementMode(task: Task): "per_seat" | "unified" | null {
  if (task.kind !== "collaborative") return null;
  return task.collaborative?.settlement_mode === "per_seat" ? "per_seat" : "unified";
}

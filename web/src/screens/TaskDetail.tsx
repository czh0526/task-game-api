import { useEffect, useState, type ReactNode } from "react";
import { createIdempotencyKey, type Message, type ResolutionOutcome, type Task } from "taskgame-client";
import { Avatar } from "../Avatar";
import { isSignedIn } from "../client";
import { useApp } from "../context";
import {
  ENTRY_LABEL,
  MILESTONE_STATUS_LABEL,
  PAYMENT_MODE_LABEL,
  RECURRENCE_LABEL,
  ROLE_STATUS_LABEL,
  SETTLEMENT_MODE_LABEL,
  STATUS_LABEL,
  URGENCY_LABEL,
  confirmFailure,
  executorRequirementLabel,
  formatCredits,
  formatError,
  formatTime,
  isAbort,
} from "../format";
import { InvitePicker } from "../friends";
import { SenderName, UserRef } from "../names";
import { href } from "../route";
import {
  canInviteSeat,
  canInviteTask,
  canRemoveSeat,
  cancelReason,
  confirmTaskBody,
  currentSubmissionsForMilestone,
  inReviewMilestoneSubmissionId,
  inviteUserIds,
  milestoneConfirmBlock,
  milestoneNextStep,
  milestoneSubmitBlock,
  submitMilestoneField,
} from "../stageLoop";
import { stageTone, viewerHint, viewerLabel, viewerRole } from "../taskDetailView";
import { Button, ErrorNote, Field, IdText, KindPill, Screen, StatusPill, cx } from "../ui";

export function TaskDetail({ id }: { id: string }) {
  const { client, clientError, settings } = useApp();
  const signedIn = isSignedIn(settings);
  const [task, setTask] = useState<Task | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const [submissionIds, setSubmissionIds] = useState<string[] | null>(null);
  const [chosen, setChosen] = useState("");
  const [roleId, setRoleId] = useState("");
  const [submitMilestoneId, setSubmitMilestoneId] = useState("");
  const [confirmMilestoneId, setConfirmMilestoneId] = useState("");
  const [reason, setReason] = useState("");
  const [reviseReason, setReviseReason] = useState("");
  const [outcome, setOutcome] = useState<ResolutionOutcome>("pay_executor");
  const [inviteUser, setInviteUser] = useState("");
  const [seatInviteUsers, setSeatInviteUsers] = useState<Record<string, string>>({});
  const [chatText, setChatText] = useState("");
  const [messages, setMessages] = useState<Message[]>([]);
  const [chatNote, setChatNote] = useState<string | null>(null);
  const [isArbiter, setIsArbiter] = useState(false);

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
    const stageId =
      task.payment.mode === "milestone"
        ? (task.payment.milestones ?? []).find((milestone) => milestone.status === "submitted")?.id
        : undefined;
    const current = currentSubmissionsForMilestone(task, stageId);
    setChosen((prev) => (current.some((item) => item.id === prev) ? prev : (current[0]?.id ?? "")));
  }, [task]);

  useEffect(() => {
    if (!client || !signedIn) return;
    const ac = new AbortController();
    client
      .me({ signal: ac.signal })
      .then((profile) => {
        if (!ac.signal.aborted) setIsArbiter(Boolean(profile.is_arbiter));
      })
      .catch(() => {
        if (!ac.signal.aborted) setIsArbiter(false);
      });
    return () => ac.abort();
  }, [client, signedIn]);

  useEffect(() => {
    if (!client || !signedIn || !task?.chat_id) return;
    const ac = new AbortController();
    const chatId = task.chat_id;
    setChatNote(null);
    client
      .listMessages(chatId, { limit: 50 }, { signal: ac.signal })
      .then((page) => {
        if (!ac.signal.aborted) setMessages(page.items);
      })
      .catch((err: unknown) => {
        if (isAbort(err)) return;
        setMessages([]);
        setChatNote(formatError(err));
      });
    return () => ac.abort();
  }, [client, signedIn, task?.chat_id, task?.updated_at]);

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
  const taskId = task.id;

  async function inviteSeat(roleId: string) {
    const ids = inviteUserIds(seatInviteUsers[roleId] ?? "");
    if (ids.length === 0 || !client) return;
    setPending(`seat-invite-${roleId}`);
    setError(null);
    try {
      await client.createTaskInvites(
        taskId,
        { user_ids: ids, role_id: roleId },
        { idempotencyKey: createIdempotencyKey(`invite-${roleId}`) },
      );
      setSeatInviteUsers((current) => ({ ...current, [roleId]: "" }));
      setTask(await client.getTask(taskId));
    } catch (err) {
      setError(formatError(err));
    } finally {
      setPending(null);
    }
  }

  async function removeSeat(roleId: string) {
    if (!client) return;
    if (!window.confirm(`删除席位 ${roleId}？该席位托管的份额会退回发布者，绑定在该席位上的邀请会被删除。`)) return;
    setPending(`seat-remove-${roleId}`);
    setError(null);
    try {
      await client.removeSeat(taskId, roleId, { idempotencyKey: createIdempotencyKey(`remove-${roleId}`) });
      setTask(await client.getTask(taskId));
    } catch (err) {
      setError(formatError(err));
    } finally {
      setPending(null);
    }
  }

  const pendingMilestones = (task.payment.milestones ?? []).filter((milestone) => (milestone.status ?? "pending") === "pending");
  const submittedMilestones = (task.payment.milestones ?? []).filter((milestone) => milestone.status === "submitted");
  const submitStageId = pendingMilestones.some((milestone) => milestone.id === submitMilestoneId)
    ? submitMilestoneId
    : (pendingMilestones[0]?.id ?? "");
  const confirmStageId = submittedMilestones.some((milestone) => milestone.id === confirmMilestoneId)
    ? confirmMilestoneId
    : (submittedMilestones[0]?.id ?? "");
  const reviewMilestoneId = task.payment.mode === "milestone" ? confirmStageId || undefined : undefined;
  const reviewSubmissions = currentSubmissionsForMilestone(task, reviewMilestoneId);
  const openRoles = (task.collaborative?.roles ?? []).filter((role) => (role.status ?? "open") === "open");
  const mySeat = (task.collaborative?.roles ?? []).find(
    (role) => role.assignee_user_id === me && (role.status === "filled" || role.status === "submitted"),
  );

  // Only the operations that are legal for this status and this viewer are
  // rendered in the main column. Everything else lives in the panels below.
  const canFund = fundReason(task, me) === null;
  const canStart = task.kind === "collaborative" && startReason(task, me) === null;
  const canConfirm = mode !== "per_seat" && confirmReason(task, me) === null;
  const canRevise = reviseReasonOf(task, me) === null;
  const canCancel = cancelReason(task, me) === null;
  const canDispute = disputeReason(task, me) === null;
  const canAccept = acceptReason(task, me, roleId) === null;
  const canSubmit = submitReason(task, me) === null;
  const canDropout = dropoutReason(task, me) === null;
  const disputed = task.status === "disputed" || Boolean(task.dispute_role_id);
  const canResolve = disputed && resolveReason(task, me) === null;
  const canArbitrate = disputed && isArbiter;
  const canInvite = canInviteTask(me, task);
  const operationCount =
    Number(canFund) + Number(canStart) + Number(canConfirm) + Number(canRevise) + Number(canCancel) +
    Number(canDispute) + Number(canAccept) + Number(canSubmit) + Number(canDropout) +
    Number(canResolve) + Number(canArbitrate) + Number(canInvite);
  const role = viewerRole(task, me);
  const tone = stageTone(task.status);

  return (
    <Screen title={task.title}>
      <div className="task-top">
        <StatusPill status={task.status} />
        <KindPill kind={task.kind} />
        <IdText value={task.id} />
        <a className="btn ghost" href={href.tasks}>
          返回列表
        </a>
      </div>
      <ErrorNote message={error} />

      {/* Status banner: one glance tells the viewer where the task is and whose turn it is. */}
      <section className={cx("card", "task-hero", `tone-${tone}`)}>
        <div className="hero-main">
          <span className="hero-eyebrow">当前状态</span>
          <div className="hero-status">
            <strong>{STATUS_LABEL[task.status]}</strong>
            <span className="en">{task.status}</span>
          </div>
          <p className="hero-hint">{viewerHint(task, me)}</p>
          <p className="hero-sub">流程：{nextStep(task)}</p>
        </div>
        <div className="hero-identity">
          <span className="identity-tag">{viewerLabel(role)}</span>
          <span className="identity-id">{me ? <UserRef userId={me} nameOnly /> : "未登录"}</span>
        </div>
      </section>

      {/* 任务描述是执行者判断如何干活的核心依据，单独成块、始终展开，不收进折叠面板。 */}
      <section className="card task-brief">
        <div className="section-head">
          <h2>任务描述</h2>
          <span className="section-count">执行前请先读完</span>
        </div>
        {task.description ? (
          <p className="task-brief-body">{task.description}</p>
        ) : (
          <p className="hint">发布者没有填写描述。执行前请在任务会话中确认具体要求。</p>
        )}
      </section>

      <div className="task-layout">
        <section className="card task-actions-card">
          <div className="section-head">
            <h2>你现在可以做</h2>
            <span className="section-count">{signedIn ? operationCount : "登录后可操作"}</span>
          </div>
          {!signedIn ? (
            <div className="op-empty">
              <p>登录后即可查看当前状态属于你的操作，并与对方沟通。</p>
              <a className="btn primary" href={`${href.login}?next=${encodeURIComponent(href.task(task.id))}`}>
                登录 / 注册
              </a>
            </div>
          ) : operationCount === 0 ? (
            <div className="op-empty">
              <span className="op-check" aria-hidden="true">✓</span>
              <p>当前没有需要你执行的操作。</p>
              <p className="hint">{viewerHint(task, me)}</p>
            </div>
          ) : (
            <div className="op-list">
              {canFund ? (
                <Operation title="注资" description="把报酬托管到平台，任务才会开放给执行者。" primary>
                  <Button
                    type="button"
                    kind="primary"
                    pending={pending === "fund"}
                    disabled={pending !== null}
                    onClick={() => void run("fund", () => client.fundTask(task.id, { idempotencyKey: createIdempotencyKey("fund") }))}
                  >
                    注资
                  </Button>
                </Operation>
              ) : null}

              {canStart ? (
                <Operation title="开始任务" description="席位已经坐满，开始后执行者可以通知完成。" primary>
                  <Button
                    type="button"
                    kind="primary"
                    pending={pending === "start"}
                    disabled={pending !== null}
                    onClick={() => void run("start", () => client.startTask(task.id, { idempotencyKey: createIdempotencyKey("start") }))}
                  >
                    开始任务
                  </Button>
                </Operation>
              ) : null}

              {canAccept ? (
                <Operation title="接单" description="接下这个任务，成为执行者。" primary>
                  {task.kind === "collaborative" ? (
                    <Field label="选择席位" hint="选一个空席。邀请会绑定席位，接单后自动落到该席。">
                      <select value={roleId} onChange={(event) => setRoleId(event.target.value)}>
                        {openRoles.length === 0 ? <option value="">没有空席</option> : null}
                        {openRoles.map((seat) => (
                          <option key={seat.id} value={seat.id}>
                            {seat.id} · {seat.name} · {seat.share_bps} bps
                          </option>
                        ))}
                      </select>
                    </Field>
                  ) : null}
                  <Button
                    type="button"
                    kind="primary"
                    pending={pending === "accept"}
                    disabled={pending !== null}
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
                    接单
                  </Button>
                </Operation>
              ) : null}

              {canSubmit ? (
                <Operation title="通知完成" description="告诉发布者任务已完成，对方会发起结算并拨付托管金额。">
                  {task.payment.mode === "milestone" ? (
                    <Field label="完成阶段" hint="选择刚完成的阶段；同一时间只能有一个阶段待结算。">
                      <select value={submitStageId} onChange={(event) => setSubmitMilestoneId(event.target.value)}>
                        {pendingMilestones.length === 0 ? <option value="">没有待完成的阶段</option> : null}
                        {pendingMilestones.map((milestone) => (
                          <option key={milestone.id} value={milestone.id}>
                            {milestone.id} · {formatCredits(milestone.amount)} · {milestone.condition}
                          </option>
                        ))}
                      </select>
                    </Field>
                  ) : null}
                  <Button
                    type="button"
                    kind="primary"
                    pending={pending === "submit"}
                    disabled={pending !== null}
                    onClick={() => {
                      const milestoneField = submitMilestoneField(task, submitStageId);
                      if (typeof milestoneField === "string") {
                        setError(milestoneField);
                        return;
                      }
                      void run("submit", () =>
                        client.submitTask(
                          task.id,
                          { payload: { status: "completed" }, note: "任务已完成，请发起结算", ...milestoneField },
                          { idempotencyKey: createIdempotencyKey("complete") },
                        ),
                      );
                    }}
                  >
                    通知完成
                  </Button>
                </Operation>
              ) : null}

              {canConfirm ? (
                <Operation
                  title={task.payment.mode === "milestone" ? "发起本阶段结算" : "发起结算"}
                  description="执行者已通知完成，确认后拨付对应金额。"
                  primary
                >
                  {task.payment.mode === "milestone" ? (
                    <Field label="确认阶段" hint="只拨付这一阶段的金额，任务会回到进行中。">
                      <select value={confirmStageId} onChange={(event) => setConfirmMilestoneId(event.target.value)}>
                        {submittedMilestones.length === 0 ? <option value="">没有待结算的阶段</option> : null}
                        {submittedMilestones.map((milestone) => (
                          <option key={milestone.id} value={milestone.id}>
                            {milestone.id} · {formatCredits(milestone.amount)} · {milestone.condition}
                          </option>
                        ))}
                      </select>
                    </Field>
                  ) : null}
                  {task.kind === "competitive" ? (
                    <div className="picker">
                      <p className="hint">
                        选择一位执行者作为结算对象；只有一位时会自动带上。
                        {task.winner_select_deadline
                          ? ` 选赢家截止 ${formatTime(task.winner_select_deadline)}，超时会退回托管。`
                          : ""}
                      </p>
                      <fieldset>
                        <legend>submission_id</legend>
                        {reviewSubmissions.map((submission) => (
                          <label key={submission.id} className="choice">
                            <input
                              type="radio"
                              name="submission"
                              value={submission.id}
                              checked={chosen === submission.id}
                              onChange={() => setChosen(submission.id)}
                            />
                            <UserRef userId={submission.executor_id} nameOnly />
                            <code className="mono">{submission.id}</code>
                          </label>
                        ))}
                      </fieldset>
                    </div>
                  ) : null}
                  <Button
                    type="button"
                    kind="primary"
                    pending={pending === "confirm"}
                    disabled={pending !== null}
                    onClick={() =>
                      void run("confirm", () => {
                        const body = confirmTaskBody(task, chosen, confirmStageId, submissionIds);
                        if (typeof body === "string") throw new Error(body);
                        return client.confirmTask(task.id, body, { idempotencyKey: createIdempotencyKey("confirm") });
                      })
                    }
                  >
                    {task.payment.mode === "milestone" ? "发起本阶段结算" : submissionIds ? "按所选执行者结算" : "发起结算"}
                  </Button>
                </Operation>
              ) : null}

              {canRevise ? (
                <Operation title="请对方返工" description="认为任务尚未完成时，退回给执行者继续处理，并说明原因。">
                  <Field label="修改原因">
                    <input value={reviseReason} maxLength={2000} onChange={(event) => setReviseReason(event.target.value)} />
                  </Field>
                  <Button
                    type="button"
                    pending={pending === "revise"}
                    disabled={pending !== null}
                    onClick={() => {
                      const text = reviseReason.trim();
                      if (!text) {
                        setError("请填写修改原因");
                        return;
                      }
                      const stageSubmission = inReviewMilestoneSubmissionId(task, chosen);
                      if (stageSubmission === null) {
                        setError("没有可退回的阶段完成通知");
                        return;
                      }
                      const current = (task.submissions ?? []).filter((submission) => submission.status === "current");
                      const submittedSeats = (task.collaborative?.roles ?? []).filter((role) => role.status === "submitted");
                      const submissionId =
                        stageSubmission ??
                        (task.kind === "competitive" && current.length === 1 ? current[0]?.id : chosen || undefined);
                      void run("revise", () =>
                        client.reviseTask(
                          task.id,
                          {
                            reason: text,
                            role_id: task.kind === "collaborative" && submittedSeats.length === 1 ? submittedSeats[0]?.id : undefined,
                            submission_id: submissionId,
                          },
                          { idempotencyKey: createIdempotencyKey("revise") },
                        ),
                      );
                    }}
                  >
                    请对方返工
                  </Button>
                </Operation>
              ) : null}

              {canDispute ? (
                <Operation title="发起争议" description="双方无法达成一致时，把任务交给协商与裁定流程。" danger>
                  <Field label="争议原因">
                    <textarea value={reason} rows={2} maxLength={2000} onChange={(event) => setReason(event.target.value)} />
                  </Field>
                  <Button
                    type="button"
                    kind="danger"
                    pending={pending === "dispute"}
                    disabled={pending !== null}
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
                </Operation>
              ) : null}

              {canResolve || canArbitrate ? (
                <Operation title="协商与裁定" description="双方提交同一方案即生效；裁定人可直接给出结果。" primary={canResolve}>
                  <Field label="协商结果">
                    <select value={outcome} onChange={(event) => setOutcome(event.target.value as ResolutionOutcome)}>
                      <option value="pay_executor">付给执行者</option>
                      <option value="refund_publisher">退回发布者</option>
                      <option value="split">按比例拆分</option>
                    </select>
                  </Field>
                  <Field label="理由">
                    <textarea value={reason} rows={2} maxLength={2000} onChange={(event) => setReason(event.target.value)} />
                  </Field>
                  <p className="hint">
                    当前方案：
                    {(task.resolve_proposals ?? []).map((proposal) => `${proposal.side} ${proposal.outcome}`).join("；") || "还没有"}
                  </p>
                  {canResolve ? (
                    <Button
                      type="button"
                      kind="primary"
                      pending={pending === "resolve"}
                      disabled={pending !== null}
                      onClick={() =>
                        void run("resolve", () =>
                          client.resolveTask(
                            task.id,
                            {
                              outcome,
                              reason: reason.trim() || undefined,
                              role_id: task.dispute_role_id || undefined,
                              submission_id: task.kind === "competitive" ? chosen || undefined : undefined,
                            },
                            { idempotencyKey: createIdempotencyKey("resolve") },
                          ),
                        )
                      }
                    >
                      提交协商
                    </Button>
                  ) : null}
                  {canArbitrate ? (
                    <Button
                      type="button"
                      kind="danger"
                      pending={pending === "arbitrate"}
                      disabled={pending !== null || !reason.trim()}
                      onClick={() =>
                        void run("arbitrate", () =>
                          client.arbitrateTask(
                            task.id,
                            {
                              outcome,
                              reason: reason.trim(),
                              role_id: task.dispute_role_id || undefined,
                              submission_id: task.kind === "competitive" ? chosen || undefined : undefined,
                            },
                            { idempotencyKey: createIdempotencyKey("arbitrate") },
                          ),
                        )
                      }
                    >
                      裁定
                    </Button>
                  ) : null}
                  {!canArbitrate ? (
                    <p className="hint">裁定需要 Escrow.ArbiterUserIds 中的用户。</p>
                  ) : null}
                </Operation>
              ) : null}

              {canDropout ? (
                <Operation title="退出席位" description="放弃当前席位，佣金与能力分记为零。" danger>
                  {mySeat ? <p className="hint">你占着 {mySeat.id}，退出后同一席位可以被别人接单。</p> : null}
                  <Button
                    type="button"
                    kind="danger"
                    pending={pending === "dropout"}
                    disabled={pending !== null}
                    onClick={() => {
                      if (!window.confirm("退出后该席位回到 open，佣金和能力分记为 0。继续？")) return;
                      void run("dropout", () => client.dropoutTask(task.id, { idempotencyKey: createIdempotencyKey("dropout") }));
                    }}
                  >
                    退出席位
                  </Button>
                </Operation>
              ) : null}

              {canCancel ? (
                <Operation title="取消任务" description="终止任务，未支付的托管退回发布者。" danger>
                  <Field label="取消原因（可选）">
                    <input value={reason} maxLength={2000} onChange={(event) => setReason(event.target.value)} />
                  </Field>
                  <Button
                    type="button"
                    kind="danger"
                    pending={pending === "cancel"}
                    disabled={pending !== null}
                    onClick={() => {
                      if (!window.confirm("取消后，未开始或未通知完成的托管会退回发布者。已通知完成的席位不会被这条路径取消。继续？")) return;
                      void run("cancel", () =>
                        client.cancelTask(task.id, { reason: reason.trim() || undefined }, { idempotencyKey: createIdempotencyKey("cancel") }),
                      );
                    }}
                  >
                    取消任务
                  </Button>
                </Operation>
              ) : null}

              {canInvite ? (
                <Operation title="邀请执行者" description="邀请指定用户参与，按昵称选择。">
                  <InvitePicker
                    value={inviteUser}
                    onChange={setInviteUser}
                    disabled={pending !== null}
                    label="邀请对象"
                  />
                  <Button
                    type="button"
                    pending={pending === "invite"}
                    disabled={pending !== null || !inviteUser.trim()}
                    onClick={() =>
                      void (async () => {
                        setPending("invite");
                        setError(null);
                        try {
                          await client.createTaskInvites(
                            task.id,
                            { user_ids: inviteUser.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean) },
                            { idempotencyKey: createIdempotencyKey("invite") },
                          );
                          setInviteUser("");
                          setTask(await client.getTask(task.id));
                        } catch (err) {
                          setError(formatError(err));
                        } finally {
                          setPending(null);
                        }
                      })()
                    }
                  >
                    发送邀请
                  </Button>
                </Operation>
              ) : null}
            </div>
          )}
        </section>

        <aside className="card task-summary">
          <div className="section-head">
            <h2>任务速览</h2>
          </div>
          <div className="summary-grid">
            <Fact label="报酬" value={formatCredits(task.payment.amount)} />
            <Fact label="结算" value={PAYMENT_MODE_LABEL[task.payment.mode] ?? task.payment.mode} />
            <Fact label="发布者" value={<UserRef userId={task.publisher_id} nameOnly />} />
            <Fact
              label={task.kind === "collaborative" ? "席位" : "执行者"}
              value={
                task.kind === "collaborative" ? (
                  `${(task.collaborative?.roles ?? []).filter((r) => r.status === "filled" || r.status === "submitted").length} / ${task.collaborative?.roles?.length ?? 0} 已占`
                ) : (task.executor_ids ?? []).length === 0 ? (
                  "尚未接单"
                ) : (
                  <span className="person-list">
                    {(task.executor_ids ?? []).map((userId) => (
                      <UserRef key={userId} userId={userId} nameOnly />
                    ))}
                  </span>
                )
              }
            />
            <Fact label="位置" value={task.attributes?.location || "—"} />
            <Fact label="时限" value={URGENCY_LABEL[task.attributes?.urgency ?? "other"] ?? "—"} />
            <Fact
              label="关键时间"
              value={
                task.status === "in_review"
                  ? formatTime(task.review_deadline)
                  : task.status === "open"
                    ? formatTime(task.open_deadline)
                    : task.status === "active"
                      ? formatTime(task.acceptance.deadline)
                      : formatTime(task.acceptance.deadline)
              }
            />
          </div>
          {(task.keywords ?? []).length > 0 ? (
            <div className="summary-tags">
              {(task.keywords ?? []).map((keyword) => (
                <span key={keyword} className="card-keyword">#{keyword}</span>
              ))}
            </div>
          ) : null}
          {task.kind === "collaborative" && mode ? (
            <p className="summary-note">席位结算：{SETTLEMENT_MODE_LABEL[mode] ?? mode}</p>
          ) : null}
        </aside>
      </div>

      {task.collaborative ? (
        <Panel
          title="席位与分工"
          badge={`${(task.collaborative.roles ?? []).filter((r) => r.status === "filled" || r.status === "submitted").length}/${task.collaborative.roles?.length ?? 0} 已占`}
          open={task.status === "open" || task.status === "active"}
        >
          <p className="hint">
            每个席位独立邀请和结算。空席可以邀请指定用户（邀请绑定该席位，接单后落到该席）；发布者也可以删除空席，份额退回并清除绑定邀请。
          </p>
          <div className="table-scroll">
            <table className="seat-table">
              <thead>
                <tr>
                  <th>席位</th>
                  <th>名称</th>
                  <th>份额</th>
                  <th>状态</th>
                  <th>执行者</th>
                  <th>邀请 / 删除</th>
                  {mode === "per_seat" ? <th>结算</th> : null}
                </tr>
              </thead>
              <tbody>
                {task.collaborative.roles.map((seat) => {
                  const action = perSeatRowAction(task, me, seat.status);
                  return (
                    <tr key={seat.id} className={seat.assignee_user_id === me ? "mine-row" : undefined}>
                      <td><IdText value={seat.id} /></td>
                      <td>{seat.name}</td>
                      <td>{seat.share_bps} <span className="en">{(seat.share_bps / 100).toFixed(2)}%</span></td>
                      <td>
                        {ROLE_STATUS_LABEL[seat.status ?? "open"] ?? seat.status}{" "}
                        <span className="en">{seat.status ?? "open"}</span>
                      </td>
                      <td>{seat.assignee_user_id ? <UserRef userId={seat.assignee_user_id} nameOnly /> : "—"}</td>
                      <td className="seat-invite">
                        {canInviteSeat(me, task, seat.status) ? (
                          <InvitePicker
                            value={seatInviteUsers[seat.id] ?? ""}
                            onChange={(next) =>
                              setSeatInviteUsers((current) => ({ ...current, [seat.id]: next }))
                            }
                            disabled={pending !== null}
                            label={`邀请 ${seat.name || seat.id}`}
                          />
                        ) : null}
                        {canInviteSeat(me, task, seat.status) ? (
                          <Button
                            type="button"
                            kind="primary"
                            className="compact"
                            pending={pending === `seat-invite-${seat.id}`}
                            disabled={pending !== null || inviteUserIds(seatInviteUsers[seat.id] ?? "").length === 0}
                            onClick={() => void inviteSeat(seat.id)}
                          >
                            邀请
                          </Button>
                        ) : null}
                        {canRemoveSeat(me, task, seat.status, seat.assignee_user_id) ? (
                          <Button
                            type="button"
                            kind="danger"
                            className="compact"
                            pending={pending === `seat-remove-${seat.id}`}
                            disabled={pending !== null}
                            onClick={() => void removeSeat(seat.id)}
                          >
                            删除席位
                          </Button>
                        ) : null}
                        {!canInviteSeat(me, task, seat.status) && !canRemoveSeat(me, task, seat.status, seat.assignee_user_id)
                          ? "—"
                          : null}
                      </td>
                      {mode === "per_seat" ? (
                        <td className="seat-action">
                          {action === "pay" ? (
                            <Button
                              type="button"
                              kind="primary"
                              className="compact"
                              pending={pending === `seat-${seat.id}`}
                              disabled={pending !== null}
                              onClick={() =>
                                void run(`seat-${seat.id}`, () =>
                                  client.confirmTask(
                                    task.id,
                                    { role_id: seat.id },
                                    { idempotencyKey: createIdempotencyKey(`confirm-${seat.id}`) },
                                  ),
                                )
                              }
                            >
                              发起结算
                            </Button>
                          ) : null}
                          {action === "refund" ? (
                            <Button
                              type="button"
                              kind="danger"
                              className="compact"
                              pending={pending === `seat-${seat.id}`}
                              disabled={pending !== null}
                              onClick={() => {
                                if (!window.confirm(`退回 ${seat.id} 的份额给发布者？这一席不会支付执行者。`)) return;
                                void run(`seat-${seat.id}`, () =>
                                  client.confirmTask(
                                    task.id,
                                    { role_id: seat.id },
                                    { idempotencyKey: createIdempotencyKey(`refund-${seat.id}`) },
                                  ),
                                );
                              }}
                            >
                              退回该席
                            </Button>
                          ) : null}
                          {action === null ? "—" : null}
                        </td>
                      ) : null}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Panel>
      ) : null}

      {task.payment.mode === "milestone" ? (
        <Panel title="阶段进度" badge={`${(task.payment.milestones ?? []).filter((m) => m.status === "released").length}/${task.payment.milestones?.length ?? 0} 已拨付`} open={task.status === "in_review"}>
          <p className="hint">发起结算只拨付该阶段金额，任务回到进行中，直到每个阶段都已拨付。</p>
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>阶段</th>
                  <th>金额</th>
                  <th>条件</th>
                  <th>状态</th>
                </tr>
              </thead>
              <tbody>
                {(task.payment.milestones ?? []).map((milestone) => {
                  const status = milestone.status ?? "pending";
                  return (
                    <tr key={milestone.id} className={status === "submitted" ? "stage-submitted" : undefined}>
                      <td><IdText value={milestone.id} /></td>
                      <td>{formatCredits(milestone.amount)}</td>
                      <td>{milestone.condition}</td>
                      <td>{MILESTONE_STATUS_LABEL[status] ?? status} <span className="en">{status}</span></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Panel>
      ) : null}

      <Panel title="完成通知记录" badge={`${(task.submissions ?? []).filter((s) => s.status === "current").length} 条当前`} open={task.status === "in_review" || task.status === "disputed"}>
        {(task.submissions ?? []).length === 0 ? (
          <p className="hint">还没有完成通知。</p>
        ) : (
          <ul className="plain submission-list">
            {(task.submissions ?? []).map((submission) => (
              <li key={submission.id}>
                <div className="submission-head">
                  <IdText value={submission.id} /> · <UserRef userId={submission.executor_id} nameOnly />
                  {submission.role_id ? <> · 席位 <IdText value={submission.role_id} /></> : null}
                  {submission.milestone_id ? <> · 阶段 <IdText value={submission.milestone_id} /></> : null}
                  <span className="submission-status">{submission.status === "current" ? "当前" : "已作废"}</span>
                </div>
                <p className="hint">{formatTime(submission.created_at)}</p>
                {submission.note ? <p>{submission.note}</p> : null}
                <pre className="json-block">{JSON.stringify(submission.payload, null, 2)}</pre>
              </li>
            ))}
          </ul>
        )}
      </Panel>

      <Panel title="资金托管" badge={task.escrow ? formatCredits(task.escrow.held) : "未注资"}>
        {task.escrow ? (
          <>
            <p>
              托管账户 <IdText value={task.escrow.id} /> · 持有 {formatCredits(task.escrow.held)} · {task.escrow.currency || "credits"}
            </p>
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>类型</th>
                    <th>金额</th>
                    <th>对象</th>
                    <th>时间</th>
                  </tr>
                </thead>
                <tbody>
                  {(task.escrow.entries ?? []).map((entry) => (
                    <tr key={entry.id}>
                      <td>{ENTRY_LABEL[entry.type] ?? entry.type} <span className="en">{entry.type}</span></td>
                      <td>{formatCredits(entry.amount)}</td>
                      <td>{entry.to_user_id ? <UserRef userId={entry.to_user_id} nameOnly /> : "—"}</td>
                      <td title={entry.created_at}>{formatTime(entry.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        ) : (
          <p className="hint">尚未注资，没有托管账户。</p>
        )}
      </Panel>

      {task.chat_id ? (
        <Panel title="任务会话" badge="发布者与执行者" open={task.status === "disputed"}>
          <p className="hint">注资时自动创建，接单的成员自动加入。会话绑定 <IdText value={task.chat_id} />。</p>
          {!signedIn ? (
            <p className="hint">登录后可以查看消息并参与讨论。</p>
          ) : (
            <>
              {chatNote ? <p className="hint">{chatNote}</p> : null}
              {messages.length === 0 && !chatNote ? <p className="hint">还没有消息。</p> : null}
              <ul className="plain chat-inline">
                {messages.map((message) => (
                  <li key={message.id}>
                    <div className="chat-inline-head">
                      <Avatar userId={message.sender_id} className="bubble-avatar" />
                      <SenderName userId={message.sender_id} nameOnly />
                      <time dateTime={message.created_at}>{formatTime(message.created_at)}</time>
                    </div>
                    <div>{message.text || message.attachment_ref}</div>
                  </li>
                ))}
              </ul>
              <div className="chat-send">
                <input
                  aria-label="发一条消息"
                  value={chatText}
                  maxLength={8000}
                  placeholder="发一条消息…"
                  onChange={(event) => setChatText(event.target.value)}
                />
                <Button
                  type="button"
                  pending={pending === "chat"}
                  disabled={pending !== null || !chatText.trim()}
                  onClick={() =>
                    void (async () => {
                      if (!task.chat_id) return;
                      setPending("chat");
                      setError(null);
                      try {
                        const message = await client.postMessage(
                          task.chat_id,
                          { type: "text", text: chatText.trim() },
                          { idempotencyKey: createIdempotencyKey("task-chat") },
                        );
                        setMessages((prev) => [...prev, message]);
                        setChatText("");
                      } catch (err) {
                        setError(formatError(err));
                      } finally {
                        setPending(null);
                      }
                    })()
                  }
                >
                  发送
                </Button>
              </div>
            </>
          )}
        </Panel>
      ) : null}

      <Panel title="全部任务信息">
        <div className="facts task-full-facts">
          {(task.attributes?.latitude != null && task.attributes?.longitude != null) ? (
            <Fact label="坐标" value={`${task.attributes.latitude}, ${task.attributes.longitude}`} />
          ) : null}
          <Fact label="周期" value={RECURRENCE_LABEL[task.attributes?.recurrence ?? "one_time"] ?? "—"} />
          {(task.attributes?.executor_requirements ?? []).length > 0 ? (
            <Fact
              label="完成者条件"
              value={
                <span className="person-list">
                  {(task.attributes?.executor_requirements ?? []).map((item) => (
                    <span key={item} className="card-keyword">{executorRequirementLabel(item)}</span>
                  ))}
                </span>
              }
            />
          ) : null}
          <Fact label="验收标准" value={task.acceptance.quality} />
          <Fact label="验收时限" value={formatTime(task.acceptance.deadline)} raw={task.acceptance.deadline} />
          <Fact label="验收复核时长" value={String(task.acceptance.review_hours ?? "—")} />
          <Fact label="开放截止" value={formatTime(task.open_deadline)} raw={task.open_deadline ?? undefined} />
          <Fact label="开始截止" value={formatTime(task.start_deadline)} raw={task.start_deadline ?? undefined} />
          <Fact label="评审截止" value={formatTime(task.review_deadline)} raw={task.review_deadline ?? undefined} />
          <Fact label="选赢家截止" value={formatTime(task.winner_select_deadline)} raw={task.winner_select_deadline ?? undefined} />
          <Fact label="胜出执行者" value={<IdText value={task.winning_submission_id} />} />
          {task.kind === "competitive" ? (
            <Fact label="竞争" value={`${task.competitive?.slots ?? "—"} 个名额 · ${task.competitive?.settlement_rule ?? "winner_take_all"}`} />
          ) : null}
          {task.kind === "collaborative" ? <Fact label="退出规则" value={task.collaborative?.on_dropout ?? "replace"} /> : null}
        </div>
        {(task.kind === "competitive" || task.kind === "collaborative") && signedIn ? (
          <p className="hint">
            开发配置只有一把执行者密钥 dev-executor-key（usr_executor）。第二个执行者可以执行{" "}
            <code className="mono">make token USER_ID=usr_extra</code>，切换鉴权后来接单或通知完成。
          </p>
        ) : null}
      </Panel>
    </Screen>
  );
}

/** A single, focused action. Only rendered when it is legal for the current viewer. */
function Operation({
  title,
  description,
  primary,
  danger,
  children,
}: {
  title: string;
  description: string;
  primary?: boolean;
  danger?: boolean;
  children: ReactNode;
}) {
  return (
    <div className={cx("op", primary && "op-primary", danger && "op-danger")}>
      <div className="op-head">
        <h3>{title}</h3>
        <p>{description}</p>
      </div>
      <div className="op-body">{children}</div>
    </div>
  );
}

/** Collapsible secondary information, with a badge telling what is inside. */
function Panel({
  title,
  badge,
  open = false,
  children,
}: {
  title: string;
  badge?: ReactNode;
  open?: boolean;
  children: ReactNode;
}) {
  return (
    <details className="card task-panel" open={open}>
      <summary>
        <span className="panel-title">{title}</span>
        {badge ? <span className="panel-badge">{badge}</span> : null}
        <span className="panel-arrow" aria-hidden="true">⌄</span>
      </summary>
      <div className="panel-body">{children}</div>
    </details>
  );
}

function Fact({ label, value, raw }: { label: string; value: ReactNode; raw?: string }) {
  return (
    <div className="summary-item">
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
  if (mode === "per_seat") return "分别结算不需要开始，入席后即可通知完成";
  if (me !== task.publisher_id) return "只有发布者可以开始";
  if (task.status === "active" || task.status === "in_review") return "任务已经开始";
  if (task.status !== "open") return "只有 open 可以开始";
  const open = (task.collaborative?.roles ?? []).filter((role) => (role.status ?? "open") === "open");
  if (open.length > 0) return "坐满后才能开始";
  return null;
}

function perSeatRowAction(task: Task, me: string, status: string | undefined): "pay" | "refund" | null {
  if (settlementMode(task) !== "per_seat") return null;
  if (me !== task.publisher_id) return null;
  if (task.status !== "open" && task.status !== "active" && task.status !== "in_review") return null;
  const seat = status || "open";
  if (seat === "submitted") return "pay";
  if (seat === "open" || seat === "dropped") return "refund";
  return null;
}

function confirmReason(task: Task, me: string): string | null {
  if (me !== task.publisher_id) return "只有发布者可以发起结算";
  const mode = settlementMode(task);
  if (mode === "unified") {
    if (task.status === "open") return "统一结算要先开始，并等全部席位通知完成";
    if (task.status === "active") return "还有席位未通知完成或未坐满，结算会失败";
  }
  if (task.status !== "in_review") return "只有待结算状态可以发起结算";
  return milestoneConfirmBlock(task);
}

function disputeReason(task: Task, me: string): string | null {
  if (me !== task.publisher_id) return "这里由发布者发起争议";
  if (task.status !== "in_review") return "只有 in_review 可以争议";
  return null;
}

function acceptReason(task: Task, me: string, roleId: string): string | null {
  if (!me) return "请在左侧填写当前用户 id";
  if (me === task.publisher_id) return "发布者不能接单自己的任务";
  if (task.kind === "collaborative") {
    if (
      (task.collaborative?.roles ?? []).some(
        (role) => role.assignee_user_id === me && (role.status === "filled" || role.status === "submitted"),
      )
    ) {
      return "你已经占了一个席位";
    }
    if (task.status !== "open" && task.status !== "active") return "当前状态不能接单";
    if (!roleId) return "没有可接单的空席";
    return null;
  }
  if ((task.executor_ids ?? []).includes(me)) return "你已经接单过";
  if (task.kind === "exclusive") {
    if (task.status !== "open") return "单人任务只有 open 可以接单";
    return null;
  }
  if (task.status !== "open" && task.status !== "active") return "当前状态不能接单";
  const slots = task.competitive?.slots ?? 0;
  if ((task.executor_ids ?? []).length >= slots) return "竞争名额已满";
  return null;
}

function submitReason(task: Task, me: string): string | null {
  if (!me) return "请在左侧填写当前用户 id";
  if (task.kind === "collaborative") {
    const mine = (task.collaborative?.roles ?? []).find((role) => role.assignee_user_id === me);
    if (!mine) return "只有当前席位的执行者可以通知完成";
    if (mine.status === "submitted") return "你已经通知过完成";
    if (mine.status !== "filled") return "你没有可通知完成的席位";
    if (settlementMode(task) === "unified" && task.status === "open") return "统一结算要等发布者开始";
    if (task.status !== "active" && task.status !== "in_review") return "任务需要处于进行中";
    return null;
  }
  if (!(task.executor_ids ?? []).includes(me)) return "只有执行者可以通知完成";
  if (task.payment.mode === "milestone") return milestoneSubmitBlock(task);
  if (task.kind === "competitive") {
    if (task.status !== "active" && task.status !== "in_review") return "竞争任务在进行中或待结算时可以通知完成";
    return null;
  }
  if (task.status !== "active") return "任务需要处于进行中";
  return null;
}

function dropoutReason(task: Task, me: string): string | null {
  if (task.kind !== "collaborative") return "只有协作任务可以退出";
  if (task.status !== "open" && task.status !== "active") return "只有 open 或 active 时可以退出";
  const submitted = (task.collaborative?.roles ?? []).find((role) => role.assignee_user_id === me && role.status === "submitted");
  if (submitted) return "已通知完成的席位不能退出";
  const seat = (task.collaborative?.roles ?? []).find((role) => role.assignee_user_id === me && role.status === "filled");
  if (!seat) return "你没有已入席的 role";
  return null;
}

function nextStep(task: Task): string {
  if (task.status === "cancelled") {
    return task.cancel_code ? `任务已取消（${task.cancel_code}）。` : "任务已取消。";
  }
  if (task.status === "settled") return "任务已结算。";
  if (task.status === "disputed" || task.dispute_role_id) return "争议中，可协商或等待裁定。";
  if (task.status === "draft") return "等待发布者注资。";
  const stageStep = milestoneNextStep(task);
  if (stageStep) return stageStep;
  const mode = settlementMode(task);
  if (mode === "unified" && task.status === "open" && task.start_deadline) return "席位已坐满，等待发布者开始。";
  if (task.status === "open") return "等待执行者接单。";
  if (task.status === "active") return "等待执行者通知完成。";
  if (task.status === "in_review" && task.kind === "competitive") return "等待发布者选定结算对象。";
  if (task.status === "in_review") return "等待发布者发起结算。";
  return "查看当前状态。";
}

function reviseReasonOf(task: Task, me: string): string | null {
  if (me !== task.publisher_id) return "只有发布者可以请对方修改";
  if (task.status === "disputed") return "争议中请协商或等待裁定";
  const submitted = (task.collaborative?.roles ?? []).some((role) => role.status === "submitted");
  if (task.status === "in_review" || submitted) return null;
  return "现在没有待结算的完成通知";
}

function resolveReason(task: Task, me: string): string | null {
  if (task.status !== "disputed" && !task.dispute_role_id) return "只有争议中可以协商";
  if (me === task.publisher_id) return null;
  if ((task.executor_ids ?? []).includes(me)) return null;
  return "只有发布者或执行者可以协商";
}

function settlementMode(task: Task): "per_seat" | "unified" | null {
  if (task.kind !== "collaborative") return null;
  return task.collaborative?.settlement_mode === "per_seat" ? "per_seat" : "unified";
}

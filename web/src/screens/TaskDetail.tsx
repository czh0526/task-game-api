import { useEffect, useState } from "react";
import { createIdempotencyKey, type Message, type ResolutionOutcome, type Task } from "taskgame-client";
import { useApp } from "../context";
import {
  ENTRY_LABEL,
  MILESTONE_STATUS_LABEL,
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
import {
  confirmTaskBody,
  currentSubmissionsForMilestone,
  inReviewMilestoneSubmissionId,
  milestoneConfirmBlock,
  milestoneNextStep,
  milestoneSubmitBlock,
  submitMilestoneField,
} from "../stageLoop";
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
  const [payloadText, setPayloadText] = useState('{\n  "body": ""\n}');
  const [note, setNote] = useState("");
  const [submitMilestoneId, setSubmitMilestoneId] = useState("");
  const [confirmMilestoneId, setConfirmMilestoneId] = useState("");
  const [reason, setReason] = useState("");
  const [reviseReason, setReviseReason] = useState("");
  const [outcome, setOutcome] = useState<ResolutionOutcome>("pay_executor");
  const [inviteUser, setInviteUser] = useState("");
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
    if (!client) return;
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
  }, [client]);

  useEffect(() => {
    if (!client || !task?.chat_id) return;
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
  }, [client, task?.chat_id, task?.updated_at]);

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
        <Fact label="开放截止" value={formatTime(task.open_deadline)} raw={task.open_deadline ?? undefined} />
        <Fact label="开始截止" value={formatTime(task.start_deadline)} raw={task.start_deadline ?? undefined} />
        <Fact label="评审截止 review_deadline" value={formatTime(task.review_deadline)} raw={task.review_deadline ?? undefined} />
        <Fact label="选赢家截止" value={formatTime(task.winner_select_deadline)} raw={task.winner_select_deadline ?? undefined} />
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
        <h2>下一步</h2>
        <p>{nextStep(task)}</p>
      </section>

      <section className="card">
        <h2>提交 submissions</h2>
        {(task.submissions ?? []).length === 0 ? <p className="hint">还没有提交。</p> : null}
        <ul className="plain">
          {(task.submissions ?? []).map((submission) => (
            <li key={submission.id}>
              <IdText value={submission.id} /> · <IdText value={submission.executor_id} />
              {submission.role_id ? (
                <>
                  {" "}
                  · 席位 <IdText value={submission.role_id} />
                </>
              ) : null}
              {submission.milestone_id ? (
                <>
                  {" "}
                  · 阶段 <IdText value={submission.milestone_id} />
                </>
              ) : null}{" "}
              · {submission.status === "current" ? "当前" : "已作废"} · {formatTime(submission.created_at)}
              {submission.note ? <div>{submission.note}</div> : null}
              <div className="hint mono">{JSON.stringify(submission.payload)}</div>
            </li>
          ))}
        </ul>
      </section>

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
              ? " 分别结算：拨付和退回在这一席上。已提交的席位可以确认拨付；空席或已退出的席位可以把份额退回发布者。全部席位拨付或退回后任务才 settled。"
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
                <th>席位退回截止</th>
                <th>assignee_user_id</th>
                {mode === "per_seat" ? <th>操作</th> : null}
              </tr>
            </thead>
            <tbody>
              {task.collaborative.roles.map((role) => {
                const action = perSeatRowAction(task, me, role.status);
                return (
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
                    <td title={role.refund_deadline ?? undefined}>{formatTime(role.refund_deadline)}</td>
                    <td>{role.assignee_user_id ? <IdText value={role.assignee_user_id} /> : "—"}</td>
                    {mode === "per_seat" ? (
                      <td className="seat-action">
                        {action === "pay" ? (
                          <Button
                            type="button"
                            kind="primary"
                            className="compact"
                            pending={pending === `seat-${role.id}`}
                            disabled={pending !== null}
                            onClick={() =>
                              void run(`seat-${role.id}`, () =>
                                client.confirmTask(
                                  task.id,
                                  { role_id: role.id },
                                  { idempotencyKey: createIdempotencyKey(`confirm-${role.id}`) },
                                ),
                              )
                            }
                          >
                            确认拨付
                          </Button>
                        ) : null}
                        {action === "refund" ? (
                          <Button
                            type="button"
                            kind="danger"
                            className="compact"
                            pending={pending === `seat-${role.id}`}
                            disabled={pending !== null}
                            onClick={() => {
                              if (!window.confirm(`退回 ${role.id} 的份额给发布者？这一席不会支付执行者。`)) return;
                              void run(`seat-${role.id}`, () =>
                                client.confirmTask(
                                  task.id,
                                  { role_id: role.id },
                                  { idempotencyKey: createIdempotencyKey(`refund-${role.id}`) },
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
        </section>
      ) : null}

      {task.payment.mode === "milestone" ? (
        <section className="card">
          <h2>阶段 milestones</h2>
          <p className="hint">
            确认只拨付该阶段的金额，任务回到 active，直到每个阶段都是 released。同一时间只能有一个阶段处于 submitted。
            {task.kind === "competitive" ? " 竞争任务以该阶段的第一份提交为准，任务回到 active 之前不能再交。" : ""}
          </p>
          {(task.payment.milestones ?? []).length === 0 ? <p className="hint">没有阶段。</p> : null}
          <table>
            <thead>
              <tr>
                <th>id</th>
                <th>金额</th>
                <th>条件 condition</th>
                <th>状态</th>
              </tr>
            </thead>
            <tbody>
              {(task.payment.milestones ?? []).map((milestone) => {
                const status = milestone.status ?? "pending";
                return (
                  <tr key={milestone.id} className={status === "submitted" ? "stage-submitted" : undefined}>
                    <td>
                      <IdText value={milestone.id} />
                    </td>
                    <td>{formatCredits(milestone.amount)}</td>
                    <td>{milestone.condition}</td>
                    <td>
                      {MILESTONE_STATUS_LABEL[status] ?? status} <span className="en">{status}</span>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
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

          {mode === "per_seat" ? (
            <p className="hint">分别结算不在这里确认。已提交的席位点该行的「确认拨付」，空席或已退出的席位点「退回该席」。</p>
          ) : (
            <>
              {task.payment.mode === "milestone" && task.status === "in_review" ? (
                <Field label="确认阶段 milestone_id" hint="只拨付这一阶段的金额。还有未拨付阶段时，任务回到 active，托管留下剩余金额。">
                  <select value={confirmStageId} onChange={(event) => setConfirmMilestoneId(event.target.value)}>
                    {submittedMilestones.length === 0 ? <option value="">没有待确认的阶段</option> : null}
                    {submittedMilestones.map((milestone) => (
                      <option key={milestone.id} value={milestone.id}>
                        {milestone.id} · {formatCredits(milestone.amount)} · {milestone.condition}
                      </option>
                    ))}
                  </select>
                </Field>
              ) : null}
              <ActionRow title="确认 confirm" reason={confirmReason(task, me)}>
                <Button
                  type="button"
                  kind="primary"
                  pending={pending === "confirm"}
                  disabled={confirmReason(task, me) !== null || pending !== null}
                  onClick={() =>
                    void run("confirm", () => {
                      const body = confirmTaskBody(task, chosen, confirmStageId, submissionIds);
                      if (typeof body === "string") throw new Error(body);
                      return client.confirmTask(task.id, body, { idempotencyKey: createIdempotencyKey("confirm") });
                    })
                  }
                >
                  {task.payment.mode === "milestone" ? "确认本阶段" : submissionIds ? "按所选提交确认" : "确认结算"}
                </Button>
              </ActionRow>
            </>
          )}
          {task.kind === "competitive" && task.status === "in_review" ? (
            <div className="picker">
              <p className="hint">
                {task.payment.mode === "milestone"
                  ? "分阶段的竞争任务只评审这一阶段的当前提交。第一份提交进入待确认后，其他人要等确认或退回修改、任务回到 active 才能再交。确认会带上 milestone_id；这一阶段只有一份提交时也会带上它的 submission_id。"
                  : "从任务上的 submissions 里选一份当前提交作为赢家。多份时必须带 submission_id，页面不再从 400 的 message 里猜。"}
                {task.winner_select_deadline ? ` 选赢家截止 ${formatTime(task.winner_select_deadline)}，到点未选定会把托管退回发布者。` : ""}
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
                      <IdText value={submission.executor_id} />
                      <code className="mono">{submission.id}</code>
                    </label>
                  ))}
              </fieldset>
            </div>
          ) : null}

          <ActionRow title="请对方修改 revise" reason={reviseReasonOf(task, me)}>
            <Button
              type="button"
              pending={pending === "revise"}
              disabled={reviseReasonOf(task, me) !== null || pending !== null}
              onClick={() => {
                const text = reviseReason.trim();
                if (!text) {
                  setError("请填写修改原因");
                  return;
                }
                const stageSubmission = inReviewMilestoneSubmissionId(task, chosen);
                if (stageSubmission === null) {
                  setError("没有可退回的阶段提交");
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
              请对方修改
            </Button>
          </ActionRow>
          <Field label="修改原因">
            <input value={reviseReason} maxLength={2000} onChange={(event) => setReviseReason(event.target.value)} />
          </Field>
          {task.payment.mode === "milestone" ? (
            <p className="hint">请对方修改会把这一阶段的当前提交标成 superseded，阶段回到 pending。已经拨付的阶段不会被退回。</p>
          ) : null}

          <ActionRow title="取消 cancel" reason={cancelReason(task, me)}>
            <Button
              type="button"
              kind="danger"
              pending={pending === "cancel"}
              disabled={cancelReason(task, me) !== null || pending !== null}
              onClick={() => {
                if (!window.confirm("取消后，未开始或未提交的托管会退回发布者。已提交的席位不会被这条路径取消。继续？")) return;
                void run("cancel", () =>
                  client.cancelTask(task.id, { reason: reason.trim() || undefined }, { idempotencyKey: createIdempotencyKey("cancel") }),
                );
              }}
            >
              取消任务
            </Button>
          </ActionRow>

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

          {task.payment.mode === "milestone" ? (
            <Field label="提交阶段 milestone_id" hint="只能选择 pending 的阶段。已有阶段待确认时不能再提交。">
              <select value={submitStageId} onChange={(event) => setSubmitMilestoneId(event.target.value)}>
                {pendingMilestones.length === 0 ? <option value="">没有待提交的阶段</option> : null}
                {pendingMilestones.map((milestone) => (
                  <option key={milestone.id} value={milestone.id}>
                    {milestone.id} · {formatCredits(milestone.amount)} · {milestone.condition}
                  </option>
                ))}
              </select>
            </Field>
          ) : null}
          {task.kind === "competitive" && task.payment.mode === "milestone" ? (
            <p className="hint">
              竞争任务里，某一阶段的第一份提交会让任务进入 in_review。在发布者确认或请对方修改、任务回到 active 之前，不能再交这一阶段或下一阶段。
            </p>
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
                const milestoneField = submitMilestoneField(task, submitStageId);
                if (typeof milestoneField === "string") {
                  setError(milestoneField);
                  return;
                }
                void run("submit", () =>
                  client.submitTask(
                    task.id,
                    {
                      payload,
                      note: note.trim() || undefined,
                      ...milestoneField,
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

          {task.status === "disputed" || task.dispute_role_id ? (
            <>
              <Field label="协商结果 outcome">
                <select value={outcome} onChange={(event) => setOutcome(event.target.value as ResolutionOutcome)}>
                  <option value="pay_executor">付给执行者 pay_executor</option>
                  <option value="refund_publisher">退回发布者 refund_publisher</option>
                  <option value="split">按比例拆分 split</option>
                </select>
              </Field>
              <p className="hint">
                双方要提交同一个结果才会生效。当前方案：
                {(task.resolve_proposals ?? []).map((proposal) => `${proposal.side} ${proposal.outcome}`).join("；") || "还没有"}
              </p>
              <ActionRow title="协商 resolve" reason={resolveReason(task, me)}>
                <Button
                  type="button"
                  kind="primary"
                  pending={pending === "resolve"}
                  disabled={resolveReason(task, me) !== null || pending !== null}
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
              </ActionRow>
              {isArbiter ? (
                <ActionRow title="裁定 arbitrate" reason={null}>
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
                </ActionRow>
              ) : (
                <p className="hint">裁定需要 Escrow.ArbiterUserIds 里的用户。开发配置是 usr_arbiter / dev-arbiter-key。</p>
              )}
            </>
          ) : null}

          {me === task.publisher_id && (task.status === "open" || task.status === "active") ? (
            <>
              <Field label="再邀请 user_id" hint="还有空位时可以继续邀请。指定人任务会把 id 加进 assignee_policy。">
                <input value={inviteUser} spellCheck={false} onChange={(event) => setInviteUser(event.target.value)} />
              </Field>
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
                邀请
              </Button>
            </>
          ) : null}
        </div>
        {task.chat_id ? (
          <section className="card">
            <h2>任务会话</h2>
            <p className="hint">
              注资时自动绑定。接受的执行者会加入。 <IdText value={task.chat_id} />
            </p>
            {chatNote ? <p className="hint">{chatNote}</p> : null}
            <ul className="plain">
              {messages.map((message) => (
                <li key={message.id}>
                  <IdText value={message.sender_id} /> · {formatTime(message.created_at)}
                  <div>{message.text || message.attachment_ref}</div>
                </li>
              ))}
            </ul>
            {messages.length === 0 && !chatNote ? <p className="hint">还没有消息。</p> : null}
            <Field label="发一条消息">
              <input value={chatText} maxLength={8000} onChange={(event) => setChatText(event.target.value)} />
            </Field>
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
          </section>
        ) : null}
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
  if (me !== task.publisher_id) return "只有发布者可以确认";
  const mode = settlementMode(task);
  if (mode === "unified") {
    if (task.status === "open") return "统一结算要先开始，并等全部席位提交";
    if (task.status === "active") return "还有席位未提交或未坐满，确认会失败";
  }
  if (task.status !== "in_review") return "只有 in_review 可以确认";
  return milestoneConfirmBlock(task);
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
  if (task.payment.mode === "milestone") return milestoneSubmitBlock(task);
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
  if (task.status === "open") return "等待执行者接受。";
  if (task.status === "active") return "等待执行者提交。";
  if (task.status === "in_review" && task.kind === "competitive") return "等待发布者选定赢家。";
  if (task.status === "in_review") return "等待发布者确认。";
  return "查看当前状态。";
}

function reviseReasonOf(task: Task, me: string): string | null {
  if (me !== task.publisher_id) return "只有发布者可以请对方修改";
  if (task.status === "disputed") return "争议中请协商或等待裁定";
  const submitted = (task.collaborative?.roles ?? []).some((role) => role.status === "submitted");
  if (task.status === "in_review" || submitted) return null;
  return "现在没有待确认的提交";
}

function cancelReason(task: Task, me: string): string | null {
  if (me !== task.publisher_id) return "只有发布者可以取消";
  if (task.status === "draft" || task.status === "open" || task.status === "active") return null;
  if (task.status === "in_review" || task.status === "disputed") return "请改用修改、确认、协商或裁定";
  return "这个状态不能取消";
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

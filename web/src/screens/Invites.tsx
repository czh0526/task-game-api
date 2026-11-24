import { useEffect, useState } from "react";
import { createIdempotencyKey, type Invite } from "taskgame-client";
import { useApp } from "../context";
import { formatError, formatTime, isAbort } from "../format";
import { href } from "../route";
import { Button, ErrorNote, Screen } from "../ui";

export function Invites() {
  const { client, clientError } = useApp();
  const [items, setItems] = useState<Invite[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState<string | null>(null);

  useEffect(() => {
    if (!client) return;
    const ac = new AbortController();
    client
      .listMyInvites({ status: "pending" }, { signal: ac.signal })
      .then((page) => {
        if (!ac.signal.aborted) setItems(page.items);
      })
      .catch((err: unknown) => {
        if (isAbort(err)) return;
        setError(formatError(err));
      });
    return () => ac.abort();
  }, [client]);

  async function accept(invite: Invite) {
    if (!client) return;
    setPending(invite.id);
    setError(null);
    try {
      await client.acceptTask(
        invite.task_id,
        invite.role_id ? { role_id: invite.role_id } : {},
        { idempotencyKey: createIdempotencyKey("accept-invite") },
      );
      setItems((prev) => prev.filter((item) => item.id !== invite.id));
    } catch (err) {
      setError(`${formatError(err)} 旧的协作邀请若未绑定席位，请打开任务详情选一个空席再接受。`);
    } finally {
      setPending(null);
    }
  }

  async function decline(invite: Invite) {
    if (!client) return;
    setPending(invite.id);
    setError(null);
    try {
      await client.declineInvite(invite.id, { idempotencyKey: createIdempotencyKey("decline") });
      setItems((prev) => prev.filter((item) => item.id !== invite.id));
    } catch (err) {
      setError(formatError(err));
    } finally {
      setPending(null);
    }
  }

  if (!client) {
    return (
      <Screen title="邀请">
        <ErrorNote message={clientError} />
      </Screen>
    );
  }

  return (
    <Screen title="邀请" lede="待处理的任务邀请。席位邀请接受后会自动落到对应席位；旧的任务级邀请仍需到任务详情选席位。拒绝之后，指定人任务不能再接，除非发布者重新邀请。">
      <ErrorNote message={error} />
      {items.length === 0 ? <p className="empty">没有待处理的邀请。</p> : null}
      <ul className="task-list">
        {items.map((invite) => (
          <li key={invite.id}>
            <div className="task-top">
              <a href={href.task(invite.task_id)}>{invite.task_id}</a>
              <span className="en">{invite.status}</span>
            </div>
            <p className="meta">
              <span>{formatTime(invite.created_at)}</span>
              {invite.role_id ? <span>席位 {invite.role_id}</span> : null}
              <code className="mono">{invite.id}</code>
            </p>
            <div className="actions">
              <Button type="button" kind="primary" pending={pending === invite.id} disabled={pending !== null} onClick={() => void accept(invite)}>
                接受
              </Button>
              <Button type="button" kind="danger" pending={pending === invite.id} disabled={pending !== null} onClick={() => void decline(invite)}>
                拒绝
              </Button>
            </div>
          </li>
        ))}
      </ul>
    </Screen>
  );
}

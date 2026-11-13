import { useCallback, useEffect, useRef, useState } from "react";
import {
  createIdempotencyKey,
  type Friend,
  type FriendGroup,
  type FriendList,
  type FriendRequest,
} from "taskgame-client";
import { useApp } from "../context";
import { formatError, formatTime, isAbort, validUserId } from "../format";
import { PersonName, rememberNickname } from "../names";
import { openDirectChat } from "../openChat";
import { personLabel } from "../person";
import { subscribeDirectRead } from "../readSync";
import { Button, ErrorNote, Field, OkNote, Screen } from "../ui";

interface GroupDraft {
  name: string;
  sort: string;
}

const FRIEND_POLL_MS = 5000;

export function Friends() {
  const { client, clientError, settings } = useApp();
  const [list, setList] = useState<FriendList | null>(null);
  const [incoming, setIncoming] = useState<FriendRequest[]>([]);
  const [outgoing, setOutgoing] = useState<FriendRequest[]>([]);
  const [drafts, setDrafts] = useState<Record<string, GroupDraft>>({});
  const [userId, setUserId] = useState("");
  const [groupName, setGroupName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const opening = useRef(false);
  const openedAt = useRef(0);

  const reload = useCallback(
    async (signal?: AbortSignal) => {
      if (!client) return;
      const [friends, inc, out] = await Promise.all([
        client.listFriends({ signal }),
        client.listFriendRequests({ direction: "incoming" }, { signal }),
        client.listFriendRequests({ direction: "outgoing" }, { signal }),
      ]);
      if (signal?.aborted) return;
      setList(friends);
      setIncoming(inc.items);
      setOutgoing(out.items);
      setDrafts(
        Object.fromEntries(friends.groups.map((group) => [group.id, { name: group.name, sort: String(group.sort_order) }])),
      );
    },
    [client],
  );

  useEffect(() => {
    if (!client) return;
    const ac = new AbortController();
    const run = () => {
      reload(ac.signal).catch((err: unknown) => {
        if (isAbort(err)) return;
        setError(formatError(err));
      });
    };
    run();
    const timer = window.setInterval(() => {
      if (document.visibilityState !== "visible") return;
      run();
    }, FRIEND_POLL_MS);
    const onShow = () => {
      if (document.visibilityState === "visible") run();
    };
    document.addEventListener("visibilitychange", onShow);
    window.addEventListener("focus", onShow);
    const stop = subscribeDirectRead(settings.userId, run);
    return () => {
      ac.abort();
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", onShow);
      window.removeEventListener("focus", onShow);
      stop();
    };
  }, [client, reload, settings.userId]);

  useEffect(() => {
    const friends = [...(list?.ungrouped ?? [])];
    for (const group of list?.groups ?? []) friends.push(...group.friends);
    for (const friend of friends) rememberNickname(friend.user_id, friend.nickname);
    for (const request of [...incoming, ...outgoing]) {
      rememberNickname(request.from_user_id, request.from_nickname);
      rememberNickname(request.to_user_id, request.to_nickname);
    }
  }, [list, incoming, outgoing]);

  async function run(key: string, action: () => Promise<string | void>) {
    if (!client) return;
    setPending(key);
    setError(null);
    setNote(null);
    try {
      const message = await action();
      if (message) setNote(message);
      await reload();
    } catch (err) {
      setError(formatError(err));
    } finally {
      setPending(null);
    }
  }

  async function openConversation(friend: Friend) {
    if (!client || opening.current || pending !== null) return;
    if (Date.now() - openedAt.current < 600) return;
    openedAt.current = Date.now();
    opening.current = true;
    setPending(`chat-${friend.user_id}`);
    setError(null);
    setNote(null);
    try {
      await openDirectChat(client, friend.user_id);
    } catch (err) {
      setError(formatError(err));
    } finally {
      opening.current = false;
      setPending(null);
    }
  }

  if (!client) {
    return (
      <Screen title="好友">
        <ErrorNote message={clientError} />
      </Screen>
    );
  }

  const groups = list?.groups ?? [];

  return (
    <Screen
      title="好友"
      lede="好友是双方的关系。对方接受之后，两边的列表都会出现彼此。分组只属于你自己，对方看不到你把他们放在哪一组。一个人最多在一个分组里，也可以先不分组。单击一位好友、未读数字，或「发消息」，会在新的浏览器标签里打开你们的一对一会话。已经有过会话就打开原来的那条。红色数字只统计对方在这一对会话里发来、你还没看过的消息，不含任务会话。看过之后数字会消失。"
    >
      <ErrorNote message={error} />
      <OkNote message={note} />

      <form
        className="form"
        onSubmit={(event) => {
          event.preventDefault();
          const id = userId.trim();
          if (!validUserId(id)) {
            setError("用户 id 无效");
            return;
          }
          void run("add", async () => {
            const result = await client.createFriendRequest(
              { user_id: id },
              { idempotencyKey: createIdempotencyKey("friend") },
            );
            setUserId("");
            return result.status === "accepted"
              ? "对方已经向你发过申请，这次直接成为好友。"
              : "申请已发出，等待对方接受。";
          });
        }}
      >
        <Field label="用用户 id 加好友" hint="对方必须已经注册，或曾经使用过这个服务。不能加自己。如果对方已经向你发过申请，这次会直接接受。">
          <input value={userId} spellCheck={false} placeholder="usr_…" onChange={(event) => setUserId(event.target.value)} />
        </Field>
        <Button type="submit" kind="primary" pending={pending === "add"} disabled={pending !== null || !userId.trim()}>
          发送申请
        </Button>
      </form>

      <section className="card">
        <h2>收到的申请</h2>
        {incoming.length === 0 ? <p className="empty">没有待处理的申请。</p> : null}
        <ul className="plain">
          {incoming.map((request) => (
            <li key={request.id} className="friend-row">
              <div>
                <PersonName nickname={request.from_nickname} userId={request.from_user_id} />
                <p className="meta">
                  <span>{formatTime(request.created_at)}</span>
                  <code className="mono">{request.id}</code>
                </p>
              </div>
              <div className="friend-actions">
                <Button
                  type="button"
                  kind="primary"
                  pending={pending === request.id}
                  disabled={pending !== null}
                  onClick={() =>
                    void run(request.id, async () => {
                      await client.acceptFriendRequest(request.id, { idempotencyKey: createIdempotencyKey("accept-friend") });
                      return "已成为好友。";
                    })
                  }
                >
                  接受
                </Button>
                <Button
                  type="button"
                  kind="danger"
                  pending={pending === `decline-${request.id}`}
                  disabled={pending !== null}
                  onClick={() =>
                    void run(`decline-${request.id}`, async () => {
                      await client.declineFriendRequest(request.id, { idempotencyKey: createIdempotencyKey("decline-friend") });
                      return "已拒绝。";
                    })
                  }
                >
                  拒绝
                </Button>
              </div>
            </li>
          ))}
        </ul>
      </section>

      <section className="card">
        <h2>发出的申请</h2>
        {outgoing.length === 0 ? <p className="empty">没有待处理的申请。</p> : null}
        <ul className="plain">
          {outgoing.map((request) => (
            <li key={request.id} className="friend-row">
              <div>
                <PersonName nickname={request.to_nickname} userId={request.to_user_id} />
                <p className="meta">
                  <span>{formatTime(request.created_at)}</span>
                  <span className="en">{request.status}</span>
                </p>
              </div>
              <Button
                type="button"
                kind="danger"
                pending={pending === request.id}
                disabled={pending !== null}
                onClick={() =>
                  void run(request.id, async () => {
                    await client.cancelFriendRequest(request.id, { idempotencyKey: createIdempotencyKey("cancel-friend") });
                    return "已撤回申请。";
                  })
                }
              >
                撤回
              </Button>
            </li>
          ))}
        </ul>
      </section>

      <section className="card">
        <h2>分组</h2>
        <p className="hint">最多 50 个。名称 1–32 个字，去掉首尾空格后不能重复。删除分组不会解除好友。</p>
        <form
          className="friend-actions"
          onSubmit={(event) => {
            event.preventDefault();
            const name = groupName.trim();
            if (!name) return;
            void run("new-group", async () => {
              await client.createFriendGroup({ name }, { idempotencyKey: createIdempotencyKey("group") });
              setGroupName("");
              return `已建立分组「${name}」。`;
            });
          }}
        >
          <input
            value={groupName}
            maxLength={32}
            placeholder="例如 同事"
            onChange={(event) => setGroupName(event.target.value)}
          />
          <Button type="submit" pending={pending === "new-group"} disabled={pending !== null || !groupName.trim()}>
            新建分组
          </Button>
        </form>
        {groups.length === 0 ? <p className="empty">还没有分组。</p> : null}
        <ul className="plain">
          {groups.map((group) => (
            <li key={group.id} className="friend-row">
              <div className="group-edit">
                <input
                  value={drafts[group.id]?.name ?? group.name}
                  maxLength={32}
                  aria-label={`分组名称 ${group.name}`}
                  onChange={(event) =>
                    setDrafts((prev) => ({
                      ...prev,
                      [group.id]: { name: event.target.value, sort: prev[group.id]?.sort ?? String(group.sort_order) },
                    }))
                  }
                />
                <input
                  value={drafts[group.id]?.sort ?? String(group.sort_order)}
                  inputMode="numeric"
                  aria-label={`排序 ${group.name}`}
                  className="sort-input"
                  onChange={(event) =>
                    setDrafts((prev) => ({
                      ...prev,
                      [group.id]: { name: prev[group.id]?.name ?? group.name, sort: event.target.value },
                    }))
                  }
                />
                <Button
                  type="button"
                  pending={pending === `save-${group.id}`}
                  disabled={pending !== null}
                  onClick={() =>
                    void run(`save-${group.id}`, async () => {
                      const draft = drafts[group.id] ?? { name: group.name, sort: String(group.sort_order) };
                      const sort = Number(draft.sort);
                      if (!Number.isInteger(sort)) throw new Error("排序需要是整数");
                      await client.updateFriendGroup(
                        group.id,
                        { name: draft.name, sort_order: sort },
                        { idempotencyKey: createIdempotencyKey("rename-group") },
                      );
                      return "分组已保存。";
                    })
                  }
                >
                  保存
                </Button>
                <Button
                  type="button"
                  kind="danger"
                  pending={pending === `del-${group.id}`}
                  disabled={pending !== null}
                  onClick={() =>
                    void run(`del-${group.id}`, async () => {
                      await client.deleteFriendGroup(group.id, { idempotencyKey: createIdempotencyKey("delete-group") });
                      return `已删除分组「${group.name}」，里面的好友回到未分组。`;
                    })
                  }
                >
                  删除
                </Button>
              </div>
            </li>
          ))}
        </ul>
      </section>

      <FriendSection
        title="未分组"
        friends={list?.ungrouped ?? []}
        groups={groups}
        currentGroupId=""
        pending={pending}
        onMessage={(friend) => void openConversation(friend)}
        onMove={(friend, groupId) =>
          void run(`move-${friend.user_id}`, async () => {
            await client.setFriendGroup(
              friend.user_id,
              { group_id: groupId || null },
              { idempotencyKey: createIdempotencyKey("move-friend") },
            );
            return groupId ? "已移动到分组。" : "已移出分组。";
          })
        }
        onUnfriend={(friend) =>
          void run(`unfriend-${friend.user_id}`, async () => {
            await client.unfriend(friend.user_id, { idempotencyKey: createIdempotencyKey("unfriend") });
            return "已解除好友。";
          })
        }
      />
      {groups.map((group) => (
        <FriendSection
          key={group.id}
          title={group.name}
          friends={group.friends}
          groups={groups}
          currentGroupId={group.id}
          pending={pending}
          onMessage={(friend) => void openConversation(friend)}
          onMove={(friend, groupId) =>
            void run(`move-${friend.user_id}`, async () => {
              await client.setFriendGroup(
                friend.user_id,
                { group_id: groupId || null },
                { idempotencyKey: createIdempotencyKey("move-friend") },
              );
              return groupId ? "已移动到分组。" : "已移出分组。";
            })
          }
          onUnfriend={(friend) =>
            void run(`unfriend-${friend.user_id}`, async () => {
              await client.unfriend(friend.user_id, { idempotencyKey: createIdempotencyKey("unfriend") });
              return "已解除好友。";
            })
          }
        />
      ))}
    </Screen>
  );
}

function openLabel(friend: Friend): string {
  const name = personLabel(friend.nickname, friend.user_id);
  if (friend.unread_count > 0) {
    return `打开与 ${name} 的会话，${friend.unread_count} 条未读`;
  }
  return `打开与 ${name} 的会话`;
}

function UnreadBadge({ count }: { count: number }) {
  if (!Number.isFinite(count) || count <= 0) return null;
  const label = count > 99 ? "99+" : String(count);
  return (
    <span className="unread-badge" title={`${count} 条未读消息`}>
      {label}
    </span>
  );
}

function FriendSection({
  title,
  friends,
  groups,
  currentGroupId,
  pending,
  onMessage,
  onMove,
  onUnfriend,
}: {
  title: string;
  friends: Friend[];
  groups: FriendGroup[];
  currentGroupId: string;
  pending: string | null;
  onMessage: (friend: Friend) => void;
  onMove: (friend: Friend, groupId: string) => void;
  onUnfriend: (friend: Friend) => void;
}) {
  return (
    <section className="card">
      <h2>{title}</h2>
      {friends.length === 0 ? <p className="empty">这里还没有好友。</p> : null}
      <ul className="plain">
        {friends.map((friend) => (
          <li
            key={friend.user_id}
            className="friend-row"
            onDoubleClick={(event) => {
              const target = event.target;
              if (!(target instanceof Element)) return;
              if (target.closest("button, select, input, textarea, a, label")) return;
              onMessage(friend);
            }}
          >
            <button
              type="button"
              className="friend-open"
              disabled={pending !== null}
              aria-label={openLabel(friend)}
              onClick={() => onMessage(friend)}
            >
              <span className="friend-name">
                <PersonName nickname={friend.nickname} userId={friend.user_id} chat={false} />
                <UnreadBadge count={friend.unread_count} />
              </span>
              <p className="meta">
                <span>诚信 {friend.integrity}</span>
                <span>能力 {friend.ability}</span>
                <span>{formatTime(friend.since)}</span>
                <span>单击进入会话</span>
              </p>
            </button>
            <div className="friend-actions">
              <Button
                type="button"
                kind="primary"
                pending={pending === `chat-${friend.user_id}`}
                disabled={pending !== null}
                onClick={() => onMessage(friend)}
              >
                发消息
              </Button>
              <label className="inline">
                <span className="hint">分组</span>
                <select
                  value={currentGroupId}
                  disabled={pending !== null}
                  onChange={(event) => {
                    if (event.target.value !== currentGroupId) onMove(friend, event.target.value);
                  }}
                >
                  <option value="">未分组</option>
                  {groups.map((group) => (
                    <option key={group.id} value={group.id}>
                      {group.name}
                    </option>
                  ))}
                </select>
              </label>
              <Button
                type="button"
                kind="danger"
                pending={pending === `unfriend-${friend.user_id}`}
                disabled={pending !== null}
                onClick={() => {
                  if (!window.confirm("解除后双方都不再是好友，这一对的分组也会清掉。确定解除吗？")) return;
                  onUnfriend(friend);
                }}
              >
                解除好友
              </Button>
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent as ReactDragEvent,
  type ReactNode,
} from "react";
import {
  createIdempotencyKey,
  type Friend,
  type FriendList,
  type FriendRequest,
} from "taskgame-client";
import { Avatar as SharedAvatar } from "../Avatar";
import { useApp } from "../context";
import { formatError, formatTime, isAbort, validUserId } from "../format";
import { friendSections, placementChanged, requestPerson } from "../friendsView";
import { PersonName, rememberNickname } from "../names";
import { openDirectChat } from "../openChat";
import { personLabel } from "../person";
import { subscribeDirectRead } from "../readSync";
import { href } from "../route";
import { Button, cx, ErrorNote, OkNote } from "../ui";
import { Conversation } from "./Conversation";

interface GroupDraft {
  name: string;
  sort: string;
}

type Panel = "contacts" | "requests" | "groups";
type DetailView = "chat" | "profile";
type IconName = "contacts" | "requests" | "groups" | "settings" | "search";

interface ActiveChat {
  userId: string;
  chatId: string;
}

const FRIEND_POLL_MS = 5000;

export function Friends() {
  const { client, clientError, settings } = useApp();
  const [list, setList] = useState<FriendList | null>(null);
  const [incoming, setIncoming] = useState<FriendRequest[]>([]);
  const [outgoing, setOutgoing] = useState<FriendRequest[]>([]);
  const [drafts, setDrafts] = useState<Record<string, GroupDraft>>({});
  const [panel, setPanel] = useState<Panel>("contacts");
  const [query, setQuery] = useState("");
  const [selectedUserId, setSelectedUserId] = useState<string | null>(null);
  const [selectedRequestId, setSelectedRequestId] = useState<string | null>(null);
  const [selectedGroupId, setSelectedGroupId] = useState<string | null>(null);
  const [detailView, setDetailView] = useState<DetailView>("chat");
  const [activeChat, setActiveChat] = useState<ActiveChat | null>(null);
  const [chatBusy, setChatBusy] = useState(false);
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const [dragOverKey, setDragOverKey] = useState<string | null>(null);
  const [userId, setUserId] = useState("");
  const [groupName, setGroupName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const opening = useRef(false);
  const openedAt = useRef(0);
  const chatRequest = useRef(0);

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
        Object.fromEntries(
          friends.groups.map((group) => [group.id, { name: group.name, sort: String(group.sort_order) }]),
        ),
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

  const groups = list?.groups ?? [];
  const allFriends = useMemo(
    () => [...(list?.ungrouped ?? []), ...groups.flatMap((group) => group.friends)],
    [list, groups],
  );
  const sections = useMemo(() => friendSections(groups, list?.ungrouped ?? [], query), [groups, list, query]);
  // While dragging, show every group (including empty ones) as a drop target,
  // and ignore the search box so a filtered-out folder is still reachable.
  const visibleSections = useMemo(
    () => (draggingId ? friendSections(groups, list?.ungrouped ?? [], "", true) : sections),
    [draggingId, groups, list, sections],
  );
  const groupNameById = useMemo(() => {
    const map = new Map<string, string>();
    for (const group of groups) for (const friend of group.friends) map.set(friend.user_id, group.name);
    return map;
  }, [groups]);
  const groupIdById = useMemo(() => {
    const map = new Map<string, string>();
    for (const group of groups) for (const friend of group.friends) map.set(friend.user_id, group.id);
    return map;
  }, [groups]);

  const selectedFriend = allFriends.find((friend) => friend.user_id === selectedUserId) ?? null;
  const selectedGroup = groups.find((group) => group.id === selectedGroupId) ?? null;
  const requests = useMemo(
    () => [
      ...incoming.map((request) => ({ request, direction: "incoming" as const })),
      ...outgoing.map((request) => ({ request, direction: "outgoing" as const })),
    ],
    [incoming, outgoing],
  );
  const selectedRequest = requests.find((item) => item.request.id === selectedRequestId) ?? null;
  const selectedRequestPerson = selectedRequest ? requestPerson(selectedRequest.request, settings.userId) : null;

  useEffect(() => {
    if (selectedUserId && !allFriends.some((friend) => friend.user_id === selectedUserId)) {
      setSelectedUserId(null);
      setActiveChat(null);
    }
    if (activeChat && !allFriends.some((friend) => friend.user_id === activeChat.userId)) {
      setActiveChat(null);
    }
  }, [allFriends, selectedUserId, activeChat]);

  useEffect(() => {
    if (selectedGroupId && groups.length > 0 && !selectedGroup) setSelectedGroupId(null);
  }, [groups, selectedGroup, selectedGroupId]);

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

  function switchPanel(next: Panel) {
    setPanel(next);
    setError(null);
    setNote(null);
  }

  /** Select a contact and open (or reuse) the 1:1 chat directly in the right pane. */
  async function selectFriend(friend: Friend) {
    if (!client) return;
    // Only the latest click may decide which conversation the right pane shows.
    const seq = ++chatRequest.current;
    setSelectedUserId(friend.user_id);
    setDetailView("chat");
    setError(null);
    setNote(null);
    if (activeChat?.userId === friend.user_id) return;
    setActiveChat(null);
    setChatBusy(true);
    try {
      let chatId = friend.direct_chat_id ?? null;
      if (!chatId) {
        const chat = await client.createChat(
          { type: "direct", user_id: friend.user_id },
          { idempotencyKey: createIdempotencyKey("direct-chat") },
        );
        chatId = chat.id;
      }
      if (chatRequest.current !== seq) return;
      setActiveChat({ userId: friend.user_id, chatId });
      // Refresh so the list picks up direct_chat_id and clears the unread badge.
      void reload().catch(() => {});
    } catch (err) {
      if (chatRequest.current !== seq) return;
      setError(formatError(err));
      setDetailView("profile");
    } finally {
      if (chatRequest.current === seq) setChatBusy(false);
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

  function moveFriend(friend: Friend, groupId: string) {
    void run(`move-${friend.user_id}`, async () => {
      await client!.setFriendGroup(
        friend.user_id,
        { group_id: groupId || null },
        { idempotencyKey: createIdempotencyKey("move-friend") },
      );
      return groupId ? "已移动到分组。" : "已移出分组。";
    });
  }

  function moveFriendById(userId: string, groupId: string) {
    const friend = allFriends.find((item) => item.user_id === userId);
    if (!friend) return;
    if (!placementChanged(groupIdById.get(userId) ?? "", groupId)) return;
    moveFriend(friend, groupId);
  }

  function unfriend(friend: Friend) {
    if (!window.confirm(`解除后双方都不再是好友，这一对的分组也会清掉。确定解除与 ${personLabel(friend.nickname, friend.user_id)} 的好友关系吗？`)) {
      return;
    }
    void run(`unfriend-${friend.user_id}`, async () => {
      await client!.unfriend(friend.user_id, { idempotencyKey: createIdempotencyKey("unfriend") });
      return "已解除好友。";
    });
  }

  function onDragStart(event: ReactDragEvent<HTMLButtonElement>, friend: Friend) {
    event.dataTransfer.setData("text/plain", friend.user_id);
    event.dataTransfer.effectAllowed = "move";
    setDraggingId(friend.user_id);
  }

  function onDragEnd() {
    setDraggingId(null);
    setDragOverKey(null);
  }

  if (!client) {
    return (
      <section className="screen wx-friends">
        <ErrorNote message={clientError} />
      </section>
    );
  }

  const needle = query.trim().toLocaleLowerCase();
  const matchesRequest = (request: FriendRequest): boolean => {
    const person = requestPerson(request, settings.userId);
    return (
      !needle ||
      person.userId.toLocaleLowerCase().includes(needle) ||
      (person.nickname ?? "").toLocaleLowerCase().includes(needle) ||
      request.id.toLocaleLowerCase().includes(needle)
    );
  };
  const visibleIncoming = incoming.filter(matchesRequest);
  const visibleOutgoing = outgoing.filter(matchesRequest);
  const displayName = (friend: Friend): string =>
    friend.nickname?.trim() || personLabel(friend.nickname, friend.user_id);

  const addFriendForm = (
    <form
      className="wx-add-form"
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
          setSelectedRequestId(result.id);
          return result.status === "accepted"
            ? "对方已经向你发过申请，这次直接成为好友。"
            : "申请已发出，等待对方接受。";
        });
      }}
    >
      <label htmlFor="wx-add-id">对方的用户 ID</label>
      <div className="wx-add-row">
        <input
          id="wx-add-id"
          value={userId}
          spellCheck={false}
          placeholder="usr_…"
          onChange={(event) => setUserId(event.target.value)}
        />
        <Button type="submit" kind="primary" pending={pending === "add"} disabled={pending !== null || !userId.trim()}>
          发送申请
        </Button>
      </div>
      <p className="wx-hint">对方必须已注册。如果对方先向你发过申请，这次会直接成为好友。</p>
    </form>
  );

  function renderProfile(friend: Friend) {
    return (
      <div className="wx-profile">
        <div className="wx-detail-title">
          <h2>联系人资料</h2>
          <button type="button" className="wx-text-btn" onClick={() => setDetailView("chat")}>
            返回聊天
          </button>
        </div>
        <div className="wx-profile-head">
          <Avatar userId={friend.user_id} name={friend.nickname} large />
          <div className="wx-profile-title">
            <h2>
              <PersonName
                nickname={friend.nickname}
                userId={friend.user_id}
                label={displayName(friend)}
              />
            </h2>
            <p className="wx-id mono">{friend.user_id}</p>
            <span className="wx-chip">{groupNameById.get(friend.user_id) ?? "未分组"}</span>
          </div>
        </div>
        <dl className="wx-profile-fields">
          <div>
            <dt>诚信</dt>
            <dd>{friend.integrity}</dd>
          </div>
          <div>
            <dt>能力</dt>
            <dd>{friend.ability}</dd>
          </div>
          <div>
            <dt>成为好友</dt>
            <dd>{formatTime(friend.since)}</dd>
          </div>
          <div>
            <dt>未读消息</dt>
            <dd>{friend.unread_count > 0 ? `${friend.unread_count} 条` : "无"}</dd>
          </div>
        </dl>
        <div className="wx-profile-actions">
          <Button
            type="button"
            kind="primary"
            pending={pending === `chat-${friend.user_id}`}
            disabled={pending !== null}
            onClick={() => void openConversation(friend)}
          >
            在新标签打开会话
          </Button>
          <label className="wx-inline-field">
            <span>所在分组</span>
            <select
              value={groupIdById.get(friend.user_id) ?? ""}
              disabled={pending !== null}
              onChange={(event) => {
                if (event.target.value !== (groupIdById.get(friend.user_id) ?? "")) {
                  moveFriend(friend, event.target.value);
                }
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
          <Button type="button" kind="danger" disabled={pending !== null} onClick={() => unfriend(friend)}>
            解除好友
          </Button>
        </div>
        <p className="wx-footnote">点击昵称可查看公开资料卡；也可以把左侧联系人拖到分组标题来移动。</p>
      </div>
    );
  }

  function renderChat(friend: Friend, chat: ActiveChat) {
    return (
      <>
        <div className="wx-chat-head">
          <Avatar userId={friend.user_id} name={friend.nickname} />
          <div className="wx-chat-title">
            <strong>{displayName(friend)}</strong>
            <small>
              {groupNameById.get(friend.user_id) ?? "未分组"} · {friend.user_id}
            </small>
          </div>
          <button type="button" className="wx-chat-action" onClick={() => setDetailView("profile")}>
            资料
          </button>
          <button
            type="button"
            className="wx-chat-action"
            title="在新标签打开会话"
            aria-label="在新标签打开会话"
            onClick={() => void openConversation(friend)}
          >
            ↗
          </button>
        </div>
        <Conversation
          key={chat.chatId}
          id={chat.chatId}
          embedded
          onRead={() => {
            void reload().catch(() => {});
          }}
        />
      </>
    );
  }

  const chatOpen = panel === "contacts" && detailView === "chat" && Boolean(activeChat);

  return (
    <section className="screen wx-friends">
      <header className="wx-head">
        <div>
          <h1>通讯录</h1>
          <p>点击左侧联系人，右侧直接聊天；把联系人拖到分组标题即可调整分组。红色数字是还没看过的消息。</p>
        </div>
        <div className="wx-head-meta">
          <span className="wx-total">{allFriends.length} 位好友</span>
          <Button type="button" kind="primary" onClick={() => switchPanel("requests")}>
            添加朋友
          </Button>
        </div>
      </header>

      <ErrorNote message={error} />
      <OkNote message={note} />

      <div className="wx-shell">
        <nav className="wx-rail" aria-label="通讯录功能">
          <button
            type="button"
            className="wx-rail-avatar"
            title="个人设置"
            aria-label="个人设置"
            onClick={() => {
              window.location.hash = href.settings;
            }}
          >
            <SharedAvatar
              userId={settings.userId || "me"}
              name={settings.email || settings.userId || "我"}
              className="wx-rail-avatar-img"
            />
          </button>
          <RailButton name="contacts" label="联系人" active={panel === "contacts"} onClick={() => switchPanel("contacts")} />
          <RailButton
            name="requests"
            label="新的朋友"
            active={panel === "requests"}
            badge={incoming.length}
            onClick={() => switchPanel("requests")}
          />
          <RailButton name="groups" label="分组管理" active={panel === "groups"} onClick={() => switchPanel("groups")} />
          <span className="wx-rail-spacer" />
          <RailButton
            name="settings"
            label="账号设置"
            onClick={() => {
              window.location.hash = href.settings;
            }}
          />
        </nav>

        <aside className="wx-list-pane">
          <div className="wx-search">
            <RailGlyph name="search" />
            <input
              value={query}
              aria-label="搜索"
              placeholder={
                panel === "contacts" ? "搜索昵称或用户 ID" : panel === "requests" ? "搜索好友申请" : "搜索分组"
              }
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>

          {panel === "contacts" && draggingId ? (
            <div className="wx-drag-tip">把联系人拖到下面的分组，松手即可移动</div>
          ) : null}

          {panel === "contacts" ? (
            <>
              <button type="button" className="wx-entry" onClick={() => switchPanel("requests")}>
                <span className="wx-entry-plus">＋</span>
                <span className="wx-entry-text">
                  <strong>新的朋友</strong>
                  <small>好友申请与添加好友</small>
                </span>
                <UnreadBadge count={incoming.length} />
              </button>
              <button type="button" className="wx-entry" onClick={() => switchPanel("groups")}>
                <RailGlyph name="groups" />
                <span className="wx-entry-text">
                  <strong>分组管理</strong>
                  <small>{groups.length} 个分组 · 也可拖动联系人</small>
                </span>
              </button>
              <div className="wx-list-scroll">
                {!draggingId && visibleSections.length === 0 ? (
                  <p className="wx-empty-list">没有匹配的好友</p>
                ) : null}
                {visibleSections.map((section) => (
                  <div key={section.key || "ungrouped"} className="wx-list-section">
                    <div
                      className={cx(
                        "wx-list-section-title",
                        draggingId && "wx-drop-target",
                        dragOverKey === section.key && "wx-drop-over",
                      )}
                      onDragOver={
                        draggingId
                          ? (event) => {
                              event.preventDefault();
                              event.dataTransfer.dropEffect = "move";
                              if (dragOverKey !== section.key) setDragOverKey(section.key);
                            }
                          : undefined
                      }
                      onDragLeave={
                        draggingId
                          ? (event) => {
                              if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
                                setDragOverKey(null);
                              }
                            }
                          : undefined
                      }
                      onDrop={
                        draggingId
                          ? (event) => {
                              event.preventDefault();
                              const dragged = event.dataTransfer.getData("text/plain") || draggingId;
                              setDraggingId(null);
                              setDragOverKey(null);
                              if (dragged) moveFriendById(dragged, section.key);
                            }
                          : undefined
                      }
                    >
                      {section.title} <span>{section.friends.length}</span>
                      {draggingId ? (
                        <em className="wx-drop-hint">
                          {section.key ? `放这里 → 移入「${section.title}」` : "放这里 → 移出分组"}
                        </em>
                      ) : null}
                    </div>
                    {section.friends.map((friend) => (
                      <button
                        key={friend.user_id}
                        type="button"
                        draggable
                        className={cx(
                          "wx-contact",
                          selectedUserId === friend.user_id && "on",
                          draggingId === friend.user_id && "dragging",
                        )}
                        aria-current={selectedUserId === friend.user_id ? "true" : undefined}
                        title="点击开始聊天；拖到分组标题可移动"
                        onDragStart={(event) => onDragStart(event, friend)}
                        onDragEnd={onDragEnd}
                        onClick={() => void selectFriend(friend)}
                      >
                        <Avatar userId={friend.user_id} name={friend.nickname} />
                        <span className="wx-contact-main">
                          <strong>
                            <PersonName
                              nickname={friend.nickname}
                              userId={friend.user_id}
                              chat={false}
                              label={displayName(friend)}
                            />
                          </strong>
                          <small>
                            {groupNameById.get(friend.user_id) ?? "未分组"} · {friend.user_id}
                          </small>
                        </span>
                        <UnreadBadge count={friend.unread_count} />
                      </button>
                    ))}
                  </div>
                ))}
              </div>
            </>
          ) : null}

          {panel === "requests" ? (
            <div className="wx-list-scroll">
              <div className="wx-list-section-title">
                收到的申请 <span>{incoming.length}</span>
              </div>
              {visibleIncoming.length === 0 ? <p className="wx-empty-list">没有待处理的申请</p> : null}
              {visibleIncoming.map((request) => {
                const person = requestPerson(request, settings.userId);
                return (
                  <button
                    key={request.id}
                    type="button"
                    className={cx("wx-contact", selectedRequestId === request.id && "on")}
                    onClick={() => setSelectedRequestId(request.id)}
                  >
                    <Avatar userId={person.userId} name={person.nickname} />
                    <span className="wx-contact-main">
                      <strong>{person.nickname?.trim() || personLabel(person.nickname, person.userId)}</strong>
                      <small>请求添加你 · {formatTime(request.created_at)}</small>
                    </span>
                    <span className="wx-dot" />
                  </button>
                );
              })}
              <div className="wx-list-section-title">
                发出的申请 <span>{outgoing.length}</span>
              </div>
              {visibleOutgoing.length === 0 ? <p className="wx-empty-list">没有待处理的申请</p> : null}
              {visibleOutgoing.map((request) => {
                const person = requestPerson(request, settings.userId);
                return (
                  <button
                    key={request.id}
                    type="button"
                    className={cx("wx-contact", selectedRequestId === request.id && "on")}
                    onClick={() => setSelectedRequestId(request.id)}
                  >
                    <Avatar userId={person.userId} name={person.nickname} />
                    <span className="wx-contact-main">
                      <strong>{person.nickname?.trim() || personLabel(person.nickname, person.userId)}</strong>
                      <small>等待确认 · {formatTime(request.created_at)}</small>
                    </span>
                  </button>
                );
              })}
            </div>
          ) : null}

          {panel === "groups" ? (
            <div className="wx-list-scroll">
              <div className="wx-list-section-title">
                我的分组 <span>{groups.length}</span>
              </div>
              {groups.length === 0 ? <p className="wx-empty-list">还没有分组</p> : null}
              {groups
                .filter((group) => !needle || group.name.toLocaleLowerCase().includes(needle))
                .map((group) => (
                  <button
                    key={group.id}
                    type="button"
                    className={cx("wx-contact", selectedGroupId === group.id && "on")}
                    onClick={() => setSelectedGroupId(group.id)}
                  >
                    <span className="wx-avatar wx-avatar-group" aria-hidden="true">
                      ▦
                    </span>
                    <span className="wx-contact-main">
                      <strong>{group.name}</strong>
                      <small>
                        {group.friends.length} 位好友 · 排序 {group.sort_order}
                      </small>
                    </span>
                  </button>
                ))}
              <button type="button" className="wx-entry wx-entry-inline" onClick={() => setSelectedGroupId(null)}>
                <span className="wx-entry-plus">＋</span>
                <span className="wx-entry-text">
                  <strong>新建分组</strong>
                  <small>最多 50 个</small>
                </span>
              </button>
            </div>
          ) : null}
        </aside>

        <section className={cx("wx-detail-pane", chatOpen && "wx-detail-pane-chat")}>
          {panel === "contacts" ? (
            !selectedFriend ? (
              <EmptyState title="选择一位联系人开始聊天" hint="点击左侧任意联系人，右侧会直接打开你们的一对一会话。" />
            ) : detailView === "profile" ? (
              renderProfile(selectedFriend)
            ) : chatBusy ? (
              <div className="wx-chat-loading">正在打开会话…</div>
            ) : activeChat && activeChat.userId === selectedFriend.user_id ? (
              renderChat(selectedFriend, activeChat)
            ) : (
              <EmptyState
                title="会话还没打开"
                hint="点一下左侧这位联系人，或切到资料面板。"
              />
            )
          ) : null}

          {panel === "requests" ? (
            <div className="wx-detail-section">
              <div className="wx-detail-title">
                <h2>{selectedRequest ? "申请详情" : "新的朋友"}</h2>
                {selectedRequest ? (
                  <button type="button" className="wx-text-btn" onClick={() => setSelectedRequestId(null)}>
                    返回
                  </button>
                ) : null}
              </div>
              {selectedRequest ? (
                <>
                  <div className="wx-request-card">
                    <Avatar
                      userId={selectedRequestPerson?.userId ?? ""}
                      name={selectedRequestPerson?.nickname}
                      large
                    />
                    <div>
                      <h3>
                        {selectedRequestPerson?.nickname?.trim() ||
                          personLabel(selectedRequestPerson?.nickname, selectedRequestPerson?.userId ?? "")}
                      </h3>
                      <p>
                        {selectedRequest.direction === "incoming"
                          ? "请求添加你为好友"
                          : "你请求添加对方为好友"}
                      </p>
                      <p className="wx-id mono">{selectedRequest.request.id}</p>
                      <p>{formatTime(selectedRequest.request.created_at)}</p>
                    </div>
                  </div>
                  <div className="wx-profile-actions">
                    {selectedRequest.direction === "incoming" ? (
                      <>
                        <Button
                          type="button"
                          kind="primary"
                          pending={pending === selectedRequest.request.id}
                          disabled={pending !== null}
                          onClick={() =>
                            void run(selectedRequest.request.id, async () => {
                              await client.acceptFriendRequest(selectedRequest.request.id, {
                                idempotencyKey: createIdempotencyKey("accept-friend"),
                              });
                              return "已成为好友。";
                            })
                          }
                        >
                          接受
                        </Button>
                        <Button
                          type="button"
                          kind="danger"
                          pending={pending === `decline-${selectedRequest.request.id}`}
                          disabled={pending !== null}
                          onClick={() =>
                            void run(`decline-${selectedRequest.request.id}`, async () => {
                              await client.declineFriendRequest(selectedRequest.request.id, {
                                idempotencyKey: createIdempotencyKey("decline-friend"),
                              });
                              return "已拒绝。";
                            })
                          }
                        >
                          拒绝
                        </Button>
                      </>
                    ) : (
                      <Button
                        type="button"
                        kind="danger"
                        pending={pending === `cancel-${selectedRequest.request.id}`}
                        disabled={pending !== null}
                        onClick={() =>
                          void run(`cancel-${selectedRequest.request.id}`, async () => {
                            await client.cancelFriendRequest(selectedRequest.request.id, {
                              idempotencyKey: createIdempotencyKey("cancel-friend"),
                            });
                            return "已撤回申请。";
                          })
                        }
                      >
                        撤回申请
                      </Button>
                    )}
                  </div>
                  <hr className="wx-divider" />
                </>
              ) : (
                <p className="wx-hint">选择一条申请查看详情，或在下面输入用户 ID 添加好友。</p>
              )}
              {addFriendForm}
            </div>
          ) : null}

          {panel === "groups" ? (
            <div className="wx-detail-section">
              <div className="wx-detail-title">
                <h2>{selectedGroup ? "分组设置" : "新建分组"}</h2>
                {selectedGroup ? (
                  <button type="button" className="wx-text-btn" onClick={() => setSelectedGroupId(null)}>
                    新建分组
                  </button>
                ) : null}
              </div>
              <form
                className="wx-group-form"
                onSubmit={(event) => {
                  event.preventDefault();
                  if (selectedGroup) {
                    const draft = drafts[selectedGroup.id] ?? {
                      name: selectedGroup.name,
                      sort: String(selectedGroup.sort_order),
                    };
                    void run(`save-${selectedGroup.id}`, async () => {
                      const sort = Number(draft.sort);
                      if (!Number.isInteger(sort)) throw new Error("排序需要是整数");
                      await client.updateFriendGroup(
                        selectedGroup.id,
                        { name: draft.name, sort_order: sort },
                        { idempotencyKey: createIdempotencyKey("rename-group") },
                      );
                      return "分组已保存。";
                    });
                    return;
                  }
                  const name = groupName.trim();
                  if (!name) return;
                  void run("new-group", async () => {
                    const created = await client.createFriendGroup(
                      { name },
                      { idempotencyKey: createIdempotencyKey("group") },
                    );
                    setGroupName("");
                    setSelectedGroupId(created.id);
                    return `已建立分组「${name}」。`;
                  });
                }}
              >
                <label className="wx-inline-field">
                  <span>分组名称</span>
                  <input
                    value={selectedGroup ? (drafts[selectedGroup.id]?.name ?? selectedGroup.name) : groupName}
                    maxLength={32}
                    placeholder="例如 同事"
                    onChange={(event) => {
                      if (selectedGroup) {
                        setDrafts((prev) => ({
                          ...prev,
                          [selectedGroup.id]: {
                            name: event.target.value,
                            sort: prev[selectedGroup.id]?.sort ?? String(selectedGroup.sort_order),
                          },
                        }));
                      } else {
                        setGroupName(event.target.value);
                      }
                    }}
                  />
                </label>
                {selectedGroup ? (
                  <label className="wx-inline-field">
                    <span>排序</span>
                    <input
                      inputMode="numeric"
                      value={drafts[selectedGroup.id]?.sort ?? String(selectedGroup.sort_order)}
                      onChange={(event) =>
                        setDrafts((prev) => ({
                          ...prev,
                          [selectedGroup.id]: {
                            name: prev[selectedGroup.id]?.name ?? selectedGroup.name,
                            sort: event.target.value,
                          },
                        }))
                      }
                    />
                  </label>
                ) : null}
                <div className="wx-profile-actions">
                  <Button
                    type="submit"
                    kind="primary"
                    pending={pending === (selectedGroup ? `save-${selectedGroup.id}` : "new-group")}
                    disabled={pending !== null}
                  >
                    {selectedGroup ? "保存" : "新建分组"}
                  </Button>
                  {selectedGroup ? (
                    <Button
                      type="button"
                      kind="danger"
                      disabled={pending !== null}
                      onClick={() => {
                        if (!window.confirm(`删除分组「${selectedGroup.name}」？里面的好友会回到未分组。`)) return;
                        void run(`del-${selectedGroup.id}`, async () => {
                          await client.deleteFriendGroup(selectedGroup.id, {
                            idempotencyKey: createIdempotencyKey("delete-group"),
                          });
                          return `已删除分组「${selectedGroup.name}」。`;
                        });
                      }}
                    >
                      删除分组
                    </Button>
                  ) : null}
                </div>
                <p className="wx-hint">最多 50 个分组，名称 1–32 个字。删除分组不会解除好友关系。</p>
              </form>
            </div>
          ) : null}
        </section>
      </div>
    </section>
  );
}

/** Contact-list avatars keep the WeChat tile size but show the uploaded picture. */
function Avatar({
  userId,
  name,
  large = false,
}: {
  userId: string;
  name?: string | null;
  large?: boolean;
}) {
  return <SharedAvatar userId={userId} name={name} large={large} className="wx-avatar" />;
}

function UnreadBadge({ count }: { count: number }) {
  if (!Number.isFinite(count) || count <= 0) return null;
  return (
    <span className="unread-badge wx-unread" title={`${count} 条未读消息`}>
      {count > 99 ? "99+" : String(count)}
    </span>
  );
}

function EmptyState({ title, hint }: { title: string; hint: string }) {
  return (
    <div className="wx-empty">
      <span className="wx-empty-mark" aria-hidden="true">
        💬
      </span>
      <strong>{title}</strong>
      <p>{hint}</p>
    </div>
  );
}

function RailButton({
  name,
  label,
  active = false,
  badge = 0,
  onClick,
}: {
  name: IconName;
  label: string;
  active?: boolean;
  badge?: number;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className={cx("wx-rail-btn", active && "on")}
      title={label}
      aria-label={label}
      aria-current={active ? "page" : undefined}
      onClick={onClick}
    >
      <RailGlyph name={name} />
      {badge > 0 ? <span className="wx-rail-badge">{badge > 99 ? "99+" : badge}</span> : null}
    </button>
  );
}

function RailGlyph({ name }: { name: IconName }): ReactNode {
  const common = {
    width: 22,
    height: 22,
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 1.8,
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    "aria-hidden": true,
  };
  switch (name) {
    case "contacts":
      return (
        <svg {...common}>
          <circle cx="9" cy="8" r="3.2" />
          <path d="M3.5 19c.5-3.2 2.7-5 5.5-5s5 1.8 5.5 5" />
          <path d="M16 6.5a3 3 0 0 1 0 5.8M17.5 14.5c2 .7 3.2 2.3 3.5 4.5" />
        </svg>
      );
    case "requests":
      return (
        <svg {...common}>
          <circle cx="10" cy="8" r="3.2" />
          <path d="M4 19c.5-3.2 2.7-5 5.8-5 1.5 0 2.9.4 3.9 1.2" />
          <path d="M18 14v6M15 17h6" />
        </svg>
      );
    case "groups":
      return (
        <svg {...common}>
          <rect x="3.5" y="4" width="7" height="7" rx="1.5" />
          <rect x="13.5" y="4" width="7" height="7" rx="1.5" />
          <rect x="3.5" y="13" width="7" height="7" rx="1.5" />
          <rect x="13.5" y="13" width="7" height="7" rx="1.5" />
        </svg>
      );
    case "settings":
      return (
        <svg {...common}>
          <circle cx="12" cy="12" r="3" />
          <path d="M12 2.8v2.4M12 18.8v2.4M4.6 7.5l2.1 1.2M17.3 15.3l2.1 1.2M4.6 16.5l2.1-1.2M17.3 8.7l2.1-1.2" />
        </svg>
      );
    case "search":
      return (
        <svg {...common} width={16} height={16}>
          <circle cx="10.5" cy="10.5" r="6.5" />
          <path d="m15.5 15.5 4.5 4.5" />
        </svg>
      );
  }
}

import { useState } from "react";
import { createIdempotencyKey, type Message } from "taskgame-client";
import { appendMessage, loadChats, saveChats, upsertChat, type LocalChat, type LocalMessage } from "../chats";
import { useApp } from "../context";
import { counterparty, formatError, formatTime, validUserId } from "../format";
import { Button, ErrorNote, Field, IdText, Screen } from "../ui";

export function ChatPane() {
  const { client, clientError, settings } = useApp();
  const [chats, setChats] = useState<LocalChat[]>(() => loadChats());
  const [activeId, setActiveId] = useState<string>(() => loadChats()[0]?.id ?? "");
  const [members, setMembers] = useState(() => counterparty(settings.userId));
  const [existingId, setExistingId] = useState("");
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState<"create" | "post" | null>(null);

  const active = chats.find((chat) => chat.id === activeId) ?? null;

  async function onCreate() {
    if (!client) return;
    const ids = members
      .split(/[\s,]+/)
      .map((item) => item.trim())
      .filter(Boolean);
    if (ids.length === 0) {
      setError("请填写对方的 user id");
      return;
    }
    if (ids.some((id) => !validUserId(id))) {
      setError("member_ids 里有无效的用户 id");
      return;
    }
    setPending("create");
    setError(null);
    try {
      const chat = await client.createChat({ member_ids: ids }, { idempotencyKey: createIdempotencyKey("chat") });
      const next = upsertChat({ id: chat.id, member_ids: chat.member_ids, messages: [] });
      setChats(next);
      setActiveId(chat.id);
    } catch (err) {
      setError(formatError(err));
    } finally {
      setPending(null);
    }
  }

  function onOpenExisting() {
    const id = existingId.trim();
    if (!/^cht_[A-Za-z0-9]+$/.test(id)) {
      setError("chat id 需要匹配 ^cht_");
      return;
    }
    setError(null);
    const known = loadChats();
    if (!known.some((chat) => chat.id === id)) {
      known.unshift({ id, member_ids: [], messages: [] });
      saveChats(known);
      setChats(known);
    } else {
      setChats(known);
    }
    setActiveId(id);
  }

  async function onPost() {
    if (!client || !active) return;
    const body = text.trim();
    if (!body) {
      setError("请填写文字");
      return;
    }
    setPending("post");
    setError(null);
    try {
      const message = await client.postMessage(
        active.id,
        { type: "text", text: body },
        { idempotencyKey: createIdempotencyKey("msg") },
      );
      setChats(appendMessage(active.id, toLocal(message)));
      setText("");
    } catch (err) {
      setError(formatError(err));
    } finally {
      setPending(null);
    }
  }

  return (
    <Screen
      title="会话"
      lede="v0.1 可以创建会话并发送文字。没有列出消息的接口，所以下面只显示本浏览器 postMessage 拿到的记录。两个角色共用这份 localStorage，可以在同一台机器上对看。"
    >
      <ErrorNote message={client ? error : clientError} />
      <form
        className="form"
        onSubmit={(event) => {
          event.preventDefault();
          void onCreate();
        }}
      >
        <Field label="对方 member_ids" hint="服务会把当前调用者并进成员。多个 id 用逗号或空格分开。">
          <input value={members} spellCheck={false} onChange={(event) => setMembers(event.target.value)} />
        </Field>
        <div className="toolbar">
          <Button type="submit" kind="primary" pending={pending === "create"} disabled={!client || pending !== null}>
            创建会话
          </Button>
          <Button
            type="button"
            kind="ghost"
            onClick={() => {
              saveChats([]);
              setChats([]);
              setActiveId("");
            }}
          >
            清空本机记录
          </Button>
        </div>
      </form>

      <form
        className="toolbar"
        onSubmit={(event) => {
          event.preventDefault();
          onOpenExisting();
        }}
      >
        <Field label="已有 chat id">
          <input value={existingId} spellCheck={false} placeholder="cht_…" onChange={(event) => setExistingId(event.target.value)} />
        </Field>
        <Button type="submit" kind="secondary">
          记到本机列表
        </Button>
      </form>

      <div className="chat-layout">
        <ul className="chat-list">
          {chats.length === 0 ? <li className="empty">还没有会话。</li> : null}
          {chats.map((chat) => (
            <li key={chat.id}>
              <button type="button" className={chat.id === activeId ? "chat-pick on" : "chat-pick"} onClick={() => setActiveId(chat.id)}>
                <IdText value={chat.id} />
                <span className="hint">{chat.member_ids.join(", ") || "成员未知"}</span>
              </button>
            </li>
          ))}
        </ul>
        <div className="thread">
          {active ? (
            <>
              <p className="hint">
                成员 {active.member_ids.length > 0 ? active.member_ids.join(", ") : "创建响应里没有带到本机"}
              </p>
              <div className="messages">
                {active.messages.length === 0 ? <p className="empty">还没有在本机发送过消息。</p> : null}
                {active.messages.map((message) => (
                  <article key={message.id} className={message.sender_id === settings.userId ? "bubble mine" : "bubble"}>
                    <header>
                      <IdText value={message.sender_id} />
                      <time dateTime={message.created_at}>{formatTime(message.created_at)}</time>
                    </header>
                    <p>{message.text}</p>
                    <footer>
                      <code className="mono">{message.id}</code>
                    </footer>
                  </article>
                ))}
              </div>
              <form
                className="composer"
                onSubmit={(event) => {
                  event.preventDefault();
                  void onPost();
                }}
              >
                <textarea
                  rows={3}
                  maxLength={8000}
                  value={text}
                  placeholder="发送 type=text"
                  onChange={(event) => setText(event.target.value)}
                />
                <Button type="submit" kind="primary" pending={pending === "post"} disabled={!client || pending !== null}>
                  发送
                </Button>
              </form>
            </>
          ) : (
            <p className="empty">选择或创建一个会话。</p>
          )}
        </div>
      </div>
    </Screen>
  );
}

function toLocal(message: Message): LocalMessage {
  return {
    id: message.id,
    chat_id: message.chat_id,
    sender_id: message.sender_id,
    type: message.type,
    text: message.text,
    created_at: message.created_at,
  };
}

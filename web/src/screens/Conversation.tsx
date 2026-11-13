import { useCallback, useEffect, useRef, useState } from "react";
import { createIdempotencyKey, type Chat, type Message } from "taskgame-client";
import { useApp } from "../context";
import { formatError, formatTime, isAbort } from "../format";
import { PersonName, UserRef, useNickname } from "../names";
import { personLabel } from "../person";
import { notifyDirectRead } from "../readSync";
import { href } from "../route";
import { Button, ErrorNote, IdText, Screen } from "../ui";

const PAGE = 100;
const MAX_PAGES = 20;

export function Conversation({ id }: { id: string }) {
  const { client, clientError, settings } = useApp();
  const [chat, setChat] = useState<Chat | null>(null);
  const [messages, setMessages] = useState<Message[]>([]);
  const [truncated, setTruncated] = useState(false);
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [loading, setLoading] = useState(true);
  const seenCursor = useRef<string | null>(null);

  const load = useCallback(
    async (signal?: AbortSignal) => {
      if (!client) return;
      const meta = await client.getChat(id, { signal });
      const items: Message[] = [];
      let cursor: string | undefined;
      let more = false;
      for (let page = 0; page < MAX_PAGES; page += 1) {
        const history = await client.listMessages(id, { limit: PAGE, cursor }, { signal });
        items.push(...history.items);
        if (!history.next_cursor) {
          more = false;
          break;
        }
        cursor = history.next_cursor;
        more = page === MAX_PAGES - 1;
      }
      if (signal?.aborted) return;
      setChat(meta);
      setMessages(items);
      setTruncated(more);
      if (document.visibilityState !== "visible") return;
      const seen = items.length === 0 ? "" : items[items.length - 1].id;
      if (seenCursor.current === seen) return;
      await client.markChatRead(id, seen ? { message_id: seen } : {}, { signal });
      if (signal?.aborted) return;
      seenCursor.current = seen;
      if (meta.type === "direct") notifyDirectRead(settings.userId, id);
    },
    [client, id, settings.userId],
  );

  useEffect(() => {
    if (!client) return;
    const ac = new AbortController();
    setLoading(true);
    setError(null);
    load(ac.signal)
      .catch((err: unknown) => {
        if (isAbort(err)) return;
        setError(formatError(err));
      })
      .finally(() => {
        if (!ac.signal.aborted) setLoading(false);
      });
    return () => ac.abort();
  }, [client, load]);

  useEffect(() => {
    if (!client || error) return;
    const refresh = () => {
      if (document.visibilityState !== "visible") return;
      load().catch((err: unknown) => {
        if (isAbort(err)) return;
        setError(formatError(err));
      });
    };
    const timer = window.setInterval(refresh, 5000);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", refresh);
    };
  }, [client, error, load]);

  async function onPost() {
    if (!client) return;
    const body = text.trim();
    if (!body) return;
    setPending(true);
    setError(null);
    try {
      const message = await client.postMessage(
        id,
        { type: "text", text: body },
        { idempotencyKey: createIdempotencyKey("msg") },
      );
      setMessages((prev) => (prev.some((item) => item.id === message.id) ? prev : [...prev, message]));
      setText("");
    } catch (err) {
      setError(formatError(err));
    } finally {
      setPending(false);
    }
  }

  const peer = chat ? otherMember(chat, settings.userId) : "";
  const peerNick = useNickname(peer);
  const peerLabel = personLabel(peerNick, peer);

  if (!client) {
    return (
      <Screen title="会话">
        <ErrorNote message={clientError} />
      </Screen>
    );
  }
  const title = !chat ? "会话" : chat.type === "direct" ? `与 ${peerLabel} 的会话` : chat.type === "task" ? "任务会话" : "会话";
  const lede = ledeFor(chat);

  return (
    <Screen title={title} lede={lede}>
      <ErrorNote message={error} />
      {loading && !chat ? <p className="hint">正在读取会话…</p> : null}
      {chat ? (
        <div className="thread">
          <p className="hint">
            <span className="pill kind">{kindLabel(chat.type)}</span> <IdText value={chat.id} />
            {chat.task_id ? (
              <>
                {" "}
                <a href={href.task(chat.task_id)}>打开任务</a>
              </>
            ) : null}
            {chat.type === "direct" && peer ? (
              <span>
                {" "}
                对方 <PersonName nickname={peerNick} userId={peer} />
              </span>
            ) : null}
            {chat.type !== "direct" ? (
              <span className="person-list">
                {" "}
                成员{" "}
                {chat.member_ids.map((member) => (
                  <UserRef key={member} userId={member} />
                ))}
              </span>
            ) : null}
          </p>
          <div className="toolbar">
            <Button
              type="button"
              kind="ghost"
              onClick={() => {
                setError(null);
                load().catch((err: unknown) => setError(formatError(err)));
              }}
            >
              刷新
            </Button>
          </div>
          <div className="messages">
            {messages.length === 0 ? <p className="empty">还没有消息。写一句发给对方。</p> : null}
            {messages.map((message) => (
              <article key={message.id} className={message.sender_id === settings.userId ? "bubble mine" : "bubble"}>
                <header>
                  <UserRef userId={message.sender_id} />
                  <time dateTime={message.created_at}>{formatTime(message.created_at)}</time>
                </header>
                <p>{message.text || message.attachment_ref}</p>
              </article>
            ))}
          </div>
          {truncated ? <p className="hint">消息较多，这里只显示读到的前 {MAX_PAGES * PAGE} 条。</p> : null}
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
              placeholder="发送文字"
              onChange={(event) => setText(event.target.value)}
            />
            <Button type="submit" kind="primary" pending={pending} disabled={pending || !text.trim()}>
              发送
            </Button>
          </form>
        </div>
      ) : null}
    </Screen>
  );
}

function otherMember(chat: Chat, me: string): string {
  const others = chat.member_ids.filter((member) => member !== me);
  if (others.length === 1) return others[0] ?? "";
  if (others.length > 1) return others.join("、");
  return chat.member_ids[0] ?? "";
}

function kindLabel(type: Chat["type"]): string {
  if (type === "direct") return "好友会话";
  if (type === "task") return "任务会话";
  return "多人会话";
}

function ledeFor(chat: Chat | null): string {
  if (!chat) return "读取这条会话的历史，并在这里继续发消息。";
  if (chat.type === "direct") {
    return "这是你们两个人的会话，和任务里的会话不是同一条。解除好友之后，已经有的消息仍然可以看，也可以继续发。";
  }
  if (chat.type === "task") {
    return "这是任务注资时绑定的会话。任务详情里也能打开同一条。";
  }
  return "这是一条多人会话。成员可以在这里发消息。";
}

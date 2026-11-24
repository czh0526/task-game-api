import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { createIdempotencyKey, type Chat, type Message } from "taskgame-client";
import { Avatar } from "../Avatar";
import { centerMessageScrollTop, messageTopInContent } from "../chatView";
import { useApp } from "../context";
import { asStickerChar, EMOJI_STICKERS, insertAtCaret, stickerLabel } from "../emojis";
import { subscribeSignal } from "../eventStream";
import { formatError, formatTime, isAbort } from "../format";
import { SenderName, UserRef, useNickname } from "../names";
import { personLabel } from "../person";
import { notifyDirectRead } from "../readSync";
import { href } from "../route";
import { Button, cx, ErrorNote, IdText, Screen } from "../ui";

const PAGE = 100;
const MAX_PAGES = 20;
/** Safety net for a lost SSE frame; normal refreshes are signal-driven. */
const FALLBACK_POLL_MS = 45_000;

export function Conversation({
  id,
  embedded = false,
  onRead,
}: {
  id: string;
  /** Render without the page title so the thread fits inside the friends detail pane. */
  embedded?: boolean;
  /** Called after a direct chat is marked read, so the host can refresh unread counts. */
  onRead?: () => void;
}) {
  const { client, clientError, settings } = useApp();
  const onReadRef = useRef(onRead);
  onReadRef.current = onRead;
  const [chat, setChat] = useState<Chat | null>(null);
  const [messages, setMessages] = useState<Message[]>([]);
  const [truncated, setTruncated] = useState(false);
  const [text, setText] = useState("");
  const [emojiOpen, setEmojiOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [loading, setLoading] = useState(true);
  const seenCursor = useRef<string | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const messagesRef = useRef<HTMLDivElement | null>(null);
  const newestRef = useRef<HTMLElement | null>(null);
  /** Only re-center when a genuinely new message shows up, not on unrelated re-renders. */
  const centeredIdRef = useRef<string | null>(null);

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
      if (meta.type === "direct") {
        notifyDirectRead(settings.userId, id);
        onReadRef.current?.();
      }
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
    // SSE signals drive normal refreshes; this slow timer only covers a
    // dropped frame or a stream that cannot be established at all.
    const timer = window.setInterval(refresh, FALLBACK_POLL_MS);
    document.addEventListener("visibilitychange", refresh);
    const stop = subscribeSignal(() => refresh());
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", refresh);
      stop();
    };
  }, [client, error, load]);

  // A newly displayed message should land with its first line at the vertical
  // middle of the panel, so it is easy to spot regardless of scroll position.
  // Key on the newest id: a re-render or a refresh with no new message must not
  // yank the reader away from older messages they are looking at.
  const newestId = messages.length > 0 ? messages[messages.length - 1]?.id : null;
  useLayoutEffect(() => {
    if (!newestId || newestId === centeredIdRef.current) return;
    const panel = messagesRef.current;
    const newest = newestRef.current;
    if (!panel || !newest) return;
    centeredIdRef.current = newestId;
    const top = messageTopInContent({
      messageViewportTop: newest.getBoundingClientRect().top,
      panelViewportTop: panel.getBoundingClientRect().top,
      panelScrollTop: panel.scrollTop,
    });
    panel.scrollTop = centerMessageScrollTop({
      messageTopInContent: top,
      panelHeight: panel.clientHeight,
      maxScrollTop: panel.scrollHeight - panel.clientHeight,
    });
  }, [newestId]);

  async function post(body: string, type: "text" | "sticker") {
    body = body.trim();
    if (!client || !body || pending) return;
    setPending(true);
    setError(null);
    try {
      const message = await client.postMessage(
        id,
        { type, text: body },
        { idempotencyKey: createIdempotencyKey("msg") },
      );
      setMessages((prev) => (prev.some((item) => item.id === message.id) ? prev : [...prev, message]));
      setText("");
      setEmojiOpen(false);
    } catch (err) {
      setError(formatError(err));
    } finally {
      setPending(false);
    }
  }

  async function onPost() {
    const body = text.trim();
    const sticker = asStickerChar(body);
    await post(body, sticker ? "sticker" : "text");
  }

  function addEmoji(char: string) {
    const el = textareaRef.current;
    const start = el?.selectionStart ?? text.length;
    const end = el?.selectionEnd ?? text.length;
    const next = insertAtCaret(text, char, start, end);
    setText(next.text);
    requestAnimationFrame(() => {
      el?.focus();
      el?.setSelectionRange(next.caret, next.caret);
    });
  }

  const peer = chat ? otherMember(chat, settings.userId) : "";
  const peerNick = useNickname(peer);
  const peerLabel = personLabel(peerNick, peer);

  if (!client) {
    const body = <ErrorNote message={clientError} />;
    return embedded ? <div className="chat-embedded">{body}</div> : <Screen title="会话">{body}</Screen>;
  }
  const title = !chat ? "会话" : chat.type === "direct" ? `与 ${peerLabel} 的会话` : chat.type === "task" ? "任务会话" : "会话";
  const lede = ledeFor(chat);

  const content = (
    <>
      <ErrorNote message={error} />
      {loading && !chat ? <p className="hint">正在读取会话…</p> : null}
      {chat ? (
        <div className={cx("thread", embedded && "thread-embedded")}>
          <p className="hint">
            <span className="pill kind">{kindLabel(chat.type)}</span> <IdText value={chat.id} />
            {chat.task_id ? (
              <>
                {" "}
                <a href={href.task(chat.task_id)}>打开任务</a>
              </>
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
          <div className="messages" ref={messagesRef}>
            {messages.length === 0 ? <p className="empty">还没有消息。写一句发给对方。</p> : null}
            {messages.map((message, index) => (
              <article
                key={message.id}
                ref={index === messages.length - 1 ? newestRef : undefined}
                className={message.sender_id === settings.userId ? "bubble mine" : "bubble"}
              >
                <header>
                  <span className="bubble-sender">
                    <Avatar userId={message.sender_id} className="bubble-avatar" />
                    <SenderName userId={message.sender_id} />
                  </span>
                  <time dateTime={message.created_at}>{formatTime(message.created_at)}</time>
                </header>
                {message.type === "sticker" ? (
                  <p className="sticker" title={stickerLabel(message.text ?? "")}>
                    <span className="sticker-face" aria-hidden="true">{message.text}</span>
                    <span className="sticker-label">{stickerLabel(message.text ?? "")}</span>
                  </p>
                ) : (
                  <p>{message.text || message.attachment_ref}</p>
                )}
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
            <div className="composer-input">
              <textarea
                ref={textareaRef}
                rows={3}
                maxLength={8000}
                value={text}
                placeholder="写消息，点表情可插入文字中；只发一个表情会显示为大表情包"
                onChange={(event) => setText(event.target.value)}
              />
              <div className="emoji-bar">
                <button
                  type="button"
                  className={emojiOpen ? "emoji-toggle on" : "emoji-toggle"}
                  aria-expanded={emojiOpen}
                  disabled={pending}
                  onClick={() => setEmojiOpen((open) => !open)}
                >
                  😀 表情
                </button>
                {emojiOpen ? (
                  <div className="emoji-panel" role="group" aria-label="插入表情到消息">
                    {EMOJI_STICKERS.map((sticker) => (
                      <button
                        type="button"
                        key={sticker.char}
                        className="emoji-option"
                        title={sticker.label}
                        aria-label={`插入表情 ${sticker.label}`}
                        disabled={pending}
                        onClick={() => addEmoji(sticker.char)}
                      >
                        <span aria-hidden="true">{sticker.char}</span>
                        <small>{sticker.label}</small>
                      </button>
                    ))}
                  </div>
                ) : null}
              </div>
            </div>
            <Button type="submit" kind="primary" pending={pending} disabled={pending || !text.trim()}>
              发送
            </Button>
          </form>
        </div>
      ) : null}
    </>
  );

  if (embedded) return <div className="chat-embedded">{content}</div>;
  return (
    <Screen title={title} lede={lede}>
      {content}
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
  if (type === "direct") return "一对一";
  if (type === "task") return "任务会话";
  return "多人会话";
}

function ledeFor(chat: Chat | null): string {
  if (!chat) return "读取这条会话的历史，并在这里继续发消息。";
  if (chat.type === "direct") {
    return "这是你们两个人的会话，和任务里的会话不是同一条。点别人的昵称也会打开或回到这一条，不需要先加好友。";
  }
  if (chat.type === "task") {
    return "这是任务注资时绑定的会话。任务详情里也能打开同一条。";
  }
  return "这是一条多人会话。成员可以在这里发消息。";
}

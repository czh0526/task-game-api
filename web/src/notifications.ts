/**
 * Desktop notifications for incoming chat signals.
 *
 * This is the *display* half of the SSE channel: the stream only carries ids,
 * so a notification says "new message" and the click opens the thread, which
 * then fetches the message body over the normal REST endpoint. Permission is
 * requested only from a user gesture (the settings button / top-bar menu).
 */
import { subscribeSignal, type StreamSignal } from "./eventStream";
import { peekNickname } from "./names";
import {
  notificationStatus,
  notificationTitle,
  permissionLabel,
  shouldNotify,
  type NotifyContext,
  type NotifyPermission,
} from "./notificationView";
import { senderDisplayName } from "./person";
import { href } from "./route";

const PREF_KEY = "taskgame.web.desktopNotifications";
const PREF_EVENT = "taskgame:notify-pref";

export type NotifyBaseContext = Pick<NotifyContext, "myUserId" | "visible" | "activeChatId">;

export function notificationSupport(): boolean {
  return typeof Notification !== "undefined";
}

export function notificationPermission(): NotifyPermission {
  if (!notificationSupport()) return "unsupported";
  return Notification.permission as NotifyPermission;
}

export function notificationsEnabled(): boolean {
  try {
    return localStorage.getItem(PREF_KEY) === "1";
  } catch {
    return false;
  }
}

function setEnabled(enabled: boolean): void {
  try {
    if (enabled) localStorage.setItem(PREF_KEY, "1");
    else localStorage.removeItem(PREF_KEY);
  } catch {
    // Private mode: the toggle still works for this tab.
  }
  window.dispatchEvent(new CustomEvent<boolean>(PREF_EVENT, { detail: enabled }));
}

export function subscribeNotificationPref(handler: (enabled: boolean) => void): () => void {
  const onEvent = (event: Event) => handler((event as CustomEvent<boolean>).detail === true);
  window.addEventListener(PREF_EVENT, onEvent);
  return () => window.removeEventListener(PREF_EVENT, onEvent);
}

/** Must be called from a click handler: browsers only grant permission on a gesture. */
export async function toggleDesktopNotifications(): Promise<string> {
  if (!notificationSupport()) return "当前浏览器不支持桌面通知，仍可在顶栏看到未读角标。";
  if (notificationsEnabled()) {
    setEnabled(false);
    return "已关闭桌面通知。";
  }
  let permission = notificationPermission();
  if (permission === "default") {
    try {
      permission = (await Notification.requestPermission()) as NotifyPermission;
    } catch {
      permission = "denied";
    }
  }
  if (permission !== "granted") {
    return permission === "denied"
      ? "浏览器已拒绝通知权限，请在站点设置里手动允许。"
      : "未获得通知权限。";
  }
  setEnabled(true);
  return "已开启桌面通知。页面不可见或不在该会话时会弹出提示。";
}

export { notificationStatus, permissionLabel };
export type { NotifyPermission } from "./notificationView";

/**
 * Watch the global SSE signal bus for messages from other members and raise a
 * system notification. Returns an unsubscribe function. The context getter is
 * evaluated per signal so route/visibility changes are always current.
 */
export function startDesktopNotifications(getContext: () => NotifyBaseContext): () => void {
  const seen = new Set<string>();
  return subscribeSignal((signal: StreamSignal) => {
    const ctx: NotifyContext = {
      ...getContext(),
      permission: notificationPermission(),
      enabled: notificationsEnabled(),
    };
    if (!shouldNotify(signal, ctx)) return;
    const key = signal.message_id || `${signal.chat_id}:${signal.sender_id}`;
    if (seen.has(key)) return;
    if (seen.size > 256) seen.clear();
    seen.add(key);
    show(signal.chat_id as string, signal.sender_id ?? "");
  });
}

function show(chatId: string, senderId: string): void {
  const nickname = peekNickname(senderId);
  const label = senderDisplayName(nickname, senderId);
  try {
    const note = new Notification(notificationTitle(label), {
      body: "点击查看新消息",
      // One notification per thread: a burst of messages replaces the previous
      // bubble instead of stacking a wall of toasts.
      tag: `taskgame-chat-${chatId}`,
    });
    note.onclick = () => {
      window.focus();
      window.location.hash = href.conversation(chatId);
      note.close();
    };
  } catch {
    // Some browsers throw when constructing a Notification outside a service
    // worker context; the unread badge remains the fallback.
  }
}

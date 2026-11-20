/**
 * Pure presentation rules for the notification layer. Kept free of DOM/React
 * imports so the same rules can be unit-tested in plain Node.
 */

export interface UnreadFriend {
  unread_count?: number | null;
}

export interface UnreadList {
  groups?: { friends?: UnreadFriend[] }[];
  ungrouped?: UnreadFriend[];
}

/** Sum the caller's unread direct-chat messages across grouped and ungrouped friends. */
export function sumUnread(list: UnreadList | null | undefined): number {
  if (!list) return 0;
  let total = 0;
  for (const group of list.groups ?? []) {
    for (const friend of group.friends ?? []) total += cleanCount(friend?.unread_count);
  }
  for (const friend of list.ungrouped ?? []) total += cleanCount(friend?.unread_count);
  return total;
}

function cleanCount(value: number | null | undefined): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? Math.floor(value) : 0;
}

/** Badge text, or null when the counter should be hidden. */
export function badgeLabel(count: number): string | null {
  if (!Number.isFinite(count) || count <= 0) return null;
  return count > 99 ? "99+" : String(Math.floor(count));
}

export interface NotifySignal {
  type: string;
  chat_id?: string;
  message_id?: string;
  sender_id?: string;
}

export type NotifyPermission = "default" | "granted" | "denied" | "unsupported";

export interface NotifyContext {
  myUserId: string;
  /** false when the tab is hidden or the window is not focused. */
  visible: boolean;
  /** The chat the user is currently reading, if any. */
  activeChatId: string | null;
  permission: NotifyPermission;
  enabled: boolean;
}

/**
 * Only a real message from somebody else deserves a desktop notification.
 * `hello` / `resync` are transport frames, self-sent messages are already on
 * screen, and a visible thread with the user in it does not need interrupting.
 */
export function shouldNotify(signal: NotifySignal, ctx: NotifyContext): boolean {
  if (!signal || signal.type !== "chat.message") return false;
  if (!ctx.enabled || ctx.permission !== "granted") return false;
  const sender = signal.sender_id ?? "";
  if (!sender || !ctx.myUserId || sender === ctx.myUserId) return false;
  if (!signal.chat_id) return false;
  if (ctx.visible && ctx.activeChatId === signal.chat_id) return false;
  return true;
}

/** Short sender label for the notification title; empty when unknown. */
export function notificationTitle(senderLabel: string | null | undefined): string {
  const name = senderLabel?.trim();
  return name ? `新消息 · ${name}` : "新消息";
}

export function permissionLabel(permission: NotifyPermission): string {
  switch (permission) {
    case "granted":
      return "已允许";
    case "denied":
      return "已拒绝（需在浏览器站点设置中手动允许）";
    case "default":
      return "尚未询问";
    default:
      return "当前浏览器不支持桌面通知";
  }
}

export function notificationStatus(permission: NotifyPermission, enabled: boolean): string {
  if (permission === "unsupported") return "当前浏览器不支持桌面通知，仍可在顶栏看到未读角标。";
  if (permission === "denied") return "浏览器已拒绝通知权限，请在站点设置里手动允许。";
  if (!enabled) return "未开启：收到新消息时只更新顶栏的未读角标。";
  return "已开启：收到别人发来的新消息时会弹出系统通知。";
}

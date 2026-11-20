const CHANNEL = "taskgame-direct-read";
export const UNREAD_REFRESH_EVENT = "taskgame:unread-refresh";

interface DirectReadNotice {
  userId: string;
  chatId: string;
}

/** Ask every listener in this tab (top-bar badge included) to recompute unread counts. */
export function notifyUnreadRefresh(): void {
  window.dispatchEvent(new Event(UNREAD_REFRESH_EVENT));
}

export function subscribeUnreadRefresh(handler: () => void): () => void {
  window.addEventListener(UNREAD_REFRESH_EVENT, handler);
  return () => window.removeEventListener(UNREAD_REFRESH_EVENT, handler);
}

/** Tell this tab and this browser's other tabs that a direct chat was marked read. */
export function notifyDirectRead(userId: string, chatId: string): void {
  // BroadcastChannel only reaches *other* tabs, so the local listeners need a
  // separate event or the top-bar badge would stay stale in the tab that read.
  notifyUnreadRefresh();
  if (!userId || typeof BroadcastChannel === "undefined") return;
  try {
    const channel = new BroadcastChannel(CHANNEL);
    const notice: DirectReadNotice = { userId, chatId };
    channel.postMessage(notice);
    channel.close();
  } catch {
    // The friends list also polls.
  }
}

/** Reload the friends list when another tab of this user marks a direct chat read. */
export function subscribeDirectRead(userId: string, onNotice: () => void): () => void {
  if (!userId || typeof BroadcastChannel === "undefined") return () => {};
  try {
    const channel = new BroadcastChannel(CHANNEL);
    channel.onmessage = (event: MessageEvent<DirectReadNotice>) => {
      if (event.data?.userId === userId) onNotice();
    };
    return () => channel.close();
  } catch {
    return () => {};
  }
}

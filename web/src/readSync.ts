const CHANNEL = "taskgame-direct-read";

interface DirectReadNotice {
  userId: string;
  chatId: string;
}

/** Tell this browser's other tabs that the signed-in user marked a direct chat read. */
export function notifyDirectRead(userId: string, chatId: string): void {
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

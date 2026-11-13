import { createIdempotencyKey, type TaskGameClient } from "taskgame-client";
import { formatError } from "./format";

export const CHAT_ERROR_EVENT = "taskgame-chat-error";

/** Open the pair's direct chat in a new tab. Reuses the existing thread. Does not create a friendship. */
export async function openDirectChat(client: TaskGameClient, userId: string): Promise<void> {
  const tab = window.open("", "_blank");
  if (tab) tab.opener = null;
  try {
    const chat = await client.createChat(
      { type: "direct", user_id: userId },
      { idempotencyKey: createIdempotencyKey("direct-chat") },
    );
    const url = new URL(window.location.href);
    url.hash = `/chat/${encodeURIComponent(chat.id)}`;
    if (tab && !tab.closed) {
      tab.location.replace(url.toString());
      return;
    }
    throw new Error("浏览器拦截了新标签。请允许本站弹出窗口后再点一次。");
  } catch (err) {
    if (tab && !tab.closed) tab.close();
    throw err;
  }
}

export function reportChatError(error: unknown): void {
  const message = formatError(error);
  if (!message) return;
  window.dispatchEvent(new CustomEvent(CHAT_ERROR_EVENT, { detail: message }));
}

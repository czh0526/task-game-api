/**
 * One global unread counter for the top bar, independent of which page is
 * open. It re-reads the friends list whenever the SSE channel signals a change
 * (and on a slow fallback poll), so the badge is not tied to the friends screen.
 *
 * Scope note: the server exposes unread_count only on friends, so this counter
 * covers 1:1 chats with friends. Desktop notifications are signal-driven and
 * therefore also cover direct chats with people who are not friends.
 */
import { useCallback, useEffect, useState } from "react";
import type { TaskGameClient } from "taskgame-client";
import { subscribeSignal } from "./eventStream";
import { sumUnread } from "./notificationView";
import { subscribeDirectRead, subscribeUnreadRefresh } from "./readSync";

/** Matches the screens' fallback poll; SSE signals are the normal trigger. */
const UNREAD_FALLBACK_POLL_MS = 45_000;

export function useUnreadTotal(
  client: TaskGameClient | null,
  userId: string,
  signedIn: boolean,
): number {
  const [total, setTotal] = useState(0);

  const refresh = useCallback(
    async (signal?: AbortSignal) => {
      if (!client || !signedIn) {
        setTotal(0);
        return;
      }
      try {
        const list = await client.listFriends({ signal });
        if (!signal?.aborted) setTotal(sumUnread(list));
      } catch {
        // Keep the last known number; a failed read should not flash zero.
      }
    },
    [client, signedIn],
  );

  useEffect(() => {
    if (!client || !signedIn) {
      setTotal(0);
      return;
    }
    const ac = new AbortController();
    const run = () => {
      void refresh(ac.signal);
    };
    run();
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") run();
    }, UNREAD_FALLBACK_POLL_MS);
    const onShow = () => {
      if (document.visibilityState === "visible") run();
    };
    document.addEventListener("visibilitychange", onShow);
    window.addEventListener("focus", onShow);
    const stopSignal = subscribeSignal(run);
    const stopRead = subscribeDirectRead(userId, run);
    const stopLocalRead = subscribeUnreadRefresh(run);
    return () => {
      ac.abort();
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", onShow);
      window.removeEventListener("focus", onShow);
      stopSignal();
      stopRead();
      stopLocalRead();
    };
  }, [client, signedIn, userId, refresh]);

  return total;
}

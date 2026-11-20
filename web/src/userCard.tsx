import { useEffect, useState } from "react";
import { createIdempotencyKey, type FriendList, type UserScores } from "taskgame-client";
import { Avatar } from "./Avatar";
import { isSignedIn } from "./client";
import { useApp } from "./context";
import { formatError, isAbort } from "./format";
import { LoginGate } from "./LoginGate";
import { openDirectChat, reportChatError } from "./openChat";
import { personLabel } from "./person";
import { Button, ErrorNote } from "./ui";

const OPEN_EVENT = "taskgame-user-card";

/** Open the profile card for a user. Safe to call from any component. */
export function openUserCard(userId: string): void {
  if (!userId) return;
  window.dispatchEvent(new CustomEvent(OPEN_EVENT, { detail: userId }));
}

/**
 * Single global host for the profile card. Mounted once in App; any PersonName
 * click opens it. Shows public scores and a friend action that greys out once
 * the two accounts are already friends.
 */
export function UserCardHost() {
  const { client, settings } = useApp();
  const signedIn = isSignedIn(settings);
  const [userId, setUserId] = useState<string | null>(null);
  const [scores, setScores] = useState<UserScores | null>(null);
  const [isFriend, setIsFriend] = useState(false);
  const [hasPending, setHasPending] = useState(false);
  const [loading, setLoading] = useState(false);
  const [acting, setActing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loginOpen, setLoginOpen] = useState(false);

  useEffect(() => {
    const onOpen = (event: Event) => {
      setUserId((event as CustomEvent<string>).detail || null);
    };
    window.addEventListener(OPEN_EVENT, onOpen);
    return () => window.removeEventListener(OPEN_EVENT, onOpen);
  }, []);

  useEffect(() => {
    if (!userId) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setUserId(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [userId]);

  useEffect(() => {
    if (!userId || !client) return;
    const ac = new AbortController();
    setScores(null);
    setIsFriend(false);
    setHasPending(false);
    setError(null);
    setLoading(true);

    // Public profile is readable without an account. Keep its result separate
    // from friend state so an anonymous visitor never sees a token error.
    void client
      .getUserScores(userId, { signal: ac.signal })
      .then((profile) => {
        if (!ac.signal.aborted) setScores(profile);
      })
      .catch((err: unknown) => {
        if (ac.signal.aborted || isAbort(err)) return;
        setError(formatError(err));
      })
      .finally(() => {
        if (!ac.signal.aborted) setLoading(false);
      });

    // Friend state needs a session. Only ask when signed in; a failure here is
    // ignored so the public card still renders normally.
    if (signedIn) {
      void Promise.all([
        client.listFriends({ signal: ac.signal }),
        client.listFriendRequests({ direction: "outgoing" }, { signal: ac.signal }),
      ])
        .then(([friends, outgoing]) => {
          if (ac.signal.aborted) return;
          setIsFriend(friendIds(friends).has(userId));
          setHasPending(
            outgoing.items.some((request) => request.to_user_id === userId && request.status === "pending"),
          );
        })
        .catch(() => {
          // Non-fatal: the public profile above is what matters here.
        });
    }

    return () => ac.abort();
  }, [userId, client, signedIn]);

  if (!userId) return null;

  const self = userId === settings.userId;
  const addDisabled = self || acting || loading || (signedIn && (isFriend || hasPending));
  const addLabel = !signedIn
    ? "登录后加为好友"
    : self
      ? "这是你自己"
      : isFriend
        ? "已是好友"
        : hasPending
          ? "已发申请"
          : "加为好友";

  async function addFriend() {
    if (!client || !userId || addDisabled) return;
    if (!signedIn) {
      setLoginOpen(true);
      return;
    }
    setActing(true);
    setError(null);
    try {
      const result = await client.createFriendRequest(
        { user_id: userId },
        { idempotencyKey: createIdempotencyKey("friend") },
      );
      if (result.status === "accepted") setIsFriend(true);
      else setHasPending(true);
    } catch (err) {
      setError(formatError(err));
    } finally {
      setActing(false);
    }
  }

  async function sendMessage() {
    if (!client || !userId || self) return;
    if (!signedIn) {
      setLoginOpen(true);
      return;
    }
    setActing(true);
    setError(null);
    try {
      await openDirectChat(client, userId);
    } catch (err) {
      reportChatError(err);
    } finally {
      setActing(false);
    }
  }

  const label = personLabel(scores?.nickname, userId);

  return (
    <>
      <div
        className="usercard-backdrop"
        onMouseDown={(event) => {
          if (event.target === event.currentTarget) setUserId(null);
        }}
      >
        <div className="usercard" role="dialog" aria-modal="true" aria-label={`${label} 的资料`}>
          <button type="button" className="usercard-close" aria-label="关闭" onClick={() => setUserId(null)}>
            ×
          </button>
          <div className="usercard-head">
            <Avatar userId={userId} name={scores?.nickname} className="usercard-avatar" />
            <div>
              <h2 className="usercard-name">{label}</h2>
              <p className="usercard-id mono" title={userId}>
                {userId}
              </p>
            </div>
          </div>

          {loading ? <p className="hint">正在加载资料…</p> : null}
          <ErrorNote message={error} />

          {scores ? (
            <div className="usercard-stats">
              <div>
                <span>诚信</span>
                <strong>{scores.integrity}</strong>
              </div>
              <div>
                <span>能力</span>
                <strong>{scores.ability}</strong>
              </div>
            </div>
          ) : null}

          <div className="usercard-actions">
            <Button type="button" kind="primary" disabled={addDisabled} onClick={() => void addFriend()}>
              {addLabel}
            </Button>
            <Button type="button" kind="secondary" disabled={self || acting} onClick={() => void sendMessage()}>
              {signedIn ? "发消息" : "登录后发消息"}
            </Button>
          </div>
          <p className="usercard-tip">
            {signedIn
              ? "好友关系需对方接受后生效。已经是好友时，「加为好友」会变灰。"
              : "查看资料无需登录；加好友或发消息需要先登录。"}
          </p>
        </div>
      </div>
      {loginOpen ? <LoginGate onClose={() => setLoginOpen(false)} /> : null}
    </>
  );
}

function friendIds(list: FriendList): Set<string> {
  const ids = new Set<string>();
  for (const friend of list.ungrouped) ids.add(friend.user_id);
  for (const group of list.groups) {
    for (const friend of group.friends) ids.add(friend.user_id);
  }
  return ids;
}

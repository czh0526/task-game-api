import { useEffect, useState } from "react";
import type { FriendList } from "taskgame-client";
import { useApp } from "./context";
import { formatError, isAbort } from "./format";
import { PersonName } from "./names";

export function appendUserId(current: string, id: string): string {
  const parts = current
    .split(/[\s,]+/)
    .map((item) => item.trim())
    .filter(Boolean);
  if (parts.includes(id)) return current;
  return parts.length === 0 ? id : `${parts.join(", ")}, ${id}`;
}

/** Grouped friend list used when a publisher fills user ids. */
export function FriendPicker({ onPick, disabled }: { onPick: (userId: string) => void; disabled?: boolean }) {
  const { client } = useApp();
  const [list, setList] = useState<FriendList | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!client) return;
    const ac = new AbortController();
    client
      .listFriends({ signal: ac.signal })
      .then((page) => {
        if (!ac.signal.aborted) setList(page);
      })
      .catch((err: unknown) => {
        if (isAbort(err)) return;
        setError(formatError(err));
      });
    return () => ac.abort();
  }, [client]);

  if (!client) return null;
  const rows = flatten(list);

  return (
    <details className="friend-pick">
      <summary>从好友列表选择</summary>
      {error ? <p className="hint">{error}</p> : null}
      {list && rows.length === 0 ? <p className="hint">还没有好友。先到「好友」里互加。</p> : null}
      {rows.map((row) => (
        <div key={row.group} className="pick-block">
          <p className="hint">{row.group}</p>
          <div className="pick-list">
            {row.friends.map((friend) => (
              <button key={friend.user_id} type="button" className="btn ghost" disabled={disabled} onClick={() => onPick(friend.user_id)}>
                <PersonName nickname={friend.nickname} userId={friend.user_id} chat={false} />
              </button>
            ))}
          </div>
        </div>
      ))}
    </details>
  );
}

function flatten(list: FriendList | null): Array<{ group: string; friends: FriendList["ungrouped"] }> {
  if (!list) return [];
  const rows: Array<{ group: string; friends: FriendList["ungrouped"] }> = [];
  for (const group of list.groups) {
    if (group.friends.length > 0) rows.push({ group: group.name, friends: group.friends });
  }
  if (list.ungrouped.length > 0) rows.push({ group: "未分组", friends: list.ungrouped });
  return rows;
}

import { useEffect, useState } from "react";
import type { FriendList } from "taskgame-client";
import { useApp } from "./context";
import { formatError, isAbort } from "./format";
import { PersonName, useNicknames } from "./names";
import { nicknameOnly } from "./person";
import { inviteUserIds } from "./stageLoop";

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

/**
 * Invite editor that shows selected users by nickname instead of raw ids.
 * The value stays the same comma/space-separated id string used by existing
 * submit logic, so callers only swap the input UI and keep their payload code.
 */
export function InvitePicker({
  value,
  onChange,
  disabled,
  label = "邀请对象",
  placeholder = "输入用户 id，或从下方好友列表点选",
}: {
  value: string;
  onChange: (next: string) => void;
  disabled?: boolean;
  label?: string;
  placeholder?: string;
}) {
  const ids = inviteUserIds(value);
  const names = useNicknames(ids);
  const [draft, setDraft] = useState("");

  function addIds(incoming: string[]) {
    const merged = inviteUserIds([...ids, ...incoming].join(", "));
    onChange(merged.join(", "));
    setDraft("");
  }

  function remove(id: string) {
    onChange(
      ids
        .filter((item) => item !== id)
        .join(", "),
    );
  }

  return (
    <div className="invite-picker">
      <span className="field-label">{label}</span>
      <div className="invite-chips">
        {ids.length === 0 ? <span className="hint">还没有选择</span> : null}
        {ids.map((id) => (
          <span className="invite-chip" key={id} title={nicknameOnly(names[id])}>
            <PersonName nickname={names[id]} userId={id} chat={false} label={nicknameOnly(names[id])} />
            <button
              type="button"
              className="invite-chip-remove"
              aria-label={`移除 ${nicknameOnly(names[id])}`}
              disabled={disabled}
              onClick={() => remove(id)}
            >
              ×
            </button>
          </span>
        ))}
      </div>
      <details className="friend-pick invite-manual">
        <summary>手动添加用户</summary>
        <div className="invite-manual-row">
          <input
            value={draft}
            spellCheck={false}
            placeholder={placeholder}
            disabled={disabled}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                const parts = inviteUserIds(draft);
                if (parts.length > 0) addIds(parts);
              }
            }}
          />
          <button
            type="button"
            className="btn secondary compact"
            disabled={disabled || inviteUserIds(draft).length === 0}
            onClick={() => addIds(inviteUserIds(draft))}
          >
            添加
          </button>
        </div>
      </details>
      <FriendPicker disabled={disabled} onPick={(userId) => addIds([userId])} />
    </div>
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

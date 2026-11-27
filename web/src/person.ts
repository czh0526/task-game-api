/** Last 6 characters of the user id. Shorter ids are kept whole. */
export function idSuffix(userId: string): string {
  if (!userId) return "";
  return userId.length <= 6 ? userId : userId.slice(-6);
}

/**
 * One line, nickname only — for example `阿宁同学`. The user id suffix used to
 * be appended as `昵称(...后6位)` but is no longer shown when a nickname exists.
 * A user without a nickname still falls back to `...后6位`, so they remain
 * distinguishable; never blank and never a second line.
 */
export function personLabel(nickname: string | null | undefined, userId: string): string {
  const name = nickname?.trim();
  if (name) return name;
  const suffix = idSuffix(userId);
  return suffix ? `...${suffix}` : "未命名";
}

/**
 * Sender name inside a chat bubble: the nickname on its own (no id suffix),
 * falling back to the short id when no nickname is known yet. Same display
 * rule as everywhere else in the UI.
 */
export function senderDisplayName(nickname: string | null | undefined, userId: string): string {
  return personLabel(nickname, userId);
}

/**
 * Nickname only, never an id suffix. Used where the page must show a person by
 * name and must not leak an id (e.g. the task detail screen). A user without a
 * nickname gets a neutral placeholder; the id stays available via the tooltip
 * on the wrapping element.
 */
export function nicknameOnly(nickname: string | null | undefined, placeholder = "用户"): string {
  const name = nickname?.trim();
  return name || placeholder;
}

/** Client-side check matching the server: trimmed, 1–32 characters, no control characters. */
export function nicknameError(raw: string): string | null {
  const name = raw.trim();
  if (!name) return "请填写昵称。去掉空白后不能为空。";
  const chars = [...name];
  if (chars.length > 32) return "昵称最长 32 个字符。";
  if (chars.some((ch) => isControl(ch))) return "昵称不能包含控制字符。";
  return null;
}

export function nicknameServerError(message: string): string | null {
  if (!message.toLowerCase().includes("nickname")) return null;
  return "昵称需要 1 到 32 个字符，不能只是空白或控制字符。";
}

function isControl(ch: string): boolean {
  const code = ch.codePointAt(0) ?? 0;
  return code <= 31 || code === 127;
}

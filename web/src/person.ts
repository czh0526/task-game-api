/** Last 6 characters of the user id. Shorter ids are kept whole. */
export function idSuffix(userId: string): string {
  if (!userId) return "";
  return userId.length <= 6 ? userId : userId.slice(-6);
}

/**
 * One line: `昵称(后6位)`, for example `阿宁同学(a1b2c3)`.
 * A missing nickname is just those 6 characters, never a blank and never a second line.
 */
export function personLabel(nickname: string | null | undefined, userId: string): string {
  const name = nickname?.trim();
  const suffix = idSuffix(userId);
  if (name && suffix) return `${name}(${suffix})`;
  if (name) return name;
  if (suffix) return suffix;
  return "未命名";
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

/** Visible label when a user has not set a nickname. Long ids stay recognizable and short. */
export function shortUserId(userId: string): string {
  if (!userId) return "未命名";
  if (userId.length <= 18) return userId;
  if (userId.startsWith("usr_") && userId.length > 12) {
    const rest = userId.slice(4);
    return `usr_${rest.slice(0, 4)}…${rest.slice(-4)}`;
  }
  return `${userId.slice(0, 6)}…${userId.slice(-4)}`;
}

/** Prefer a trimmed nickname. Otherwise show a short form of the id, never a blank. */
export function personLabel(nickname: string | null | undefined, userId: string): string {
  const name = nickname?.trim();
  if (name) return name;
  return shortUserId(userId);
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

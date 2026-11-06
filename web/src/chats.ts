export const CHATS_KEY = "taskgame.web.chats";

export interface LocalMessage {
  id: string;
  chat_id: string;
  sender_id: string;
  type: "text" | "attachment_ref";
  text?: string;
  created_at: string;
}

export interface LocalChat {
  id: string;
  member_ids: string[];
  messages: LocalMessage[];
}

export function loadChats(): LocalChat[] {
  try {
    const raw = localStorage.getItem(CHATS_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(isChat);
  } catch {
    return [];
  }
}

export function saveChats(chats: LocalChat[]): void {
  localStorage.setItem(CHATS_KEY, JSON.stringify(chats));
}

export function upsertChat(chat: LocalChat): LocalChat[] {
  const chats = loadChats().filter((item) => item.id !== chat.id);
  chats.unshift(chat);
  saveChats(chats);
  return chats;
}

export function appendMessage(chatId: string, message: LocalMessage): LocalChat[] {
  const chats = loadChats();
  const index = chats.findIndex((item) => item.id === chatId);
  if (index < 0) {
    return upsertChat({ id: chatId, member_ids: [], messages: [message] });
  }
  const current = chats[index];
  if (!current) return chats;
  if (current.messages.some((item) => item.id === message.id)) return chats;
  const next = { ...current, messages: [...current.messages, message] };
  chats.splice(index, 1);
  chats.unshift(next);
  saveChats(chats);
  return chats;
}

function isChat(value: unknown): value is LocalChat {
  if (!value || typeof value !== "object") return false;
  const record = value as Partial<LocalChat>;
  return typeof record.id === "string" && Array.isArray(record.member_ids) && Array.isArray(record.messages);
}

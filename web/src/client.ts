import { TaskGameClient, apiKey, bearer } from "taskgame-client";
import type { Settings } from "./settings";

export function createClient(settings: Settings): TaskGameClient {
  const baseUrl = settings.baseUrl.trim();
  if (settings.authMode === "bearer") {
    const token = jwtFromPaste(settings.bearerToken);
    if (!token) throw new Error("请填写 Bearer token");
    return new TaskGameClient({ baseUrl, auth: bearer(token) });
  }
  const key = settings.apiKey.trim();
  if (!key) throw new Error("请填写 API Key");
  return new TaskGameClient({ baseUrl, auth: apiKey(key) });
}

/** `make token` echoes the go command, then prints the JWT. Keep the JWT line. */
export function jwtFromPaste(raw: string): string {
  const parts = raw.split(/\s+/).map((part) => part.trim()).filter(Boolean);
  return [...parts].reverse().find((part) => part.split(".").length === 3) ?? "";
}

export function clientErrorText(error: unknown): string {
  if (!(error instanceof Error)) return "无法创建客户端";
  const message = error.message;
  if (message.startsWith("baseUrl")) {
    return "Base URL 需要是 http(s) 绝对地址，路径留空或以 /v1 结尾。";
  }
  return message;
}

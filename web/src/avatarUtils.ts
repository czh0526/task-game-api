import type { TaskGameClient } from "taskgame-client";

/** Every uploaded avatar is normalized to this square size. */
export const AVATAR_SIZE = 720;

export interface CropRect {
  x: number;
  y: number;
  size: number;
}

/** Middle square of a source image, used by both the browser preview and the server. */
export function cropSquare(width: number, height: number): CropRect {
  const w = Math.max(0, Math.floor(width));
  const h = Math.max(0, Math.floor(height));
  const size = Math.min(w, h);
  if (size <= 0) return { x: 0, y: 0, size: 0 };
  return {
    x: Math.floor((w - size) / 2),
    y: Math.floor((h - size) / 2),
    size,
  };
}

/**
 * Read an image File and produce a 720x720 JPEG data URL with a centre crop.
 * The preview shown in Settings is therefore exactly what the server stores.
 */
export async function fileToAvatarDataUrl(file: File): Promise<string> {
  if (!file.type.startsWith("image/")) {
    throw new Error("请选择 JPG、PNG 或 GIF 图片");
  }
  const objectUrl = URL.createObjectURL(file);
  const img = new Image();
  try {
    img.src = objectUrl;
    await img.decode();
  } catch {
    throw new Error("这张图片无法解码，请换一张");
  } finally {
    URL.revokeObjectURL(objectUrl);
  }
  const width = img.naturalWidth;
  const height = img.naturalHeight;
  if (!width || !height) {
    throw new Error("图片尺寸无效");
  }
  const rect = cropSquare(width, height);
  const canvas = document.createElement("canvas");
  canvas.width = AVATAR_SIZE;
  canvas.height = AVATAR_SIZE;
  const ctx = canvas.getContext("2d");
  if (!ctx) {
    throw new Error("当前浏览器不支持图片裁剪");
  }
  // JPEG has no alpha: composite transparent PNG/GIF pixels over white.
  ctx.fillStyle = "#ffffff";
  ctx.fillRect(0, 0, AVATAR_SIZE, AVATAR_SIZE);
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(img, rect.x, rect.y, rect.size, rect.size, 0, 0, AVATAR_SIZE, AVATAR_SIZE);
  return canvas.toDataURL("image/jpeg", 0.88);
}

const AVATAR_EVENT = "taskgame-avatar";
const AVATAR_CHANNEL = "taskgame-avatar";
const versions = new Map<string, number>();
let channel: BroadcastChannel | null = null;

function avatarChannel(): BroadcastChannel | null {
  if (typeof window === "undefined" || typeof BroadcastChannel === "undefined") return null;
  if (!channel) channel = new BroadcastChannel(AVATAR_CHANNEL);
  return channel;
}

/** Tell every open tab (and this one) to reload a user's avatar image. */
export function notifyAvatar(userId: string, version = Date.now()): void {
  if (!userId) return;
  versions.set(userId, version);
  if (typeof window !== "undefined") {
    window.dispatchEvent(new CustomEvent(AVATAR_EVENT, { detail: { userId, version } }));
  }
  avatarChannel()?.postMessage({ userId, version });
}

export function getAvatarVersion(userId: string): number {
  return versions.get(userId) ?? 0;
}

/** Subscribe to avatar updates. Returns an unsubscribe function. */
export function subscribeAvatar(listener: (userId: string, version: number) => void): () => void {
  if (typeof window === "undefined") return () => {};
  const onLocal = (event: Event) => {
    const detail = (event as CustomEvent<{ userId: string; version: number }>).detail;
    if (detail?.userId && typeof detail.version === "number") {
      listener(detail.userId, detail.version);
    }
  };
  const ch = avatarChannel();
  const onMessage = (event: MessageEvent) => {
    const data = event.data as { userId?: string; version?: number } | null;
    if (data?.userId && typeof data.version === "number") {
      listener(data.userId, data.version);
    }
  };
  window.addEventListener(AVATAR_EVENT, onLocal);
  ch?.addEventListener("message", onMessage);
  return () => {
    window.removeEventListener(AVATAR_EVENT, onLocal);
    ch?.removeEventListener("message", onMessage);
  };
}

/** Public avatar URL for an `<img src>`; `version` forces a reload after upload. */
export function avatarUrl(client: TaskGameClient, userId: string, version = 0): string {
  const base = client.getUserAvatar(userId);
  return version > 0 ? `${base}?v=${version}` : base;
}

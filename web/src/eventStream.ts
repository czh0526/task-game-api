/**
 * Browser-side SSE transport for near-real-time signals.
 *
 * This is an internal UI channel, not part of the public JSON protocol:
 * EventSource cannot send our Authorization / X-Api-Key headers, so the
 * stream is read with fetch() + ReadableStream. Frames carry ids only; the
 * screens re-read their data over the normal REST endpoints.
 */
import { isSignedIn, jwtFromPaste } from "./client";
import type { Settings } from "./settings";
import { parseFrames, streamUrl } from "./sse";

export const SIGNAL_EVENT = "taskgame:signal";

export interface StreamSignal {
  type: "chat.message" | "hello" | "resync";
  chat_id?: string;
  message_id?: string;
  sender_id?: string;
}

const BASE_BACKOFF_MS = 1_000;
const MAX_BACKOFF_MS = 30_000;
const MAX_BACKOFF_STEP = 6;

export function dispatchSignal(signal: StreamSignal): void {
  window.dispatchEvent(new CustomEvent<StreamSignal>(SIGNAL_EVENT, { detail: signal }));
}

/** Subscribe to stream signals; returns an unsubscribe function. */
export function subscribeSignal(handler: (signal: StreamSignal) => void): () => void {
  const onEvent = (event: Event) => {
    const signal = (event as CustomEvent<StreamSignal>).detail;
    if (signal && typeof signal.type === "string") handler(signal);
  };
  window.addEventListener(SIGNAL_EVENT, onEvent);
  return () => window.removeEventListener(SIGNAL_EVENT, onEvent);
}

/**
 * Open one stream for the signed-in user and return a stop function. The
 * connection is re-established with exponential backoff; the remembered
 * Last-Event-ID makes the server answer with a resync frame, which asks the
 * screens to re-read everything published while the tab was disconnected.
 */
export function openEventStream(settings: Settings): () => void {
  if (!isSignedIn(settings)) return () => {};
  const url = streamUrl(settings.baseUrl);
  const headers: Record<string, string> = { Accept: "text/event-stream" };
  if (settings.authMode === "bearer") {
    headers.Authorization = `Bearer ${jwtFromPaste(settings.bearerToken)}`;
  } else {
    headers["X-Api-Key"] = settings.apiKey.trim();
  }

  const controller = new AbortController();
  let lastEventId = "";
  let stopped = false;
  let attempt = 0;
  let timer: number | undefined;

  const sleep = (ms: number) =>
    new Promise<void>((resolve) => {
      timer = window.setTimeout(resolve, ms);
    });

  const run = async () => {
    while (!stopped) {
      try {
        const res = await fetch(url, {
          method: "GET",
          headers: lastEventId ? { ...headers, "Last-Event-ID": lastEventId } : headers,
          signal: controller.signal,
          cache: "no-store",
        });
        if (!res.ok || !res.body) throw new Error(`stream returned ${res.status}`);
        attempt = 0;
        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          const { frames, rest } = parseFrames(buffer);
          buffer = rest;
          for (const frame of frames) {
            if (frame.id) lastEventId = frame.id;
            if (!frame.data) continue;
            const signal = JSON.parse(frame.data) as StreamSignal;
            if (signal && typeof signal.type === "string") dispatchSignal(signal);
          }
        }
      } catch {
        if (stopped || controller.signal.aborted) return;
      }
      if (stopped) return;
      attempt = Math.min(attempt + 1, MAX_BACKOFF_STEP);
      await sleep(Math.min(BASE_BACKOFF_MS * 2 ** (attempt - 1), MAX_BACKOFF_MS));
    }
  };
  void run();

  return () => {
    stopped = true;
    controller.abort();
    if (timer !== undefined) window.clearTimeout(timer);
  };
}

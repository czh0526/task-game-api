/** Pure SSE helpers. No imports, so they can be unit-tested directly. */

export interface SSEFrame {
  id: string;
  data: string;
}

/**
 * Resolve the stream endpoint. The SDK accepts a bare origin or an origin
 * already ending in /v1; both must map to the same URL.
 */
export function streamUrl(baseUrl: string): string {
  const base = baseUrl.trim().replace(/\/+$/, "");
  return base.endsWith("/v1") ? `${base}/events/stream` : `${base}/v1/events/stream`;
}

/**
 * Split a text chunk into complete frames and return the unterminated tail.
 * The server emits LF only; CRLF is tolerated. Comment-only frames
 * (heartbeats) have no data field and are skipped.
 */
export function parseFrames(buffer: string): { frames: SSEFrame[]; rest: string } {
  const text = buffer.replace(/\r\n/g, "\n");
  const parts = text.split("\n\n");
  const rest = parts.pop() ?? "";
  const frames: SSEFrame[] = [];
  for (const block of parts) {
    const frame = parseBlock(block);
    if (frame) frames.push(frame);
  }
  return { frames, rest };
}

function parseBlock(block: string): SSEFrame | null {
  let id = "";
  const data: string[] = [];
  for (const line of block.split("\n")) {
    if (line === "" || line.startsWith(":")) continue;
    const colon = line.indexOf(":");
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "id") id = value;
    else if (field === "data") data.push(value);
  }
  if (data.length === 0) return null;
  return { id, data: data.join("\n") };
}

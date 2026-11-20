import assert from "node:assert/strict";
import test from "node:test";
import { parseFrames, streamUrl } from "../src/sse.ts";

test("streamUrl handles a bare origin and a /v1 base", () => {
  assert.equal(streamUrl("http://127.0.0.1:8891"), "http://127.0.0.1:8891/v1/events/stream");
  assert.equal(streamUrl("http://127.0.0.1:8891/"), "http://127.0.0.1:8891/v1/events/stream");
  assert.equal(streamUrl("https://api.example.com/v1"), "https://api.example.com/v1/events/stream");
  assert.equal(streamUrl("https://api.example.com/v1/"), "https://api.example.com/v1/events/stream");
});

test("parseFrames reads a complete signal frame", () => {
  const { frames, rest } = parseFrames('id: 7\nevent: signal\ndata: {"type":"chat.message","chat_id":"cht_1"}\n\n');
  assert.equal(rest, "");
  assert.deepEqual(frames, [
    { id: "7", data: '{"type":"chat.message","chat_id":"cht_1"}' },
  ]);
});

test("parseFrames keeps an unterminated tail for the next chunk", () => {
  const first = parseFrames("id: 1\ndata: a\n\nid: 2\ndata: b");
  assert.deepEqual(first.frames, [{ id: "1", data: "a" }]);
  assert.equal(first.rest, "id: 2\ndata: b");

  const second = parseFrames(first.rest + "\n\n");
  assert.deepEqual(second.frames, [{ id: "2", data: "b" }]);
  assert.equal(second.rest, "");
});

test("parseFrames handles split chunks at arbitrary byte boundaries", () => {
  const wire = 'id: 9\ndata: {"type":"resync"}\n\n: ping\n\nid: 10\ndata: {"type":"hello"}\n\n';
  let buffer = "";
  const seen = [];
  for (const ch of wire) {
    buffer += ch;
    const { frames, rest } = parseFrames(buffer);
    buffer = rest;
    seen.push(...frames);
  }
  assert.deepEqual(seen, [
    { id: "9", data: '{"type":"resync"}' },
    { id: "10", data: '{"type":"hello"}' },
  ]);
  assert.equal(buffer, "");
});

test("parseFrames skips heartbeat comments and joins multiline data", () => {
  const { frames } = parseFrames(": ping\n\nid: 3\ndata: one\ndata: two\n\n");
  assert.deepEqual(frames, [{ id: "3", data: "one\ntwo" }]);
});

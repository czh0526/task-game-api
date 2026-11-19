import assert from "node:assert/strict";
import test from "node:test";
import { EMOJI_STICKERS, stickerLabel } from "../src/emojis.ts";

test("sticker palette has unique emoji with labels", () => {
  assert.ok(EMOJI_STICKERS.length >= 12);
  const chars = EMOJI_STICKERS.map((item) => item.char);
  assert.equal(new Set(chars).size, chars.length);
  for (const item of EMOJI_STICKERS) {
    assert.ok(item.char.length > 0, "emoji char is set");
    assert.ok(item.label.length > 0, "label is set");
  }
});

test("stickerLabel maps known emoji and falls back to raw text", () => {
  assert.equal(stickerLabel("😂"), "笑哭");
  assert.equal(stickerLabel("  😂  "), "笑哭");
  assert.equal(stickerLabel("👍"), "赞");
  assert.equal(stickerLabel("自定义表情"), "自定义表情");
  assert.equal(stickerLabel(""), "");
});

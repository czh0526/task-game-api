import assert from "node:assert/strict";
import test from "node:test";
import { asStickerChar, EMOJI_STICKERS, insertAtCaret, stickerLabel } from "../src/emojis.ts";

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

test("only a lone built-in emoji becomes a big sticker", () => {
  assert.equal(asStickerChar("😂"), "😂");
  assert.equal(asStickerChar("  🎉  "), "🎉");
  assert.equal(asStickerChar("好的"), null);
  assert.equal(asStickerChar("来啦 🎉"), null); // embedded emoji stays inline text
  assert.equal(asStickerChar("😂🎉"), null); // multiple emojis are inline text
  assert.equal(asStickerChar("🚀"), null); // not in the built-in palette
});

test("insertAtCaret inserts emoji at caret and replaces selection", () => {
  // caret offsets are UTF-16 code units, matching DOM selectionStart/setSelectionRange;
  // astral emojis occupy two units.
  assert.deepEqual(insertAtCaret("你好", "😀", 2, 2), { text: "你好😀", caret: 4 });
  assert.deepEqual(insertAtCaret("你好世界", "🎉", 0, 0), { text: "🎉你好世界", caret: 2 });
  assert.deepEqual(insertAtCaret("你好世界", "👍", 2, 4), { text: "你好👍", caret: 4 });
  // caret beyond length is clamped
  assert.deepEqual(insertAtCaret("hi", "😀", 99, 99), { text: "hi😀", caret: 4 });
});

import assert from "node:assert/strict";
import test from "node:test";
import { centerMessageScrollTop, messageTopInContent } from "../src/chatView.ts";

test("messageTopInContent converts a viewport position into content offset", () => {
  // panel top at viewport y=100, message top at y=260, panel already scrolled 40.
  assert.equal(
    messageTopInContent({ messageViewportTop: 260, panelViewportTop: 100, panelScrollTop: 40 }),
    200,
  );
  assert.equal(
    messageTopInContent({ messageViewportTop: 100, panelViewportTop: 100, panelScrollTop: 0 }),
    0,
  );
});

test("centerMessageScrollTop places the message top at the panel's vertical middle", () => {
  // Message sits 800px into the content; a 400px panel wants it at y=200.
  assert.equal(
    centerMessageScrollTop({ messageTopInContent: 800, panelHeight: 400, maxScrollTop: 5000 }),
    600,
  );
  assert.equal(
    centerMessageScrollTop({ messageTopInContent: 1234, panelHeight: 400, maxScrollTop: 5000 }),
    1034,
  );
});

test("centerMessageScrollTop clamps to the real scroll range", () => {
  // Near the top: cannot scroll above 0.
  assert.equal(
    centerMessageScrollTop({ messageTopInContent: 120, panelHeight: 400, maxScrollTop: 5000 }),
    0,
  );
  // Near the bottom: cannot scroll past the last message.
  assert.equal(
    centerMessageScrollTop({ messageTopInContent: 4800, panelHeight: 400, maxScrollTop: 4600 }),
    4600,
  );
});

test("centerMessageScrollTop does nothing when the panel has no scroll range", () => {
  assert.equal(
    centerMessageScrollTop({ messageTopInContent: 100, panelHeight: 600, maxScrollTop: 0 }),
    0,
  );
  assert.equal(
    centerMessageScrollTop({ messageTopInContent: 100, panelHeight: 600, maxScrollTop: -200 }),
    0,
  );
});

test("centerMessageScrollTop rounds to whole pixels and tolerates non-finite input", () => {
  assert.equal(
    centerMessageScrollTop({ messageTopInContent: 800.6, panelHeight: 400.4, maxScrollTop: 5000 }),
    600,
  );
  assert.equal(
    centerMessageScrollTop({ messageTopInContent: Number.NaN, panelHeight: 400, maxScrollTop: 5000 }),
    0,
  );
});

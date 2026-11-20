import assert from "node:assert/strict";
import test from "node:test";
import {
  badgeLabel,
  notificationStatus,
  notificationTitle,
  permissionLabel,
  shouldNotify,
  sumUnread,
} from "../src/notificationView.ts";

const CTX = {
  myUserId: "usr_b",
  visible: false,
  activeChatId: null,
  permission: "granted",
  enabled: true,
};

function signal(overrides = {}) {
  return {
    type: "chat.message",
    chat_id: "cht_1",
    message_id: "msg_1",
    sender_id: "usr_a",
    ...overrides,
  };
}

test("sumUnread adds grouped and ungrouped friends", () => {
  assert.equal(
    sumUnread({
      groups: [{ friends: [{ unread_count: 2 }, { unread_count: 0 }] }, { friends: [{ unread_count: 5 }] }],
      ungrouped: [{ unread_count: 1 }, { unread_count: 0 }],
    }),
    8,
  );
});

test("sumUnread tolerates missing lists and bad values", () => {
  assert.equal(sumUnread(null), 0);
  assert.equal(sumUnread(undefined), 0);
  assert.equal(sumUnread({}), 0);
  assert.equal(
    sumUnread({
      groups: [{ friends: [{ unread_count: -3 }, { unread_count: null }, {}, { unread_count: 2.7 }] }, {}],
      ungrouped: [{ unread_count: Number.NaN }, null],
    }),
    2,
  );
});

test("badgeLabel hides zero and caps at 99+", () => {
  assert.equal(badgeLabel(0), null);
  assert.equal(badgeLabel(-4), null);
  assert.equal(badgeLabel(Number.NaN), null);
  assert.equal(badgeLabel(1), "1");
  assert.equal(badgeLabel(12.9), "12");
  assert.equal(badgeLabel(99), "99");
  assert.equal(badgeLabel(100), "99+");
  assert.equal(badgeLabel(2000), "99+");
});

test("shouldNotify accepts an incoming message while the tab is hidden", () => {
  assert.equal(shouldNotify(signal(), CTX), true);
  assert.equal(shouldNotify(signal(), { ...CTX, visible: true }), true);
});

test("shouldNotify suppresses transport frames and self-sent messages", () => {
  assert.equal(shouldNotify(signal({ type: "hello" }), CTX), false);
  assert.equal(shouldNotify(signal({ type: "resync" }), CTX), false);
  assert.equal(shouldNotify(signal({ sender_id: "usr_b" }), CTX), false);
  assert.equal(shouldNotify(signal({ sender_id: "" }), CTX), false);
  assert.equal(shouldNotify(signal(), { ...CTX, myUserId: "" }), false);
  assert.equal(shouldNotify(signal({ chat_id: "" }), CTX), false);
  assert.equal(shouldNotify({ type: "chat.message" }, CTX), false);
});

test("shouldNotify respects permission, the preference and the open thread", () => {
  assert.equal(shouldNotify(signal(), { ...CTX, enabled: false }), false);
  for (const permission of ["default", "denied", "unsupported"]) {
    assert.equal(shouldNotify(signal(), { ...CTX, permission }), false);
  }
  assert.equal(shouldNotify(signal(), { ...CTX, visible: true, activeChatId: "cht_1" }), false);
  assert.equal(shouldNotify(signal(), { ...CTX, visible: true, activeChatId: "cht_other" }), true);
  assert.equal(shouldNotify(signal(), { ...CTX, visible: false, activeChatId: "cht_1" }), true);
});

test("notificationTitle and status text stay readable", () => {
  assert.equal(notificationTitle("阿宁"), "新消息 · 阿宁");
  assert.equal(notificationTitle("  阿宁  "), "新消息 · 阿宁");
  assert.equal(notificationTitle(""), "新消息");
  assert.equal(notificationTitle(null), "新消息");
  assert.match(permissionLabel("denied"), /拒绝/);
  assert.match(permissionLabel("unsupported"), /不支持/);
  assert.match(notificationStatus("denied", true), /拒绝/);
  assert.match(notificationStatus("granted", false), /未开启/);
  assert.match(notificationStatus("granted", true), /已开启/);
  assert.match(notificationStatus("unsupported", false), /不支持/);
});

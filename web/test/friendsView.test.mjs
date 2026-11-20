import assert from "node:assert/strict";
import test from "node:test";
import { avatarColor, avatarInitial, friendSections, placementChanged, requestPerson } from "../src/friendsView.ts";

const friend = (userId, nickname, unread_count = 0) => ({
  user_id: userId,
  nickname,
  integrity: 90,
  ability: 80,
  since: "2026-01-01T00:00:00Z",
  unread_count,
});

test("avatar helpers are stable", () => {
  assert.equal(avatarInitial("小明"), "小");
  assert.equal(avatarInitial("  alice "), "A");
  assert.equal(avatarInitial(""), "?");
  assert.equal(avatarColor("usr_1"), avatarColor("usr_1"));
  assert.match(avatarColor("usr_1"), /^#[0-9a-f]{6}$/);
});

test("contact sections keep group order and filter by name or id", () => {
  const groups = [
    {
      id: "grp_1",
      name: "同事",
      sort_order: 1,
      friends: [friend("usr_a", "Alice", 2), friend("usr_b", "Bob")],
    },
    { id: "grp_2", name: "家人", sort_order: 2, friends: [] },
  ];
  const ungrouped = [friend("usr_c", "小明")];

  assert.deepEqual(
    friendSections(groups, ungrouped, "").map((section) => [section.title, section.friends.length]),
    [
      ["未分组", 1],
      ["同事", 2],
    ],
  );
  assert.deepEqual(
    friendSections(groups, ungrouped, "ali").map((section) => section.friends[0].user_id),
    ["usr_a"],
  );
  assert.deepEqual(
    friendSections(groups, ungrouped, "USR_C").map((section) => section.title),
    ["未分组"],
  );
  assert.deepEqual(friendSections(groups, ungrouped, "nobody"), []);
});

test("drag mode keeps empty groups and the ungrouped bucket as drop targets", () => {
  const groups = [
    { id: "grp_1", name: "同事", sort_order: 1, friends: [friend("usr_a", "Alice")] },
    { id: "grp_2", name: "家人", sort_order: 2, friends: [] },
  ];
  const sections = friendSections(groups, [], "", true);
  assert.deepEqual(
    sections.map((section) => [section.key, section.title, section.friends.length]),
    [
      ["", "未分组", 0],
      ["grp_1", "同事", 1],
      ["grp_2", "家人", 0],
    ],
  );
});

test("placementChanged ignores no-op drops but catches real moves", () => {
  assert.equal(placementChanged("grp_1", "grp_1"), false);
  assert.equal(placementChanged("", ""), false);
  assert.equal(placementChanged("grp_1", ""), true);
  assert.equal(placementChanged("", "grp_2"), true);
});

test("request person is the other side of the request", () => {
  const request = {
    id: "req_1",
    from_user_id: "usr_me",
    to_user_id: "usr_you",
    from_nickname: "我",
    to_nickname: "对方",
    status: "pending",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
  assert.deepEqual(requestPerson(request, "usr_me"), { userId: "usr_you", nickname: "对方" });
  assert.deepEqual(requestPerson(request, "usr_you"), { userId: "usr_me", nickname: "我" });
});

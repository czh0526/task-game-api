import assert from "node:assert/strict";
import test from "node:test";
import { stageTone, viewerHint, viewerLabel, viewerRole } from "../src/taskDetailView.ts";

const base = {
  status: "open",
  kind: "exclusive",
  publisher_id: "usr_a",
  executor_ids: [],
};

test("viewer role follows identity before and after acceptance", () => {
  assert.equal(viewerRole(base, undefined), "guest");
  assert.equal(viewerRole(base, "usr_a"), "publisher");
  assert.equal(viewerRole(base, "usr_b"), "observer");
  assert.equal(viewerRole({ ...base, executor_ids: ["usr_b"] }, "usr_b"), "executor");
  const collab = {
    ...base,
    kind: "collaborative",
    collaborative: { roles: [{ assignee_user_id: "usr_c", status: "filled" }] },
  };
  assert.equal(viewerRole(collab, "usr_c"), "executor");
  assert.equal(viewerRole(collab, "usr_d"), "observer");
});

test("hint stops promising invites when the only slot is taken", () => {
  assert.match(viewerHint(base, "usr_a"), /你可以邀请/);
  const full = { ...base, executor_ids: ["usr_b"] };
  assert.match(viewerHint(full, "usr_a"), /名额已满/);
  assert.doesNotMatch(viewerHint(full, "usr_a"), /邀请合适的人/);
});

test("hint tells the publisher and the executor whose turn it is", () => {
  assert.match(viewerHint({ ...base, status: "draft" }, "usr_a"), /注资/);
  assert.match(viewerHint({ ...base, status: "draft" }, "usr_b"), /发布者/);
  assert.match(viewerHint({ ...base, status: "open" }, "usr_a"), /等待执行者接单/);
  assert.match(viewerHint({ ...base, status: "open" }, "usr_b"), /接单/);
  assert.match(viewerHint({ ...base, status: "active" }, "usr_a"), /发起结算/);
  assert.match(viewerHint({ ...base, status: "in_review" }, "usr_a"), /发起结算/);
  assert.match(viewerHint({ ...base, status: "in_review" }, "usr_b"), /等待发布者发起结算/);
  assert.match(viewerHint({ ...base, status: "disputed" }, "usr_a"), /协商方案/);
  assert.match(viewerHint({ ...base, status: "settled" }, "usr_a"), /已结算/);
  assert.match(viewerHint({ ...base, status: "cancelled" }, "usr_a"), /已取消/);
});

test("collaborative unified seating changes the open hint", () => {
  const collab = {
    ...base,
    kind: "collaborative",
    collaborative: { settlement_mode: "unified" },
  };
  assert.match(viewerHint(collab, "usr_a"), /开始任务/);
  assert.doesNotMatch(viewerHint({ ...collab, collaborative: { settlement_mode: "per_seat" } }, "usr_a"), /开始任务/);
});

test("status tone and viewer labels are stable", () => {
  assert.equal(stageTone("in_review"), "warn");
  assert.equal(stageTone("disputed"), "bad");
  assert.equal(stageTone("draft"), "neutral");
  assert.equal(viewerLabel("publisher"), "发布者");
  assert.equal(viewerLabel("guest"), "未登录");
});

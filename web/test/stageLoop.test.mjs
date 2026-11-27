import assert from "node:assert/strict";
import test from "node:test";
import {
  canInviteSeat,
  canInviteTask,
  canRemoveSeat,
  cancelReason,
  confirmTaskBody,
  inReviewMilestoneSubmissionId,
  inviteUserIds,
  milestoneSubmitBlock,
  submitMilestoneField,
} from "../src/stageLoop.ts";

const stages = [
  { id: "m1", amount: 200, condition: "draft", status: "released" },
  { id: "m2", amount: 300, condition: "final", status: "submitted" },
];

const submissions = [
  { id: "sub_old", status: "current", milestone_id: "m1", executor_id: "usr_executor" },
  { id: "sub_new", status: "current", milestone_id: "m2", executor_id: "usr_executor" },
];

test("submit requires a pending stage and blocks while one is in review", () => {
  const task = {
    kind: "exclusive",
    status: "in_review",
    payment: { mode: "milestone", amount: 500, milestones: stages },
    submissions,
  };
  assert.match(milestoneSubmitBlock(task) ?? "", /待结算/);
  assert.match(submitMilestoneField(task, "m1") , /待结算/);

  const open = {
    ...task,
    status: "active",
    payment: {
      mode: "milestone",
      amount: 500,
      milestones: [
        { id: "m1", amount: 200, condition: "draft", status: "released" },
        { id: "m2", amount: 300, condition: "final", status: "pending" },
      ],
    },
  };
  assert.equal(milestoneSubmitBlock(open), null);
  assert.deepEqual(submitMilestoneField(open, "m2"), { milestone_id: "m2" });
  assert.equal(submitMilestoneField(open, "m1"), "请选择一个待完成的阶段");
  assert.deepEqual(submitMilestoneField({ ...open, payment: { mode: "one_shot", amount: 500 } }, ""), {});
});

test("competitive milestone stays blocked until the task is active again", () => {
  const task = {
    kind: "competitive",
    status: "in_review",
    payment: {
      mode: "milestone",
      amount: 500,
      milestones: [{ id: "m1", amount: 500, condition: "only", status: "submitted" }],
    },
  };
  assert.match(milestoneSubmitBlock(task) ?? "", /待结算/);
  assert.match(milestoneSubmitBlock(task) ?? "", /进行中/);
});

test("confirm releases only the submitted stage and ignores an older current submission", () => {
  const task = {
    kind: "exclusive",
    status: "in_review",
    payment: { mode: "milestone", amount: 500, milestones: stages },
    submissions,
  };
  assert.deepEqual(confirmTaskBody(task, "sub_old", "", null), { milestone_id: "m2" });
  assert.equal(confirmTaskBody(task, "sub_old", "m1", null), "请选择待结算的阶段");
});

test("competitive confirm uses the in-review stage submission, not the first current one", () => {
  const task = {
    kind: "competitive",
    status: "in_review",
    payment: { mode: "milestone", amount: 500, milestones: stages },
    submissions,
  };
  assert.deepEqual(confirmTaskBody(task, "sub_old", "m2", null), {
    milestone_id: "m2",
    submission_id: "sub_new",
  });
});

test("one_shot confirm keeps the existing submission choice", () => {
  const exclusive = {
    kind: "exclusive",
    status: "in_review",
    payment: { mode: "one_shot", amount: 500 },
    submissions: [{ id: "sub_only", status: "current", milestone_id: null }],
  };
  assert.deepEqual(confirmTaskBody(exclusive, "sub_only", "", null), {});

  const competitive = {
    kind: "competitive",
    status: "in_review",
    payment: { mode: "one_shot", amount: 500 },
    submissions: [
      { id: "sub_a", status: "current" },
      { id: "sub_b", status: "current" },
    ],
  };
  assert.deepEqual(confirmTaskBody(competitive, "sub_b", "", null), { submission_id: "sub_b" });
  assert.equal(confirmTaskBody(competitive, "", "", null), "请选择一份 submission_id");
});

test("seat invite is offered only to the publisher for an open collaborative seat", () => {
  const live = { kind: "collaborative", status: "open", publisher_id: "usr_publisher" };
  assert.equal(canInviteSeat("usr_publisher", live, "open"), true);
  assert.equal(canInviteSeat("usr_publisher", { ...live, status: "active" }, undefined), true);
  assert.equal(canInviteSeat("usr_other", live, "open"), false);
  assert.equal(canInviteSeat(undefined, live, "open"), false);
  assert.equal(canInviteSeat("usr_publisher", { ...live, kind: "exclusive" }, "open"), false);
  assert.equal(canInviteSeat("usr_publisher", { ...live, status: "settled" }, "open"), false);
  assert.equal(canInviteSeat("usr_publisher", live, "filled"), false);
  assert.equal(canInviteSeat("usr_publisher", live, "submitted"), false);
});

test("only the publisher can remove an empty seat of a live collaborative task", () => {
  const live = { kind: "collaborative", status: "open", publisher_id: "usr_publisher" };
  assert.equal(canRemoveSeat("usr_publisher", live, "open"), true);
  assert.equal(canRemoveSeat("usr_publisher", { ...live, status: "active" }, "dropped"), true);
  assert.equal(canRemoveSeat("usr_publisher", live, "open", "usr_someone"), false);
  assert.equal(canRemoveSeat("usr_other", live, "open"), false);
  assert.equal(canRemoveSeat(undefined, live, "open"), false);
  assert.equal(canRemoveSeat("usr_publisher", { ...live, kind: "exclusive" }, "open"), false);
  assert.equal(canRemoveSeat("usr_publisher", { ...live, status: "settled" }, "open"), false);
  assert.equal(canRemoveSeat("usr_publisher", live, "filled"), false);
  assert.equal(canRemoveSeat("usr_publisher", live, "submitted"), false);
  assert.equal(canRemoveSeat("usr_publisher", live, "removed"), false);
});

test("task invite disappears once the only exclusive slot is taken", () => {
  const publisher = "usr_publisher";
  const exclusive = { kind: "exclusive", status: "open", publisher_id: publisher, executor_ids: [] };
  assert.equal(canInviteTask(publisher, exclusive), true);
  assert.equal(canInviteTask(publisher, { ...exclusive, executor_ids: ["usr_executor"] }), false);
  assert.equal(canInviteTask(publisher, { ...exclusive, status: "active" }), false);
  assert.equal(canInviteTask("usr_other", exclusive), false);
  assert.equal(canInviteTask(undefined, exclusive), false);

  const competitive = { ...exclusive, kind: "competitive", competitive: { slots: 3 } };
  assert.equal(canInviteTask(publisher, competitive), true);
  assert.equal(canInviteTask(publisher, { ...competitive, status: "active", executor_ids: ["a", "b"] }), true);
  assert.equal(canInviteTask(publisher, { ...competitive, executor_ids: ["a", "b", "c"] }), false);
  assert.equal(canInviteTask(publisher, { ...competitive, status: "in_review" }), false);
  assert.equal(canInviteTask(publisher, { ...exclusive, kind: "collaborative" }), false);
});

test("seat invite input splits ids on commas and whitespace and deduplicates", () => {
  assert.deepEqual(inviteUserIds(" usr_a, usr_b  usr_a,,usr_c "), ["usr_a", "usr_b", "usr_c"]);
  assert.deepEqual(inviteUserIds("  ,,\t\n "), []);
  assert.deepEqual(inviteUserIds("usr_a"), ["usr_a"]);
});

test("cancel button follows the server: a submission returned for revision still blocks cancel", () => {
  const task = {
    status: "active",
    publisher_id: "usr_publisher",
    kind: "exclusive",
    submissions: [{ id: "sub_1", status: "superseded" }],
  };
  assert.match(cancelReason(task, "usr_publisher") ?? "", /已经有完成通知/);
  assert.equal(cancelReason({ ...task, submissions: [] }, "usr_publisher"), null);
  assert.equal(cancelReason({ ...task, status: "open" }, "usr_publisher"), null);
  assert.equal(cancelReason({ ...task, status: "draft" }, "usr_publisher"), null);
  assert.match(cancelReason({ ...task, status: "in_review" }, "usr_publisher") ?? "", /返工|结算/);
  assert.match(cancelReason({ ...task, status: "disputed" }, "usr_publisher") ?? "", /协商|裁定/);
  assert.match(cancelReason({ ...task, status: "settled" }, "usr_publisher") ?? "", /不能取消/);
  assert.match(cancelReason(task, "usr_executor") ?? "", /只有发布者/);
});

test("per-seat collaboration can still cancel unfinished seats; frozen escrow cannot", () => {
  const perSeat = {
    status: "active",
    publisher_id: "usr_publisher",
    kind: "collaborative",
    submissions: [{ id: "sub_1", status: "superseded" }],
    collaborative: {
      settlement_mode: "per_seat",
      roles: [{ status: "submitted" }, { status: "filled" }],
    },
  };
  assert.equal(cancelReason(perSeat, "usr_publisher"), null);
  assert.match(
    cancelReason(
      { ...perSeat, collaborative: { ...perSeat.collaborative, roles: [{ status: "submitted" }] } },
      "usr_publisher",
    ) ?? "",
    /没有可退款的未完成席位/,
  );
  assert.match(cancelReason({ ...perSeat, escrow: { frozen: 100 } }, "usr_publisher") ?? "", /冻结/);
  assert.equal(
    cancelReason({ ...perSeat, escrow: { frozen: 100 }, dispute_role_id: "role_1" }, "usr_publisher"),
    null,
  );
});

test("revise targets the in-review milestone when an earlier stage submission is still current", () => {
  const task = {
    kind: "exclusive",
    status: "in_review",
    payment: { mode: "milestone", amount: 500, milestones: stages },
    submissions,
  };
  assert.equal(inReviewMilestoneSubmissionId(task, "sub_old"), "sub_new");
  assert.equal(
    inReviewMilestoneSubmissionId(
      { ...task, payment: { mode: "one_shot", amount: 500, milestones: [] } },
      "sub_old",
    ),
    undefined,
  );
});

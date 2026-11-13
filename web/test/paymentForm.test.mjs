import assert from "node:assert/strict";
import test from "node:test";
import {
  buildPayment,
  defaultMilestones,
  evenMilestoneAmounts,
  milestoneSumLabel,
  nextMilestoneId,
  remainderAmount,
} from "../src/paymentForm.ts";

test("one_shot payment omits milestones", () => {
  const payment = buildPayment({
    kind: "exclusive",
    mode: "one_shot",
    amountText: "500",
    milestones: [{ id: "m1", amount: "500", condition: "done" }],
  });
  assert.deepEqual(payment, { mode: "one_shot", amount: 500 });
  assert.equal("milestones" in payment, false);
});

test("collaborative create stays one_shot even if milestone drafts are present", () => {
  const payment = buildPayment({
    kind: "collaborative",
    mode: "milestone",
    amountText: "500",
    milestones: [{ id: "bad id", amount: "", condition: "" }],
  });
  assert.deepEqual(payment, { mode: "one_shot", amount: 500 });
});

test("milestone payment is a valid split and does not send status", () => {
  const payment = buildPayment({
    kind: "competitive",
    mode: "milestone",
    amountText: "500",
    milestones: [
      { id: " draft ", amount: "200", condition: " 交出初稿 " },
      { id: "final", amount: "300", condition: "按验收标准交终稿" },
    ],
  });
  assert.deepEqual(payment, {
    mode: "milestone",
    amount: 500,
    milestones: [
      { id: "draft", amount: 200, condition: "交出初稿" },
      { id: "final", amount: 300, condition: "按验收标准交终稿" },
    ],
  });
  for (const stage of payment.milestones) {
    assert.deepEqual(Object.keys(stage).sort(), ["amount", "condition", "id"]);
  }
});

test("milestone validation catches the server rules before submit", () => {
  const base = {
    kind: "exclusive",
    mode: "milestone",
    amountText: "500",
  };
  assert.equal(
    buildPayment({ ...base, milestones: [] }),
    "分阶段需要 1 到 20 个阶段",
  );
  assert.equal(
    buildPayment({
      ...base,
      milestones: Array.from({ length: 21 }, (_, index) => ({
        id: `m${index + 1}`,
        amount: index === 0 ? "500" : "0",
        condition: "ok",
      })),
    }),
    "分阶段需要 1 到 20 个阶段",
  );
  assert.equal(
    buildPayment({ ...base, milestones: [{ id: "m.1", amount: "500", condition: "ok" }] }),
    "阶段 id 需要匹配 [A-Za-z0-9_-]{1,64}",
  );
  assert.equal(
    buildPayment({
      ...base,
      milestones: [
        { id: "m1", amount: "200", condition: "a" },
        { id: " m1 ", amount: "300", condition: "b" },
      ],
    }),
    "阶段 id 重复：m1",
  );
  assert.equal(
    buildPayment({ ...base, milestones: [{ id: "m1", amount: "-1", condition: "ok" }] }),
    "阶段金额需要是非负整数",
  );
  assert.equal(
    buildPayment({ ...base, milestones: [{ id: "m1", amount: "500", condition: "   " }] }),
    "请填写阶段验收条件",
  );
  assert.equal(
    buildPayment({ ...base, milestones: [{ id: "m1", amount: "500", condition: "测".repeat(200) }] }),
    "阶段条件不能超过 500 字节",
  );
  assert.equal(
    buildPayment({
      ...base,
      milestones: [
        { id: "m1", amount: "200", condition: "a" },
        { id: "m2", amount: "200", condition: "b" },
      ],
    }),
    "阶段金额合计是 400，需要等于 payment.amount 500",
  );
  assert.equal(buildPayment({ ...base, amountText: "nope", milestones: [] }), "金额需要是非负整数");
});

test("amount stays the escrow total and stages are split against it", () => {
  assert.deepEqual(defaultMilestones("500"), [{ id: "m1", amount: "500", condition: "" }]);
  assert.equal(nextMilestoneId([{ id: "m1", amount: "500", condition: "" }]), "m2");
  assert.equal(remainderAmount("500", [{ id: "m1", amount: "200", condition: "a" }]), "300");
  assert.equal(remainderAmount("500", [{ id: "m1", amount: "500", condition: "a" }]), "");
  assert.deepEqual(evenMilestoneAmounts(500, 3), [166, 167, 167]);
  assert.deepEqual(milestoneSumLabel("500", [{ id: "m1", amount: "200", condition: "" }, { id: "m2", amount: "300", condition: "" }]), {
    text: "阶段合计 500，与报酬 500 一致。",
    matched: true,
  });
  assert.equal(milestoneSumLabel("500", [{ id: "m1", amount: "200", condition: "" }]).matched, false);
});

import assert from "node:assert/strict";
import test from "node:test";
import { nicknameError, personLabel, shortUserId } from "../src/person.ts";

test("missing nickname falls back to a short id", () => {
  const id = "usr_" + "ab12cd34ef56ab12cd34ef56";
  assert.equal(personLabel("", id), "usr_ab12…ef56");
  assert.equal(personLabel("   ", id), "usr_ab12…ef56");
  assert.equal(personLabel(undefined, id), shortUserId(id));
  assert.notEqual(personLabel("", id), "");
});

test("a set nickname wins and short ids stay whole", () => {
  assert.equal(personLabel("  阿宁  ", "usr_abc"), "阿宁");
  assert.equal(personLabel("", "usr_publisher"), "usr_publisher");
});

test("nickname validation is visible in Chinese", () => {
  assert.match(nicknameError("   ") ?? "", /不能为空/);
  assert.match(nicknameError("名".repeat(33)) ?? "", /32/);
  assert.match(nicknameError("a\nb") ?? "", /控制字符/);
  assert.equal(nicknameError("  阿宁  "), null);
});

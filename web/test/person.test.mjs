import assert from "node:assert/strict";
import test from "node:test";
import { idSuffix, nicknameError, personLabel } from "../src/person.ts";

test("a nickname and the last 6 id characters share one line", () => {
  const id = "usr_ab12cd34ef56ab12cda1b2c3";
  assert.equal(idSuffix(id), "a1b2c3");
  assert.equal(personLabel("阿宁同学", id), "阿宁同学(...a1b2c3)");
  assert.equal(personLabel("  阿宁同学  ", id), "阿宁同学(...a1b2c3)");
  assert.equal(personLabel("阿宁同学", id).includes("\n"), false);
});

test("a missing nickname is the last 6 characters, on one line", () => {
  const id = "usr_" + "ab12cd34ef56ab12cd34ef56";
  assert.equal(personLabel("", id), "...34ef56");
  assert.equal(personLabel("   ", id), "...34ef56");
  assert.equal(personLabel(undefined, id), "...34ef56");
  assert.equal(personLabel("", "usr_publisher"), "...lisher");
  assert.equal(personLabel("阿宁", "ab"), "阿宁(...ab)");
  assert.equal(personLabel("", ""), "未命名");
});

test("nickname validation is visible in Chinese", () => {
  assert.match(nicknameError("   ") ?? "", /不能为空/);
  assert.match(nicknameError("名".repeat(33)) ?? "", /32/);
  assert.match(nicknameError("a\nb") ?? "", /控制字符/);
  assert.equal(nicknameError("  阿宁  "), null);
});

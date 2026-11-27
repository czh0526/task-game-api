import assert from "node:assert/strict";
import test from "node:test";
import { idSuffix, nicknameError, nicknameOnly, personLabel, senderDisplayName } from "../src/person.ts";

test("a nickname is shown on its own, without an id suffix", () => {
  const id = "usr_ab12cd34ef56ab12cda1b2c3";
  assert.equal(idSuffix(id), "a1b2c3");
  assert.equal(personLabel("阿宁同学", id), "阿宁同学");
  assert.equal(personLabel("  阿宁同学  ", id), "阿宁同学");
  assert.equal(personLabel("阿宁", "ab"), "阿宁");
  assert.equal(personLabel("阿宁同学", id).includes("\n"), false);
});

test("a missing nickname falls back to the last 6 characters, on one line", () => {
  const id = "usr_" + "ab12cd34ef56ab12cd34ef56";
  assert.equal(personLabel("", id), "...34ef56");
  assert.equal(personLabel("   ", id), "...34ef56");
  assert.equal(personLabel(undefined, id), "...34ef56");
  assert.equal(personLabel("", "usr_publisher"), "...lisher");
  assert.equal(personLabel("", ""), "未命名");
});

test("chat bubble sender names show the nickname without an id suffix", () => {
  const id = "usr_ab12cd34ef56ab12cda1b2c3";
  assert.equal(senderDisplayName("李娜", id), "李娜");
  assert.equal(senderDisplayName("  李娜  ", id), "李娜");
  assert.equal(senderDisplayName("", id), "...a1b2c3");
  assert.equal(senderDisplayName(undefined, id), "...a1b2c3");
  assert.equal(senderDisplayName(null, ""), "未命名");
  assert.equal(senderDisplayName("张伟", ""), "张伟");
});

test("name-only mode never leaks an id suffix", () => {
  const id = "usr_ab12cd34ef56ab12cda1b2c3";
  assert.equal(nicknameOnly("阿宁同学", undefined), "阿宁同学");
  assert.equal(nicknameOnly("  阿宁同学  ", undefined), "阿宁同学");
  assert.equal(nicknameOnly("", undefined), "用户");
  assert.equal(nicknameOnly("   ", undefined), "用户");
  assert.equal(nicknameOnly(undefined, undefined), "用户");
  assert.equal(nicknameOnly(null, undefined), "用户");
  assert.equal(nicknameOnly("", "匿名"), "匿名");
});

test("nickname validation is visible in Chinese", () => {
  assert.match(nicknameError("   ") ?? "", /不能为空/);
  assert.match(nicknameError("名".repeat(33)) ?? "", /32/);
  assert.match(nicknameError("a\nb") ?? "", /控制字符/);
  assert.equal(nicknameError("  阿宁  "), null);
});

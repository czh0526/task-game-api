import assert from "node:assert/strict";
import test from "node:test";
import { href, parseRoute } from "../src/route.ts";

test("empty hash lands on the homepage", () => {
  assert.deepEqual(parseRoute(""), { name: "home" });
  assert.deepEqual(parseRoute("#/"), { name: "home" });
  assert.equal(href.home, "#/home");
});

test("kind query preselects the task list filter", () => {
  assert.deepEqual(parseRoute("#/tasks?kind=competitive"), { name: "tasks", kind: "competitive" });
  assert.equal(href.tasksByKind("collaborative"), "#/tasks?kind=collaborative");
  assert.deepEqual(parseRoute("#/tasks?kind=unknown"), { name: "tasks", kind: undefined });
});

test("existing task routes still parse", () => {
  assert.deepEqual(parseRoute("#/tasks/new"), { name: "new" });
  assert.deepEqual(parseRoute("#/tasks/task_123"), { name: "task", id: "task_123" });
  assert.deepEqual(parseRoute("#/friends"), { name: "friends" });
});

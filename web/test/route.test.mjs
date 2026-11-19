import assert from "node:assert/strict";
import test from "node:test";
import { afterLoginHash, defaultHash, href, isProtectedRoute, parseRoute } from "../src/route.ts";

test("empty hash lands on the homepage", () => {
  assert.deepEqual(parseRoute(""), { name: "home" });
  assert.deepEqual(parseRoute("#/"), { name: "home" });
  assert.equal(href.home, "#/home");
  assert.deepEqual(parseRoute("#/login"), { name: "auth" });
  assert.equal(href.login, "#/login");
});

test("default landing page is always the public home", () => {
  assert.equal(defaultHash(), "#/home");
  assert.deepEqual(parseRoute(defaultHash()), { name: "home" });
});

test("protected routes are defined by the URL, not sign-in state", () => {
  assert.equal(isProtectedRoute({ name: "home" }), false);
  assert.equal(isProtectedRoute({ name: "task", id: "t1" }), false);
  assert.equal(isProtectedRoute({ name: "auth" }), false);
  assert.equal(isProtectedRoute({ name: "tasks" }), true);
  assert.equal(isProtectedRoute({ name: "new" }), true);
  assert.equal(isProtectedRoute({ name: "friends" }), true);
  assert.equal(isProtectedRoute({ name: "conversation", id: "c1" }), true);
  assert.equal(isProtectedRoute({ name: "settings" }), true);
  assert.equal(isProtectedRoute({ name: "scores" }), true);
  assert.equal(isProtectedRoute({ name: "invites" }), true);
});

test("after-login redirect honours a safe next hash", () => {
  assert.equal(afterLoginHash("#/login"), "#/home");
  const next = encodeURIComponent("#/tasks/task_123");
  assert.equal(afterLoginHash(`#/login?next=${next}`), "#/tasks/task_123");
  // Off-site or malformed next values fall back to the public home.
  assert.equal(afterLoginHash(`#/login?next=${encodeURIComponent("https://evil.example")}`), "#/home");
  assert.equal(afterLoginHash(`#/login?next=${encodeURIComponent("//evil.example")}`), "#/home");
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

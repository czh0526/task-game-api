import { useEffect, useState } from "react";
import type { TaskKind } from "taskgame-client";

/** Which panel of the auth screen is showing. The hash is the source of truth. */
export type AuthMode = "login" | "register";

export type Route =
  | { name: "home" }
  | { name: "auth"; mode: AuthMode }
  | { name: "tasks"; kind?: TaskKind }
  | { name: "new" }
  | { name: "task"; id: string }
  | { name: "scores" }
  | { name: "conversation"; id: string }
  | { name: "invites" }
  | { name: "friends" }
  | { name: "settings" };

const KINDS: TaskKind[] = ["exclusive", "competitive", "collaborative"];

export const href = {
  home: "#/home",
  login: "#/login",
  /** Auth screen with the 注册 tab selected. `tab` is omitted for 登录. */
  register: "#/login?tab=register",
  tasks: "#/tasks",
  tasksByKind: (kind: TaskKind) => `#/tasks?kind=${encodeURIComponent(kind)}`,
  newTask: "#/tasks/new",
  task: (id: string) => `#/tasks/${encodeURIComponent(id)}`,
  scores: "#/scores",
  conversation: (id: string) => `#/chat/${encodeURIComponent(id)}`,
  invites: "#/invites",
  friends: "#/friends",
  settings: "#/settings",
};

export function parseRoute(hash: string): Route {
  const raw = hash.startsWith("#") ? hash.slice(1) : hash;
  const question = raw.indexOf("?");
  const path = question >= 0 ? raw.slice(0, question) : raw;
  const query = question >= 0 ? new URLSearchParams(raw.slice(question + 1)) : null;
  const normalized = path.startsWith("/") ? path : `/${path}`;
  const parts = normalized.split("/").filter(Boolean);
  const head = parts[0];
  if (!head) return { name: "home" };
  if (head === "home") return { name: "home" };
  if (head === "login") return { name: "auth", mode: authModeFromQuery(query) };
  if (head === "tasks" && parts[1] === "new") return { name: "new" };
  if (head === "tasks" && parts[1]) return { name: "task", id: safeDecode(parts[1]) };
  if (head === "tasks") return { name: "tasks", kind: parseKind(query?.get("kind")) };
  if (head === "scores") return { name: "scores" };
  if (head === "chat" && parts[1]) return { name: "conversation", id: safeDecode(parts[1]) };
  if (head === "invites") return { name: "invites" };
  if (head === "friends") return { name: "friends" };
  if (head === "settings") return { name: "settings" };
  return { name: "home" };
}

/**
 * Landing route when the app opens with no hash. Navigation is driven only by
 * the URL, never by sign-in state, so the default is always the public home.
 */
export function defaultHash(): string {
  return href.home;
}

/** Pages that require a signed-in user; an anonymous visit shows a login gate. */
export function isProtectedRoute(route: Route): boolean {
  return (
    route.name === "tasks" ||
    route.name === "new" ||
    route.name === "scores" ||
    route.name === "conversation" ||
    route.name === "invites" ||
    route.name === "friends" ||
    route.name === "settings"
  );
}

/**
 * Hash to open after sign-in. A login link may carry `?next=<hash>` so that
 * e.g. a visitor reading a public task detail returns there after logging in.
 * Only same-app hashes (starting with "#/") are honoured; anything else is
 * ignored to avoid open redirects. Defaults to the public home.
 */
export function afterLoginHash(hash: string = window.location.hash): string {
  return safeNext(hash) ?? href.home;
}

/**
 * Hash for the auth screen on `mode`, keeping a safe `next` from `hash`.
 * Tab switches write this so refresh and back/forward stay on the same tab.
 */
export function authHref(mode: AuthMode, hash = ""): string {
  const params = new URLSearchParams();
  const next = hash ? safeNext(hash) : null;
  if (next) params.set("next", next);
  if (mode === "register") params.set("tab", "register");
  const text = params.toString();
  return text ? `${href.login}?${text}` : href.login;
}

export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(() => {
    if (!window.location.hash) {
      // Decide the landing page before the first render. Child screen effects
      // (e.g. Home writing "#/home") run before this hook's effect, so waiting
      // until an effect would let them overwrite the redirect.
      const next = defaultHash();
      window.history.replaceState(null, "", next);
      return parseRoute(next);
    }
    return parseRoute(window.location.hash);
  });
  useEffect(() => {
    const onChange = () => setRoute(parseRoute(window.location.hash));
    window.addEventListener("hashchange", onChange);
    return () => window.removeEventListener("hashchange", onChange);
  }, []);
  return route;
}

function authModeFromQuery(query: URLSearchParams | null): AuthMode {
  return query?.get("tab") === "register" ? "register" : "login";
}

/** Same-app `next` hash, or null when missing or unsafe (open-redirect guard). */
function safeNext(hash: string): string | null {
  const raw = hash.startsWith("#") ? hash.slice(1) : hash;
  const question = raw.indexOf("?");
  if (question < 0) return null;
  const next = new URLSearchParams(raw.slice(question + 1)).get("next");
  if (next && next.startsWith("#/") && !next.startsWith("//")) return next;
  return null;
}

function parseKind(value: string | null | undefined): TaskKind | undefined {
  return KINDS.find((kind) => kind === value);
}

function safeDecode(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

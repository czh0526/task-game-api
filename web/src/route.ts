import { useEffect, useState } from "react";
import type { TaskKind } from "taskgame-client";

export type Route =
  | { name: "home" }
  | { name: "auth" }
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
  tasks: "#/tasks",
  tasksByKind: (kind: TaskKind) => `#/tasks?kind=${encodeURIComponent(kind)}`,
  newTask: "#/tasks/new",
  agentZone: "#/home?requirement=agent",
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
  if (head === "login") return { name: "auth" };
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

/** Landing route when the app opens with no hash: members start at their tasks. */
export function defaultHash(signedIn: boolean): string {
  return signedIn ? href.tasks : href.home;
}

export function useRoute(signedIn: boolean): Route {
  const [route, setRoute] = useState<Route>(() => {
    if (!window.location.hash) {
      // Decide the landing page before the first render. Child screen effects
      // (e.g. Home writing "#/home") run before this hook's effect, so waiting
      // until an effect would let them overwrite the redirect.
      const next = defaultHash(signedIn);
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

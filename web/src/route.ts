import { useEffect, useState } from "react";

export type Route =
  | { name: "tasks" }
  | { name: "new" }
  | { name: "task"; id: string }
  | { name: "scores" }
  | { name: "chat" };

export const href = {
  tasks: "#/tasks",
  newTask: "#/tasks/new",
  task: (id: string) => `#/tasks/${encodeURIComponent(id)}`,
  scores: "#/scores",
  chat: "#/chat",
};

export function parseRoute(hash: string): Route {
  const raw = hash.startsWith("#") ? hash.slice(1) : hash;
  const path = raw.startsWith("/") ? raw : `/${raw}`;
  const parts = path.split("/").filter(Boolean);
  const head = parts[0];
  if (head === "scores") return { name: "scores" };
  if (head === "chat") return { name: "chat" };
  if (head === "tasks" && parts[1] === "new") return { name: "new" };
  if (head === "tasks" && parts[1]) return { name: "task", id: safeDecode(parts[1]) };
  return { name: "tasks" };
}

export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(() => parseRoute(window.location.hash));
  useEffect(() => {
    const onChange = () => setRoute(parseRoute(window.location.hash));
    window.addEventListener("hashchange", onChange);
    if (!window.location.hash) window.location.replace("#/tasks");
    return () => window.removeEventListener("hashchange", onChange);
  }, []);
  return route;
}

function safeDecode(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

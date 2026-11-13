import { useEffect, useState, type ReactNode } from "react";
import type { TaskGameClient } from "taskgame-client";
import { useApp } from "./context";
import { personLabel } from "./person";

const nicknames = new Map<string, string>();
const listeners = new Set<() => void>();
let loadedClient: TaskGameClient | null = null;
let friendsLoaded = false;
let generation = 0;
const queued = new Set<string>();
let flushTimer = 0;

export const PROFILE_EVENT = "taskgame-profile";

export function rememberNickname(userId: string, nickname: string | null | undefined): void {
  if (!userId) return;
  const next = nickname?.trim() ?? "";
  if (nicknames.get(userId) === next) return;
  nicknames.set(userId, next);
  listeners.forEach((listener) => listener());
}

export function notifyProfile(userId: string, nickname: string): void {
  rememberNickname(userId, nickname);
  window.dispatchEvent(new CustomEvent(PROFILE_EVENT, { detail: { userId, nickname } }));
}

export function PersonName({ nickname, userId }: { nickname?: string | null; userId: string }) {
  return (
    <span className="person" title={userId || undefined}>
      {personLabel(nickname, userId)}
    </span>
  );
}

export function UserRef({ userId }: { userId: string }): ReactNode {
  const nickname = useNickname(userId);
  return <PersonName nickname={nickname} userId={userId} />;
}

export function useNickname(userId: string): string | undefined {
  const { client } = useApp();
  const [value, setValue] = useState<string | undefined>(() => (userId ? nicknames.get(userId) : undefined));

  useEffect(() => {
    const listener = () => setValue(userId ? nicknames.get(userId) : undefined);
    listeners.add(listener);
    listener();
    return () => {
      listeners.delete(listener);
    };
  }, [userId]);

  useEffect(() => {
    if (!client || !userId || nicknames.has(userId)) return;
    schedule(client, userId);
  }, [client, userId]);

  return value;
}

function schedule(client: TaskGameClient, userId: string): void {
  if (loadedClient !== client) {
    loadedClient = client;
    friendsLoaded = false;
    generation += 1;
    queued.clear();
    nicknames.clear();
    listeners.forEach((listener) => listener());
  }
  if (nicknames.has(userId)) return;
  const gen = generation;
  queued.add(userId);
  if (flushTimer) return;
  flushTimer = window.setTimeout(() => {
    flushTimer = 0;
    const ids = [...queued];
    queued.clear();
    void fill(client, ids, gen);
  }, 20);
}

async function fill(client: TaskGameClient, ids: string[], gen: number): Promise<void> {
  if (gen !== generation) return;
  if (!friendsLoaded) {
    try {
      const list = await client.listFriends();
      if (gen !== generation) return;
      for (const group of list.groups) {
        for (const friend of group.friends) rememberNickname(friend.user_id, friend.nickname);
      }
      for (const friend of list.ungrouped) rememberNickname(friend.user_id, friend.nickname);
      friendsLoaded = true;
    } catch {
      if (gen === generation) friendsLoaded = false;
    }
  }
  if (gen !== generation) return;
  const missing = ids.filter((id) => id && !nicknames.has(id));
  await Promise.all(
    missing.map(async (id) => {
      try {
        const scores = await client.getUserScores(id);
        if (gen !== generation) return;
        rememberNickname(id, scores.nickname);
      } catch {
        if (gen !== generation) return;
        rememberNickname(id, "");
      }
    }),
  );
}

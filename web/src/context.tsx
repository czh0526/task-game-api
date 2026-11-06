import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import type { TaskGameClient } from "taskgame-client";
import { clientErrorText, createClient } from "./client";
import { loadSettings, saveSettings, type Settings } from "./settings";

interface AppApi {
  settings: Settings;
  setSettings: (next: Settings) => void;
  patchSettings: (patch: Partial<Settings>) => void;
  client: TaskGameClient | null;
  clientError: string | null;
  health: "checking" | "ok" | "down";
}

const AppContext = createContext<AppApi | null>(null);

export function AppProvider({ children }: { children: ReactNode }) {
  const [settings, setSettingsState] = useState<Settings>(() => loadSettings());
  const [committed, setCommitted] = useState(settings);
  const [health, setHealth] = useState<AppApi["health"]>("checking");

  useEffect(() => {
    const timer = window.setTimeout(() => setCommitted(settings), 350);
    return () => window.clearTimeout(timer);
  }, [settings]);

  const { client, clientError } = useMemo(() => {
    try {
      return { client: createClient(committed), clientError: null };
    } catch (error) {
      return { client: null, clientError: clientErrorText(error) };
    }
  }, [committed]);

  useEffect(() => {
    if (!client) {
      setHealth("down");
      return;
    }
    const ac = new AbortController();
    setHealth("checking");
    client
      .health({ signal: ac.signal })
      .then((status) => {
        if (!ac.signal.aborted) setHealth(status.status === "ok" ? "ok" : "down");
      })
      .catch(() => {
        if (!ac.signal.aborted) setHealth("down");
      });
    return () => ac.abort();
  }, [client]);

  const api = useMemo<AppApi>(() => {
    const setSettings = (next: Settings) => {
      saveSettings(next);
      setSettingsState(next);
      setCommitted(next);
    };
    return {
      settings,
      setSettings,
      patchSettings: (patch) => {
        setSettingsState((prev) => {
          const next = { ...prev, ...patch };
          saveSettings(next);
          return next;
        });
      },
      client,
      clientError,
      health,
    };
  }, [settings, client, clientError, health]);

  return <AppContext.Provider value={api}>{children}</AppContext.Provider>;
}

export function useApp(): AppApi {
  const value = useContext(AppContext);
  if (!value) throw new Error("useApp must be used inside AppProvider");
  return value;
}

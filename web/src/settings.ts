export const SETTINGS_KEY = "taskgame.web.settings";

export const PRESETS = {
  publisher: { apiKey: "dev-publisher-key", userId: "usr_publisher", label: "发布者" },
  executor: { apiKey: "dev-executor-key", userId: "usr_executor", label: "执行者" },
} as const;

export type Role = keyof typeof PRESETS;
export type AuthMode = "apiKey" | "bearer";

export interface Settings {
  baseUrl: string;
  role: Role;
  authMode: AuthMode;
  apiKey: string;
  bearerToken: string;
  userId: string;
}

const DEFAULTS: Settings = {
  baseUrl: "http://127.0.0.1:8891",
  role: "publisher",
  authMode: "apiKey",
  apiKey: PRESETS.publisher.apiKey,
  bearerToken: "",
  userId: PRESETS.publisher.userId,
};

export function loadSettings(): Settings {
  const envBase = import.meta.env.VITE_TASKGAME_BASE_URL?.trim();
  const fallback = { ...DEFAULTS, baseUrl: envBase || DEFAULTS.baseUrl };
  try {
    const raw = localStorage.getItem(SETTINGS_KEY);
    if (!raw) return fallback;
    const parsed = JSON.parse(raw) as Partial<Settings>;
    return normalize({ ...fallback, ...parsed });
  } catch {
    return fallback;
  }
}

export function saveSettings(settings: Settings): void {
  localStorage.setItem(SETTINGS_KEY, JSON.stringify(normalize(settings)));
}

export function settingsForRole(current: Settings, role: Role): Settings {
  const preset = PRESETS[role];
  return normalize({
    ...current,
    role,
    authMode: "apiKey",
    apiKey: preset.apiKey,
    userId: preset.userId,
  });
}

export function activePreset(settings: Settings): Role | null {
  if (settings.authMode !== "apiKey") return null;
  for (const role of Object.keys(PRESETS) as Role[]) {
    const preset = PRESETS[role];
    if (settings.apiKey === preset.apiKey && settings.userId === preset.userId) return role;
  }
  return null;
}

function normalize(input: Settings): Settings {
  const role: Role = input.role === "executor" ? "executor" : "publisher";
  const authMode: AuthMode = input.authMode === "bearer" ? "bearer" : "apiKey";
  return {
    baseUrl: typeof input.baseUrl === "string" && input.baseUrl.trim() ? input.baseUrl.trim() : DEFAULTS.baseUrl,
    role,
    authMode,
    apiKey: typeof input.apiKey === "string" ? input.apiKey : DEFAULTS.apiKey,
    bearerToken: typeof input.bearerToken === "string" ? input.bearerToken : "",
    userId: typeof input.userId === "string" ? input.userId.trim() : "",
  };
}

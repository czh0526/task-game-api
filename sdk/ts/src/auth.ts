/** How a call authenticates. The server accepts one of these headers. */

export type BearerToken = string | (() => string | Promise<string>);

export type Auth =
  | { readonly type: "apiKey"; readonly apiKey: string }
  | { readonly type: "bearer"; readonly token: BearerToken };

/** `X-Api-Key` header. Development keys are in etc/taskgame.yaml. */
export function apiKey(value: string): Auth {
  if (typeof value !== "string" || value.trim() === "") {
    throw new TypeError("apiKey must be a non-empty string");
  }
  return { type: "apiKey", apiKey: value };
}

/**
 * `Authorization: Bearer` header. `token` is the raw JWT, or a function that
 * returns one. A value that already starts with "Bearer " is not prefixed twice.
 */
export function bearer(token: BearerToken): Auth {
  if (typeof token === "string") {
    if (token.trim() === "") throw new TypeError("bearer token must be a non-empty string");
    return { type: "bearer", token };
  }
  if (typeof token !== "function") {
    throw new TypeError("bearer token must be a string or a function");
  }
  return { type: "bearer", token };
}

export function authorizationHeader(token: string): string {
  const trimmed = token.trim();
  if (trimmed.toLowerCase().startsWith("bearer ")) return trimmed;
  return `Bearer ${trimmed}`;
}

export async function authHeaders(auth: Auth | undefined): Promise<Record<string, string>> {
  if (!auth) return {};
  if (auth.type === "apiKey") {
    return { "X-Api-Key": auth.apiKey };
  }
  const raw = typeof auth.token === "function" ? await auth.token() : auth.token;
  if (typeof raw !== "string" || raw.trim() === "") {
    throw new TypeError("bearer token must be a non-empty string");
  }
  return { Authorization: authorizationHeader(raw) };
}

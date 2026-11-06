import { apiKey, authHeaders, bearer, type Auth, type BearerToken } from "./auth.js";
import { TaskGameError, taskGameErrorFromResponse } from "./errors.js";
import type {
  AcceptTaskBody,
  Chat,
  ChatCreate,
  ConfirmTaskBody,
  DisputeTaskBody,
  HealthStatus,
  ListTasksQuery,
  Message,
  MessageCreate,
  SubmissionCreate,
  Task,
  TaskCreate,
  TaskList,
  UserScores,
  Webhook,
  WebhookCreate,
} from "./types.js";

export interface FetchInit {
  method: string;
  headers: Record<string, string>;
  body?: string;
  signal?: AbortSignal;
}

export type FetchLike = (url: string, init: FetchInit) => Promise<Pick<Response, "ok" | "status" | "text">>;

export interface TaskGameClientOptions {
  /**
   * Process origin (`http://127.0.0.1:8891`) or the OpenAPI server URL
   * (`https://api.example.com/v1`). A trailing `/v1` is the protocol prefix.
   * `GET /health` is requested on the origin, without `/v1`.
   */
  baseUrl: string;
  /** Use `apiKey()` or `bearer()`. */
  auth?: Auth;
  /** Shorthand for `auth: apiKey(...)`. */
  apiKey?: string;
  /** Shorthand for `auth: bearer(...)`. The value is the raw JWT. */
  bearerToken?: BearerToken;
  /** Defaults to the global `fetch`. */
  fetch?: FetchLike;
}

export interface CallOptions {
  /** Sent as the `Idempotency-Key` header on operations that declare it. */
  idempotencyKey?: string;
  /** Replaces the client auth for this call. */
  auth?: Auth;
  signal?: AbortSignal;
}

type IdempotencyMode = "header" | "forbidden";

interface ServiceUrls {
  origin: string;
  apiBase: string;
}

/**
 * HTTP client for the operations in api/openapi.yaml, plus `GET /health`.
 * Paths are not invented here: see `protocolOperations`.
 */
export class TaskGameClient {
  /** Origin used for `GET /health`, with no `/v1` suffix. */
  readonly origin: string;
  /** OpenAPI server URL, ending in `/v1`. */
  readonly apiBase: string;

  private readonly inputBaseUrl: string;
  private readonly auth: Auth | undefined;
  private readonly fetchImpl: FetchLike | undefined;

  constructor(options: TaskGameClientOptions) {
    if (!options || typeof options.baseUrl !== "string") {
      throw new TypeError("baseUrl is required");
    }
    const urls = resolveServiceUrls(options.baseUrl);
    this.inputBaseUrl = options.baseUrl;
    this.origin = urls.origin;
    this.apiBase = urls.apiBase;
    this.auth = resolveAuth(options);
    this.fetchImpl = options.fetch;
  }

  /** Same transport and base URL, different credentials. */
  withAuth(auth: Auth): TaskGameClient {
    return new TaskGameClient({
      baseUrl: this.inputBaseUrl,
      auth,
      fetch: this.fetchImpl,
    });
  }

  /** `GET /health`. No auth header. Not part of the OpenAPI document. */
  health(options?: Pick<CallOptions, "signal">): Promise<HealthStatus> {
    return this.send("GET", `${this.origin}/health`, options, undefined, false, "forbidden", false);
  }

  /** `GET /v1/tasks` */
  listTasks(query?: ListTasksQuery, options?: CallOptions): Promise<TaskList> {
    if (query?.limit !== undefined) {
      if (!Number.isInteger(query.limit) || query.limit < 1 || query.limit > 100) {
        throw new TypeError("limit must be an integer from 1 to 100");
      }
    }
    return this.send("GET", `${this.apiBase}/tasks${listQuery(query)}`, options, undefined, false, "header");
  }

  /** `POST /v1/tasks` → 201 */
  createTask(body: TaskCreate, options?: CallOptions): Promise<Task> {
    return this.send("POST", `${this.apiBase}/tasks`, options, body, true, "header");
  }

  /** `GET /v1/tasks/{taskId}` */
  getTask(taskId: string, options?: CallOptions): Promise<Task> {
    return this.send("GET", `${this.apiBase}/tasks/${segment(taskId, "taskId")}`, options, undefined, false, "forbidden");
  }

  /** `POST /v1/tasks/{taskId}/fund` */
  fundTask(taskId: string, options?: CallOptions): Promise<Task> {
    return this.send(
      "POST",
      `${this.apiBase}/tasks/${segment(taskId, "taskId")}/fund`,
      options,
      undefined,
      false,
      "header",
    );
  }

  /** `POST /v1/tasks/{taskId}/accept` */
  acceptTask(taskId: string, body?: AcceptTaskBody, options?: CallOptions): Promise<Task> {
    return this.send(
      "POST",
      `${this.apiBase}/tasks/${segment(taskId, "taskId")}/accept`,
      options,
      body ?? {},
      true,
      "header",
    );
  }

  /** `POST /v1/tasks/{taskId}/submit` */
  submitTask(taskId: string, body: SubmissionCreate, options?: CallOptions): Promise<Task> {
    return this.send(
      "POST",
      `${this.apiBase}/tasks/${segment(taskId, "taskId")}/submit`,
      options,
      body,
      true,
      "header",
    );
  }

  /** `POST /v1/tasks/{taskId}/confirm` */
  confirmTask(taskId: string, body?: ConfirmTaskBody, options?: CallOptions): Promise<Task> {
    return this.send(
      "POST",
      `${this.apiBase}/tasks/${segment(taskId, "taskId")}/confirm`,
      options,
      body ?? {},
      true,
      "header",
    );
  }

  /** `POST /v1/tasks/{taskId}/dispute` */
  disputeTask(taskId: string, body: DisputeTaskBody, options?: CallOptions): Promise<Task> {
    return this.send(
      "POST",
      `${this.apiBase}/tasks/${segment(taskId, "taskId")}/dispute`,
      options,
      body,
      true,
      "header",
    );
  }

  /** `POST /v1/tasks/{taskId}/dropout` */
  dropoutTask(taskId: string, options?: CallOptions): Promise<Task> {
    return this.send(
      "POST",
      `${this.apiBase}/tasks/${segment(taskId, "taskId")}/dropout`,
      options,
      {},
      true,
      "header",
    );
  }

  /** `POST /v1/webhooks` → 201 */
  createWebhook(body: WebhookCreate, options?: CallOptions): Promise<Webhook> {
    return this.send("POST", `${this.apiBase}/webhooks`, options, body, true, "header");
  }

  /** `POST /v1/chats` → 201 */
  createChat(body: ChatCreate, options?: CallOptions): Promise<Chat> {
    return this.send("POST", `${this.apiBase}/chats`, options, body, true, "header");
  }

  /** `POST /v1/chats/{chatId}/messages` → 201 */
  postMessage(chatId: string, body: MessageCreate, options?: CallOptions): Promise<Message> {
    return this.send(
      "POST",
      `${this.apiBase}/chats/${segment(chatId, "chatId")}/messages`,
      options,
      body,
      true,
      "header",
    );
  }

  /** `GET /v1/users/{userId}/scores` */
  getUserScores(userId: string, options?: CallOptions): Promise<UserScores> {
    return this.send(
      "GET",
      `${this.apiBase}/users/${segment(userId, "userId")}/scores`,
      options,
      undefined,
      false,
      "forbidden",
    );
  }

  private async send<T>(
    method: string,
    url: string,
    options: CallOptions | Pick<CallOptions, "signal"> | undefined,
    body: unknown,
    sendBody: boolean,
    idempotency: IdempotencyMode,
    includeAuth = true,
  ): Promise<T> {
    const call = options as CallOptions | undefined;
    const headers: Record<string, string> = { Accept: "application/json" };
    if (includeAuth) {
      const auth = await authHeaders(call?.auth ?? this.auth);
      Object.assign(headers, auth);
    }
    if (call?.idempotencyKey !== undefined) {
      if (idempotency === "forbidden") {
        throw new TypeError("Idempotency-Key is not defined for this operation");
      }
      headers["Idempotency-Key"] = call.idempotencyKey;
    }
    const init: FetchInit = { method, headers, signal: call?.signal };
    if (sendBody) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body ?? {});
    }
    const fetchImpl = this.fetchImpl ?? globalFetch();
    const response = await fetchImpl(url, init);
    return readSuccess<T>(response);
  }
}

function globalFetch(): FetchLike {
  const fetchImpl = globalThis.fetch;
  if (typeof fetchImpl !== "function") {
    throw new TypeError("global fetch is not available; pass options.fetch");
  }
  return (url, init) => fetchImpl(url, init);
}

async function readSuccess<T>(response: Pick<Response, "ok" | "status" | "text">): Promise<T> {
  const text = await response.text();
  let parsed: unknown;
  if (text) {
    try {
      parsed = JSON.parse(text) as unknown;
    } catch {
      if (response.ok) {
        throw new TaskGameError(response.status, "invalid_response", "response was not JSON", text);
      }
      throw new TaskGameError(response.status, "http_error", text.slice(0, 500), text);
    }
  }
  if (!response.ok) {
    throw taskGameErrorFromResponse(response.status, parsed, text);
  }
  if (parsed === undefined) {
    throw new TaskGameError(response.status, "invalid_response", "response body was empty", text);
  }
  return parsed as T;
}

function resolveAuth(options: TaskGameClientOptions): Auth | undefined {
  const hasShorthand = options.apiKey !== undefined || options.bearerToken !== undefined;
  if (options.auth && hasShorthand) {
    throw new TypeError("Pass auth or apiKey/bearerToken, not both");
  }
  if (options.apiKey !== undefined && options.bearerToken !== undefined) {
    throw new TypeError(
      "Pass apiKey or bearerToken. When both headers are present the server authenticates the API key.",
    );
  }
  if (options.auth) return options.auth;
  if (options.apiKey !== undefined) return apiKey(options.apiKey);
  if (options.bearerToken !== undefined) return bearer(options.bearerToken);
  return undefined;
}

export function resolveServiceUrls(baseUrl: string): ServiceUrls {
  const trimmed = baseUrl.trim();
  if (!trimmed) throw new TypeError("baseUrl is required");
  if (!/^https?:\/\//i.test(trimmed)) {
    throw new TypeError("baseUrl must be an absolute http(s) URL");
  }
  let url: URL;
  try {
    url = new URL(trimmed);
  } catch {
    throw new TypeError("baseUrl is not a valid URL");
  }
  if (url.username || url.password) throw new TypeError("baseUrl must not include userinfo");
  if (url.search || url.hash) throw new TypeError("baseUrl must not include a query or hash");
  const path = url.pathname.replace(/\/+$/, "");
  let originPath = path;
  if (path === "/v1") {
    originPath = "";
  } else if (path.endsWith("/v1")) {
    originPath = path.slice(0, -3).replace(/\/+$/, "");
  } else if (path !== "") {
    throw new TypeError("baseUrl path must be empty or end with /v1");
  }
  const origin = originPath ? `${url.origin}${originPath}` : url.origin;
  return { origin, apiBase: `${origin}/v1` };
}

function listQuery(query: ListTasksQuery | undefined): string {
  if (!query) return "";
  const params = new URLSearchParams();
  if (query.status !== undefined) params.set("status", query.status);
  if (query.kind !== undefined) params.set("kind", query.kind);
  if (query.limit !== undefined) params.set("limit", String(query.limit));
  if (query.cursor !== undefined) params.set("cursor", query.cursor);
  const text = params.toString();
  return text ? `?${text}` : "";
}

function segment(value: string, name: string): string {
  if (typeof value !== "string" || value.trim() === "") {
    throw new TypeError(`${name} is required`);
  }
  return encodeURIComponent(value);
}

/** Opaque key for `CallOptions.idempotencyKey`. The server allows up to 200 characters. */
export function createIdempotencyKey(prefix = "idem"): string {
  const randomUUID = globalThis.crypto?.randomUUID;
  if (typeof randomUUID !== "function") {
    throw new TypeError("crypto.randomUUID is not available");
  }
  return `${prefix}_${randomUUID.call(globalThis.crypto)}`;
}

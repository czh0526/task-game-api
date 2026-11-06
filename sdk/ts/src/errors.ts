/**
 * HTTP error vocabulary from the server (`{"code","message"}`).
 * Status and code pairs match internal/biz/error.go.
 */

export class TaskGameError extends Error {
  readonly status: number;
  readonly code: string;
  readonly responseBody: unknown;

  constructor(status: number, code: string, message: string, responseBody: unknown) {
    super(message);
    this.name = new.target.name;
    this.status = status;
    this.code = code;
    this.responseBody = responseBody;
  }
}

/** 400 invalid_argument */
export class InvalidArgumentError extends TaskGameError {}

/** 401 unauthorized */
export class UnauthorizedError extends TaskGameError {}

/** 403 forbidden */
export class ForbiddenError extends TaskGameError {}

/** 404 not_found */
export class NotFoundError extends TaskGameError {}

/** 409 conflict */
export class ConflictError extends TaskGameError {}

/** 409 failed_precondition */
export class FailedPreconditionError extends TaskGameError {}

/** 409 insufficient_credits */
export class InsufficientCreditsError extends TaskGameError {}

/**
 * 409 idempotency_conflict.
 * The same Idempotency-Key was sent again with a different request.
 */
export class IdempotencyConflictError extends TaskGameError {}

/** 503 unavailable */
export class UnavailableError extends TaskGameError {}

const ERROR_TYPES: Record<string, new (status: number, code: string, message: string, body: unknown) => TaskGameError> = {
  invalid_argument: InvalidArgumentError,
  unauthorized: UnauthorizedError,
  forbidden: ForbiddenError,
  not_found: NotFoundError,
  conflict: ConflictError,
  failed_precondition: FailedPreconditionError,
  insufficient_credits: InsufficientCreditsError,
  idempotency_conflict: IdempotencyConflictError,
  unavailable: UnavailableError,
};

export function isTaskGameError(error: unknown): error is TaskGameError {
  return error instanceof TaskGameError;
}

export function isIdempotencyConflict(error: unknown): error is IdempotencyConflictError {
  return error instanceof IdempotencyConflictError;
}

export function taskGameErrorFromResponse(status: number, body: unknown, fallbackText: string): TaskGameError {
  const parsed = readErrorBody(body);
  const code = parsed?.code ?? "http_error";
  const message = parsed?.message ?? (fallbackText.trim() || `HTTP ${status}`);
  const Ctor = ERROR_TYPES[code] ?? TaskGameError;
  return new Ctor(status, code, message, body ?? fallbackText);
}

function readErrorBody(body: unknown): { code: string; message: string } | undefined {
  if (!body || typeof body !== "object") return undefined;
  const record = body as Record<string, unknown>;
  if (typeof record.code !== "string" || typeof record.message !== "string") return undefined;
  return { code: record.code, message: record.message };
}

/**
 * Prefix of the 400 invalid_argument message from confirm when a competitive
 * task has more than one submission. v0.1 task bodies do not list submissions;
 * scripts/smoke.sh reads the ids from this sentence, in created_at order.
 */
export const MULTIPLE_SUBMISSIONS_PREFIX = "submission_id is required when multiple submissions exist: ";

/**
 * Returns submission ids from a competitive confirm that omitted submission_id.
 * Undefined when `error` is a different failure.
 */
export function submissionIdsFromConfirmError(error: unknown): string[] | undefined {
  if (!(error instanceof InvalidArgumentError)) return undefined;
  if (error.status !== 400) return undefined;
  if (!error.message.startsWith(MULTIPLE_SUBMISSIONS_PREFIX)) return undefined;
  const ids = error.message
    .slice(MULTIPLE_SUBMISSIONS_PREFIX.length)
    .split(",")
    .map((part) => part.trim())
    .filter((part) => part.length > 0);
  return ids.length > 0 ? ids : undefined;
}

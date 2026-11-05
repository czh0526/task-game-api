// Package biz is the HTTP error vocabulary for the task game API.
package biz

// Error is a client-visible failure with a stable code and HTTP status.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Body is the JSON object written for this error.
func (e *Error) Body() map[string]string {
	return map[string]string{"code": e.Code, "message": e.Message}
}

// New builds an error. Prefer the named constructors.
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func Invalid(message string) *Error {
	return New(400, "invalid_argument", message)
}

func Unauthorized(message string) *Error {
	return New(401, "unauthorized", message)
}

func Forbidden(message string) *Error {
	return New(403, "forbidden", message)
}

func NotFound(message string) *Error {
	return New(404, "not_found", message)
}

func Conflict(message string) *Error {
	return New(409, "conflict", message)
}

func FailedPrecondition(message string) *Error {
	return New(409, "failed_precondition", message)
}

func Insufficient(message string) *Error {
	return New(409, "insufficient_credits", message)
}

func IdempotencyConflict() *Error {
	return New(409, "idempotency_conflict", "Idempotency-Key was reused with a different request")
}

func Unavailable(message string) *Error {
	return New(503, "unavailable", message)
}

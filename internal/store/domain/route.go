package domain

import (
	"strings"

	"taskgame/internal/plane"
)

// RouteForRequest derives the primary aggregate-root domain from an HTTP
// request. It mirrors cell.KeyFromPath but returns the domain as well: in
// domain mode /v1/tasks/{id}/... pins the task store, /v1/chats/{id}/... the
// message store, and everything else without an id falls back to the caller's
// user domain. An anonymous public read stays unrouted and is fanned out by
// the repositories.
func RouteForRequest(method, path, userID string) (Route, bool) {
	_ = method // create ids are generated inside the engine, so no create flag here
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "v1" && parts[2] != "" {
		switch parts[1] {
		case "tasks":
			return Route{Domain: plane.DomainTask, Key: parts[2]}, true
		case "chats":
			return Route{Domain: plane.DomainMessage, Key: parts[2]}, true
		case "users":
			return Route{Domain: plane.DomainUser, Key: parts[2]}, true
		}
	}
	if len(parts) >= 2 && parts[0] == "v1" && parts[1] != "" {
		switch parts[1] {
		case "tasks":
			if userID != "" {
				return Route{Domain: plane.DomainTask, Key: userID}, true
			}
		case "chats":
			if userID != "" {
				return Route{Domain: plane.DomainMessage, Key: userID}, true
			}
		}
	}
	if userID != "" {
		return Route{Domain: plane.DomainUser, Key: userID}, true
	}
	return Route{}, false
}

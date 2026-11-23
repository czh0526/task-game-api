package cell

import "strings"

// KeyFromPath extracts the aggregate-root id that dominates a request. The
// API routes are /v1/tasks/:taskId..., /v1/chats/:chatId... and
// /v1/users/:userId/.... A route without an id (the market feed, /me,
// /friends, /webhooks) is left unpinned: it is routed by the authenticated
// user id, and global list reads are fanned out by the repositories.
func KeyFromPath(path string) (string, string) {
	p := strings.Trim(path, "/")
	parts := strings.Split(p, "/")
	if len(parts) < 3 || parts[0] != "v1" {
		return "", ""
	}
	switch parts[1] {
	case "tasks":
		if parts[2] != "" {
			return parts[2], KindTask
		}
	case "chats":
		if parts[2] != "" {
			return parts[2], KindChat
		}
	case "users":
		if parts[2] != "" {
			return parts[2], KindUser
		}
	}
	return "", ""
}

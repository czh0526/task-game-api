package engine

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"taskgame/internal/biz"
	"taskgame/internal/model"
)

func hoursDuration(hours int) time.Duration {
	return time.Duration(hours) * time.Hour
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func dedupe(ss []string) []string {
	seen := make(map[string]struct{}, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func parties(t model.Task, extra ...string) []string {
	ids := []string{t.PublisherID}
	ids = append(ids, t.ExecutorIDs...)
	if t.Collaborative != nil {
		for _, role := range t.Collaborative.Roles {
			if role.AssigneeUserID != "" {
				ids = append(ids, role.AssigneeUserID)
			}
		}
	}
	ids = append(ids, extra...)
	return dedupe(ids)
}

func removeID(ids []string, id string) []string {
	out := make([]string, 0, len(ids))
	for _, x := range ids {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}

func encodeCursor(offset int) *string {
	s := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
	return &s
}

func decodeCursor(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, biz.Invalid("invalid cursor")
	}
	n, err := strconv.Atoi(string(b))
	if err != nil || n < 0 {
		return 0, biz.Invalid("invalid cursor")
	}
	return n, nil
}

func trim(s string) string { return strings.TrimSpace(s) }

func filledRoles(t model.Task) []model.Role {
	if t.Collaborative == nil {
		return nil
	}
	var out []model.Role
	for _, role := range t.Collaborative.Roles {
		if role.Status == model.RoleFilled && role.AssigneeUserID != "" {
			out = append(out, role)
		}
	}
	return out
}

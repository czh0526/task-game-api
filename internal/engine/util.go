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

func submissionCurrent(s model.Submission) bool {
	return s.Status == "" || s.Status == model.SubmissionCurrent
}

func currentSubmissions(subs []model.Submission) []model.Submission {
	out := make([]model.Submission, 0, len(subs))
	for _, s := range subs {
		if submissionCurrent(s) {
			out = append(out, s)
		}
	}
	return out
}

func hasOpenSeat(t model.Task) bool {
	if t.Collaborative == nil {
		return false
	}
	for _, role := range t.Collaborative.Roles {
		if role.Status == model.RoleOpen || role.Status == model.RoleDropped || role.Status == "" {
			return true
		}
	}
	return false
}

func unifiedReadyToStart(t model.Task) bool {
	if !isUnifiedCollab(t) || t.Collaborative == nil || len(t.Collaborative.Roles) == 0 {
		return false
	}
	for _, role := range t.Collaborative.Roles {
		if role.Status != model.RoleFilled || role.AssigneeUserID == "" {
			return false
		}
	}
	return true
}

func taskDisputed(t model.Task) bool {
	return t.Status == model.StatusDisputed || t.DisputeRoleID != ""
}

func spendable(acc model.EscrowAccount, t model.Task) int64 {
	if acc.Frozen <= 0 {
		return acc.Held
	}
	if t.DisputeRoleID == "" {
		return 0
	}
	left := acc.Held - acc.Frozen
	if left < 0 {
		return 0
	}
	return left
}

func clearReview(t *model.Task) {
	t.ReviewDeadline = nil
	t.WinnerSelectDeadline = nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

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

// settlingRoles are the seats a unified confirm pays: still filled, or already
// marked submitted, with an assignee. Open seats stay out so their share is
// the refund side of splitBps.
func settlingRoles(t model.Task) []model.Role {
	if t.Collaborative == nil {
		return nil
	}
	var out []model.Role
	for _, role := range t.Collaborative.Roles {
		if role.AssigneeUserID == "" {
			continue
		}
		if role.Status == model.RoleFilled || role.Status == model.RoleSubmitted {
			out = append(out, role)
		}
	}
	return out
}

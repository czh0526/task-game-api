package engine

import (
	"encoding/json"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/types"
)

func (e *Engine) toTask(t model.Task, acc *model.EscrowAccount, subs []model.Submission) types.Task {
	out := types.Task{
		Kind:           t.Kind,
		Title:          t.Title,
		Description:    t.Description,
		Payment:        toPayment(t.Payment),
		Acceptance:     toAcceptance(t.Acceptance),
		AssigneePolicy: toPolicy(t.AssigneePolicy),
		Id:             t.ID,
		Status:         t.Status,
		PublisherId:    t.PublisherID,
		ExecutorIds:    append([]string{}, nonNil(t.ExecutorIDs)...),
		Submissions:    toSubmissions(subs),
		CreatedAt:      model.FormatTime(t.CreatedAt),
		UpdatedAt:      model.FormatTime(t.UpdatedAt),
	}
	if t.Competitive != nil {
		out.Competitive = &types.Competitive{
			Slots:          int64(t.Competitive.Slots),
			SettlementRule: t.Competitive.SettlementRule,
		}
	}
	if t.Collaborative != nil {
		roles := make([]types.CollaborativeRole, 0, len(t.Collaborative.Roles))
		for _, role := range t.Collaborative.Roles {
			item := types.CollaborativeRole{
				Id:         role.ID,
				Name:       role.Name,
				ShareBps:   role.ShareBps,
				TemplateId: role.TemplateID,
				Status:     role.Status,
			}
			if role.AssigneeUserID != "" {
				uid := role.AssigneeUserID
				item.AssigneeUserId = &uid
			}
			if deadline := e.seatRefundDeadline(t, role); deadline != nil {
				s := model.FormatTime(*deadline)
				item.RefundDeadline = &s
			}
			roles = append(roles, item)
		}
		mode := t.Collaborative.SettlementMode
		if mode == "" {
			mode = model.SettlementUnified
		}
		out.Collaborative = &types.Collaborative{
			Roles:          roles,
			OnDropout:      t.Collaborative.OnDropout,
			SettlementMode: mode,
		}
	}
	if t.WinningSubmissionID != nil && *t.WinningSubmissionID != "" {
		v := *t.WinningSubmissionID
		out.WinningSubmissionId = &v
	}
	if t.ReviewDeadline != nil {
		s := model.FormatTime(*t.ReviewDeadline)
		out.ReviewDeadline = &s
	}
	if deadline := e.openDeadline(t); deadline != nil {
		s := model.FormatTime(*deadline)
		out.OpenDeadline = &s
	}
	if t.StartDeadline != nil && t.Status == model.StatusOpen {
		s := model.FormatTime(*t.StartDeadline)
		out.StartDeadline = &s
	}
	if t.WinnerSelectDeadline != nil && t.Status == model.StatusInReview && t.WinningSubmissionID == nil {
		s := model.FormatTime(*t.WinnerSelectDeadline)
		out.WinnerSelectDeadline = &s
	}
	if t.ChatID != "" {
		id := t.ChatID
		out.ChatId = &id
	}
	if t.CancelReason != "" {
		s := t.CancelReason
		out.CancelReason = &s
	}
	if t.CancelCode != "" {
		s := t.CancelCode
		out.CancelCode = &s
	}
	if t.CancelledAt != nil {
		s := model.FormatTime(*t.CancelledAt)
		out.CancelledAt = &s
	}
	if t.DisputeRoleID != "" {
		s := t.DisputeRoleID
		out.DisputeRoleId = &s
	}
	if props := resolveProposals(t.Resolve); len(props) > 0 {
		out.ResolveProposals = props
	}
	if acc != nil {
		out.Escrow = toEscrow(*acc)
	}
	return out
}

func toPayment(p model.Payment) types.Payment {
	out := types.Payment{
		Mode:       p.Mode,
		Amount:     p.Amount,
		Conditions: nonNil(p.Conditions),
	}
	if len(p.Milestones) > 0 {
		out.Milestones = make([]types.Milestone, 0, len(p.Milestones))
		for _, m := range p.Milestones {
			out.Milestones = append(out.Milestones, types.Milestone{
				Id:        m.ID,
				Amount:    m.Amount,
				Condition: m.Condition,
				Status:    m.Status,
			})
		}
	}
	if len(out.Conditions) == 0 {
		out.Conditions = nil
	}
	return out
}

func toAcceptance(a model.Acceptance) types.Acceptance {
	maxRev := a.MaxRevisions
	if maxRev <= 0 {
		maxRev = defaultMaxRevisions
	}
	out := types.Acceptance{
		Deadline:     model.FormatTime(a.Deadline),
		Quality:      a.Quality,
		ReviewHours:  int64(a.ReviewHours),
		MaxRevisions: int64(maxRev),
		Constraints:  nonNil(a.Constraints),
	}
	if len(out.Constraints) == 0 {
		out.Constraints = nil
	}
	return out
}

func toPolicy(p model.AssigneePolicy) types.AssigneePolicy {
	out := types.AssigneePolicy{
		Type:       p.Type,
		MinAbility: p.MinAbility,
	}
	if len(p.UserIDs) > 0 {
		out.UserIds = append([]string{}, p.UserIDs...)
	}
	return out
}

func toEscrow(a model.EscrowAccount) *types.EscrowAccount {
	entries := make([]types.EscrowEntry, 0, len(a.Entries))
	for _, e := range a.Entries {
		entries = append(entries, toEntry(e))
	}
	return &types.EscrowAccount{
		Id:       a.ID,
		Held:     a.Held,
		Frozen:   a.Frozen,
		Currency: a.Currency,
		Entries:  entries,
	}
}

func toSubmissions(subs []model.Submission) []types.Submission {
	out := make([]types.Submission, 0, len(subs))
	for _, s := range subs {
		status := s.Status
		if status == "" {
			status = model.SubmissionCurrent
		}
		item := types.Submission{
			Id:         s.ID,
			ExecutorId: s.ExecutorID,
			Note:       s.Note,
			Status:     status,
			CreatedAt:  model.FormatTime(s.CreatedAt),
			Payload:    map[string]any{},
		}
		if len(s.Payload) > 0 {
			_ = json.Unmarshal(s.Payload, &item.Payload)
		}
		if item.Payload == nil {
			item.Payload = map[string]any{}
		}
		if s.RoleID != "" {
			role := s.RoleID
			item.RoleId = &role
		}
		if s.MilestoneID != "" {
			ms := s.MilestoneID
			item.MilestoneId = &ms
		}
		out = append(out, item)
	}
	return out
}

func resolveProposals(state model.ResolveState) []types.ResolveProposal {
	var out []types.ResolveProposal
	if state.Publisher != nil {
		out = append(out, toProposal("publisher", *state.Publisher))
	}
	if state.Executor != nil {
		out = append(out, toProposal("executor", *state.Executor))
	}
	return out
}

func toProposal(side string, p model.ResolveProposal) types.ResolveProposal {
	out := types.ResolveProposal{
		UserId:       p.UserID,
		Side:         side,
		Outcome:      p.Outcome,
		Reason:       p.Reason,
		RoleId:       p.RoleID,
		SubmissionId: p.SubmissionID,
		CreatedAt:    model.FormatTime(p.At),
	}
	for _, split := range p.Splits {
		out.Splits = append(out.Splits, types.SplitLine{UserId: split.UserID, Amount: split.Amount, ShareBps: split.ShareBps})
	}
	return out
}

func (e *Engine) openDeadline(t model.Task) *time.Time {
	if t.Status != model.StatusOpen || t.FundedAt == nil || isPerSeatCollab(t) {
		return nil
	}
	if isUnifiedCollab(t) && !hasOpenSeat(t) {
		return nil
	}
	deadline := t.FundedAt.Add(e.openTimeout())
	if !t.Acceptance.Deadline.IsZero() && t.Acceptance.Deadline.Before(deadline) {
		deadline = t.Acceptance.Deadline
	}
	return &deadline
}

func (e *Engine) seatRefundDeadline(t model.Task, role model.Role) *time.Time {
	if !isPerSeatCollab(t) || role.Status != model.RoleOpen {
		return nil
	}
	opened := role.OpenedAt
	if opened == nil {
		opened = t.FundedAt
	}
	if opened == nil {
		return nil
	}
	deadline := opened.Add(e.seatTimeout())
	if !t.Acceptance.Deadline.IsZero() && t.Acceptance.Deadline.Before(deadline) {
		deadline = t.Acceptance.Deadline
	}
	return &deadline
}

func toEntry(e model.EscrowEntry) types.EscrowEntry {
	return types.EscrowEntry{
		Id:        e.ID,
		Type:      e.Type,
		Amount:    e.Amount,
		ToUserId:  e.ToUserID,
		CreatedAt: model.FormatTime(e.CreatedAt),
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

package engine

import (
	"taskgame/internal/model"
	"taskgame/internal/types"
)

func toTask(t model.Task, acc *model.EscrowAccount) types.Task {
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
	out := types.Acceptance{
		Deadline:    model.FormatTime(a.Deadline),
		Quality:     a.Quality,
		ReviewHours: int64(a.ReviewHours),
		Constraints: nonNil(a.Constraints),
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
		Currency: a.Currency,
		Entries:  entries,
	}
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

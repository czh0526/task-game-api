package engine

import (
	"fmt"
	"net/url"

	"taskgame/internal/biz"
	"taskgame/internal/id"
	"taskgame/internal/model"
	"taskgame/internal/types"
)

// maxCollaborativeSeats is the cap on both role templates and materialized seats.
const maxCollaborativeSeats = 20

func (e *Engine) buildTask(publisherID string, req *types.CreateTaskReq) (model.Task, error) {
	if req == nil {
		return model.Task{}, biz.Invalid("request body is required")
	}
	title := trim(req.Title)
	description := trim(req.Description)
	quality := trim(req.Acceptance.Quality)
	if title == "" || len(title) > 200 {
		return model.Task{}, biz.Invalid("title is required and must be at most 200 characters")
	}
	if description == "" || len(description) > 20000 {
		return model.Task{}, biz.Invalid("description is required and must be at most 20000 characters")
	}
	if quality == "" || len(quality) > 2000 {
		return model.Task{}, biz.Invalid("acceptance.quality is required and must be at most 2000 characters")
	}
	if req.Payment.Amount < 0 || req.Payment.Amount > maxMoney {
		return model.Task{}, biz.Invalid("payment.amount is out of range")
	}
	switch req.Payment.Mode {
	case model.PayOneShot, model.PayMilestone:
	default:
		return model.Task{}, biz.Invalid("payment.mode must be one_shot or milestone")
	}
	switch req.Kind {
	case model.KindExclusive, model.KindCompetitive, model.KindCollaborative:
	default:
		return model.Task{}, biz.Invalid("kind must be exclusive, competitive, or collaborative")
	}
	deadline, err := model.ParseTime(trim(req.Acceptance.Deadline))
	if err != nil {
		return model.Task{}, biz.Invalid("acceptance.deadline must be an RFC3339 timestamp")
	}
	if !deadline.After(e.now()) {
		return model.Task{}, biz.Invalid("acceptance.deadline must be in the future")
	}
	hours := int(req.Acceptance.ReviewHours)
	if hours == 0 {
		hours = defaultReviewHours
	}
	if hours < 1 || hours > maxReviewHours {
		return model.Task{}, biz.Invalid("review_hours must be between 1 and 2160")
	}
	conditions, err := cleanConditions(req.Payment.Conditions)
	if err != nil {
		return model.Task{}, err
	}
	constraints, err := cleanStrings(req.Acceptance.Constraints, 20, 500, "acceptance.constraints")
	if err != nil {
		return model.Task{}, err
	}
	milestones, err := cleanMilestones(req.Payment.Mode, req.Payment.Amount, req.Payment.Milestones)
	if err != nil {
		return model.Task{}, err
	}
	policy, err := cleanPolicy(req.AssigneePolicy)
	if err != nil {
		return model.Task{}, err
	}
	var competitive *model.Competitive
	var collaborative *model.Collaborative
	switch req.Kind {
	case model.KindExclusive:
		if req.Competitive != nil || req.Collaborative != nil {
			return model.Task{}, biz.Invalid("exclusive tasks cannot include competitive or collaborative")
		}
	case model.KindCompetitive:
		if req.Competitive == nil || req.Collaborative != nil {
			return model.Task{}, biz.Invalid("competitive tasks require competitive and no collaborative block")
		}
		rule := req.Competitive.SettlementRule
		if rule == "" {
			rule = model.SettlementWinnerTakeAll
		}
		if rule != model.SettlementWinnerTakeAll {
			return model.Task{}, biz.Invalid("competitive.settlement_rule must be winner_take_all")
		}
		if req.Competitive.Slots < 2 || req.Competitive.Slots > 100 {
			return model.Task{}, biz.Invalid("competitive.slots must be between 2 and 100")
		}
		competitive = &model.Competitive{Slots: int(req.Competitive.Slots), SettlementRule: rule}
	case model.KindCollaborative:
		if req.Collaborative == nil || req.Competitive != nil {
			return model.Task{}, biz.Invalid("collaborative tasks require collaborative and no competitive block")
		}
		block, err := cleanCollaborative(req.Collaborative)
		if err != nil {
			return model.Task{}, err
		}
		collaborative = block
	}
	now := e.now()
	return model.Task{
		Kind:           req.Kind,
		Title:          title,
		Description:    description,
		Status:         model.StatusDraft,
		PublisherID:    publisherID,
		Payment:        model.Payment{Mode: req.Payment.Mode, Amount: req.Payment.Amount, Milestones: milestones, Conditions: conditions},
		Acceptance:     model.Acceptance{Deadline: deadline, Quality: quality, Constraints: constraints, ReviewHours: hours},
		AssigneePolicy: policy,
		Competitive:    competitive,
		Collaborative:  collaborative,
		ExecutorIDs:    []string{},
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

func cleanConditions(in []string) ([]string, error) {
	allowed := map[string]struct{}{
		"on_accept": {}, "on_milestone_pass": {}, "on_final_confirm": {},
	}
	out, err := cleanStrings(in, 3, 64, "payment.conditions")
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, c := range out {
		if _, ok := allowed[c]; !ok {
			return nil, biz.Invalid("payment.conditions contains an unknown value")
		}
		if _, ok := seen[c]; ok {
			return nil, biz.Invalid("payment.conditions contains a duplicate")
		}
		seen[c] = struct{}{}
	}
	return out, nil
}

func cleanStrings(in []string, maxN, maxLen int, field string) ([]string, error) {
	if len(in) > maxN {
		return nil, biz.Invalid(field + " has too many items")
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = trim(s)
		if s == "" || len(s) > maxLen {
			return nil, biz.Invalid(field + " contains an empty or oversized item")
		}
		out = append(out, s)
	}
	return out, nil
}

func cleanMilestones(mode string, amount int64, in []types.Milestone) ([]model.Milestone, error) {
	if mode == model.PayOneShot {
		if len(in) > 0 {
			return nil, biz.Invalid("milestones are only valid when payment.mode is milestone")
		}
		return []model.Milestone{}, nil
	}
	if len(in) == 0 || len(in) > 20 {
		return nil, biz.Invalid("milestone payments require between 1 and 20 milestones")
	}
	seen := map[string]struct{}{}
	var sum int64
	out := make([]model.Milestone, 0, len(in))
	for _, m := range in {
		if !id.ValidRefID(m.Id) {
			return nil, biz.Invalid("milestone id must match [A-Za-z0-9_-]{1,64}")
		}
		if _, ok := seen[m.Id]; ok {
			return nil, biz.Invalid("milestone ids must be unique")
		}
		seen[m.Id] = struct{}{}
		if m.Amount < 0 || m.Amount > maxMoney {
			return nil, biz.Invalid("milestone amount is out of range")
		}
		if sum > maxMoney-m.Amount {
			return nil, biz.Invalid("milestone amounts overflow")
		}
		sum += m.Amount
		cond := trim(m.Condition)
		if cond == "" || len(cond) > 500 {
			return nil, biz.Invalid("milestone condition is required")
		}
		if m.Status != "" && m.Status != model.MilestonePending {
			return nil, biz.Invalid("milestone status is assigned by the server")
		}
		out = append(out, model.Milestone{ID: m.Id, Amount: m.Amount, Condition: cond, Status: model.MilestonePending})
	}
	if sum != amount {
		return nil, biz.Invalid("milestone amounts must sum to payment.amount")
	}
	return out, nil
}

func cleanPolicy(p types.AssigneePolicy) (model.AssigneePolicy, error) {
	switch p.Type {
	case model.PolicySpecific:
		if p.MinAbility != nil {
			return model.AssigneePolicy{}, biz.Invalid("min_ability is only valid for criteria")
		}
		ids, err := cleanUserIDs(p.UserIds)
		if err != nil {
			return model.AssigneePolicy{}, err
		}
		if len(ids) == 0 {
			return model.AssigneePolicy{}, biz.Invalid("specific assignee_policy requires user_ids")
		}
		return model.AssigneePolicy{Type: p.Type, UserIDs: ids}, nil
	case model.PolicyCriteria:
		if len(p.UserIds) > 0 {
			return model.AssigneePolicy{}, biz.Invalid("user_ids is only valid for specific")
		}
		if p.MinAbility != nil && *p.MinAbility < 0 {
			return model.AssigneePolicy{}, biz.Invalid("min_ability must be >= 0")
		}
		return model.AssigneePolicy{Type: p.Type, MinAbility: p.MinAbility}, nil
	case model.PolicyOpen:
		if len(p.UserIds) > 0 || p.MinAbility != nil {
			return model.AssigneePolicy{}, biz.Invalid("open assignee_policy does not take user_ids or min_ability")
		}
		return model.AssigneePolicy{Type: p.Type}, nil
	default:
		return model.AssigneePolicy{}, biz.Invalid("assignee_policy.type must be specific, criteria, or open")
	}
}

func cleanUserIDs(in []string) ([]string, error) {
	if len(in) > 100 {
		return nil, biz.Invalid("user_ids has too many items")
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = trim(s)
		if !id.ValidUserID(s) {
			return nil, biz.Invalid("user_ids contains an invalid user id")
		}
		out = append(out, s)
	}
	return dedupe(out), nil
}

// cleanCollaborative materializes role templates into seats.
//
// share_bps is the template total, not a per-seat figure. The numbers on the
// templates must still sum to 10000. quantity defaults to 1 and does not
// multiply share_bps. quantity 1 keeps the template id as the seat id, so a
// flat roles[] payload is unchanged. quantity N becomes seats {id}_1 .. {id}_N.
// Each seat receives floor(share_bps / quantity); the remainder is one extra
// basis point on each of the last (share_bps mod quantity) seats.
func cleanCollaborative(in *types.Collaborative) (*model.Collaborative, error) {
	rule := in.OnDropout
	if rule == "" {
		rule = model.DropoutReplace
	}
	if rule != model.DropoutReplace {
		return nil, biz.Invalid("collaborative.on_dropout must be replace")
	}
	if len(in.Roles) == 0 || len(in.Roles) > maxCollaborativeSeats {
		return nil, biz.Invalid("collaborative tasks require between 1 and 20 roles")
	}
	seenTemplate := map[string]struct{}{}
	var sum int64
	type template struct {
		id, name string
		qty      int
		share    int64
	}
	templates := make([]template, 0, len(in.Roles))
	for _, role := range in.Roles {
		if !id.ValidRefID(role.Id) {
			return nil, biz.Invalid("role id must match [A-Za-z0-9_-]{1,64}")
		}
		if _, ok := seenTemplate[role.Id]; ok {
			return nil, biz.Invalid("role ids must be unique")
		}
		seenTemplate[role.Id] = struct{}{}
		name := trim(role.Name)
		if name == "" || len(name) > 80 {
			return nil, biz.Invalid("role name is required")
		}
		qty, err := roleQuantity(role.Quantity)
		if err != nil {
			return nil, err
		}
		if role.ShareBps < 0 || role.ShareBps > 10000 {
			return nil, biz.Invalid("share_bps must be between 0 and 10000")
		}
		if sum > 10000-role.ShareBps {
			return nil, biz.Invalid("collaborative role share_bps must sum to 10000")
		}
		sum += role.ShareBps
		if role.AssigneeUserId != nil && trim(*role.AssigneeUserId) != "" {
			return nil, biz.Invalid("assignee_user_id is set when the seat is accepted")
		}
		if role.Status != "" && role.Status != model.RoleOpen {
			return nil, biz.Invalid("role status is assigned by the server")
		}
		templates = append(templates, template{id: role.Id, name: name, qty: qty, share: role.ShareBps})
	}
	if sum != 10000 {
		return nil, biz.Invalid("collaborative role share_bps must sum to 10000")
	}
	roles := make([]model.Role, 0)
	seenSeat := map[string]struct{}{}
	for _, tmpl := range templates {
		parts := splitTemplateShare(tmpl.share, tmpl.qty)
		for n, share := range parts {
			seatID := tmpl.id
			if tmpl.qty > 1 {
				seatID = fmt.Sprintf("%s_%d", tmpl.id, n+1)
			}
			if !id.ValidRefID(seatID) {
				return nil, biz.Invalid("role id is too long to expand into seat ids")
			}
			if _, ok := seenSeat[seatID]; ok {
				return nil, biz.Invalid("expanded seat ids must be unique")
			}
			seenSeat[seatID] = struct{}{}
			roles = append(roles, model.Role{
				ID:         seatID,
				Name:       tmpl.name,
				ShareBps:   share,
				TemplateID: tmpl.id,
				Status:     model.RoleOpen,
			})
		}
	}
	if len(roles) == 0 || len(roles) > maxCollaborativeSeats {
		return nil, biz.Invalid("collaborative tasks expand to at most 20 seats")
	}
	return &model.Collaborative{Roles: roles, OnDropout: rule}, nil
}

func roleQuantity(q *int64) (int, error) {
	if q == nil {
		return 1, nil
	}
	if *q < 1 || *q > maxCollaborativeSeats {
		return 0, biz.Invalid("quantity must be between 1 and 20")
	}
	return int(*q), nil
}

// splitTemplateShare divides a template total across quantity seats.
// The last (total mod quantity) seats each receive one extra basis point.
func splitTemplateShare(total int64, quantity int) []int64 {
	base := total / int64(quantity)
	remainder := total % int64(quantity)
	firstFlat := int64(quantity) - remainder
	out := make([]int64, quantity)
	for i := 0; i < quantity; i++ {
		out[i] = base
		if int64(i) >= firstFlat {
			out[i] = base + 1
		}
	}
	return out
}

func validateWebhookURL(raw string) error {
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return biz.Invalid("url must be an absolute http or https URL")
	}
	if len(raw) > 2048 {
		return biz.Invalid("url is too long")
	}
	return nil
}

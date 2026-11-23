package cell

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"
)

// Store implements store.Repos. Each accessor returns a routing repository;
// the actual cell is resolved per call from the request context.
func (s *Store) Users() store.UserRepo { return &routingUserRepo{s} }
func (s *Store) Tasks() store.TaskRepo { return &routingTaskRepo{s} }
func (s *Store) Submissions() store.SubmissionRepo {
	return &routingSubmissionRepo{s}
}
func (s *Store) Escrow() store.EscrowRepo       { return &routingEscrowRepo{s} }
func (s *Store) Webhooks() store.WebhookRepo    { return &routingWebhookRepo{s} }
func (s *Store) Deliveries() store.DeliveryRepo { return &routingDeliveryRepo{s} }
func (s *Store) Chats() store.ChatRepo          { return &routingChatRepo{s} }
func (s *Store) Invites() store.InviteRepo      { return &routingInviteRepo{s} }
func (s *Store) Friends() store.FriendRepo      { return &routingFriendRepo{s} }
func (s *Store) Idem() store.IdemRepo           { return &routingIdemRepo{s} }

// byKey resolves a read to the cell that owns key. cellForEntity checks the
// request-level cache first and falls back to searching every cell.
func (s *Store) byKey(ctx context.Context, key, kind string) int {
	i := s.loc.CellFor(ctx, key, kind)
	if i < 0 || i >= len(s.cells) {
		return 0
	}
	return i
}

// byRequest resolves a write to the cell pinned for this request.
func (s *Store) byRequest(ctx context.Context) int { return s.pick(ctx) }

// ---------------------------------------------------------------- users ----

type routingUserRepo struct{ s *Store }

func (r *routingUserRepo) Ensure(ctx context.Context, id string, now time.Time) (model.User, error) {
	i := r.s.byKey(ctx, id, KindUser)
	u, err := r.s.cells[i].Users().Ensure(ctx, id, now)
	if err == nil {
		r.s.loc.Remember(id, i)
	}
	return u, err
}

func (r *routingUserRepo) Get(ctx context.Context, id string) (model.User, error) {
	return r.s.cells[r.s.byKey(ctx, id, KindUser)].Users().Get(ctx, id)
}

// InsertRegistered has no authenticated user yet, so the fresh user id itself
// is the routing key. That is what spreads registrations evenly over cells.
func (r *routingUserRepo) InsertRegistered(ctx context.Context, id, email, passwordHash string, now time.Time) (model.User, error) {
	i := r.s.router.CellFor(id)
	r.s.loc.Remember(id, i)
	return r.s.cells[i].Users().InsertRegistered(ctx, id, email, passwordHash, now)
}

// GetByEmail is a login lookup: the email column is unique inside each cell,
// so all cells are searched and the first match wins.
func (r *routingUserRepo) GetByEmail(ctx context.Context, email string) (model.User, error) {
	var firstErr error
	for _, c := range r.s.cells {
		u, err := c.Users().GetByEmail(ctx, email)
		if err == nil {
			return u, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return model.User{}, firstErr
	}
	return model.User{}, store.ErrNotFound
}

func (r *routingUserRepo) SetNickname(ctx context.Context, id, nickname string) error {
	return r.s.cells[r.s.byKey(ctx, id, KindUser)].Users().SetNickname(ctx, id, nickname)
}

func (r *routingUserRepo) SetAvatar(ctx context.Context, id string, jpeg []byte, updatedAt string) error {
	return r.s.cells[r.s.byKey(ctx, id, KindUser)].Users().SetAvatar(ctx, id, jpeg, updatedAt)
}

func (r *routingUserRepo) GetAvatar(ctx context.Context, id string) (model.UserAvatar, error) {
	return r.s.cells[r.s.byKey(ctx, id, KindUser)].Users().GetAvatar(ctx, id)
}

func (r *routingUserRepo) AddBalance(ctx context.Context, id string, delta int64) error {
	return r.s.cells[r.s.byKey(ctx, id, KindUser)].Users().AddBalance(ctx, id, delta)
}

func (r *routingUserRepo) ApplyScore(ctx context.Context, id string, deltaIntegrity, deltaAbility int64) error {
	return r.s.cells[r.s.byKey(ctx, id, KindUser)].Users().ApplyScore(ctx, id, deltaIntegrity, deltaAbility)
}

func (r *routingUserRepo) AddScoreEvent(ctx context.Context, ev model.ScoreEvent) error {
	return r.s.cells[r.s.byKey(ctx, ev.UserID, KindUser)].Users().AddScoreEvent(ctx, ev)
}

func (r *routingUserRepo) ListScoreEvents(ctx context.Context, userID string, limit int) ([]model.ScoreEvent, error) {
	return r.s.cells[r.s.byKey(ctx, userID, KindUser)].Users().ListScoreEvents(ctx, userID, limit)
}

// ---------------------------------------------------------------- tasks ----

type routingTaskRepo struct{ s *Store }

func (r *routingTaskRepo) Insert(ctx context.Context, t model.Task) error {
	i := r.s.byRequest(ctx)
	if err := r.s.cells[i].Tasks().Insert(ctx, t); err != nil {
		return err
	}
	r.s.loc.Remember(t.ID, i)
	return nil
}

func (r *routingTaskRepo) Update(ctx context.Context, t model.Task) error {
	return r.s.cells[r.s.byKey(ctx, t.ID, KindTask)].Tasks().Update(ctx, t)
}

func (r *routingTaskRepo) Get(ctx context.Context, id string) (model.Task, error) {
	return r.s.cells[r.s.byKey(ctx, id, KindTask)].Tasks().Get(ctx, id)
}

func (r *routingTaskRepo) Lock(ctx context.Context, id string) (model.Task, error) {
	return r.s.cells[r.s.byKey(ctx, id, KindTask)].Tasks().Lock(ctx, id)
}

func (r *routingTaskRepo) TryLock(ctx context.Context, id string) (model.Task, error) {
	return r.s.cells[r.s.byKey(ctx, id, KindTask)].Tasks().TryLock(ctx, id)
}

// ClaimDueReview belongs to a background sweep that pins one cell at a time.
// When called unpinned we still search every cell so no due row is missed.
func (r *routingTaskRepo) ClaimDueReview(ctx context.Context, now time.Time, exclude []string) (string, error) {
	if _, pinned := CellFrom(ctx); pinned {
		return r.s.cells[r.s.byRequest(ctx)].Tasks().ClaimDueReview(ctx, now, exclude)
	}
	for _, c := range r.s.cells {
		id, err := c.Tasks().ClaimDueReview(ctx, now, exclude)
		if err != nil || id != "" {
			return id, err
		}
	}
	return "", nil
}

// ListSweep is used by the timeout sweeper inside a pinned transaction.
func (r *routingTaskRepo) ListSweep(ctx context.Context, afterCreated, afterID string, limit int) ([]model.Task, error) {
	if _, pinned := CellFrom(ctx); pinned {
		return r.s.cells[r.s.byRequest(ctx)].Tasks().ListSweep(ctx, afterCreated, afterID, limit)
	}
	var out []model.Task
	for _, c := range r.s.cells {
		part, err := c.Tasks().ListSweep(ctx, afterCreated, afterID, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	return out, nil
}

// List is the public market feed: every cell holds a slice of the tasks, so
// the pages are merged and re-sorted globally before pagination is applied.
func (r *routingTaskRepo) List(ctx context.Context, f model.TaskFilter) ([]model.Task, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	// Ask each cell for enough rows to cover a global page.
	per := f
	per.Limit = limit + offset
	per.Offset = 0
	var all []model.Task
	for _, c := range r.s.cells {
		part, err := c.Tasks().List(ctx, per)
		if err != nil {
			return nil, err
		}
		all = append(all, part...)
	}
	sortTasksGlobally(all, f)
	if offset >= len(all) {
		return []model.Task{}, nil
	}
	all = all[offset:]
	if limit < len(all) {
		all = all[:limit]
	}
	return all, nil
}

func (r *routingTaskRepo) Count(ctx context.Context, f model.TaskFilter) (int64, error) {
	var total int64
	for _, c := range r.s.cells {
		n, err := c.Tasks().Count(ctx, f)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

func sortTasksGlobally(tasks []model.Task, f model.TaskFilter) {
	less := func(a, b model.Task) bool {
		switch f.Sort {
		case "amount_asc":
			if a.Payment.Amount != b.Payment.Amount {
				return a.Payment.Amount < b.Payment.Amount
			}
		case "amount_desc":
			if a.Payment.Amount != b.Payment.Amount {
				return a.Payment.Amount > b.Payment.Amount
			}
		case "deadline_asc":
			if !a.Acceptance.Deadline.Equal(b.Acceptance.Deadline) {
				return a.Acceptance.Deadline.Before(b.Acceptance.Deadline)
			}
		case "distance_asc":
			if f.Latitude != nil && f.Longitude != nil && a.Attributes.Latitude != nil && a.Attributes.Longitude != nil && b.Attributes.Latitude != nil && b.Attributes.Longitude != nil {
				da := haversine(*f.Latitude, *f.Longitude, *a.Attributes.Latitude, *a.Attributes.Longitude)
				db := haversine(*f.Latitude, *f.Longitude, *b.Attributes.Latitude, *b.Attributes.Longitude)
				if da != db {
					return da < db
				}
			} else if f.Latitude != nil && f.Longitude != nil {
				// Tasks without coordinates sort last.
				if (a.Attributes.Latitude == nil) != (b.Attributes.Latitude == nil) {
					return a.Attributes.Latitude != nil
				}
			}
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID > b.ID
	}
	sort.SliceStable(tasks, func(i, j int) bool { return less(tasks[i], tasks[j]) })
}

func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dlat := rad(lat2 - lat1)
	dlon := rad(lon2 - lon1)
	a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dlon/2)*math.Sin(dlon/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// ------------------------------------------------------- submissions ----

type routingSubmissionRepo struct{ s *Store }

func (r *routingSubmissionRepo) Insert(ctx context.Context, sub model.Submission) error {
	return r.s.cells[r.s.byKey(ctx, sub.TaskID, KindTask)].Submissions().Insert(ctx, sub)
}

func (r *routingSubmissionRepo) MarkStatus(ctx context.Context, id, status string) error {
	return r.s.cells[r.s.byRequest(ctx)].Submissions().MarkStatus(ctx, id, status)
}

func (r *routingSubmissionRepo) ListByTask(ctx context.Context, taskID string) ([]model.Submission, error) {
	return r.s.cells[r.s.byKey(ctx, taskID, KindTask)].Submissions().ListByTask(ctx, taskID)
}

// ------------------------------------------------------------ escrow ----

type routingEscrowRepo struct{ s *Store }

func (r *routingEscrowRepo) Insert(ctx context.Context, a model.EscrowAccount) error {
	return r.s.cells[r.s.byKey(ctx, a.TaskID, KindTask)].Escrow().Insert(ctx, a)
}

func (r *routingEscrowRepo) Update(ctx context.Context, a model.EscrowAccount) error {
	return r.s.cells[r.s.byKey(ctx, a.TaskID, KindTask)].Escrow().Update(ctx, a)
}

func (r *routingEscrowRepo) InsertEntry(ctx context.Context, e model.EscrowEntry) error {
	return r.s.cells[r.s.byRequest(ctx)].Escrow().InsertEntry(ctx, e)
}

func (r *routingEscrowRepo) GetByTask(ctx context.Context, taskID string) (model.EscrowAccount, error) {
	return r.s.cells[r.s.byKey(ctx, taskID, KindTask)].Escrow().GetByTask(ctx, taskID)
}

func (r *routingEscrowRepo) LockByTask(ctx context.Context, taskID string) (model.EscrowAccount, error) {
	return r.s.cells[r.s.byKey(ctx, taskID, KindTask)].Escrow().LockByTask(ctx, taskID)
}

// ----------------------------------------------------------- webhook ----

type routingWebhookRepo struct{ s *Store }

func (r *routingWebhookRepo) Insert(ctx context.Context, w model.Webhook) error {
	return r.s.cells[r.s.byKey(ctx, w.OwnerID, KindUser)].Webhooks().Insert(ctx, w)
}

// ListByOwners is used when an event is emitted. Owners may live in different
// cells, so every cell is queried and the subscriptions are merged.
func (r *routingWebhookRepo) ListByOwners(ctx context.Context, ownerIDs []string) ([]model.Webhook, error) {
	var out []model.Webhook
	for _, c := range r.s.cells {
		part, err := c.Webhooks().ListByOwners(ctx, ownerIDs)
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	return out, nil
}

// ---------------------------------------------------------- delivery ----

type routingDeliveryRepo struct{ s *Store }

func (r *routingDeliveryRepo) Insert(ctx context.Context, d model.Delivery) error {
	return r.s.cells[r.s.byRequest(ctx)].Deliveries().Insert(ctx, d)
}

func (r *routingDeliveryRepo) ListPending(ctx context.Context, maxAttempts, limit int) ([]model.Delivery, error) {
	var out []model.Delivery
	for _, c := range r.s.cells {
		part, err := c.Deliveries().ListPending(ctx, maxAttempts, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
		if len(out) >= limit && limit > 0 {
			out = out[:limit]
			break
		}
	}
	return out, nil
}

// ClaimPending claims in every cell. Each cell commits its own claim, so two
// workers can never deliver the same row twice.
func (r *routingDeliveryRepo) ClaimPending(ctx context.Context, maxAttempts, limit int, now, until time.Time) ([]model.Delivery, error) {
	var out []model.Delivery
	for _, c := range r.s.cells {
		part, err := c.Deliveries().ClaimPending(ctx, maxAttempts, limit, now, until)
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Finish has only a delivery id, so it is broadcast; the owning cell updates
// the row and the others are no-ops.
func (r *routingDeliveryRepo) Finish(ctx context.Context, id, claimToken, status, lastError string) error {
	var first error
	for _, c := range r.s.cells {
		err := c.Deliveries().Finish(ctx, id, claimToken, status, lastError)
		if err != nil && !errors.Is(err, store.ErrNotFound) && first == nil {
			first = err
		}
	}
	return first
}

package domain

import (
	"context"
	"math"
	"sort"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/plane"
	"taskgame/internal/store"
)

// ----------------------------------------------------------------- tasks ----

type taskRepo struct{ s *Store }

func (r *taskRepo) Insert(ctx context.Context, t model.Task) error {
	p, err := r.s.createPlacement(ctx, plane.DomainTask, t.ID)
	if err != nil {
		return err
	}
	if _, err := r.s.control.Resolver.Place(ctx, plane.DomainTask, t.ID, p.Shard); err != nil {
		return err
	}
	repos, err := r.s.pickPlacement(ctx, p)
	if err != nil {
		return err
	}
	return repos.Tasks().Insert(ctx, t)
}

func (r *taskRepo) Update(ctx context.Context, t model.Task) error {
	repos, err := r.s.taskRepos(ctx, t.ID, false)
	if err != nil {
		return err
	}
	return repos.Tasks().Update(ctx, t)
}

func (r *taskRepo) Get(ctx context.Context, id string) (model.Task, error) {
	repos, err := r.s.taskRepos(ctx, id, false)
	if err != nil {
		return model.Task{}, err
	}
	return repos.Tasks().Get(ctx, id)
}

func (r *taskRepo) Lock(ctx context.Context, id string) (model.Task, error) {
	repos, err := r.s.taskRepos(ctx, id, false)
	if err != nil {
		return model.Task{}, err
	}
	return repos.Tasks().Lock(ctx, id)
}

func (r *taskRepo) TryLock(ctx context.Context, id string) (model.Task, error) {
	repos, err := r.s.taskRepos(ctx, id, false)
	if err != nil {
		return model.Task{}, err
	}
	return repos.Tasks().TryLock(ctx, id)
}

func (r *taskRepo) ClaimDueReview(ctx context.Context, now time.Time, exclude []string) (string, error) {
	candidates, err := r.s.byRouteOrAll(ctx, plane.DomainTask, "")
	if err != nil {
		return "", err
	}
	for _, repos := range candidates {
		id, err := repos.Tasks().ClaimDueReview(ctx, now, exclude)
		if err != nil || id != "" {
			return id, err
		}
	}
	return "", nil
}

func (r *taskRepo) ListSweep(ctx context.Context, afterCreated, afterID string, limit int) ([]model.Task, error) {
	candidates, err := r.s.byRouteOrAll(ctx, plane.DomainTask, "")
	if err != nil {
		return nil, err
	}
	var out []model.Task
	for _, repos := range candidates {
		part, err := repos.Tasks().ListSweep(ctx, afterCreated, afterID, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	return out, nil
}

// List and Count fan out to the task domain only. In the cell prototype they
// had to scan three full stores; here a physical message split does not add
// any work to the market feed.
func (r *taskRepo) List(ctx context.Context, f model.TaskFilter) ([]model.Task, error) {
	limit, offset := f.Limit, f.Offset
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	per := f
	per.Limit, per.Offset = limit+offset, 0
	var all []model.Task
	if err := r.s.allRepos(ctx, plane.DomainTask, func(repos store.Repos) error {
		part, err := repos.Tasks().List(ctx, per)
		if err != nil {
			return err
		}
		all = append(all, part...)
		return nil
	}); err != nil {
		return nil, err
	}
	sortTasks(all, f)
	if offset >= len(all) {
		return []model.Task{}, nil
	}
	all = all[offset:]
	if limit < len(all) {
		all = all[:limit]
	}
	return all, nil
}

func (r *taskRepo) Count(ctx context.Context, f model.TaskFilter) (int64, error) {
	var total int64
	if err := r.s.allRepos(ctx, plane.DomainTask, func(repos store.Repos) error {
		n, err := repos.Tasks().Count(ctx, f)
		if err != nil {
			return err
		}
		total += n
		return nil
	}); err != nil {
		return 0, err
	}
	return total, nil
}

func sortTasks(tasks []model.Task, f model.TaskFilter) {
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
				da := distance(*f.Latitude, *f.Longitude, *a.Attributes.Latitude, *a.Attributes.Longitude)
				db := distance(*f.Latitude, *f.Longitude, *b.Attributes.Latitude, *b.Attributes.Longitude)
				if da != db {
					return da < db
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

func distance(lat1, lon1, lat2, lon2 float64) float64 {
	const earth = 6371.0
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dlat, dlon := rad(lat2-lat1), rad(lon2-lon1)
	a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dlon/2)*math.Sin(dlon/2)
	return 2 * earth * math.Asin(math.Sqrt(a))
}

// ----------------------------------------------------------- submissions ----

type submissionRepo struct{ s *Store }

func (r *submissionRepo) Insert(ctx context.Context, sub model.Submission) error {
	repos, err := r.s.taskRepos(ctx, sub.TaskID, false)
	if err != nil {
		return err
	}
	return repos.Submissions().Insert(ctx, sub)
}

func (r *submissionRepo) ListByTask(ctx context.Context, taskID string) ([]model.Submission, error) {
	repos, err := r.s.taskRepos(ctx, taskID, false)
	if err != nil {
		return nil, err
	}
	return repos.Submissions().ListByTask(ctx, taskID)
}

func (r *submissionRepo) MarkStatus(ctx context.Context, id, status string) error {
	return writeAcross(r.s, ctx, plane.DomainTask, func(repos store.Repos) error {
		return repos.Submissions().MarkStatus(ctx, id, status)
	})
}

// ---------------------------------------------------------------- escrow ----

type escrowRepo struct{ s *Store }

func (r *escrowRepo) Insert(ctx context.Context, a model.EscrowAccount) error {
	repos, err := r.s.taskRepos(ctx, a.TaskID, false)
	if err != nil {
		return err
	}
	return repos.Escrow().Insert(ctx, a)
}

func (r *escrowRepo) Update(ctx context.Context, a model.EscrowAccount) error {
	repos, err := r.s.taskRepos(ctx, a.TaskID, false)
	if err != nil {
		return err
	}
	return repos.Escrow().Update(ctx, a)
}

func (r *escrowRepo) GetByTask(ctx context.Context, taskID string) (model.EscrowAccount, error) {
	repos, err := r.s.taskRepos(ctx, taskID, false)
	if err != nil {
		return model.EscrowAccount{}, err
	}
	return repos.Escrow().GetByTask(ctx, taskID)
}

func (r *escrowRepo) LockByTask(ctx context.Context, taskID string) (model.EscrowAccount, error) {
	repos, err := r.s.taskRepos(ctx, taskID, false)
	if err != nil {
		return model.EscrowAccount{}, err
	}
	return repos.Escrow().LockByTask(ctx, taskID)
}

func (r *escrowRepo) InsertEntry(ctx context.Context, e model.EscrowEntry) error {
	return writeAcross(r.s, ctx, plane.DomainTask, func(repos store.Repos) error {
		return repos.Escrow().InsertEntry(ctx, e)
	})
}

// -------------------------------------------------------------- webhooks ----

type webhookRepo struct{ s *Store }

func (r *webhookRepo) Insert(ctx context.Context, w model.Webhook) error {
	repos, err := r.s.taskRepos(ctx, w.OwnerID, false)
	if err != nil {
		return err
	}
	return repos.Webhooks().Insert(ctx, w)
}

func (r *webhookRepo) ListByOwners(ctx context.Context, ownerIDs []string) ([]model.Webhook, error) {
	var out []model.Webhook
	if err := r.s.allRepos(ctx, plane.DomainTask, func(repos store.Repos) error {
		part, err := repos.Webhooks().ListByOwners(ctx, ownerIDs)
		if err != nil {
			return err
		}
		out = append(out, part...)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// ------------------------------------------------------------- deliveries ----

type deliveryRepo struct{ s *Store }

func (r *deliveryRepo) Insert(ctx context.Context, d model.Delivery) error {
	repos, err := r.s.taskRepos(ctx, d.ID, false)
	if err != nil {
		return err
	}
	return repos.Deliveries().Insert(ctx, d)
}

func (r *deliveryRepo) ListPending(ctx context.Context, maxAttempts, limit int) ([]model.Delivery, error) {
	var out []model.Delivery
	err := r.s.allRepos(ctx, plane.DomainTask, func(repos store.Repos) error {
		part, err := repos.Deliveries().ListPending(ctx, maxAttempts, limit)
		if err != nil {
			return err
		}
		out = append(out, part...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *deliveryRepo) ClaimPending(ctx context.Context, maxAttempts, limit int, now, until time.Time) ([]model.Delivery, error) {
	var out []model.Delivery
	err := r.s.allRepos(ctx, plane.DomainTask, func(repos store.Repos) error {
		part, err := repos.Deliveries().ClaimPending(ctx, maxAttempts, limit, now, until)
		if err != nil {
			return err
		}
		out = append(out, part...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *deliveryRepo) Finish(ctx context.Context, id, claimToken, status, lastError string) error {
	return writeAcross(r.s, ctx, plane.DomainTask, func(repos store.Repos) error {
		return repos.Deliveries().Finish(ctx, id, claimToken, status, lastError)
	})
}

// ---------------------------------------------------------------- invites ----

type inviteRepo struct{ s *Store }

func (r *inviteRepo) Upsert(ctx context.Context, inv model.Invite) error {
	repos, err := r.s.taskRepos(ctx, inv.TaskID, false)
	if err != nil {
		return err
	}
	return repos.Invites().Upsert(ctx, inv)
}

func (r *inviteRepo) Update(ctx context.Context, inv model.Invite) error {
	repos, err := r.s.taskRepos(ctx, inv.TaskID, false)
	if err != nil {
		return err
	}
	return repos.Invites().Update(ctx, inv)
}

func (r *inviteRepo) Get(ctx context.Context, id string) (model.Invite, error) {
	return findAcross(r.s, ctx, plane.DomainTask, func(repos store.Repos) (model.Invite, error) {
		return repos.Invites().Get(ctx, id)
	})
}

func (r *inviteRepo) Find(ctx context.Context, taskID, userID string) (model.Invite, error) {
	repos, err := r.s.taskRepos(ctx, taskID, false)
	if err != nil {
		return model.Invite{}, err
	}
	return repos.Invites().Find(ctx, taskID, userID)
}

func (r *inviteRepo) ListByTask(ctx context.Context, taskID string) ([]model.Invite, error) {
	repos, err := r.s.taskRepos(ctx, taskID, false)
	if err != nil {
		return nil, err
	}
	return repos.Invites().ListByTask(ctx, taskID)
}

func (r *inviteRepo) ListByUser(ctx context.Context, userID, status string) ([]model.Invite, error) {
	var out []model.Invite
	if err := r.s.allRepos(ctx, plane.DomainTask, func(repos store.Repos) error {
		part, err := repos.Invites().ListByUser(ctx, userID, status)
		if err != nil {
			return err
		}
		out = append(out, part...)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *inviteRepo) DeleteByRole(ctx context.Context, taskID, roleID string) error {
	return writeAcross(r.s, ctx, plane.DomainTask, func(repos store.Repos) error {
		return repos.Invites().DeleteByRole(ctx, taskID, roleID)
	})
}

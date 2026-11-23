package cell

import (
	"context"
	"errors"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"
)

// overlayChatRepo makes a direct-chat lookup visible across cells inside a
// transaction. Without it, two users in different cells would each create a
// separate "direct" chat for the same pair. Writes stay in the transaction's
// cell; the router pins later message requests to that cell.
type overlayChatRepo struct {
	s     *Store
	inner store.ChatRepo
	cell  int
}

func (r *overlayChatRepo) others() []store.ChatRepo {
	out := make([]store.ChatRepo, 0, len(r.s.cells)-1)
	for i, c := range r.s.cells {
		if i != r.cell {
			out = append(out, c.Chats())
		}
	}
	return out
}

func chatFirstAcross[T any](inner store.ChatRepo, others []store.ChatRepo, fn func(store.ChatRepo) (T, error)) (T, error) {
	v, err := fn(inner)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return v, err
	}
	for _, o := range others {
		if v, gerr := fn(o); gerr == nil {
			return v, nil
		}
	}
	var zero T
	return zero, store.ErrNotFound
}

func (r *overlayChatRepo) InsertChat(ctx context.Context, c model.Chat) error {
	return r.inner.InsertChat(ctx, c)
}
func (r *overlayChatRepo) UpdateChat(ctx context.Context, c model.Chat) error {
	return r.inner.UpdateChat(ctx, c)
}
func (r *overlayChatRepo) GetChat(ctx context.Context, id string) (model.Chat, error) {
	return chatFirstAcross(r.inner, r.others(), func(cr store.ChatRepo) (model.Chat, error) {
		return cr.GetChat(ctx, id)
	})
}
func (r *overlayChatRepo) FindDirect(ctx context.Context, a, b string) (model.Chat, error) {
	return chatFirstAcross(r.inner, r.others(), func(cr store.ChatRepo) (model.Chat, error) {
		return cr.FindDirect(ctx, a, b)
	})
}
func (r *overlayChatRepo) ListByTask(ctx context.Context, taskID string) ([]model.Chat, error) {
	return r.inner.ListByTask(ctx, taskID)
}
func (r *overlayChatRepo) InsertMessage(ctx context.Context, m model.Message) error {
	return r.inner.InsertMessage(ctx, m)
}
func (r *overlayChatRepo) ListDirectUnreads(ctx context.Context, userID string) ([]model.DirectUnread, error) {
	return r.inner.ListDirectUnreads(ctx, userID)
}
func (r *overlayChatRepo) MessageRowID(ctx context.Context, chatID, messageID string) (int64, error) {
	return r.inner.MessageRowID(ctx, chatID, messageID)
}
func (r *overlayChatRepo) LatestMessageRow(ctx context.Context, chatID string) (int64, string, bool, error) {
	return r.inner.LatestMessageRow(ctx, chatID)
}
func (r *overlayChatRepo) CountUnread(ctx context.Context, userID, chatID string) (int64, error) {
	return r.inner.CountUnread(ctx, userID, chatID)
}
func (r *overlayChatRepo) AdvanceRead(ctx context.Context, userID, chatID string, rowid int64, now time.Time) error {
	return r.inner.AdvanceRead(ctx, userID, chatID, rowid, now)
}
func (r *overlayChatRepo) ListMessages(ctx context.Context, chatID string, limit, offset int) ([]model.Message, error) {
	return r.inner.ListMessages(ctx, chatID, limit, offset)
}

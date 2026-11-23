package domain

import (
	"context"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/plane"
	"taskgame/internal/store"
)

type chatRepo struct{ s *Store }

// InsertChat pins a new chat twice: the ChatKey (ordered user pair for direct
// chats) keeps both members agreeing on the shard, while the chat id is also
// recorded so later id-only calls (ListMessages, AdvanceRead, ...) can resolve
// it from the directory instead of searching every message shard.
func (r *chatRepo) InsertChat(ctx context.Context, c model.Chat) error {
	p, err := r.s.placeChat(ctx, c, true)
	if err != nil {
		return err
	}
	if _, err := r.s.control.Resolver.Place(ctx, plane.DomainMessage, c.ID, p.Shard); err != nil {
		return err
	}
	repos, err := r.s.pickPlacement(ctx, p)
	if err != nil {
		return err
	}
	return repos.Chats().InsertChat(ctx, c)
}

func (r *chatRepo) UpdateChat(ctx context.Context, c model.Chat) error {
	repos, err := r.s.chatRepos(ctx, c.ID, false)
	if err != nil {
		return err
	}
	return repos.Chats().UpdateChat(ctx, c)
}

func (r *chatRepo) GetChat(ctx context.Context, id string) (model.Chat, error) {
	repos, err := r.s.chatRepos(ctx, id, false)
	if err != nil {
		return model.Chat{}, err
	}
	return repos.Chats().GetChat(ctx, id)
}

func (r *chatRepo) FindDirect(ctx context.Context, a, b string) (model.Chat, error) {
	return findAcross(r.s, ctx, plane.DomainMessage, func(repos store.Repos) (model.Chat, error) {
		return repos.Chats().FindDirect(ctx, a, b)
	})
}

func (r *chatRepo) ListByTask(ctx context.Context, taskID string) ([]model.Chat, error) {
	var out []model.Chat
	if err := r.s.allRepos(ctx, plane.DomainMessage, func(repos store.Repos) error {
		part, err := repos.Chats().ListByTask(ctx, taskID)
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

func (r *chatRepo) InsertMessage(ctx context.Context, m model.Message) error {
	repos, err := r.s.chatRepos(ctx, m.ChatID, false)
	if err != nil {
		return err
	}
	return repos.Chats().InsertMessage(ctx, m)
}

func (r *chatRepo) ListMessages(ctx context.Context, chatID string, limit, offset int) ([]model.Message, error) {
	repos, err := r.s.chatRepos(ctx, chatID, false)
	if err != nil {
		return nil, err
	}
	return repos.Chats().ListMessages(ctx, chatID, limit, offset)
}

func (r *chatRepo) ListDirectUnreads(ctx context.Context, userID string) ([]model.DirectUnread, error) {
	var out []model.DirectUnread
	if err := r.s.allRepos(ctx, plane.DomainMessage, func(repos store.Repos) error {
		part, err := repos.Chats().ListDirectUnreads(ctx, userID)
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

func (r *chatRepo) MessageRowID(ctx context.Context, chatID, messageID string) (int64, error) {
	repos, err := r.s.chatRepos(ctx, chatID, false)
	if err != nil {
		return 0, err
	}
	return repos.Chats().MessageRowID(ctx, chatID, messageID)
}

func (r *chatRepo) LatestMessageRow(ctx context.Context, chatID string) (int64, string, bool, error) {
	repos, err := r.s.chatRepos(ctx, chatID, false)
	if err != nil {
		return 0, "", false, err
	}
	return repos.Chats().LatestMessageRow(ctx, chatID)
}

func (r *chatRepo) CountUnread(ctx context.Context, userID, chatID string) (int64, error) {
	repos, err := r.s.chatRepos(ctx, chatID, false)
	if err != nil {
		return 0, err
	}
	return repos.Chats().CountUnread(ctx, userID, chatID)
}

func (r *chatRepo) AdvanceRead(ctx context.Context, userID, chatID string, rowid int64, now time.Time) error {
	repos, err := r.s.chatRepos(ctx, chatID, false)
	if err != nil {
		return err
	}
	return repos.Chats().AdvanceRead(ctx, userID, chatID, rowid, now)
}

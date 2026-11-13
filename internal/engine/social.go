package engine

import (
	"context"
	"errors"
	"net/http"

	"taskgame/internal/biz"
	"taskgame/internal/id"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"
)

// CreateWebhook registers an endpoint owned by the caller.
func (e *Engine) CreateWebhook(ctx context.Context, userID string, req *types.CreateWebhookReq) (*types.Webhook, error) {
	return call(e, ctx, userID, http.MethodPost, "/v1/webhooks", req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Webhook, error) {
		rawURL := trim(req.Url)
		if err := validateWebhookURL(rawURL); err != nil {
			return nil, err
		}
		if len(req.Secret) < 16 || len(req.Secret) > 256 {
			return nil, biz.Invalid("secret must be between 16 and 256 characters")
		}
		if len(req.Events) == 0 || len(req.Events) > len(model.WebhookEventTypes) {
			return nil, biz.Invalid("events must list at least one known event")
		}
		events := make([]string, 0, len(req.Events))
		seen := map[string]struct{}{}
		for _, ev := range req.Events {
			ev = trim(ev)
			if _, ok := model.WebhookEventTypes[ev]; !ok {
				return nil, biz.Invalid("unknown webhook event " + ev)
			}
			if _, ok := seen[ev]; ok {
				continue
			}
			seen[ev] = struct{}{}
			events = append(events, ev)
		}
		now := e.now()
		w := model.Webhook{
			ID: id.New("whk_"), OwnerID: userID, URL: rawURL, Secret: req.Secret, Events: events, CreatedAt: now,
		}
		if err := r.Webhooks().Insert(ctx, w); err != nil {
			return nil, err
		}
		return &types.Webhook{
			Url: w.URL, Secret: w.Secret, Events: append([]string{}, w.Events...), Id: w.ID, CreatedAt: model.FormatTime(w.CreatedAt),
		}, nil
	})
}

// CreateChat opens a chat.
//
// type=direct with user_id returns the one direct thread for that friend pair.
// The first call inserts it (and emits chat.created). A later call returns the
// same row. The two users must be friends at call time. Unfriend does not
// remove members, so an existing thread stays readable and writable.
//
// Omit type, or pass type=group, to keep the free-form member_ids chat. Each
// of those calls inserts a new row. Optional task_id still reuses the task chat.
func (e *Engine) CreateChat(ctx context.Context, userID string, req *types.CreateChatReq) (*types.Chat, error) {
	return call(e, ctx, userID, http.MethodPost, "/v1/chats", req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Chat, error) {
		switch trim(req.Type) {
		case model.ChatDirect:
			return e.createDirectChat(ctx, r, userID, req)
		case "", model.ChatGroup:
			return e.createGroupChat(ctx, r, userID, req)
		default:
			return nil, biz.Invalid("type must be direct or group")
		}
	})
}

func (e *Engine) createDirectChat(ctx context.Context, r store.Repos, userID string, req *types.CreateChatReq) (*types.Chat, error) {
	if len(req.MemberIds) > 0 {
		return nil, biz.Invalid("member_ids is not used for a direct chat")
	}
	if trim(req.TaskId) != "" {
		return nil, biz.Invalid("task_id is not used for a direct chat")
	}
	target := trim(req.UserId)
	if !id.ValidUserID(target) {
		return nil, biz.Invalid("user_id is invalid")
	}
	if target == userID {
		return nil, biz.Invalid("cannot open a direct chat with yourself")
	}
	if _, err := r.Friends().GetFriendship(ctx, userID, target); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, biz.Forbidden("only friends can open a direct chat")
		}
		return nil, err
	}
	existing, err := r.Chats().FindDirect(ctx, userID, target)
	if err == nil {
		return toChat(existing), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	low, high := model.OrderedPair(userID, target)
	now := e.now()
	c := model.Chat{
		ID: id.New("cht_"), Type: model.ChatDirect,
		PairLow: low, PairHigh: high, MemberIDs: []string{low, high}, CreatedAt: now,
	}
	if err := r.Chats().InsertChat(ctx, c); err != nil {
		if errors.Is(err, store.ErrConflict) {
			again, ferr := r.Chats().FindDirect(ctx, userID, target)
			if ferr != nil {
				return nil, ferr
			}
			return toChat(again), nil
		}
		return nil, err
	}
	out := toChat(c)
	if err := e.emit(ctx, r, c.MemberIDs, model.EventChatCreated, map[string]any{"chat": out}); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *Engine) createGroupChat(ctx context.Context, r store.Repos, userID string, req *types.CreateChatReq) (*types.Chat, error) {
	if len(req.MemberIds) == 0 {
		return nil, biz.Invalid("member_ids is required")
	}
	if trim(req.UserId) != "" {
		return nil, biz.Invalid("user_id is only used for a direct chat")
	}
	members := make([]string, 0, len(req.MemberIds)+1)
	for _, m := range req.MemberIds {
		m = trim(m)
		if !id.ValidUserID(m) {
			return nil, biz.Invalid("member_ids contains an invalid user id")
		}
		members = append(members, m)
	}
	members = dedupe(append(members, userID))
	if len(members) > 50 {
		return nil, biz.Invalid("too many members")
	}
	taskID := trim(req.TaskId)
	if taskID != "" {
		t, err := loadTask(ctx, r, taskID)
		if err != nil {
			return nil, err
		}
		if userID != t.PublisherID && !contains(t.ExecutorIDs, userID) {
			return nil, biz.Forbidden("only the publisher or an executor can bind a task chat")
		}
		existing, err := r.Chats().ListByTask(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		if len(existing) > 0 {
			c := existing[0]
			if c.Type == "" || c.Type == model.ChatGroup {
				c.Type = model.ChatTask
			}
			c.MemberIDs = dedupe(append(c.MemberIDs, members...))
			if err := r.Chats().UpdateChat(ctx, c); err != nil {
				return nil, err
			}
			if t.ChatID == "" {
				t.ChatID = c.ID
				t.UpdatedAt = e.now()
				if err := r.Tasks().Update(ctx, t); err != nil {
					return nil, err
				}
			}
			return toChat(c), nil
		}
		now := e.now()
		c := model.Chat{ID: id.New("cht_"), TaskID: t.ID, Type: model.ChatTask, MemberIDs: members, CreatedAt: now}
		if err := r.Chats().InsertChat(ctx, c); err != nil {
			return nil, err
		}
		if t.ChatID == "" {
			t.ChatID = c.ID
			t.UpdatedAt = now
			if err := r.Tasks().Update(ctx, t); err != nil {
				return nil, err
			}
		}
		return toChat(c), nil
	}
	now := e.now()
	c := model.Chat{ID: id.New("cht_"), Type: model.ChatGroup, MemberIDs: members, CreatedAt: now}
	if err := r.Chats().InsertChat(ctx, c); err != nil {
		return nil, err
	}
	return toChat(c), nil
}

// bindTaskChat returns the single chat bound to a task, creating it if needed, and adds userID.
func (e *Engine) bindTaskChat(ctx context.Context, r store.Repos, t *model.Task, userID string) (model.Chat, error) {
	existing, err := r.Chats().ListByTask(ctx, t.ID)
	if err != nil {
		return model.Chat{}, err
	}
	if len(existing) == 0 && t.ChatID != "" {
		c, err := r.Chats().GetChat(ctx, t.ChatID)
		if err == nil {
			existing = []model.Chat{c}
		} else if !errors.Is(err, store.ErrNotFound) {
			return model.Chat{}, err
		}
	}
	if len(existing) > 0 {
		c := existing[0]
		if c.TaskID == "" {
			c.TaskID = t.ID
		}
		if c.Type == "" || c.Type == model.ChatGroup {
			c.Type = model.ChatTask
		}
		if !contains(c.MemberIDs, userID) {
			c.MemberIDs = append(c.MemberIDs, userID)
		}
		if err := r.Chats().UpdateChat(ctx, c); err != nil {
			return model.Chat{}, err
		}
		return c, nil
	}
	now := e.now()
	c := model.Chat{ID: id.New("cht_"), TaskID: t.ID, Type: model.ChatTask, MemberIDs: []string{userID}, CreatedAt: now}
	if err := r.Chats().InsertChat(ctx, c); err != nil {
		return model.Chat{}, err
	}
	return c, nil
}

// GetChat returns chat metadata for a member.
func (e *Engine) GetChat(ctx context.Context, userID, chatID string) (*types.Chat, error) {
	if !id.ValidChatID(chatID) {
		return nil, biz.Invalid("chatId must match ^cht_")
	}
	chat, err := e.store.Chats().GetChat(ctx, chatID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, biz.NotFound("chat not found")
	}
	if err != nil {
		return nil, err
	}
	if !contains(chat.MemberIDs, userID) {
		return nil, biz.Forbidden("only members can read this chat")
	}
	return toChat(chat), nil
}

// ListChats returns the task chat when the caller is a member or the publisher.
func (e *Engine) ListChats(ctx context.Context, userID, taskID string) (*types.ChatList, error) {
	taskID = trim(taskID)
	if taskID == "" {
		return nil, biz.Invalid("task_id is required")
	}
	t, err := loadTask(ctx, e.store, taskID)
	if err != nil {
		return nil, err
	}
	rows, err := e.store.Chats().ListByTask(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	if userID != t.PublisherID {
		visible := make([]model.Chat, 0, len(rows))
		for _, row := range rows {
			if contains(row.MemberIDs, userID) {
				visible = append(visible, row)
			}
		}
		if len(visible) == 0 && len(rows) > 0 {
			return nil, biz.Forbidden("only members can read this chat")
		}
		rows = visible
	}
	items := make([]types.Chat, 0, len(rows))
	for _, row := range rows {
		items = append(items, *toChat(row))
	}
	return &types.ChatList{Items: items}, nil
}

// ListMessages returns one page of history, oldest first. Only members can read.
func (e *Engine) ListMessages(ctx context.Context, userID string, req *types.ListMessagesReq) (*types.MessageList, error) {
	if !id.ValidChatID(req.ChatId) {
		return nil, biz.Invalid("chatId must match ^cht_")
	}
	if req.Limit < 0 || req.Limit > 100 {
		return nil, biz.Invalid("limit must be between 1 and 100")
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = 50
	}
	offset, err := decodeCursor(req.Cursor)
	if err != nil {
		return nil, err
	}
	chat, err := e.store.Chats().GetChat(ctx, req.ChatId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, biz.NotFound("chat not found")
	}
	if err != nil {
		return nil, err
	}
	if !contains(chat.MemberIDs, userID) {
		return nil, biz.Forbidden("only members can read this chat")
	}
	rows, err := e.store.Chats().ListMessages(ctx, chat.ID, limit+1, offset)
	if err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeCursor(offset + limit)
	}
	items := make([]types.Message, 0, len(rows))
	for _, row := range rows {
		items = append(items, *toMessage(row))
	}
	return &types.MessageList{Items: items, NextCursor: next}, nil
}

// PostMessage appends a text or attachment_ref message from a member.
func (e *Engine) PostMessage(ctx context.Context, userID string, req *types.PostMessageReq) (*types.Message, error) {
	return call(e, ctx, userID, http.MethodPost, "/v1/chats/"+req.ChatId+"/messages", req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Message, error) {
		if !id.ValidChatID(req.ChatId) {
			return nil, biz.Invalid("chatId must match ^cht_")
		}
		chat, err := r.Chats().GetChat(ctx, req.ChatId)
		if errors.Is(err, store.ErrNotFound) {
			return nil, biz.NotFound("chat not found")
		}
		if err != nil {
			return nil, err
		}
		if !contains(chat.MemberIDs, userID) {
			return nil, biz.Forbidden("only members can post")
		}
		text := trim(req.Text)
		ref := trim(req.AttachmentRef)
		switch req.Type {
		case "text":
			if text == "" {
				return nil, biz.Invalid("text is required")
			}
			if ref != "" {
				return nil, biz.Invalid("attachment_ref is only valid for attachment_ref messages")
			}
		case "attachment_ref":
			if ref == "" {
				return nil, biz.Invalid("attachment_ref is required")
			}
		default:
			return nil, biz.Invalid("type must be text or attachment_ref")
		}
		if len(text) > 8000 || len(ref) > 500 {
			return nil, biz.Invalid("message is too long")
		}
		now := e.now()
		msg := model.Message{
			ID: id.New("msg_"), ChatID: chat.ID, SenderID: userID, Type: req.Type,
			Text: text, AttachmentRef: ref, CreatedAt: now,
		}
		if err := r.Chats().InsertMessage(ctx, msg); err != nil {
			return nil, err
		}
		out := toMessage(msg)
		if err := e.emit(ctx, r, chat.MemberIDs, model.EventChatMessageCreated, map[string]any{"message": out}); err != nil {
			return nil, err
		}
		return out, nil
	})
}

// GetUserScores returns integrity and ability. Unknown users are zeros, not an error.
func (e *Engine) GetUserScores(ctx context.Context, userID string) (*types.UserScores, error) {
	if !id.ValidUserID(userID) {
		return nil, biz.Invalid("userId is invalid")
	}
	u, err := e.store.Users().Get(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return &types.UserScores{UserId: userID, Recent: []types.ScoreChange{}}, nil
	}
	if err != nil {
		return nil, err
	}
	evs, err := e.store.Users().ListScoreEvents(ctx, userID, scoreRecentLimit)
	if err != nil {
		return nil, err
	}
	recent := make([]types.ScoreChange, 0, len(evs))
	for _, ev := range evs {
		recent = append(recent, types.ScoreChange{
			DeltaIntegrity: ev.DeltaIntegrity,
			DeltaAbility:   ev.DeltaAbility,
			Reason:         ev.Reason,
			TaskId:         ev.TaskID,
			CreatedAt:      model.FormatTime(ev.CreatedAt),
		})
	}
	return &types.UserScores{
		UserId: u.ID, Integrity: u.Integrity, Ability: u.Ability, Recent: recent,
	}, nil
}

func toChat(c model.Chat) *types.Chat {
	kind := c.Type
	if kind == "" {
		if c.TaskID != "" {
			kind = model.ChatTask
		} else {
			kind = model.ChatGroup
		}
	}
	out := &types.Chat{
		Id:        c.ID,
		Type:      kind,
		MemberIds: append([]string{}, c.MemberIDs...),
	}
	if c.TaskID != "" {
		taskID := c.TaskID
		out.TaskId = &taskID
	}
	return out
}

func toMessage(m model.Message) *types.Message {
	return &types.Message{
		Type:          m.Type,
		Text:          m.Text,
		AttachmentRef: m.AttachmentRef,
		Id:            m.ID,
		ChatId:        m.ChatID,
		SenderId:      m.SenderID,
		CreatedAt:     model.FormatTime(m.CreatedAt),
	}
}

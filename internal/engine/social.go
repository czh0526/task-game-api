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

// CreateChat opens a free-form chat. task_id stays null in v0.1.
func (e *Engine) CreateChat(ctx context.Context, userID string, req *types.CreateChatReq) (*types.Chat, error) {
	return call(e, ctx, userID, http.MethodPost, "/v1/chats", req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Chat, error) {
		if len(req.MemberIds) == 0 {
			return nil, biz.Invalid("member_ids is required")
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
		now := e.now()
		c := model.Chat{ID: id.New("cht_"), MemberIDs: members, CreatedAt: now}
		if err := r.Chats().InsertChat(ctx, c); err != nil {
			return nil, err
		}
		return toChat(c), nil
	})
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
	return &types.Chat{
		Id:        c.ID,
		MemberIds: append([]string{}, c.MemberIDs...),
	}
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

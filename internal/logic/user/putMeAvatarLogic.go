package user

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"taskgame/internal/auth"
	"taskgame/internal/avatar"
	"taskgame/internal/biz"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type PutMeAvatarLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewPutMeAvatarLogic(ctx context.Context, svcCtx *svc.ServiceContext) *PutMeAvatarLogic {
	return &PutMeAvatarLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// PutMeAvatar stores the caller's picture as a normalized 720x720 JPEG.
func (l *PutMeAvatarLogic) PutMeAvatar(req *types.PutMeAvatarReq) (*types.AvatarSummary, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, biz.Invalid("image is required")
	}
	raw, err := decodeAvatarImage(req.Image)
	if err != nil {
		return nil, biz.Invalid(err.Error())
	}
	jpeg, err := avatar.Normalize(raw)
	if err != nil {
		return nil, biz.Invalid(err.Error())
	}
	now := model.FormatTime(time.Now())
	if err := l.svcCtx.Store.Users().SetAvatar(l.ctx, userID, jpeg, now); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, biz.NotFound("user not found")
		}
		return nil, err
	}
	return &types.AvatarSummary{
		UserId:    userID,
		AvatarUrl: "/v1/users/" + userID + "/avatar",
		Width:     avatar.Size,
		Height:    avatar.Size,
		UpdatedAt: now,
	}, nil
}

// decodeAvatarImage accepts either a raw base64 string or a data URL such as
// "data:image/jpeg;base64,/9j/...". Whitespace is ignored.
func decodeAvatarImage(value string) ([]byte, error) {
	s := strings.TrimSpace(value)
	if s == "" {
		return nil, errors.New("image is required")
	}
	if strings.HasPrefix(s, "data:") {
		comma := strings.IndexByte(s, ',')
		if comma < 0 || !strings.Contains(s[:comma], ";base64") {
			return nil, errors.New("data URL must be base64 encoded")
		}
		s = s[comma+1:]
	}
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		}
		return r
	}, s)
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("image must be a valid base64 string")
	}
	return data, nil
}

package user

import (
	"context"
	"errors"

	"taskgame/internal/biz"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetUserAvatarLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetUserAvatarLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetUserAvatarLogic {
	return &GetUserAvatarLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// GetUserAvatar is public: any visitor may read a stored avatar image.
func (l *GetUserAvatarLogic) GetUserAvatar(req *types.GetUserAvatarReq) (*model.UserAvatar, error) {
	if req == nil || req.UserId == "" {
		return nil, biz.Invalid("userId is required")
	}
	found, err := l.svcCtx.Store.Users().GetAvatar(l.ctx, req.UserId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, biz.NotFound("avatar not found")
	}
	if err != nil {
		return nil, err
	}
	return &found, nil
}

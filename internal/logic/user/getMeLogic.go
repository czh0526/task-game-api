package user

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetMeLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetMeLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetMeLogic {
	return &GetMeLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *GetMeLogic) GetMe() (*types.UserSummary, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	profile, err := l.svcCtx.Accounts.Current(l.ctx, userID)
	if err != nil {
		return nil, err
	}
	return &types.UserSummary{
		Id:        profile.ID,
		Email:     profile.Email,
		Nickname:  profile.Nickname,
		IsArbiter: l.svcCtx.Engine.IsArbiter(userID),
	}, nil
}

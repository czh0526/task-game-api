package user

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type UpdateMeLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewUpdateMeLogic(ctx context.Context, svcCtx *svc.ServiceContext) *UpdateMeLogic {
	return &UpdateMeLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *UpdateMeLogic) UpdateMe(req *types.PatchMeReq) (*types.UserSummary, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		req = &types.PatchMeReq{}
	}
	profile, err := l.svcCtx.Accounts.UpdateNickname(l.ctx, userID, req.Nickname)
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

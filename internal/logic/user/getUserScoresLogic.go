package user

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetUserScoresLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetUserScoresLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetUserScoresLogic {
	return &GetUserScoresLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *GetUserScoresLogic) GetUserScores(req *types.GetUserScoresReq) (*types.UserScores, error) {
	if _, err := auth.Require(l.ctx); err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.GetUserScores(l.ctx, req.UserId)
}

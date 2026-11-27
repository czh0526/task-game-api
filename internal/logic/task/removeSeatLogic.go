package task

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type RemoveSeatLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewRemoveSeatLogic(ctx context.Context, svcCtx *svc.ServiceContext) *RemoveSeatLogic {
	return &RemoveSeatLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *RemoveSeatLogic) RemoveSeat(req *types.RemoveSeatReq) (*types.Task, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.RemoveSeat(l.ctx, userID, req)
}

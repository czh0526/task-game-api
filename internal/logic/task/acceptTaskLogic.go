package task

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type AcceptTaskLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewAcceptTaskLogic(ctx context.Context, svcCtx *svc.ServiceContext) *AcceptTaskLogic {
	return &AcceptTaskLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *AcceptTaskLogic) AcceptTask(req *types.AcceptTaskReq) (*types.Task, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.AcceptTask(l.ctx, userID, req)
}

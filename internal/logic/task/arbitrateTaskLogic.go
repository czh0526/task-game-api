package task

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type ArbitrateTaskLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewArbitrateTaskLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ArbitrateTaskLogic {
	return &ArbitrateTaskLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *ArbitrateTaskLogic) ArbitrateTask(req *types.ArbitrateTaskReq) (*types.Task, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.ArbitrateTask(l.ctx, userID, req)
}

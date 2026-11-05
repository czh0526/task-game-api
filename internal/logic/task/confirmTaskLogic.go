package task

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type ConfirmTaskLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewConfirmTaskLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ConfirmTaskLogic {
	return &ConfirmTaskLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *ConfirmTaskLogic) ConfirmTask(req *types.ConfirmTaskReq) (*types.Task, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.ConfirmTask(l.ctx, userID, req)
}

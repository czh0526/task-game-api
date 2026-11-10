package task

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type StartTaskLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewStartTaskLogic(ctx context.Context, svcCtx *svc.ServiceContext) *StartTaskLogic {
	return &StartTaskLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *StartTaskLogic) StartTask(req *types.StartTaskReq) (*types.Task, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.StartTask(l.ctx, userID, req)
}

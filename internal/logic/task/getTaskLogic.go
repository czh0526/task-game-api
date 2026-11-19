package task

import (
	"context"

	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetTaskLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetTaskLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetTaskLogic {
	return &GetTaskLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *GetTaskLogic) GetTask(req *types.GetTaskReq) (*types.Task, error) {
	// Task detail is public market data: anonymous visitors can read it before
	// sign-in. Credentials are still validated by the middleware when supplied;
	// other task routes keep requiring auth.
	return l.svcCtx.Engine.GetTask(l.ctx, req.TaskId)
}

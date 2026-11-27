package task

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type SetAssigneesLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewSetAssigneesLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SetAssigneesLogic {
	return &SetAssigneesLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *SetAssigneesLogic) SetAssignees(req *types.SetAssigneesReq) (*types.Task, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.SetAssignees(l.ctx, userID, req)
}

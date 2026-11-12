package task

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type InviteTaskLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewInviteTaskLogic(ctx context.Context, svcCtx *svc.ServiceContext) *InviteTaskLogic {
	return &InviteTaskLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *InviteTaskLogic) InviteTask(req *types.InviteUsersReq) (*types.InviteList, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.InviteTask(l.ctx, userID, req)
}

type ListTaskInvitesLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewListTaskInvitesLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ListTaskInvitesLogic {
	return &ListTaskInvitesLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *ListTaskInvitesLogic) ListTaskInvites(req *types.ListTaskInvitesReq) (*types.InviteList, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.ListTaskInvites(l.ctx, userID, req.TaskId)
}

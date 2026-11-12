package user

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type ListMyInvitesLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewListMyInvitesLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ListMyInvitesLogic {
	return &ListMyInvitesLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *ListMyInvitesLogic) ListMyInvites(req *types.ListMyInvitesReq) (*types.InviteList, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.ListMyInvites(l.ctx, userID, req.Status)
}

type DeclineInviteLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewDeclineInviteLogic(ctx context.Context, svcCtx *svc.ServiceContext) *DeclineInviteLogic {
	return &DeclineInviteLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *DeclineInviteLogic) DeclineInvite(req *types.DeclineInviteReq) (*types.Invite, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.DeclineInvite(l.ctx, userID, req)
}

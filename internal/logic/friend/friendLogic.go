package friend

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type ListFriendsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewListFriendsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ListFriendsLogic {
	return &ListFriendsLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *ListFriendsLogic) ListFriends() (*types.FriendList, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.ListFriends(l.ctx, userID)
}

type CreateFriendRequestLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewCreateFriendRequestLogic(ctx context.Context, svcCtx *svc.ServiceContext) *CreateFriendRequestLogic {
	return &CreateFriendRequestLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *CreateFriendRequestLogic) CreateFriendRequest(req *types.CreateFriendRequestReq) (*types.FriendRequest, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.CreateFriendRequest(l.ctx, userID, req)
}

type UnfriendLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewUnfriendLogic(ctx context.Context, svcCtx *svc.ServiceContext) *UnfriendLogic {
	return &UnfriendLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *UnfriendLogic) Unfriend(req *types.UnfriendReq) (*types.RemovedFriend, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.Unfriend(l.ctx, userID, req)
}

type SetFriendGroupLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewSetFriendGroupLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SetFriendGroupLogic {
	return &SetFriendGroupLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *SetFriendGroupLogic) SetFriendGroup(req *types.SetFriendGroupReq) (*types.FriendPlacement, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.SetFriendGroup(l.ctx, userID, req)
}

type ListFriendRequestsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewListFriendRequestsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ListFriendRequestsLogic {
	return &ListFriendRequestsLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *ListFriendRequestsLogic) ListFriendRequests(req *types.ListFriendRequestsReq) (*types.FriendRequestList, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.ListFriendRequests(l.ctx, userID, req.Direction)
}

type AcceptFriendRequestLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewAcceptFriendRequestLogic(ctx context.Context, svcCtx *svc.ServiceContext) *AcceptFriendRequestLogic {
	return &AcceptFriendRequestLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *AcceptFriendRequestLogic) AcceptFriendRequest(req *types.FriendRequestActionReq) (*types.FriendRequest, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.AcceptFriendRequest(l.ctx, userID, req)
}

type DeclineFriendRequestLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewDeclineFriendRequestLogic(ctx context.Context, svcCtx *svc.ServiceContext) *DeclineFriendRequestLogic {
	return &DeclineFriendRequestLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *DeclineFriendRequestLogic) DeclineFriendRequest(req *types.FriendRequestActionReq) (*types.FriendRequest, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.DeclineFriendRequest(l.ctx, userID, req)
}

type CancelFriendRequestLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewCancelFriendRequestLogic(ctx context.Context, svcCtx *svc.ServiceContext) *CancelFriendRequestLogic {
	return &CancelFriendRequestLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *CancelFriendRequestLogic) CancelFriendRequest(req *types.FriendRequestActionReq) (*types.FriendRequest, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.CancelFriendRequest(l.ctx, userID, req)
}

type ListFriendGroupsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewListFriendGroupsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ListFriendGroupsLogic {
	return &ListFriendGroupsLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *ListFriendGroupsLogic) ListFriendGroups() (*types.FriendGroupList, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.ListFriendGroups(l.ctx, userID)
}

type CreateFriendGroupLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewCreateFriendGroupLogic(ctx context.Context, svcCtx *svc.ServiceContext) *CreateFriendGroupLogic {
	return &CreateFriendGroupLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *CreateFriendGroupLogic) CreateFriendGroup(req *types.CreateFriendGroupReq) (*types.FriendGroup, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.CreateFriendGroup(l.ctx, userID, req)
}

type UpdateFriendGroupLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewUpdateFriendGroupLogic(ctx context.Context, svcCtx *svc.ServiceContext) *UpdateFriendGroupLogic {
	return &UpdateFriendGroupLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *UpdateFriendGroupLogic) UpdateFriendGroup(req *types.PatchFriendGroupReq) (*types.FriendGroup, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.UpdateFriendGroup(l.ctx, userID, req)
}

type DeleteFriendGroupLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewDeleteFriendGroupLogic(ctx context.Context, svcCtx *svc.ServiceContext) *DeleteFriendGroupLogic {
	return &DeleteFriendGroupLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *DeleteFriendGroupLogic) DeleteFriendGroup(req *types.DeleteFriendGroupReq) (*types.FriendGroup, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.DeleteFriendGroup(l.ctx, userID, req)
}

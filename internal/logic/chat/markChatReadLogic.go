package chat

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type MarkChatReadLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewMarkChatReadLogic(ctx context.Context, svcCtx *svc.ServiceContext) *MarkChatReadLogic {
	return &MarkChatReadLogic{Logger: logx.WithContext(ctx), ctx: ctx, svcCtx: svcCtx}
}

func (l *MarkChatReadLogic) MarkChatRead(req *types.MarkChatReadReq) (*types.ChatRead, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.MarkChatRead(l.ctx, userID, req)
}

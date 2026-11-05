package webhook

import (
	"context"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type CreateWebhookLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewCreateWebhookLogic(ctx context.Context, svcCtx *svc.ServiceContext) *CreateWebhookLogic {
	return &CreateWebhookLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *CreateWebhookLogic) CreateWebhook(req *types.CreateWebhookReq) (*types.Webhook, error) {
	userID, err := auth.Require(l.ctx)
	if err != nil {
		return nil, err
	}
	return l.svcCtx.Engine.CreateWebhook(l.ctx, userID, req)
}

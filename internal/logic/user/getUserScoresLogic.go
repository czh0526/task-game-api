package user

import (
	"context"

	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetUserScoresLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetUserScoresLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetUserScoresLogic {
	return &GetUserScoresLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *GetUserScoresLogic) GetUserScores(req *types.GetUserScoresReq) (*types.UserScores, error) {
	// Public profile data: anonymous visitors can read a user's nickname and
	// scores so the web UI can show a profile card before sign-in. Credentials
	// are still validated by the middleware when supplied.
	return l.svcCtx.Engine.GetUserScores(l.ctx, req.UserId)
}

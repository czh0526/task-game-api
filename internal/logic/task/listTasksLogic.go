package task

import (
	"context"
	"strings"

	"taskgame/internal/auth"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type ListTasksLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewListTasksLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ListTasksLogic {
	return &ListTasksLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *ListTasksLogic) ListTasks(req *types.ListTasksReq) (*types.TaskList, error) {
	// The market listing is public; only the personal "assignable=me" view
	// needs a caller id.
	userID, _ := auth.UserID(l.ctx)
	if strings.TrimSpace(req.Assignable) == "me" {
		uid, err := auth.Require(l.ctx)
		if err != nil {
			return nil, err
		}
		userID = uid
	}
	return l.svcCtx.Engine.ListTasks(l.ctx, userID, req)
}

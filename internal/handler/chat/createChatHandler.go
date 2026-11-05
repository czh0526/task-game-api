package chat

import (
	"net/http"

	"taskgame/internal/biz"
	"taskgame/internal/logic/chat"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/rest/httpx"
)

func CreateChatHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.CreateChatReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}

		l := chat.NewCreateChatLogic(r.Context(), svcCtx)
		resp, err := l.CreateChat(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
		} else {
			httpx.WriteJsonCtx(r.Context(), w, http.StatusCreated, resp)
		}
	}
}

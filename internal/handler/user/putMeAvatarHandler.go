package user

import (
	"net/http"

	"taskgame/internal/biz"
	"taskgame/internal/logic/user"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/rest/httpx"
)

func PutMeAvatarHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.PutMeAvatarReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}

		l := user.NewPutMeAvatarLogic(r.Context(), svcCtx)
		resp, err := l.PutMeAvatar(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
		} else {
			httpx.OkJsonCtx(r.Context(), w, resp)
		}
	}
}

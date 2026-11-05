package webhook

import (
	"net/http"

	"taskgame/internal/biz"
	"taskgame/internal/logic/webhook"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/rest/httpx"
)

func CreateWebhookHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.CreateWebhookReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}

		l := webhook.NewCreateWebhookLogic(r.Context(), svcCtx)
		resp, err := l.CreateWebhook(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
		} else {
			httpx.WriteJsonCtx(r.Context(), w, http.StatusCreated, resp)
		}
	}
}

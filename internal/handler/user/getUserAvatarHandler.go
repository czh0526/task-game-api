package user

import (
	"net/http"
	"strconv"

	"taskgame/internal/biz"
	"taskgame/internal/logic/user"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/rest/httpx"
)

// GetUserAvatarHandler writes raw 720x720 JPEG bytes instead of JSON. The
// ETag is the upload timestamp, so a changed avatar is never served stale.
func GetUserAvatarHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.GetUserAvatarReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}

		l := user.NewGetUserAvatarLogic(r.Context(), svcCtx)
		img, err := l.GetUserAvatar(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}

		etag := `"` + img.UpdatedAt + `"`
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(img.Data)))
		_, _ = w.Write(img.Data)
	}
}

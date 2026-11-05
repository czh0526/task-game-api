package task

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"taskgame/internal/biz"
	"taskgame/internal/logic/task"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/rest/httpx"
)

// SubmitTaskHandler decodes the body with encoding/json.
// go-zero's mapper cannot fill map[string]interface{} (submission payload).
func SubmitTaskHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.SubmitTaskReq
		if err := httpx.ParsePath(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}
		if err := httpx.ParseHeaders(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}
		if err := decodeSubmitBody(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}

		l := task.NewSubmitTaskLogic(r.Context(), svcCtx)
		resp, err := l.SubmitTask(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func decodeSubmitBody(r *http.Request, req *types.SubmitTaskReq) error {
	ct := r.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(ct), "application/json") {
		return biz.Invalid("content type must be application/json")
	}
	if r.Body == nil {
		return biz.Invalid("request body is required")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20+1))
	if err != nil {
		return biz.Invalid("invalid json body")
	}
	if len(raw) == 0 {
		return biz.Invalid("request body is required")
	}
	if len(raw) > 8<<20 {
		return biz.Invalid("request body is too large")
	}
	if err := json.Unmarshal(raw, req); err != nil {
		return biz.Invalid("invalid json body")
	}
	return nil
}

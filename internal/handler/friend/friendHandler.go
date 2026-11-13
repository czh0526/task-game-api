package friend

import (
	"net/http"

	"taskgame/internal/biz"
	logic "taskgame/internal/logic/friend"
	"taskgame/internal/svc"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/rest/httpx"
)

func ListFriendsHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := logic.NewListFriendsLogic(r.Context(), svcCtx)
		resp, err := l.ListFriends()
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func CreateFriendRequestHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.CreateFriendRequestReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}
		l := logic.NewCreateFriendRequestLogic(r.Context(), svcCtx)
		resp, err := l.CreateFriendRequest(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		// A new pending request is 201. Accepting the other side's pending
		// request does not create a row, so that path is 200.
		status := http.StatusCreated
		if resp.Status == "accepted" {
			status = http.StatusOK
		}
		httpx.WriteJsonCtx(r.Context(), w, status, resp)
	}
}

func UnfriendHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.UnfriendReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}
		l := logic.NewUnfriendLogic(r.Context(), svcCtx)
		resp, err := l.Unfriend(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func SetFriendGroupHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.SetFriendGroupReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}
		l := logic.NewSetFriendGroupLogic(r.Context(), svcCtx)
		resp, err := l.SetFriendGroup(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func ListFriendRequestsHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.ListFriendRequestsReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}
		l := logic.NewListFriendRequestsLogic(r.Context(), svcCtx)
		resp, err := l.ListFriendRequests(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func AcceptFriendRequestHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.FriendRequestActionReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}
		l := logic.NewAcceptFriendRequestLogic(r.Context(), svcCtx)
		resp, err := l.AcceptFriendRequest(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func DeclineFriendRequestHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.FriendRequestActionReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}
		l := logic.NewDeclineFriendRequestLogic(r.Context(), svcCtx)
		resp, err := l.DeclineFriendRequest(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func CancelFriendRequestHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.FriendRequestActionReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}
		l := logic.NewCancelFriendRequestLogic(r.Context(), svcCtx)
		resp, err := l.CancelFriendRequest(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func ListFriendGroupsHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := logic.NewListFriendGroupsLogic(r.Context(), svcCtx)
		resp, err := l.ListFriendGroups()
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func CreateFriendGroupHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.CreateFriendGroupReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}
		l := logic.NewCreateFriendGroupLogic(r.Context(), svcCtx)
		resp, err := l.CreateFriendGroup(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.WriteJsonCtx(r.Context(), w, http.StatusCreated, resp)
	}
}

func UpdateFriendGroupHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.PatchFriendGroupReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}
		l := logic.NewUpdateFriendGroupLogic(r.Context(), svcCtx)
		resp, err := l.UpdateFriendGroup(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

func DeleteFriendGroupHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.DeleteFriendGroupReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, biz.Invalid(err.Error()))
			return
		}
		l := logic.NewDeleteFriendGroupLogic(r.Context(), svcCtx)
		resp, err := l.DeleteFriendGroup(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

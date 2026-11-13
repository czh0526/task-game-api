package session

import (
	"taskgame/internal/account"
	"taskgame/internal/types"
)

func toAuthSession(s account.Session) *types.AuthSession {
	return &types.AuthSession{
		AccessToken: s.AccessToken,
		ExpiresIn:   s.ExpiresIn,
		User: types.UserSummary{
			Id:       s.UserID,
			Email:    s.Email,
			Nickname: s.Nickname,
		},
	}
}

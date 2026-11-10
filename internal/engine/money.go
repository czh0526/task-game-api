package engine

import (
	"fmt"

	"taskgame/internal/model"
)

// payout is one executor's share of a release.
type payout struct {
	UserID string
	Amount int64
}

// seatPayoutAmount is one seat's share of the original pool, using the same
// rounding as splitBps over every seat. Dust goes to the largest share, so
// paying or refunding seats one by one still sums to the pool.
func seatPayoutAmount(pool int64, roles []model.Role, seatID string) (int64, error) {
	if len(roles) == 0 {
		return 0, fmt.Errorf("no seats")
	}
	synthetic := make([]model.Role, len(roles))
	for i, role := range roles {
		synthetic[i] = role
		if synthetic[i].AssigneeUserID == "" {
			synthetic[i].AssigneeUserID = "seat:" + role.ID
		}
	}
	pays, refund, err := splitBps(pool, synthetic)
	if err != nil {
		return 0, err
	}
	if refund != 0 {
		return 0, fmt.Errorf("seat split refund %d", refund)
	}
	for i, role := range roles {
		if role.ID == seatID {
			return pays[i].Amount, nil
		}
	}
	return 0, fmt.Errorf("unknown seat %s", seatID)
}

// splitBps divides pool by role share_bps (parts of 10000 of the pool).
// Dropped or open seats are not passed in. Their portion is returned as refund.
// Integer dust goes to the filled seat with the largest share.
func splitBps(pool int64, filled []model.Role) ([]payout, int64, error) {
	if pool < 0 {
		return nil, 0, fmt.Errorf("negative pool")
	}
	if len(filled) == 0 {
		return nil, pool, nil
	}
	var active int64
	maxI := 0
	for i, role := range filled {
		if role.ShareBps < 0 {
			return nil, 0, fmt.Errorf("negative share")
		}
		active += role.ShareBps
		if role.ShareBps > filled[maxI].ShareBps {
			maxI = i
		}
	}
	if active > 10000 {
		return nil, 0, fmt.Errorf("share_bps exceed 10000")
	}
	pays := make([]payout, len(filled))
	var floorSum int64
	for i, role := range filled {
		amt := pool * role.ShareBps / 10000
		pays[i] = payout{UserID: role.AssigneeUserID, Amount: amt}
		floorSum += amt
	}
	refund := pool * (10000 - active) / 10000
	rounding := pool - floorSum - refund
	if rounding < 0 {
		return nil, 0, fmt.Errorf("split rounding %d", rounding)
	}
	pays[maxI].Amount += rounding
	return pays, refund, nil
}

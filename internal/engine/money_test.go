package engine

import (
	"testing"

	"taskgame/internal/model"
)

func TestSplitBps(t *testing.T) {
	t.Parallel()
	pays, refund, err := splitBps(100, []model.Role{
		{AssigneeUserID: "a", ShareBps: 6000},
		{AssigneeUserID: "b", ShareBps: 4000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if refund != 0 || pays[0].Amount != 60 || pays[1].Amount != 40 {
		t.Fatalf("pays=%+v refund=%d", pays, refund)
	}

	pays, refund, err = splitBps(100, []model.Role{
		{AssigneeUserID: "a", ShareBps: 3334},
		{AssigneeUserID: "b", ShareBps: 3333},
		{AssigneeUserID: "c", ShareBps: 3333},
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := refund
	for _, p := range pays {
		sum += p.Amount
	}
	if sum != 100 || pays[0].Amount != 34 {
		t.Fatalf("dust not assigned to largest share: %+v refund=%d", pays, refund)
	}

	pays, refund, err = splitBps(10, []model.Role{{AssigneeUserID: "a", ShareBps: 2500}})
	if err != nil {
		t.Fatal(err)
	}
	if pays[0].Amount+refund != 10 || refund != 7 {
		t.Fatalf("dropped share not refunded: %+v refund=%d", pays, refund)
	}
}

func TestSeatPayoutAmountSumsToPool(t *testing.T) {
	t.Parallel()
	roles := []model.Role{
		{ID: "writer_1", ShareBps: 3333},
		{ID: "writer_2", ShareBps: 3333},
		{ID: "writer_3", ShareBps: 3334},
	}
	var sum int64
	for _, role := range roles {
		amt, err := seatPayoutAmount(100, roles, role.ID)
		if err != nil {
			t.Fatal(err)
		}
		sum += amt
	}
	if sum != 100 {
		t.Fatalf("sum %d", sum)
	}
	largest, err := seatPayoutAmount(100, roles, "writer_3")
	if err != nil || largest != 34 {
		t.Fatalf("largest %d err %v", largest, err)
	}
}

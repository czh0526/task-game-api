package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/model"
)

func f64(v float64) *float64 { return &v }

// seedFilterTasks inserts a few tasks around Beijing with known coordinates.
func seedFilterTasks(t *testing.T, st *DB) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	tasks := []model.Task{
		{
			ID: "tsk_beijing", Kind: model.KindExclusive, Title: "Beijing task", Description: "d",
			Status: model.StatusOpen, PublisherID: "usr_publisher",
			Keywords:   []string{"design", "urgent"},
			Attributes: model.TaskAttributes{Location: "Beijing", Latitude: f64(39.9042), Longitude: f64(116.4074), Urgency: model.UrgencyWithin3Days, Recurrence: model.RecurrenceOneTime, ExecutorRequirements: []string{"human", "experienced"}},
			Payment:    model.Payment{Mode: model.PayOneShot, Amount: 500},
			CreatedAt:  now, UpdatedAt: now,
		},
		{
			ID: "tsk_shanghai", Kind: model.KindCompetitive, Title: "Shanghai task", Description: "d",
			Status: model.StatusOpen, PublisherID: "usr_publisher",
			Keywords:   []string{"design"},
			Attributes: model.TaskAttributes{Latitude: f64(31.2304), Longitude: f64(121.4737), Urgency: model.UrgencyWithinWeek, ExecutorRequirements: []string{"agent"}},
			Payment:    model.Payment{Mode: model.PayOneShot, Amount: 800},
			CreatedAt:  now.Add(time.Minute), UpdatedAt: now,
		},
		{
			ID: "tsk_remote", Kind: model.KindExclusive, Title: "Remote task", Description: "d",
			Status: model.StatusOpen, PublisherID: "usr_publisher",
			Attributes: model.TaskAttributes{Recurrence: model.RecurrenceWeekly},
			Payment:    model.Payment{Mode: model.PayOneShot, Amount: 200},
			CreatedAt:  now.Add(2 * time.Minute), UpdatedAt: now,
		},
	}
	for _, task := range tasks {
		if err := st.Tasks().Insert(ctx, task); err != nil {
			t.Fatalf("insert %s: %v", task.ID, err)
		}
	}
}

func TestTaskListFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "filter.db")
	st, err := Open("file:"+path+"?mode=rwc", 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seedFilterTasks(t, st)
	ctx := context.Background()

	ids := func(f model.TaskFilter) []string {
		f.Limit = 100
		rows, err := st.Tasks().List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}

	if got := ids(model.TaskFilter{}); len(got) != 3 {
		t.Fatalf("all tasks: %v", got)
	}

	if got := ids(model.TaskFilter{Keyword: "design"}); len(got) != 2 {
		t.Fatalf("keyword design: %v", got)
	}
	if got := ids(model.TaskFilter{Urgency: model.UrgencyWithin3Days}); len(got) != 1 || got[0] != "tsk_beijing" {
		t.Fatalf("urgency: %v", got)
	}
	if got := ids(model.TaskFilter{ExecutorRequirement: "agent"}); len(got) != 1 || got[0] != "tsk_shanghai" {
		t.Fatalf("executor requirement: %v", got)
	}
	if got := ids(model.TaskFilter{MinAmount: 500, MaxAmount: 800}); len(got) != 2 {
		t.Fatalf("amount range: %v", got)
	}
	if got := ids(model.TaskFilter{Sort: "amount_asc"}); len(got) != 3 || got[0] != "tsk_remote" {
		t.Fatalf("amount asc: %v", got)
	}

	// Beijing center: only the Beijing task within 50 km.
	near := ids(model.TaskFilter{Latitude: f64(39.9042), Longitude: f64(116.4074), RadiusKm: 50, Sort: "distance_asc"})
	if len(near) != 1 || near[0] != "tsk_beijing" {
		t.Fatalf("distance 50km: %v", near)
	}
	// Wide radius: all three coordinates-bearing tasks, nearest first.
	wide := ids(model.TaskFilter{Latitude: f64(39.9042), Longitude: f64(116.4074), RadiusKm: 20000, Sort: "distance_asc"})
	if len(wide) != 2 || wide[0] != "tsk_beijing" || wide[1] != "tsk_shanghai" {
		t.Fatalf("distance 20000km: %v", wide)
	}

	n, err := st.Tasks().Count(ctx, model.TaskFilter{Keyword: "design"})
	if err != nil || n != 2 {
		t.Fatalf("count design: %d %v", n, err)
	}
}

func TestTaskFullTextSearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db")
	st, err := Open("file:"+path+"?mode=rwc", 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	tasks := []model.Task{
		{ID: "tsk_title", Kind: model.KindExclusive, Title: "Deliver a website", Description: "Build a landing page",
			Status: model.StatusOpen, PublisherID: "usr_publisher", Keywords: []string{"design"},
			Payment: model.Payment{Mode: model.PayOneShot, Amount: 100}, CreatedAt: now, UpdatedAt: now},
		{ID: "tsk_desc", Kind: model.KindExclusive, Title: "Write docs", Description: "Translate the manual to French",
			Status: model.StatusOpen, PublisherID: "usr_publisher", Keywords: []string{"writing"},
			Payment: model.Payment{Mode: model.PayOneShot, Amount: 100}, CreatedAt: now, UpdatedAt: now},
		{ID: "tsk_kw", Kind: model.KindExclusive, Title: "Fix bug", Description: "d",
			Status: model.StatusOpen, PublisherID: "usr_publisher", Keywords: []string{"french"},
			Payment: model.Payment{Mode: model.PayOneShot, Amount: 100}, CreatedAt: now, UpdatedAt: now},
	}
	for _, task := range tasks {
		if err := st.Tasks().Insert(ctx, task); err != nil {
			t.Fatalf("insert %s: %v", task.ID, err)
		}
	}

	ids := func(q string) []string {
		f := model.TaskFilter{Q: q, Limit: 100}
		rows, err := st.Tasks().List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}

	// Case-insensitive match in title.
	if got := ids("WEBSITE"); len(got) != 1 || got[0] != "tsk_title" {
		t.Fatalf("q title: %v", got)
	}
	// Match in description.
	if got := ids("translate"); len(got) != 1 || got[0] != "tsk_desc" {
		t.Fatalf("q description: %v", got)
	}
	// Match in keywords.
	if got := ids("French"); len(got) != 2 {
		t.Fatalf("q keywords (desc + kw): %v", got)
	}
	// LIKE metacharacters are treated literally.
	if got := ids("%"); len(got) != 0 {
		t.Fatalf("q literal percent: %v", got)
	}
	// Count applies the full-text filter too.
	n, err := st.Tasks().Count(ctx, model.TaskFilter{Q: "French"})
	if err != nil || n != 2 {
		t.Fatalf("count q: %d %v", n, err)
	}
}

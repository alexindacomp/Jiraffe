package state

import (
	"path/filepath"
	"testing"
	"time"

	"jiraffe/internal/worklog"
)

func sec(v int64) *int64 { return &v }

var v8Zone = time.FixedZone("EET", 2*3600)

func dayOv(create int64, key string, comment int64) worklog.DayOverrides {
	return worklog.DayOverrides{CreateTotalSeconds: sec(create),
		Issues: map[string]worklog.IssueOverride{key: {CommentSeconds: sec(comment)}}}
}

func TestOverridesPersistAndSnapshotIsACopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := open(t, path)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, v8Zone)
	s.SetClock(func() time.Time { return now })
	if err := s.Update(func(tx Tx) { tx.SetOverrides("2026-11-16", dayOv(1800, "A-1", 0), now) }); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	got := snap.Overrides("2026-11-16")
	*got.CreateTotalSeconds = 1
	got.Issues["B-2"] = worklog.IssueOverride{}
	again := s.Snapshot().Overrides("2026-11-16")
	if *again.CreateTotalSeconds != 1800 || len(again.Issues) != 1 || *again.Issues["A-1"].CommentSeconds != 0 {
		t.Errorf("snapshot shares data: %+v", again)
	}
	if none := snap.Overrides("2026-11-15"); none.Issues == nil || !none.Empty() {
		t.Errorf("missing day: %+v", none)
	}

	reopened := open(t, path)
	if got := reopened.Snapshot().Overrides("2026-11-16"); got.CreateTotalSeconds == nil || *got.CreateTotalSeconds != 1800 {
		t.Errorf("not persisted: %+v", got)
	}
}

// Another process (stale in memory) saving something else must not lose
// the overrides; when both set the same day, the later one wins.
func TestOverridesMergeAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	a, b := open(t, path), open(t, path)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, v8Zone)
	for _, s := range []*Store{a, b} {
		s.SetClock(func() time.Time { return now })
	}
	if err := a.Update(func(tx Tx) { tx.SetOverrides("2026-11-16", dayOv(600, "A-1", 60), now) }); err != nil {
		t.Fatal(err)
	}
	if err := b.Update(func(tx Tx) { tx.LastWorklogDate = "2026-11-13" }); err != nil {
		t.Fatal(err)
	}
	if got := open(t, path).Snapshot().Overrides("2026-11-16"); got.CreateTotalSeconds == nil || *got.CreateTotalSeconds != 600 {
		t.Fatalf("lost by a stale writer: %+v", got)
	}

	// b sets the day later; a (stale) then saves another day: b's version stays.
	if err := b.Update(func(tx Tx) { tx.SetOverrides("2026-11-16", dayOv(1200, "B-2", 120), now.Add(time.Minute)) }); err != nil {
		t.Fatal(err)
	}
	if err := a.Update(func(tx Tx) { tx.SetOverrides("2026-11-13", dayOv(60, "C-3", 0), now.Add(2*time.Minute)) }); err != nil {
		t.Fatal(err)
	}
	snap := open(t, path).Snapshot()
	if got := snap.Overrides("2026-11-16"); *got.CreateTotalSeconds != 1200 || len(got.Issues) != 1 || got.Issues["B-2"].CommentSeconds == nil {
		t.Errorf("the later version must win: %+v", got)
	}
	if got := snap.Overrides("2026-11-13"); got.CreateTotalSeconds == nil {
		t.Errorf("the other day is lost: %+v", got)
	}

	// A reset (empty overrides) is kept too: it does not resurrect the old value.
	if err := a.Update(func(tx Tx) {
		tx.SetOverrides("2026-11-16", worklog.DayOverrides{Issues: map[string]worklog.IssueOverride{}}, now.Add(3*time.Minute))
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.Update(func(tx Tx) {}); err != nil {
		t.Fatal(err)
	}
	if got := b.Snapshot().Overrides("2026-11-16"); !got.Empty() {
		t.Errorf("reset lost: %+v", got)
	}
}

func TestOverridesOlderThan31DaysArePruned(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "state.json"))
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, v8Zone)
	s.SetClock(func() time.Time { return now })
	if err := s.Update(func(tx Tx) {
		for _, d := range []string{"2026-10-15", "2026-10-16", "2026-11-16"} {
			tx.SetOverrides(d, dayOv(60, "A-1", 60), now)
		}
	}); err != nil {
		t.Fatal(err)
	}
	ov := s.Snapshot().WorklogOverrides
	if _, ok := ov["2026-10-15"]; ok || len(ov) != 2 {
		t.Errorf("kept %v", keys(ov))
	}
	// Later on, everything gets old.
	now = now.AddDate(0, 2, 0)
	if err := s.Update(func(tx Tx) {}); err != nil {
		t.Fatal(err)
	}
	if ov := s.Snapshot().WorklogOverrides; ov != nil {
		t.Errorf("kept %v", keys(ov))
	}
}

func keys(m map[string]DayOverrides) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

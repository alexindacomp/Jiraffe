package state

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"jiraffe/internal/worklog"
)

func open(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func TestOpenMissingFileIsEmpty(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "state.json"))
	d := s.Snapshot()
	if d.LastWorklogDate != "" || len(d.History) != 0 || len(d.Notifications) != 0 || len(d.SeenComments) != 0 || d.Unread() != 0 {
		t.Errorf("got %+v", d)
	}
}

func TestOpenCorruptFileMovesItAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"lastWorklogDate":`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err == nil || s == nil {
		t.Fatalf("want a usable store and an error, got %v", err)
	}
	if b, _ := os.ReadFile(path + ".bad"); string(b) != `{"lastWorklogDate":` {
		t.Errorf(".bad content %q", b)
	}
	if d := s.Snapshot(); d.LastWorklogDate != "" {
		t.Errorf("state not reset: %+v", d)
	}
	// The store still persists after the corruption.
	if err := s.Update(func(tx Tx) { tx.LastWorklogDate = "2026-11-11" }); err != nil {
		t.Fatal(err)
	}
	if got := open(t, path).Snapshot().LastWorklogDate; got != "2026-11-11" {
		t.Errorf("after reopen: %q", got)
	}
}

func TestStatePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := open(t, path)
	err := s.Update(func(tx Tx) {
		tx.LastWorklogDate = "2026-11-11"
		tx.AddRun(worklog.Run{Date: "2026-11-11", Trigger: worklog.TriggerSchedule, TotalSeconds: 28800})
		tx.AddNotification(Notification{ID: "12", IssueKey: "A-1"})
		tx.MarkSeen("12")
		tx.CommentsServer, tx.CommentsSince, tx.LastCommentPoll = "https://sd.local", "2026-11-10T10:00:00+02:00", "2026-11-11T18:00:00+02:00"
		tx.NotificationsPaused = true
	})
	if err != nil {
		t.Fatal(err)
	}
	r := open(t, path)
	d := r.Snapshot()
	if d.LastWorklogDate != "2026-11-11" || len(d.History) != 1 || d.History[0].TotalSeconds != 28800 ||
		len(d.Notifications) != 1 || d.Unread() != 1 || d.CommentsServer != "https://sd.local" ||
		d.LastCommentPoll != "2026-11-11T18:00:00+02:00" || !d.NotificationsPaused {
		t.Errorf("reloaded %+v", d)
	}
	_ = r.Update(func(tx Tx) {
		if !tx.Seen("12") || tx.Seen("13") {
			t.Errorf("seen set not restored: 12=%v 13=%v", tx.Seen("12"), tx.Seen("13"))
		}
	})
}

func TestSeenCommentsAreCappedOldestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := open(t, path)
	const extra = 10
	if err := s.Update(func(tx Tx) {
		for i := 0; i < MaxSeenComments+extra; i++ {
			tx.MarkSeen(fmt.Sprint(i))
		}
		tx.MarkSeen("0")                                     // re-marking an evicted id adds it back as the newest
		tx.MarkSeen(fmt.Sprint(MaxSeenComments + extra - 1)) // already seen: no-op
	}); err != nil {
		t.Fatal(err)
	}
	check := func(label string, st *Store) {
		d := st.Snapshot()
		if len(d.SeenComments) != MaxSeenComments {
			t.Fatalf("%s: %d seen ids, want %d", label, len(d.SeenComments), MaxSeenComments)
		}
		if d.SeenComments[len(d.SeenComments)-1] != "0" {
			t.Errorf("%s: newest is %s, want 0", label, d.SeenComments[len(d.SeenComments)-1])
		}
		_ = st.Update(func(tx Tx) {
			for _, c := range []struct {
				id   string
				want bool
			}{{"0", true}, {"1", false}, {fmt.Sprint(extra), false}, {fmt.Sprint(extra + 1), true}, {fmt.Sprint(MaxSeenComments + extra - 1), true}} {
				if tx.Seen(c.id) != c.want {
					t.Errorf("%s: Seen(%s)=%v, want %v", label, c.id, !c.want, c.want)
				}
			}
		})
	}
	check("in memory", s)
	check("after restart", open(t, path))
}

func TestResetCommentsForgetsSeenIDs(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "state.json"))
	_ = s.Update(func(tx Tx) { tx.MarkSeen("1"); tx.MarkSeen("2") })
	_ = s.Update(func(tx Tx) {
		tx.ResetComments()
		if tx.Seen("1") || len(tx.SeenComments) != 0 {
			t.Error("reset kept seen ids")
		}
		tx.MarkSeen("3")
	})
	if d := s.Snapshot(); len(d.SeenComments) != 1 || d.SeenComments[0] != "3" {
		t.Errorf("got %v", d.SeenComments)
	}
}

func TestHistoryAndNotificationsAreCappedNewestFirst(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "state.json"))
	_ = s.Update(func(tx Tx) {
		for i := 0; i < MaxHistory+5; i++ {
			tx.AddRun(worklog.Run{Date: fmt.Sprintf("run-%d", i)})
		}
		for i := 0; i < MaxNotifications+5; i++ {
			tx.AddNotification(Notification{ID: fmt.Sprint(i), Read: i%2 == 0})
		}
	})
	d := s.Snapshot()
	if len(d.History) != MaxHistory || d.History[0].Date != fmt.Sprintf("run-%d", MaxHistory+4) || d.History[MaxHistory-1].Date != "run-5" {
		t.Errorf("history: %d entries, first %s, last %s", len(d.History), d.History[0].Date, d.History[len(d.History)-1].Date)
	}
	if len(d.Notifications) != MaxNotifications || d.Notifications[0].ID != fmt.Sprint(MaxNotifications+4) {
		t.Errorf("notifications: %d, first %s", len(d.Notifications), d.Notifications[0].ID)
	}
	if d.Unread() != MaxNotifications/2 {
		t.Errorf("unread %d", d.Unread())
	}
}

func TestSnapshotIsADeepCopy(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "state.json"))
	_ = s.Update(func(tx Tx) {
		tx.AddRun(worklog.Run{Items: []worklog.Item{{Key: "A-1"}}})
		tx.AddNotification(Notification{ID: "1"})
		tx.MarkSeen("1")
	})
	snap := s.Snapshot()
	snap.History[0].Items[0].Key = "MUTATED"
	snap.Notifications[0].Read = true
	snap.SeenComments[0] = "MUTATED"
	d := s.Snapshot()
	if d.History[0].Items[0].Key != "A-1" || d.Notifications[0].Read || d.SeenComments[0] != "1" {
		t.Errorf("snapshot shares memory with the store: %+v", d)
	}
}

func TestUpdateKeepsMemoryWhenDiskWriteFails(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := Open(filepath.Join(blocker, "state.json")) // parent is a file: reads and writes fail
	if err := s.Update(func(tx Tx) { tx.LastWorklogDate = "2026-11-11" }); err == nil {
		t.Fatal("expected a write error")
	}
	if s.Snapshot().LastWorklogDate != "2026-11-11" {
		t.Error("in-memory change lost")
	}
}

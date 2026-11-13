package state

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jiraffe/internal/worklog"
)

func run(date, at string) worklog.Run {
	return worklog.Run{Date: date, At: at, Trigger: worklog.TriggerSchedule}
}

func runKeys(rs []worklog.Run) string {
	var b []string
	for _, r := range rs {
		b = append(b, r.Date+"@"+r.At[11:16])
	}
	return strings.Join(b, " ")
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(old); log.SetFlags(flags) })
	return &buf
}

// The incident of 12 Nov: process A records the scheduled run, process B
// (started earlier, stale in memory) saves something else afterwards. The
// run must survive, and B must see it from then on.
func TestStaleProcessDoesNotLoseAnotherProcessRun(t *testing.T) {
	logs := captureLog(t)
	path := filepath.Join(t.TempDir(), "state.json")
	a, b := open(t, path), open(t, path)

	if err := a.Update(func(tx Tx) {
		tx.AddRun(run("2026-11-12", "2026-11-12T18:00:05+02:00"))
		tx.LastWorklogDate = "2026-11-12"
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.Update(func(tx Tx) {
		if len(tx.History) != 1 {
			t.Errorf("the update function does not see the other process's run: %d entries", len(tx.History))
		}
		tx.AddNotification(Notification{ID: "7", Created: "2026-11-12T19:00:00.000+0200"})
	}); err != nil {
		t.Fatal(err)
	}
	d := open(t, path).Snapshot()
	if len(d.History) != 1 || d.LastWorklogDate != "2026-11-12" || len(d.Notifications) != 1 {
		t.Fatalf("saved state lost data: history %d, last %q, notifications %d", len(d.History), d.LastWorklogDate, len(d.Notifications))
	}
	if got := b.Snapshot(); len(got.History) != 1 || got.LastWorklogDate != "2026-11-12" {
		t.Errorf("process B did not pick up the merged state: %+v", got)
	}
	if !strings.Contains(logs.String(), "state: merged 2 foreign entries") { // the run and lastWorklogDate
		t.Errorf("log %q", logs.String())
	}
}

// Acceptance 1: two stores on one data dir saving alternately never lose
// each other's history or notifications.
func TestAlternatingSavesLoseNothing(t *testing.T) {
	captureLog(t)
	path := filepath.Join(t.TempDir(), "state.json")
	stores := []*Store{open(t, path), open(t, path)}
	base := time.Date(2026, 11, 1, 18, 0, 0, 0, time.FixedZone("EET", 2*3600))
	for i := 0; i < 20; i++ {
		s := stores[i%2]
		at := base.Add(time.Duration(i) * time.Hour)
		if err := s.Update(func(tx Tx) {
			tx.AddRun(run(at.Format(worklog.DateLayout), at.Format(time.RFC3339)))
			tx.AddNotification(Notification{ID: fmt.Sprint(i), Created: at.Format("2006-01-02T15:04:05.000-0700")})
			tx.MarkSeen(fmt.Sprint(i))
		}); err != nil {
			t.Fatal(err)
		}
	}
	d := open(t, path).Snapshot()
	if len(d.History) != 20 || len(d.Notifications) != 20 || len(d.SeenComments) != 20 {
		t.Fatalf("history %d, notifications %d, seen %d; want 20 each", len(d.History), len(d.Notifications), len(d.SeenComments))
	}
	for i := 1; i < len(d.History); i++ { // newest first
		if d.History[i-1].At <= d.History[i].At {
			t.Fatalf("history order: %s", runKeys(d.History))
		}
	}
	if d.Notifications[0].ID != "19" || d.Notifications[19].ID != "0" {
		t.Errorf("notification order: first %s, last %s", d.Notifications[0].ID, d.Notifications[19].ID)
	}
}

// Two processes saving concurrently (the lock serializes the
// read-merge-write cycles).
func TestConcurrentSavesLoseNothing(t *testing.T) {
	captureLog(t)
	path := filepath.Join(t.TempDir(), "state.json")
	stores := []*Store{open(t, path), open(t, path), open(t, path)}
	var wg sync.WaitGroup
	for p, s := range stores {
		wg.Add(1)
		go func(p int, s *Store) {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				id := fmt.Sprintf("p%d-%d", p, i)
				if err := s.Update(func(tx Tx) {
					tx.AddNotification(Notification{ID: id, Created: "2026-11-12T10:00:00.000+0200"})
				}); err != nil {
					t.Error(err)
				}
			}
		}(p, s)
	}
	wg.Wait()
	if n := len(open(t, path).Snapshot().Notifications); n != 45 {
		t.Fatalf("%d notifications saved, want 45", n)
	}
}

func TestMergeRules(t *testing.T) {
	mine := Data{
		LastWorklogDate: "2026-11-11",
		FirstRunDate:    "2026-11-05",
		History: []worklog.Run{
			run("2026-11-13", "2026-11-13T18:00:00+02:00"),
			run("2026-11-11", "2026-11-11T18:00:00+02:00"),
		},
		Notifications: []Notification{
			{ID: "a", Created: "2026-11-13T09:00:00.000+0200"},
			{ID: "b", Created: "2026-11-11T09:00:00.000+0200"},
		},
		SeenComments:        []string{"1", "2"},
		CommentsServer:      "https://sd.local",
		SkippedWorklogDays:  []string{"2026-11-03"},
		NotificationsPaused: true,
		SkippedVersion:      "0.7.0",
	}
	disk := Data{
		LastWorklogDate: "2026-11-12",
		FirstRunDate:    "2026-11-01",
		History: []worklog.Run{
			run("2026-11-12", "2026-11-12T18:00:00+02:00"), // missing in mine
			run("2026-11-11", "2026-11-11T18:00:00+02:00"), // the same entry
		},
		Notifications: []Notification{
			{ID: "b", Created: "2026-11-11T09:00:00.000+0200", Read: true}, // read in the other process
			{ID: "c", Created: "2026-11-12T09:00:00.000+0200"},
		},
		SeenComments:        []string{"2", "3"},
		CommentsServer:      "https://sd.local",
		SkippedWorklogDays:  []string{"2026-11-04", "2026-11-03"},
		NotificationsPaused: false,
		SkippedVersion:      "0.6.0",
	}
	got, n := merge(mine, disk)
	if want := "2026-11-13@18:00 2026-11-12@18:00 2026-11-11@18:00"; runKeys(got.History) != want {
		t.Errorf("history %s, want %s", runKeys(got.History), want)
	}
	var ids []string
	for _, x := range got.Notifications {
		ids = append(ids, fmt.Sprintf("%s:%v", x.ID, x.Read))
	}
	if strings.Join(ids, " ") != "a:false c:false b:true" {
		t.Errorf("notifications %v", ids)
	}
	if strings.Join(got.SeenComments, ",") != "1,2,3" {
		t.Errorf("seen %v", got.SeenComments)
	}
	if strings.Join(got.SkippedWorklogDays, ",") != "2026-11-03,2026-11-04" {
		t.Errorf("skipped %v", got.SkippedWorklogDays)
	}
	if got.LastWorklogDate != "2026-11-12" || got.FirstRunDate != "2026-11-01" {
		t.Errorf("last %q first %q", got.LastWorklogDate, got.FirstRunDate)
	}
	if !got.NotificationsPaused || got.SkippedVersion != "0.7.0" {
		t.Errorf("scalar fields must stay mine: paused %v skipped %q", got.NotificationsPaused, got.SkippedVersion)
	}
	// run, notification, seen id, skipped day, lastWorklogDate, firstRunDate
	if n != 6 {
		t.Errorf("foreign entries %d, want 6", n)
	}
	// mine is not modified
	if mine.Notifications[1].Read || len(mine.History) != 2 {
		t.Error("merge modified its input")
	}
}

func TestMergeWithoutForeignEntriesKeepsMineAsIs(t *testing.T) {
	mine := Data{
		History:       []worklog.Run{run("2026-11-11", "2026-11-11T18:00:00+02:00"), run("2026-11-12", "2026-11-12T18:00:00+02:00")},
		Notifications: []Notification{{ID: "b", Created: "2026-11-11T09:00:00.000+0200"}, {ID: "a", Created: "2026-11-13T09:00:00.000+0200"}},
	}
	got, n := merge(mine, Data{History: mine.History[:1], Notifications: mine.Notifications[1:]})
	if n != 0 || runKeys(got.History) != runKeys(mine.History) || got.Notifications[0].ID != "b" {
		t.Errorf("n %d, history %s, first notification %s: the order must not change", n, runKeys(got.History), got.Notifications[0].ID)
	}
}

// Entries trimmed by the cap in this process must not come back from the
// file (nor be counted as foreign on every save).
func TestMergeHistoryCapDoesNotResurrectTrimmedEntries(t *testing.T) {
	base := time.Date(2026, 9, 1, 18, 0, 0, 0, time.UTC)
	var all []worklog.Run
	for i := MaxHistory; i >= 0; i-- { // 61 entries, newest first
		at := base.AddDate(0, 0, i)
		all = append(all, run(at.Format(worklog.DateLayout), at.Format(time.RFC3339)))
	}
	mine := all[:MaxHistory]
	got, n := merge(Data{History: mine}, Data{History: all})
	if n != 0 || len(got.History) != MaxHistory || got.History[MaxHistory-1].At != mine[MaxHistory-1].At {
		t.Errorf("n %d, len %d", n, len(got.History))
	}
}

func TestMergeSeenCommentsOfAnotherServerAreIgnored(t *testing.T) {
	mine := Data{CommentsServer: "https://new.local", SeenComments: []string{"x"}}
	got, n := merge(mine, Data{CommentsServer: "https://old.local", SeenComments: []string{"1", "2"}})
	if n != 0 || strings.Join(got.SeenComments, ",") != "x" || got.CommentsServer != "https://new.local" {
		t.Errorf("n %d, %+v", n, got)
	}
	// A process that tracks nothing yet adopts the saved tracking.
	got, _ = merge(Data{}, Data{CommentsServer: "https://sd.local", CommentsSince: "2026-11-01T00:00:00Z", SeenComments: []string{"1"}})
	if got.CommentsServer != "https://sd.local" || got.CommentsSince == "" || len(got.SeenComments) != 1 {
		t.Errorf("adopt: %+v", got)
	}
}

func TestMergeCapsSeenComments(t *testing.T) {
	var mine, disk []string
	for i := 0; i < MaxSeenComments; i++ {
		mine = append(mine, fmt.Sprint(i))
	}
	disk = []string{"new1", "new2"}
	got, n := merge(Data{CommentsServer: "s", SeenComments: mine}, Data{CommentsServer: "s", SeenComments: disk})
	if n != 2 || len(got.SeenComments) != MaxSeenComments || got.SeenComments[0] != "2" || got.SeenComments[MaxSeenComments-1] != "new2" {
		t.Errorf("n %d, len %d, first %s", n, len(got.SeenComments), got.SeenComments[0])
	}
}

// Seen ids merged from the file are known to Tx.Seen afterwards (no
// duplicate notification for a comment the other process already saw).
func TestMergedSeenIdsAreKnownToTheTransaction(t *testing.T) {
	captureLog(t)
	path := filepath.Join(t.TempDir(), "state.json")
	a, b := open(t, path), open(t, path)
	_ = a.Update(func(tx Tx) { tx.CommentsServer = "s" })
	_ = b.Update(func(tx Tx) {})
	_ = a.Update(func(tx Tx) { tx.MarkSeen("42") })
	_ = b.Update(func(tx Tx) {
		if !tx.Seen("42") {
			t.Error("id seen by the other process is unknown")
		}
	})
}

func TestCorruptFileOnSaveIsOverwrittenWithMine(t *testing.T) {
	logs := captureLog(t)
	path := filepath.Join(t.TempDir(), "state.json")
	s := open(t, path)
	if err := os.WriteFile(path, []byte(`{"history":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(tx Tx) { tx.LastWorklogDate = "2026-11-12" }); err != nil {
		t.Fatal(err)
	}
	if got := open(t, path).Snapshot().LastWorklogDate; got != "2026-11-12" {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(logs.String(), "re-read before save") {
		t.Errorf("log %q", logs.String())
	}
}

func TestSkipDayIsSortedUniqueAndCapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := open(t, path)
	_ = s.Update(func(tx Tx) {
		tx.SkipDay("2026-11-12")
		tx.SkipDay("2026-11-10")
		tx.SkipDay("2026-11-12")
	})
	if got := s.Snapshot().SkippedWorklogDays; strings.Join(got, ",") != "2026-11-10,2026-11-12" {
		t.Errorf("got %v", got)
	}
	_ = s.Update(func(tx Tx) {
		for i := 0; i < MaxSkippedDays+5; i++ {
			tx.SkipDay(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format(worklog.DateLayout))
		}
	})
	got := open(t, path).Snapshot().SkippedWorklogDays
	if len(got) != MaxSkippedDays || got[len(got)-1] != "2026-11-12" {
		t.Errorf("len %d, last %s", len(got), got[len(got)-1])
	}
}

// A save waits for another process holding the lock.
func TestSaveWaitsForTheLock(t *testing.T) {
	captureLog(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	a, b := open(t, path), open(t, path)
	release := make(chan struct{})
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_ = a.Update(func(tx Tx) {
			close(entered)
			<-release
			tx.AddRun(run("2026-11-12", "2026-11-12T18:00:00+02:00"))
		})
		close(done)
	}()
	<-entered
	saved := make(chan struct{})
	go func() {
		_ = b.Update(func(tx Tx) { tx.AddNotification(Notification{ID: "1"}) })
		close(saved)
	}()
	select {
	case <-saved:
		t.Fatal("second save did not wait for the lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-done
	<-saved
	if d := open(t, path).Snapshot(); len(d.History) != 1 || len(d.Notifications) != 1 {
		t.Errorf("history %d notifications %d", len(d.History), len(d.Notifications))
	}
}

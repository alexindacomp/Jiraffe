// Package state persists runtime state: last scheduled worklog date, worklog
// history, notifications and seen comment ids.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"jiraffe/internal/config"
	"jiraffe/internal/filelock"
	"jiraffe/internal/update"
	"jiraffe/internal/worklog"
)

// Limits of stored collections.
const (
	MaxHistory       = 60
	MaxNotifications = 200
	MaxSeenComments  = 5000
	MaxSkippedDays   = 100
)

// LockName is the lock file next to state.json that serializes saves of
// all Jiraffe processes.
const LockName = "state.lock"

// lockTimeout bounds the wait for another process's save.
const lockTimeout = 5 * time.Second

// Notification about a new comment (contract type Notification).
type Notification struct {
	ID           string `json:"id"`
	IssueKey     string `json:"issueKey"`
	IssueSummary string `json:"issueSummary"`
	Author       string `json:"author"`
	Body         string `json:"body"`
	Created      string `json:"created"`
	URL          string `json:"url"`
	Read         bool   `json:"read"`
}

// Data is the persisted state.
type Data struct {
	// LastWorklogDate is the date ("2006-01-02") of the last successful
	// scheduled worklog run.
	LastWorklogDate string `json:"lastWorklogDate,omitempty"`
	// History of worklog runs, newest first.
	History []worklog.Run `json:"history"`
	// Notifications, newest first.
	Notifications []Notification `json:"notifications"`
	// SeenComments holds comment ids in the order they were seen (oldest first).
	SeenComments []string `json:"seenComments"`
	// CommentsServer is the Jira URL the seen ids belong to; when it changes
	// the comment tracking is re-initialized.
	CommentsServer string `json:"commentsServer,omitempty"`
	// CommentsSince (RFC 3339) is when comment tracking started; older comments
	// never produce notifications.
	CommentsSince string `json:"commentsSince,omitempty"`
	// LastCommentPoll (RFC 3339) of the last successful poll.
	LastCommentPoll string `json:"lastCommentPoll,omitempty"`
	// NotificationsPaused suppresses tray balloons.
	NotificationsPaused bool `json:"notificationsPaused,omitempty"`
	// UIPrefs is the UI's preferences object, stored as raw JSON (opaque to
	// Go; independent of the Jira connection).
	UIPrefs json.RawMessage `json:"uiPrefs,omitempty"`
	// SkippedVersion is the update the user postponed ("Later"): it is not
	// offered again; a newer one is.
	SkippedVersion string `json:"skippedVersion,omitempty"`
	// UpdateCheck is the result of the last update check.
	UpdateCheck *UpdateCheck `json:"updateCheck,omitempty"`
	// FirstRunDate ("2006-01-02") is the first day Jiraffe ran on this
	// machine; missed-day detection never looks before it. Set once.
	FirstRunDate string `json:"firstRunDate,omitempty"`
	// SkippedWorklogDays are past days the user chose not to log
	// (ascending, unique); they are never reported as missed.
	SkippedWorklogDays []string `json:"skippedWorklogDays,omitempty"`
}

// UpdateCheck is the result of an update check.
type UpdateCheck struct {
	At     string `json:"at"` // RFC 3339
	OK     bool   `json:"ok"`
	Code   string `json:"code,omitempty"`   // update error code when !OK
	Detail string `json:"detail,omitempty"` // English detail when !OK
	// Latest is the latest release seen (kept when a later check fails).
	Latest *update.Release `json:"latest,omitempty"`
}

// Store keeps Data in memory and writes it to disk on every update.
// It is safe for concurrent use.
type Store struct {
	path string
	lock string // lock file serializing saves across processes
	mu   sync.Mutex
	d    Data
	seen map[string]struct{}
}

// Open loads state from path. A missing file yields an empty state; a corrupt
// one is moved aside to path+".bad" (the error is returned, the Store usable).
func Open(path string) (*Store, error) {
	s := &Store{path: path, lock: filepath.Join(filepath.Dir(path), LockName), seen: map[string]struct{}{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(b, &s.d); err != nil {
		_ = os.Rename(path, path+".bad")
		s.d = Data{}
		return s, fmt.Errorf("state %s is corrupt (moved to .bad): %w", path, err)
	}
	for _, id := range s.d.SeenComments {
		s.seen[id] = struct{}{}
	}
	return s, nil
}

// Snapshot returns a deep copy of the state.
func (s *Store) Snapshot() Data {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.clone()
}

func (d Data) clone() Data {
	c := d
	c.History = make([]worklog.Run, len(d.History))
	for i, r := range d.History {
		r.Items = append([]worklog.Item{}, r.Items...)
		c.History[i] = r
	}
	c.Notifications = append([]Notification{}, d.Notifications...)
	c.SeenComments = append([]string{}, d.SeenComments...)
	if d.SkippedWorklogDays != nil {
		c.SkippedWorklogDays = append([]string{}, d.SkippedWorklogDays...)
	}
	if d.UIPrefs != nil {
		c.UIPrefs = append(json.RawMessage{}, d.UIPrefs...)
	}
	if d.UpdateCheck != nil {
		u := *d.UpdateCheck
		if u.Latest != nil {
			l := *u.Latest
			u.Latest = &l
		}
		c.UpdateCheck = &u
	}
	return c
}

// Tx is the mutable view passed to Update.
type Tx struct {
	*Data
	s *Store
}

// Seen reports whether a comment id was already seen.
func (t Tx) Seen(id string) bool {
	_, ok := t.s.seen[id]
	return ok
}

// MarkSeen records a comment id, evicting the oldest beyond MaxSeenComments.
func (t Tx) MarkSeen(id string) {
	if t.Seen(id) {
		return
	}
	t.s.seen[id] = struct{}{}
	t.SeenComments = append(t.SeenComments, id)
	if over := len(t.SeenComments) - MaxSeenComments; over > 0 {
		for _, old := range t.SeenComments[:over] {
			delete(t.s.seen, old)
		}
		t.SeenComments = append([]string{}, t.SeenComments[over:]...)
	}
}

// ResetComments forgets all seen comment ids.
func (t Tx) ResetComments() {
	t.SeenComments = nil
	t.s.seen = map[string]struct{}{}
}

// AddRun prepends a run to the history (capped at MaxHistory).
func (t Tx) AddRun(r worklog.Run) {
	t.History = append([]worklog.Run{r}, t.History...)
	if len(t.History) > MaxHistory {
		t.History = t.History[:MaxHistory]
	}
}

// AddNotification prepends a notification (capped at MaxNotifications).
func (t Tx) AddNotification(n Notification) {
	t.Notifications = append([]Notification{n}, t.Notifications...)
	if len(t.Notifications) > MaxNotifications {
		t.Notifications = t.Notifications[:MaxNotifications]
	}
}

// Unread counts unread notifications.
func (d Data) Unread() int {
	n := 0
	for _, x := range d.Notifications {
		if !x.Read {
			n++
		}
	}
	return n
}

// SkipDay records a day the user does not want to log (kept sorted and
// unique, capped at MaxSkippedDays).
func (t Tx) SkipDay(date string) {
	t.SkippedWorklogDays = unionDays(t.SkippedWorklogDays, []string{date})
}

// Update applies fn and persists the result. Saves of all processes are
// serialized by a lock file; under it the file is re-read and merged into
// this process's state first (see merge), so another process's history,
// notifications or skipped days are never overwritten by a stale copy, and
// fn sees them. If writing fails the in-memory change is kept (so the app
// keeps working) and the error is returned.
func (s *Store) Update(fn func(tx Tx)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if unlock, err := filelock.Lock(s.lock, lockTimeout); err != nil {
		log.Printf("state: %v; saving without the lock", err)
	} else {
		defer unlock()
	}
	s.mergeFromDisk()
	fn(Tx{Data: &s.d, s: s})
	b, err := json.Marshal(s.d)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := config.WriteFileAtomic(s.path, b); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

// mergeFromDisk merges the saved file (possibly written by another process)
// into the in-memory state. Unreadable or corrupt files are ignored: this
// process's state is then written as is.
func (s *Store) mergeFromDisk() {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		log.Printf("state: re-read before save: %v", err)
		return
	}
	var disk Data
	if err := json.Unmarshal(b, &disk); err != nil {
		log.Printf("state: re-read before save: %v (overwriting)", err)
		return
	}
	before := s.d.SeenComments
	merged, n := merge(s.d, disk)
	s.d = merged // also when n == 0: e.g. a notification read elsewhere
	if !sameSlice(before, s.d.SeenComments) {
		s.seen = make(map[string]struct{}, len(s.d.SeenComments))
		for _, id := range s.d.SeenComments {
			s.seen[id] = struct{}{}
		}
	}
	if n > 0 {
		log.Printf("state: merged %d foreign entries", n)
	}
}

// sameSlice reports whether a and b are the same slice (not just equal).
func sameSlice(a, b []string) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// merge combines this process's state (mine) with the saved one (disk) and
// counts the entries taken from disk:
//   - history: union by (date, at), newest first, capped;
//   - notifications: union by id (read if read on either side), newest
//     first, capped;
//   - seen comment ids: union while both track the same Jira (a process that
//     tracks nothing yet adopts the saved tracking), capped;
//   - skipped worklog days: union; lastWorklogDate: the later one;
//     firstRunDate: the earlier one;
//   - everything else: mine.
//
// Without foreign entries mine is returned unchanged (same order).
func merge(mine, disk Data) (Data, int) {
	out := mine
	n := 0

	var h int
	out.History, h = mergeHistory(mine.History, disk.History)
	n += h
	var no int
	out.Notifications, no = mergeNotifications(mine.Notifications, disk.Notifications)
	n += no

	switch {
	case mine.CommentsServer == "" && disk.CommentsServer != "":
		out.CommentsServer, out.CommentsSince, out.LastCommentPoll = disk.CommentsServer, disk.CommentsSince, disk.LastCommentPoll
		out.SeenComments = append([]string{}, disk.SeenComments...)
		n += len(disk.SeenComments)
	case mine.CommentsServer == disk.CommentsServer:
		have := make(map[string]struct{}, len(mine.SeenComments))
		for _, id := range mine.SeenComments {
			have[id] = struct{}{}
		}
		var foreign []string
		for _, id := range disk.SeenComments {
			if _, ok := have[id]; !ok {
				have[id] = struct{}{}
				foreign = append(foreign, id)
			}
		}
		if len(foreign) > 0 {
			// Foreign ids count as the newest: they are evicted last.
			all := append(append([]string{}, mine.SeenComments...), foreign...)
			if over := len(all) - MaxSeenComments; over > 0 {
				all = all[over:]
			}
			out.SeenComments = all
			n += len(foreign)
		}
	}

	if days := unionDays(mine.SkippedWorklogDays, disk.SkippedWorklogDays); len(days) != len(mine.SkippedWorklogDays) {
		n += len(days) - len(mine.SkippedWorklogDays)
		out.SkippedWorklogDays = days
	}
	if disk.LastWorklogDate > mine.LastWorklogDate { // ISO dates compare as strings
		out.LastWorklogDate = disk.LastWorklogDate
		n++
	}
	if disk.FirstRunDate != "" && (mine.FirstRunDate == "" || disk.FirstRunDate < mine.FirstRunDate) {
		out.FirstRunDate = disk.FirstRunDate
		n++
	}
	return out, n
}

func mergeHistory(mine, disk []worklog.Run) ([]worklog.Run, int) {
	type key struct{ date, at string }
	have := make(map[key]bool, len(mine))
	for _, r := range mine {
		have[key{r.Date, r.At}] = true
	}
	var foreign []worklog.Run
	for _, r := range disk {
		if k := (key{r.Date, r.At}); !have[k] {
			have[k] = true
			foreign = append(foreign, r)
		}
	}
	if len(foreign) == 0 {
		return mine, 0
	}
	all := append(append(make([]worklog.Run, 0, len(mine)+len(foreign)), mine...), foreign...)
	sort.SliceStable(all, func(i, j int) bool { return parseTime(all[i].At).After(parseTime(all[j].At)) })
	if len(all) > MaxHistory {
		all = all[:MaxHistory]
	}
	n := 0
	for _, r := range all {
		if !inRuns(mine, r) {
			n++
		}
	}
	return all, n
}

func inRuns(list []worklog.Run, r worklog.Run) bool {
	for _, x := range list {
		if x.Date == r.Date && x.At == r.At {
			return true
		}
	}
	return false
}

func mergeNotifications(mine, disk []Notification) ([]Notification, int) {
	idx := make(map[string]int, len(mine))
	for i, x := range mine {
		idx[x.ID] = i
	}
	var out []Notification
	var foreign []Notification
	for _, x := range disk {
		if i, ok := idx[x.ID]; ok {
			if x.Read && !mine[i].Read { // a read notification stays read
				if out == nil {
					out = append([]Notification{}, mine...)
				}
				out[i].Read = true
			}
			continue
		}
		idx[x.ID] = -1
		foreign = append(foreign, x)
	}
	if out == nil {
		out = mine
	}
	if len(foreign) == 0 {
		return out, 0
	}
	all := append(append(make([]Notification, 0, len(out)+len(foreign)), out...), foreign...)
	sort.SliceStable(all, func(i, j int) bool { return parseTime(all[i].Created).After(parseTime(all[j].Created)) })
	if len(all) > MaxNotifications {
		all = all[:MaxNotifications]
	}
	mineIDs := make(map[string]bool, len(mine))
	for _, x := range mine {
		mineIDs[x.ID] = true
	}
	n := 0
	for _, x := range all {
		if !mineIDs[x.ID] {
			n++
		}
	}
	return all, n
}

// unionDays merges two date lists into a sorted unique list, keeping the
// latest MaxSkippedDays.
func unionDays(a, b []string) []string {
	set := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, l := range [][]string{a, b} {
		for _, d := range l {
			if d != "" && !set[d] {
				set[d] = true
				out = append(out, d)
			}
		}
	}
	sort.Strings(out)
	if over := len(out) - MaxSkippedDays; over > 0 {
		out = out[over:]
	}
	return out
}

// parseTime reads the timestamps of runs (RFC 3339) and notifications
// (Jira's "2006-01-02T15:04:05.000-0700"); unparseable ones sort last.
func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05-0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

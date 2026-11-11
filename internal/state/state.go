// Package state persists runtime state: last scheduled worklog date, worklog
// history, notifications and seen comment ids.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"jiraffe/internal/config"
	"jiraffe/internal/worklog"
)

// Limits of stored collections.
const (
	MaxHistory       = 60
	MaxNotifications = 200
	MaxSeenComments  = 5000
)

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
}

// Store keeps Data in memory and writes it to disk on every update.
// It is safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
	d    Data
	seen map[string]struct{}
}

// Open loads state from path. A missing file yields an empty state; a corrupt
// one is moved aside to path+".bad" (the error is returned, the Store usable).
func Open(path string) (*Store, error) {
	s := &Store{path: path, seen: map[string]struct{}{}}
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

// Update applies fn under the lock and persists the result. If writing fails
// the in-memory change is kept (so the app keeps working) and the error is
// returned.
func (s *Store) Update(fn func(tx Tx)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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

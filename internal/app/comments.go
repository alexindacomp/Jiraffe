package app

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"jiraffe/internal/jira"
	"jiraffe/internal/state"
)

const (
	maxBodyRunes     = 300
	maxPollWindowMin = 7 * 24 * 60 // never look back further than a week
	maxCommentAge    = 7 * 24 * time.Hour
	maxPollIssues    = 1000 // issues per poll (10 pages)
)

// PollComments looks for new comments by others on my issues (scheduler hook).
func (a *App) PollComments(ctx context.Context) error {
	a.pollMu.Lock()
	defer a.pollMu.Unlock()

	c, identity, err := a.client()
	if err != nil {
		return nil // not configured: nothing to do
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	me, err := a.me(ctx, c, identity)
	if err != nil {
		return err
	}
	// Alerts only for tracked projects. If they cannot be determined now,
	// alert for all (as before tracking existed) rather than drop comments.
	tracked, err := a.trackedProjects(ctx, c, identity, nil)
	if err != nil {
		log.Printf("comments: tracked projects unknown, not filtering: %v", err)
		tracked = nil
	}
	alertFor := trackedSet(tracked)
	s := a.cfg.Get()
	now := a.clock.Now()
	snap := a.st.Snapshot()

	window := s.CommentPollMinutes + 1
	if snap.CommentsServer == s.JiraURL {
		if last, err := time.Parse(time.RFC3339, snap.LastCommentPoll); err == nil {
			// Cover the gap since the last successful poll (sleep, app closed).
			if m := int(now.Sub(last).Minutes()) + 2; m > window {
				window = m
			}
		}
	}
	if window > maxPollWindowMin {
		window = maxPollWindowMin
	}
	// Oldest first, so a truncated result still covers a contiguous period
	// and the next poll continues from where this one stopped.
	jql := fmt.Sprintf("assignee = currentUser() AND updated >= -%dm ORDER BY updated ASC", window)
	issues, err := c.Search(ctx, jql, jira.SearchOptions{Fields: []string{"summary", "comment", "updated", "project"}, Limit: maxPollIssues})
	if err != nil {
		return err
	}
	horizon := now
	if len(issues) >= maxPollIssues {
		if t, err := jira.ParseTime(issues[len(issues)-1].Fields.Updated); err == nil && t.Before(now) {
			horizon = t
			log.Printf("comments: more than %d updated issues; continuing from %s next time", maxPollIssues, t.Format(time.RFC3339))
		}
	}

	type found struct {
		n       state.Notification
		created time.Time
	}
	var fresh []found
	if err := a.st.Update(func(tx state.Tx) {
		initial := false
		if tx.CommentsServer != s.JiraURL || tx.CommentsSince == "" {
			// First run for this Jira: mark everything as seen, no flood.
			tx.ResetComments()
			tx.CommentsServer = s.JiraURL
			tx.CommentsSince = now.Format(time.RFC3339)
			initial = true
		}
		since, _ := time.Parse(time.RFC3339, tx.CommentsSince)
		for _, is := range issues {
			if is.Fields.Comment == nil {
				continue
			}
			for _, cm := range is.Fields.Comment.Comments {
				if cm.ID == "" || tx.Seen(cm.ID) {
					continue
				}
				tx.MarkSeen(cm.ID)
				// Comments of untracked projects are marked seen too, so
				// tracking a project later does not replay old comments.
				if initial || me.Same(cm.Author) || alertFor != nil && !alertFor[projectOf(is)] {
					continue
				}
				created, err := jira.ParseTime(cm.Created)
				if err != nil || created.Before(since) || now.Sub(created) > maxCommentAge {
					continue // old comment on a recently updated issue
				}
				author := ""
				if cm.Author != nil {
					author = cm.Author.DisplayName
					if author == "" {
						author = cm.Author.Name
					}
				}
				n := state.Notification{
					ID:           cm.ID,
					IssueKey:     is.Key,
					IssueSummary: is.Fields.Summary,
					Author:       author,
					Body:         PlainText(cm.Body, maxBodyRunes),
					Created:      created.Local().Format(time.RFC3339),
					URL:          commentURL(c, is.Key, cm.ID),
				}
				fresh = append(fresh, found{n, created})
			}
		}
		sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].created.Before(fresh[j].created) })
		for _, f := range fresh {
			tx.AddNotification(f.n)
		}
		tx.LastCommentPoll = horizon.Format(time.RFC3339)
	}); err != nil {
		log.Printf("save comment state: %v", err)
	}
	if len(fresh) == 0 {
		return nil
	}
	log.Printf("comments: %d new", len(fresh))
	for _, f := range fresh {
		a.emit("notification", f.n)
	}
	a.emitState()
	a.invalidateBoard() // issues changed: the cached board is stale
	a.emit("board", struct{}{})
	if !a.Paused() {
		lang := a.Language()
		if len(fresh) == 1 {
			n := fresh[0].n
			a.plat.Notify(T(lang, "commentTitle", n.Author, n.IssueKey), n.Body, n.URL)
		} else {
			last := fresh[len(fresh)-1].n
			a.plat.Notify(T(lang, "commentsTitle", len(fresh)), last.Author+" · "+last.IssueKey+": "+last.Body, "")
		}
	}
	return nil
}

func commentURL(c *jira.Client, key, id string) string {
	return c.BrowseURL(key) + "?focusedCommentId=" + url.QueryEscape(id) + "#comment-" + url.QueryEscape(id)
}

// PlainText turns [~user] mentions into @user, collapses whitespace and
// truncates to max runes (with an ellipsis).
func PlainText(s string, max int) string {
	var m strings.Builder
	for {
		i := strings.Index(s, "[~")
		if i < 0 {
			m.WriteString(s)
			break
		}
		j := strings.IndexByte(s[i:], ']')
		if j < 0 || strings.ContainsAny(s[i:i+j], " \t\r\n") {
			m.WriteString(s[:i+2])
			s = s[i+2:]
			continue
		}
		m.WriteString(s[:i])
		m.WriteString("@" + s[i+2:i+j])
		s = s[i+j+1:]
	}
	out := strings.Join(strings.Fields(m.String()), " ")
	if utf8.RuneCountInString(out) <= max {
		return out
	}
	rs := []rune(out)
	return strings.TrimSpace(string(rs[:max-1])) + "…"
}

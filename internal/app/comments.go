package app

import (
	"bytes"
	"context"
	"fmt"
	"html"
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
// truncates to max runes (with an ellipsis). An HTML body (JEditor) is
// first reduced to its text: tags removed (block tags and <br> become a
// space), script/style content dropped, entities decoded.
func PlainText(s string, max int) string {
	if looksLikeHTML(s) {
		s = html.UnescapeString(stripTags(s))
	}
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

// htmlMarkers are the element tags that make a body HTML; the same set as
// the UI's detection (SPEC v0.4 §1, wiki.render).
var htmlMarkers = map[string]bool{
	"p": true, "div": true, "br": true, "span": true, "b": true, "strong": true, "i": true, "em": true,
	"u": true, "s": true, "strike": true, "del": true, "ins": true, "code": true, "pre": true, "ul": true,
	"ol": true, "li": true, "a": true, "img": true, "table": true, "thead": true, "tbody": true,
	"tfoot": true, "tr": true, "td": true, "th": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "hr": true, "blockquote": true, "sub": true, "sup": true, "font": true,
}

// looksLikeHTML reports whether a Jira text body is HTML rather than wiki
// markup or plain text. Like the UI's /<\/?(p|div|br|…)\b[^>]*>/i: a "<",
// an optional "/", a name from htmlMarkers ending at a word boundary, and a
// ">" somewhere after it. (No regexp package: ~100 KB of binary size.)
func looksLikeHTML(s string) bool {
	lastGT := strings.LastIndexByte(s, '>') // "a > follows" without rescanning (linear)
	for i := strings.IndexByte(s, '<'); i >= 0; {
		rest := s[i+1:]
		if name, closing, ok := tagName(rest); ok && htmlMarkers[name] {
			after := rest[len(name):]
			if closing {
				after = rest[1+len(name):]
			}
			if (after == "" || !isWordByte(after[0])) && lastGT >= len(s)-len(after) {
				return true
			}
		}
		j := strings.IndexByte(rest, '<')
		if j < 0 {
			return false
		}
		i += 1 + j
	}
	return false
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// inlineTags are removed without a separator; any other tag (block
// elements, <br>, unknown tags) becomes a space so words do not run together.
var inlineTags = map[string]bool{
	"a": true, "abbr": true, "b": true, "bdi": true, "bdo": true, "cite": true, "code": true, "del": true,
	"dfn": true, "em": true, "font": true, "i": true, "ins": true, "kbd": true, "mark": true, "q": true,
	"s": true, "samp": true, "small": true, "span": true, "strike": true, "strong": true, "sub": true,
	"sup": true, "time": true, "u": true, "var": true,
}

// droppedTags lose their content too (it is not text the user wrote).
var droppedTags = map[string]bool{
	"script": true, "style": true, "head": true, "title": true, "template": true, "noscript": true,
	"textarea": true, "svg": true, "math": true, "iframe": true, "object": true, "select": true,
}

// stripTags removes HTML tags and comments from s, keeping the text (entities
// are left encoded). A "<" that does not start a tag is kept as text.
func stripTags(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != '<' {
			b.WriteByte(c)
			i++
			continue
		}
		if strings.HasPrefix(s[i:], "<!--") {
			end := strings.Index(s[i+4:], "-->")
			if end < 0 {
				break // unterminated comment: drop the rest
			}
			i += 4 + end + 3
			continue
		}
		name, closing, ok := tagName(s[i+1:])
		if !ok {
			b.WriteByte(c) // "a < b": not a tag
			i++
			continue
		}
		end := tagEnd(s, i+1)
		if end < 0 {
			break // unterminated tag: drop the rest
		}
		i = end + 1
		switch {
		case !closing && droppedTags[name]:
			// A self-closing <svg/> or <math/> (foreign elements) has no
			// content to drop; <script/> etc. still open an element, as in
			// browsers.
			if !((name == "svg" || name == "math") && s[end-1] == '/') {
				i = skipElement(s, i, name)
			}
			b.WriteByte(' ')
		case !inlineTags[name]:
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// tagName reads the tag name after "<" (lower-cased); ok is false when the
// text does not start a tag. Declarations and processing instructions
// ("<!DOCTYPE …>", "<?xml …?>") are tags with an empty name.
func tagName(s string) (name string, closing, ok bool) {
	if s != "" && (s[0] == '!' || s[0] == '?') {
		return "", false, true
	}
	if s != "" && s[0] == '/' {
		closing, s = true, s[1:]
	}
	n := 0
	for n < len(s) && (s[n] >= 'a' && s[n] <= 'z' || s[n] >= 'A' && s[n] <= 'Z' || n > 0 && s[n] >= '0' && s[n] <= '9') {
		n++
	}
	if n == 0 {
		return "", false, false
	}
	return strings.ToLower(s[:n]), closing, true
}

// tagEnd returns the index of the ">" closing the tag that starts before
// from, skipping quoted attribute values; -1 if there is none.
func tagEnd(s string, from int) int {
	var quote byte
	for j := from; j < len(s); j++ {
		switch c := s[j]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return j
		}
	}
	return -1
}

// skipElement returns the index after the closing tag of name that follows
// from (case-insensitive), or len(s) when it is not closed.
func skipElement(s string, from int, name string) int {
	// ASCII-only lowering keeps byte offsets equal to those of s.
	lower := []byte(s[from:])
	for i, c := range lower {
		if c >= 'A' && c <= 'Z' {
			lower[i] = c + 'a' - 'A'
		}
	}
	for k := 0; ; {
		j := bytes.Index(lower[k:], []byte("</"+name))
		if j < 0 {
			return len(s)
		}
		k += j
		rest := lower[k+2+len(name):]
		if len(rest) == 0 || rest[0] == '>' || rest[0] == '/' || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' || rest[0] == '\r' {
			end := bytes.IndexByte(lower[k:], '>')
			if end < 0 {
				return len(s)
			}
			return from + k + end + 1
		}
		k += 2 // "</scriptx": keep looking
	}
}

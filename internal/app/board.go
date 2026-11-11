package app

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

	"jiraffe/internal/jira"
)

// StatusDTO is the Status contract type.
type StatusDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"` // new | indeterminate | done
}

// NameDTO is {name}.
type NameDTO struct {
	Name string `json:"name"`
}

// AssigneeDTO is {displayName}.
type AssigneeDTO struct {
	DisplayName string `json:"displayName"`
}

// IssueDTO is the Issue contract type.
type IssueDTO struct {
	Key      string       `json:"key"`
	Summary  string       `json:"summary"`
	URL      string       `json:"url"`
	Project  string       `json:"project"`
	Status   StatusDTO    `json:"status"`
	Type     NameDTO      `json:"type"`
	Priority NameDTO      `json:"priority"`
	Updated  string       `json:"updated"`
	Assignee *AssigneeDTO `json:"assignee"`
}

// TransitionDTO is the Transition contract type.
type TransitionDTO struct {
	ID   string    `json:"id"`
	Name string    `json:"name"`
	To   StatusDTO `json:"to"`
}

// Column of the board.
type Column struct {
	Status StatusDTO  `json:"status"`
	Issues []IssueDTO `json:"issues"`
}

// Board is the getBoard result.
type Board struct {
	Columns   []Column `json:"columns"`
	FetchedAt string   `json:"fetchedAt"`
}

const boardTTL = 60 * time.Second

var boardFields = []string{"summary", "status", "issuetype", "priority", "updated", "assignee", "project"}

func category(key string) string {
	switch key {
	case "new", "indeterminate", "done":
		return key
	default: // "undefined" (no category) and anything unexpected
		return "new"
	}
}

func categoryRank(c string) int {
	switch c {
	case "new":
		return 0
	case "indeterminate":
		return 1
	default:
		return 2
	}
}

func statusDTO(s *jira.Status) StatusDTO {
	if s == nil {
		return StatusDTO{Category: "new"}
	}
	return StatusDTO{ID: s.ID, Name: s.Name, Category: category(s.StatusCategory.Key)}
}

func rfc3339(jiraTime string) string {
	t, err := jira.ParseTime(jiraTime)
	if err != nil {
		return jiraTime
	}
	return t.Local().Format(time.RFC3339)
}

func issueDTO(c *jira.Client, is jira.Issue) IssueDTO {
	f := is.Fields
	d := IssueDTO{
		Key:     is.Key,
		Summary: f.Summary,
		URL:     c.BrowseURL(is.Key),
		Status:  statusDTO(f.Status),
		Updated: rfc3339(f.Updated),
	}
	if f.Project != nil {
		d.Project = f.Project.Key
	} else if i := strings.LastIndexByte(is.Key, '-'); i > 0 {
		d.Project = is.Key[:i]
	}
	if f.IssueType != nil {
		d.Type.Name = f.IssueType.Name
	}
	if f.Priority != nil {
		d.Priority.Name = f.Priority.Name
	}
	if f.Assignee != nil {
		d.Assignee = &AssigneeDTO{DisplayName: f.Assignee.DisplayName}
	}
	return d
}

// BuildColumns groups issues by status: columns ordered by category
// new → indeterminate → done, then by first appearance in issues.
func BuildColumns(issues []IssueDTO) []Column {
	type col struct {
		Column
		first int
	}
	var cols []*col
	byID := map[string]*col{}
	for i, is := range issues {
		id := is.Status.ID
		if id == "" {
			id = "name:" + is.Status.Name
		}
		c, ok := byID[id]
		if !ok {
			c = &col{Column: Column{Status: is.Status, Issues: []IssueDTO{}}, first: i}
			byID[id] = c
			cols = append(cols, c)
		}
		c.Issues = append(c.Issues, is)
	}
	sort.SliceStable(cols, func(i, j int) bool {
		ri, rj := categoryRank(cols[i].Status.Category), categoryRank(cols[j].Status.Category)
		if ri != rj {
			return ri < rj
		}
		return cols[i].first < cols[j].first
	})
	out := make([]Column, 0, len(cols))
	for _, c := range cols {
		out = append(out, c.Column)
	}
	return out
}

func (a *App) getBoard(ctx context.Context, force bool) (*Board, error) {
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	now := a.clock.Now()
	a.mu.Lock()
	if !force && a.board != nil && a.boardFor == id && now.Sub(a.boardAt) < boardTTL {
		b := a.board
		a.mu.Unlock()
		return b, nil
	}
	gen := a.boardGen
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	issues, err := c.Search(ctx, a.cfg.Get().BoardJQL, jira.SearchOptions{Fields: boardFields, Limit: 300})
	if err != nil {
		return nil, err
	}
	dtos := make([]IssueDTO, 0, len(issues))
	for _, is := range issues {
		dtos = append(dtos, issueDTO(c, is))
	}
	b := &Board{Columns: BuildColumns(dtos), FetchedAt: now.Format(time.RFC3339)}
	a.mu.Lock()
	// A transition (or settings change) during the fetch makes the result
	// possibly stale: return it, but do not cache it.
	if a.boardGen == gen {
		a.board, a.boardAt, a.boardFor = b, now, id
	}
	a.mu.Unlock()
	return b, nil
}

func (a *App) getTransitions(ctx context.Context, key string) ([]TransitionDTO, error) {
	c, _, err := a.client()
	if err != nil {
		return nil, err
	}
	if !jira.ValidIssueKey(key) {
		return nil, a.newErr(CodeBadParams, "err_invalid_key")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ts, err := c.Transitions(ctx, key)
	if err != nil {
		return nil, err
	}
	out := make([]TransitionDTO, 0, len(ts))
	for _, t := range ts {
		to := t.To
		out = append(out, TransitionDTO{ID: t.ID, Name: t.Name, To: statusDTO(&to)})
	}
	return out, nil
}

// transition performs a status transition and returns the fresh issue. Once
// Jira accepted the transition the call never fails: if re-reading the issue
// fails, a minimal issue with the transition's target status is returned and
// a "board" event asks the UI to refetch.
func (a *App) transition(ctx context.Context, key, transitionID string) (*IssueDTO, error) {
	c, _, err := a.client()
	if err != nil {
		return nil, err
	}
	transitionID = strings.TrimSpace(transitionID)
	if !jira.ValidIssueKey(key) || transitionID == "" {
		return nil, a.newErr(CodeBadParams, "err_transition_params")
	}
	tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// Look the transition up first: it validates the id and gives the
	// target status for the fallback result.
	ts, err := c.Transitions(tctx, key)
	if err != nil {
		return nil, err
	}
	var chosen *jira.Transition
	for i := range ts {
		if ts[i].ID == transitionID {
			chosen = &ts[i]
			break
		}
	}
	if chosen == nil {
		return nil, a.newErr(CodeBadParams, "err_transition_unavailable")
	}
	if err := c.DoTransition(tctx, key, transitionID); err != nil {
		return nil, err
	}
	a.invalidateBoard()

	gctx, gcancel := context.WithTimeout(ctx, 30*time.Second)
	defer gcancel()
	is, err := c.GetIssue(gctx, key, boardFields)
	if err != nil {
		log.Printf("transition %s → %q done, but re-reading the issue failed: %v", key, chosen.To.Name, err)
		to := chosen.To
		d := IssueDTO{Key: key, URL: c.BrowseURL(key), Status: statusDTO(&to)}
		if i := strings.LastIndexByte(key, '-'); i > 0 {
			d.Project = key[:i]
		}
		a.emit("board", struct{}{})
		return &d, nil
	}
	d := issueDTO(c, *is)
	return &d, nil
}

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

// IssueTypeDTO is the IssueType contract type.
type IssueTypeDTO struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"` // epic | story | task | subtask
	IconColor string `json:"iconColor,omitempty"`
}

// EpicChipDTO is Issue.epic: the Epic Link target.
type EpicChipDTO struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Color string `json:"color"` // Jira colour key, e.g. "ghx-label-4"
}

// IssueDTO is the Issue contract type.
type IssueDTO struct {
	ID          string       `json:"id"`
	Key         string       `json:"key"`
	Summary     string       `json:"summary"`
	URL         string       `json:"url"`
	Project     string       `json:"project"`
	Status      StatusDTO    `json:"status"`
	Type        IssueTypeDTO `json:"type"`
	Priority    NameDTO      `json:"priority"`
	Updated     string       `json:"updated"`
	Assignee    *UserDTO     `json:"assignee"`
	Mine        bool         `json:"mine"`
	Epic        *EpicChipDTO `json:"epic,omitempty"`
	StoryPoints *float64     `json:"storyPoints,omitempty"`
}

// IssueRef is the IssueRef contract type (a related issue).
type IssueRef struct {
	Key      string       `json:"key"`
	Summary  string       `json:"summary"`
	URL      string       `json:"url"`
	Status   StatusDTO    `json:"status"`
	Type     IssueTypeDTO `json:"type"`
	Assignee *UserDTO     `json:"assignee,omitempty"`
	Mine     bool         `json:"mine"`
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
	Scope     string   `json:"scope"`
	Truncated bool     `json:"truncated"`
	// Hidden (scope "mine" only) counts my issues left out because their
	// project is not tracked.
	Hidden *HiddenDTO `json:"hidden,omitempty"`
}

// HiddenDTO is Board.hidden.
type HiddenDTO struct {
	Count    int      `json:"count"`
	Projects []string `json:"projects"` // sorted project keys
}

// hiddenByTracking describes the issues filterTracked dropped.
func hiddenByTracking(all []jira.Issue, tracked []string) *HiddenDTO {
	h := &HiddenDTO{Projects: []string{}}
	set := trackedSet(tracked)
	if set == nil {
		return h
	}
	seen := map[string]bool{}
	for _, is := range all {
		if p := projectOf(is); !set[p] {
			h.Count++
			if !seen[p] && p != "" {
				seen[p] = true
				h.Projects = append(h.Projects, p)
			}
		}
	}
	sort.Strings(h.Projects)
	return h
}

// Board scopes.
const (
	ScopeMine = "mine"
	ScopeAll  = "all"
)

const (
	boardTTL      = 60 * time.Second
	mineBoardMax  = 300
	allBoardMax   = 500
	allBoardOrder = " AND statusCategory != Done ORDER BY updated DESC"
)

var boardFields = []string{"summary", "status", "issuetype", "priority", "updated", "assignee", "project"}

// AllBoardJQL is the "all" board query for the tracked projects.
func AllBoardJQL(tracked []string) string {
	return "project in (" + quoteKeys(tracked) + ")" + allBoardOrder
}

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

// parseScope maps "" to def and rejects anything but mine/all.
func (a *App) parseScope(scope, def string) (string, error) {
	switch scope {
	case "":
		return def, nil
	case ScopeMine, ScopeAll:
		return scope, nil
	}
	return "", a.newErr(CodeBadParams, "err_bad_scope")
}

func (a *App) getBoard(ctx context.Context, force bool, scope string) (*Board, error) {
	scope, err := a.parseScope(scope, ScopeMine)
	if err != nil {
		return nil, err
	}
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	key := cacheBoard + ":" + scope
	cached, gen, ok := a.cacheGet(key, id, boardTTL)
	if ok && !force {
		return cached.(*Board), nil
	}
	now := a.clock.Now()

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	tracked, err := a.trackedProjects(ctx, c, id, nil)
	if err != nil {
		return nil, err
	}
	b := &Board{Columns: []Column{}, FetchedAt: now.Format(time.RFC3339), Scope: scope}
	jql, limit := a.cfg.Get().BoardJQL, mineBoardMax
	if scope == ScopeAll {
		inJira, err := a.queryProjects(ctx, c, id, tracked)
		if err != nil {
			return nil, err
		}
		if len(inJira) == 0 {
			a.cachePut(key, id, gen, now, b)
			return b, nil
		}
		jql, limit = AllBoardJQL(inJira), allBoardMax
	}
	v, err := a.newView(ctx, c, id)
	if err != nil {
		return nil, err
	}
	res, err := c.SearchAll(ctx, jql, jira.SearchOptions{Fields: v.fields(boardFields, false), Limit: limit})
	if err != nil {
		return nil, err
	}
	issues := filterTracked(res.Issues, tracked)
	if scope == ScopeMine {
		b.Hidden = hiddenByTracking(res.Issues, tracked)
	}
	var epicKeys []string
	for i := range issues {
		epicKeys = append(epicKeys, v.epicLink(&issues[i]))
	}
	a.loadEpics(ctx, v, epicKeys)
	dtos := make([]IssueDTO, 0, len(issues))
	for _, is := range issues {
		dtos = append(dtos, v.issue(is))
	}
	b.Columns = BuildColumns(dtos)
	b.Truncated = res.Truncated()
	// A transition (or settings change) during the fetch makes the result
	// possibly stale: return it, but do not cache it.
	a.cachePut(key, id, gen, now, b)
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
	c, identity, err := a.client()
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
	v, err := a.cachedView(gctx, c, identity)
	var is *jira.Issue
	if err == nil {
		is, err = c.GetIssue(gctx, key, v.fields(boardFields, false))
	}
	if err != nil {
		log.Printf("transition %s → %q done, but re-reading the issue failed: %v", key, chosen.To.Name, err)
		to := chosen.To
		d := IssueDTO{Key: key, URL: c.BrowseURL(key), Status: statusDTO(&to), Type: IssueTypeDTO{Kind: KindTask}}
		if i := strings.LastIndexByte(key, '-'); i > 0 {
			d.Project = key[:i]
		}
		a.emit("board", struct{}{})
		return &d, nil
	}
	a.loadEpics(gctx, v, []string{v.epicLink(is)})
	d := v.issue(*is)
	return &d, nil
}

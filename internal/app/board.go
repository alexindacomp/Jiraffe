package app

import (
	"context"
	"log"
	"sort"
	"strconv"
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

// Column of the board. A status-mode column (no project, workflow
// fallback) holds one status: ID is the status id, Statuses = [Status]. An
// agile column ("col-<index>") holds the statuses of a board configuration
// column; Status is its first status (kept for compatibility).
type Column struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Category string      `json:"category"` // new | indeterminate | done
	Statuses []StatusDTO `json:"statuses"`
	Issues   []IssueDTO  `json:"issues"`
	Status   StatusDTO   `json:"status"`
}

// statusColumn is the empty status-mode column of s.
func statusColumn(s StatusDTO) Column {
	id := s.ID
	if id == "" {
		id = "name:" + s.Name
	}
	return Column{ID: id, Name: s.Name, Category: s.Category, Statuses: []StatusDTO{s}, Issues: []IssueDTO{}, Status: s}
}

// Board is the getBoard result.
type Board struct {
	Columns   []Column `json:"columns"`
	FetchedAt string   `json:"fetchedAt"`
	Scope     string   `json:"scope"`
	Truncated bool     `json:"truncated"`
	// Hidden (scopes "mine" and "reported" without a project) counts my issues left
	// out because their project is not tracked.
	Hidden *HiddenDTO `json:"hidden,omitempty"`
	// Layout describes the columns of a project board (project mode only).
	Layout *BoardLayoutDTO `json:"layout,omitempty"`
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
	// ScopeReported: issues I reported (reporter = currentUser()).
	ScopeReported = "reported"
)

// ReportedBoardJQL is the tracked-projects board of scope "reported" (the
// counterpart of the configurable "mine" boardJQL).
const ReportedBoardJQL = "reporter = currentUser() AND statusCategory != Done ORDER BY updated DESC"

const (
	boardTTL        = 60 * time.Second
	mineBoardMax    = 300
	allBoardMax     = 500
	projectBoardMax = 500 // both scopes of a project board
	allBoardOrder   = " AND statusCategory != Done ORDER BY updated DESC"
	rankOrder       = " ORDER BY Rank ASC"
	updatedOrder    = " ORDER BY updated DESC"
	// recentlyDone keeps issues done in the last 14 days on a project board.
	recentlyDone = "updated >= -14d"
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
		c, ok := byID[statusColumn(is.Status).ID]
		if !ok {
			c = &col{Column: statusColumn(is.Status), first: i}
			byID[c.ID] = c
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

// parseScope maps "" to def and rejects anything but mine/all/reported.
func (a *App) parseScope(scope, def string) (string, error) {
	switch scope {
	case "":
		return def, nil
	case ScopeMine, ScopeAll, ScopeReported:
		return scope, nil
	}
	return "", a.newErr(CodeBadParams, "err_bad_scope")
}

// projectParam validates a project parameter: "" stays "" (all tracked
// projects); anything else must be the key of a project I can browse
// (tracking is not required). It returns the key upper-cased.
func (a *App) projectParam(ctx context.Context, c *jira.Client, identity, project string) (string, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return "", nil
	}
	if !jira.ValidProjectKey(project) {
		return "", a.newErr(CodeProjectUnavailable, "err_project_unavailable")
	}
	projects, err := a.accessibleProjects(ctx, c, identity, false)
	if err != nil {
		return "", err
	}
	if !hasProject(projects, project) {
		return "", a.newErr(CodeProjectUnavailable, "err_project_unavailable")
	}
	return strings.ToUpper(project), nil
}

// boardCacheKey is the cache key of a board: scope, project ("" = tracked
// projects) and requested agile board id (0 = default).
func boardCacheKey(scope, project string, boardID int) string {
	return cacheBoard + ":" + scope + ":" + project + ":" + strconv.Itoa(boardID)
}

// ProjectBoardJQL is the query of a project board without ORDER BY: not-done
// issues plus those done in the last 14 days. "mine" uses the condition of
// boardJQL (its ORDER BY dropped) and adds my recently done issues.
func ProjectBoardJQL(scope, project, boardJQL string) string {
	p := "project = " + quoteKeys([]string{project})
	if scope == ScopeAll {
		return p + " AND (statusCategory != Done OR " + recentlyDone + ")"
	}
	if scope == ScopeReported {
		return p + " AND reporter = currentUser() AND (statusCategory != Done OR " + recentlyDone + ")"
	}
	if w := jqlWhere(boardJQL); w != "" {
		return p + " AND ((" + w + ") OR (assignee = currentUser() AND statusCategory = Done AND " + recentlyDone + "))"
	}
	return p + " AND assignee = currentUser() AND (statusCategory != Done OR " + recentlyDone + ")"
}

// jqlWhere returns the condition of a JQL query: the text before its
// ORDER BY clause (outside quoted strings), trimmed.
func jqlWhere(jql string) string {
	var quote byte
	for i := 0; i < len(jql); i++ {
		ch := jql[i]
		switch {
		case quote != 0:
			if ch == '\\' {
				i++
			} else if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'':
			quote = ch
		case (i == 0 || !jqlWordByte(jql[i-1])) && orderByAt(jql, i):
			return strings.TrimSpace(jql[:i])
		}
	}
	return strings.TrimSpace(jql)
}

// orderByAt reports whether the keywords ORDER BY (any case, any blanks
// between) start at jql[i].
func orderByAt(jql string, i int) bool {
	if len(jql)-i < len("order") || !strings.EqualFold(jql[i:i+5], "order") {
		return false
	}
	k := i + 5
	for k < len(jql) && (jql[k] == ' ' || jql[k] == '\t' || jql[k] == '\n' || jql[k] == '\r') {
		k++
	}
	if k == i+5 || len(jql)-k < 2 || !strings.EqualFold(jql[k:k+2], "by") {
		return false
	}
	return k+2 == len(jql) || !jqlWordByte(jql[k+2])
}

func jqlWordByte(b byte) bool {
	return b == '_' || b == '.' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// searchRanked runs where ORDER BY Rank ASC. A Jira without Jira Software
// rejects ordering by Rank (HTTP 400): the query is then retried ORDER BY
// updated DESC.
func searchRanked(ctx context.Context, c *jira.Client, where string, opts jira.SearchOptions) (*jira.SearchResult, error) {
	res, err := c.SearchAll(ctx, where+rankOrder, opts)
	if jira.IsCode(err, jira.CodeBadRequest) && ctx.Err() == nil {
		log.Printf("ordering by Rank was rejected, ordering by updated: %v", err)
		return c.SearchAll(ctx, where+updatedOrder, opts)
	}
	return res, err
}

func (a *App) getBoard(ctx context.Context, force bool, scope, project string, boardID int) (*Board, error) {
	scope, err := a.parseScope(scope, ScopeMine)
	if err != nil {
		return nil, err
	}
	if boardID < 0 {
		return nil, a.newErr(CodeBadParams, "err_bad_params")
	}
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if project, err = a.projectParam(ctx, c, id, project); err != nil {
		return nil, err
	}
	if project == "" {
		boardID = 0 // the agile board only matters for a project layout
	}
	key := boardCacheKey(scope, project, boardID)
	cached, gen, ok := a.cacheGet(key, id, boardTTL)
	if ok && !force {
		return cached.(*Board), nil
	}
	now := a.clock.Now()
	if project != "" {
		b, err := a.projectBoard(ctx, c, id, scope, project, boardID, force, now)
		if err != nil {
			return nil, err
		}
		a.cachePut(key, id, gen, now, b)
		return b, nil
	}

	tracked, err := a.trackedProjects(ctx, c, id, nil)
	if err != nil {
		return nil, err
	}
	b := &Board{Columns: []Column{}, FetchedAt: now.Format(time.RFC3339), Scope: scope}
	jql, limit := a.cfg.Get().BoardJQL, mineBoardMax
	if scope == ScopeReported {
		jql = ReportedBoardJQL
	}
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
	if scope != ScopeAll {
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

// projectBoard builds the board of one project: the columns of its layout
// (empty ones included) filled with its not-done and recently done issues.
// force also re-reads the layout.
func (a *App) projectBoard(ctx context.Context, c *jira.Client, id, scope, project string, boardID int, force bool, now time.Time) (*Board, error) {
	lay, err := a.projectLayout(ctx, c, id, project, boardID, force)
	if err != nil {
		return nil, err
	}
	v, err := a.newView(ctx, c, id)
	if err != nil {
		return nil, err
	}
	res, err := searchRanked(ctx, c, ProjectBoardJQL(scope, project, a.cfg.Get().BoardJQL),
		jira.SearchOptions{Fields: v.fields(boardFields, false), Limit: projectBoardMax})
	if err != nil {
		return nil, err
	}
	epicKeys := make([]string, 0, len(res.Issues))
	for i := range res.Issues {
		epicKeys = append(epicKeys, v.epicLink(&res.Issues[i]))
	}
	a.loadEpics(ctx, v, epicKeys)
	dtos := make([]IssueDTO, 0, len(res.Issues))
	for _, is := range res.Issues {
		dtos = append(dtos, v.issue(is))
	}
	layout := lay.dto
	return &Board{
		Columns:   lay.place(dtos),
		FetchedAt: now.Format(time.RFC3339),
		Scope:     scope,
		Truncated: res.Truncated(),
		Layout:    &layout,
	}, nil
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

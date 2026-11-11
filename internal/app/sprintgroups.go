package app

import (
	"context"
	"log"
	"sort"
	"strconv"
	"sync"
	"time"

	"jiraffe/internal/jira"
)

const (
	sprintGroupsTTL  = 2 * time.Minute
	maxBacklogIssues = 300
	// recentClosed is how many closed sprints includeClosed adds.
	recentClosed = 3
	// maxClosedBoards bounds the agile boards whose closed sprints are read.
	maxClosedBoards = 50
	// maxClosedQueries bounds the per-sprint issue queries of includeClosed
	// (sprints without issues of the selected projects are skipped).
	maxClosedQueries = 10
	// closedTimeout bounds the optional closed sprints part of one call.
	closedTimeout = 45 * time.Second
)

// SprintGroup is the SprintGroup contract type: the issues of one sprint
// (Sprint nil for the backlog) in Rank order.
type SprintGroup struct {
	Sprint      *SprintDTO `json:"sprint,omitempty"`
	Issues      []IssueDTO `json:"issues"`
	Progress    Progress   `json:"progress"`
	StoryPoints PointsDTO  `json:"storyPoints"`
	Truncated   bool       `json:"truncated"`
}

func newSprintGroup(s *SprintDTO, truncated bool) *SprintGroup {
	return &SprintGroup{Sprint: s, Issues: []IssueDTO{}, Truncated: truncated}
}

// add appends an issue and counts it (story points done = points of done
// issues).
func (g *SprintGroup) add(d IssueDTO) {
	g.Issues = append(g.Issues, d)
	g.Progress.add(d.Status.Category)
	if d.StoryPoints != nil {
		g.StoryPoints.Total += *d.StoryPoints
		if d.Status.Category == "done" {
			g.StoryPoints.Done += *d.StoryPoints
		}
	}
}

// SprintGroups is the getSprintGroups result.
type SprintGroups struct {
	Groups    []SprintGroup `json:"groups"`
	Backlog   *SprintGroup  `json:"backlog"` // null when empty
	FetchedAt string        `json:"fetchedAt"`
	Truncated bool          `json:"truncated"`
	// Available is false when Jira has no sprints (no Jira Software).
	Available bool `json:"available"`
	// ClosedUnavailable: includeClosed was asked for, but the closed
	// sprints could not be read (they are left out).
	ClosedUnavailable bool `json:"closedUnavailable,omitempty"`
}

// sprintIssue is a rendered issue with the sprints of its Sprint field.
type sprintIssue struct {
	issue   IssueDTO
	sprints []jira.Sprint
}

// Sprint group queries (SPEC v0.3), without the scope clause and ORDER BY.
const (
	openSprintsClause = " AND (sprint in openSprints() OR sprint in futureSprints())"
	backlogClause     = " AND statusCategory != Done AND (sprint is EMPTY OR sprint not in (openSprints(), futureSprints()))"
	mineClause        = " AND assignee = currentUser()"
)

func sprintGroupsKey(project, scope string, includeClosed bool) string {
	return cacheSprintGroups + ":" + project + ":" + scope + ":" + strconv.FormatBool(includeClosed)
}

func (a *App) getSprintGroups(ctx context.Context, project, scope string, includeClosed, force bool) (*SprintGroups, error) {
	scope, err := a.parseScope(scope, ScopeAll)
	if err != nil {
		return nil, err
	}
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if project, err = a.projectParam(ctx, c, id, project); err != nil {
		return nil, err
	}
	key := sprintGroupsKey(project, scope, includeClosed)
	cached, gen, ok := a.cacheGet(key, id, sprintGroupsTTL)
	if ok && !force {
		return cached.(*SprintGroups), nil
	}
	now := a.clock.Now()
	out := &SprintGroups{Groups: []SprintGroup{}, FetchedAt: now.Format(time.RFC3339), Available: true}
	projects := []string{project}
	if project == "" {
		if projects, err = a.trackedForQuery(ctx, c, id); err != nil {
			return nil, err
		}
	}
	if len(projects) == 0 {
		a.cachePut(key, id, gen, now, out)
		return out, nil
	}
	v, err := a.newView(ctx, c, id)
	if err != nil {
		return nil, err
	}
	sprintField := v.meta.fields.Sprint
	if sprintField == "" { // no Jira Software fields: nothing to group by
		out.Available = false
		a.cachePut(key, id, gen, now, out)
		return out, nil
	}
	opts := func(limit int) jira.SearchOptions {
		return jira.SearchOptions{Fields: append(v.fields(boardFields, false), sprintField), Limit: limit}
	}
	in := "project in (" + quoteKeys(projects) + ")"
	mine := ""
	if scope == ScopeMine {
		mine = mineClause
	}
	open, err := c.SearchAll(ctx, in+openSprintsClause+mine+rankOrder, opts(maxSprintIssues))
	if jira.IsCode(err, jira.CodeBadRequest) {
		log.Printf("sprint groups: open sprints query rejected, sprints unavailable: %v", err)
		out.Available = false
		a.cachePut(key, id, gen, now, out)
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	backlog, err := c.SearchAll(ctx, in+backlogClause+mine+rankOrder, opts(maxBacklogIssues))
	if err != nil {
		return nil, err
	}
	// Closed sprints are optional: when they cannot be read, the open
	// sprints and the backlog are returned without them (not cached).
	var closed []closedSprint
	closedFailed := false
	if includeClosed {
		cctx, ccancel := context.WithTimeout(ctx, closedTimeout)
		closed, err = a.recentClosedSprints(cctx, c, id, project, force, func(sprintID int) (*jira.SearchResult, error) {
			return c.SearchAll(cctx, in+" AND sprint = "+strconv.Itoa(sprintID)+mine+rankOrder, opts(maxSprintIssues))
		})
		ccancel()
		if err != nil {
			if parent.Err() != nil {
				return nil, parent.Err()
			}
			log.Printf("sprint groups: closed sprints left out: %v", err)
			closed, closedFailed = nil, true
			out.ClosedUnavailable = true
		}
	}

	var epicKeys []string
	lists := [][]jira.Issue{open.Issues, backlog.Issues}
	for _, cs := range closed {
		lists = append(lists, cs.issues)
	}
	for _, list := range lists {
		for i := range list {
			epicKeys = append(epicKeys, v.epicLink(&list[i]))
		}
	}
	a.loadEpics(ctx, v, epicKeys)
	render := func(list []jira.Issue) []sprintIssue {
		items := make([]sprintIssue, 0, len(list))
		for _, is := range list {
			items = append(items, sprintIssue{issue: v.issue(is), sprints: jira.ParseSprints(is.Fields.Custom[sprintField])})
		}
		return items
	}
	out.Groups = groupOpenSprints(render(open.Issues), open.Truncated())
	out.Truncated = open.Truncated()
	for _, cs := range closed {
		s := cs.sprint
		g := newSprintGroup(&s, cs.truncated)
		for _, is := range cs.issues {
			g.add(v.issue(is))
		}
		out.Groups = append(out.Groups, *g)
		out.Truncated = out.Truncated || cs.truncated
	}
	if len(backlog.Issues) > 0 {
		g := newSprintGroup(nil, backlog.Truncated())
		for _, it := range render(backlog.Issues) {
			g.add(it.issue)
		}
		out.Backlog = g
	}
	if !closedFailed {
		a.cachePut(key, id, gen, now, out)
	}
	return out, nil
}

// groupOpenSprints groups issues (in Rank order) by their active and future
// sprints: closed sprints an issue was carried over from are ignored, an
// issue in two open sprints is in both groups. Sprint metadata comes from
// the Sprint field. Order: active sprints by start date, then id; then
// future sprints by id.
func groupOpenSprints(items []sprintIssue, truncated bool) []SprintGroup {
	groups := map[int]*SprintGroup{}
	var sprints []jira.Sprint
	for _, it := range items {
		seen := map[int]bool{}
		for _, s := range it.sprints {
			if s.State != jira.SprintActive && s.State != jira.SprintFuture || seen[s.ID] {
				continue
			}
			seen[s.ID] = true
			g, ok := groups[s.ID]
			if !ok {
				d := sprintDTO(s, 0)
				g = newSprintGroup(&d, truncated)
				groups[s.ID] = g
				sprints = append(sprints, s)
			}
			g.add(it.issue)
		}
	}
	sort.SliceStable(sprints, func(i, j int) bool { return openSprintLess(sprints[i], sprints[j]) })
	out := make([]SprintGroup, 0, len(sprints))
	for _, s := range sprints {
		out = append(out, *groups[s.ID])
	}
	return out
}

func openSprintLess(a, b jira.Sprint) bool {
	if (a.State == jira.SprintActive) != (b.State == jira.SprintActive) {
		return a.State == jira.SprintActive
	}
	if a.State == jira.SprintActive {
		ta, ea := jira.ParseTime(a.StartDate)
		tb, eb := jira.ParseTime(b.StartDate)
		switch {
		case ea == nil && eb == nil && !ta.Equal(tb):
			return ta.Before(tb)
		case (ea == nil) != (eb == nil):
			return ea == nil // a known start date first
		}
	}
	return a.ID < b.ID
}

// closedSprint is a closed sprint with its issues of the selected projects.
type closedSprint struct {
	sprint    SprintDTO
	issues    []jira.Issue
	truncated bool // the sprint's own query hit its limit
}

// recentClosedSprints returns the recentClosed newest closed sprints of the
// projects' boards that have issues of the selected projects, newest first.
// query reads the issues of one sprint; sprints are queried one at a time,
// newest first, at most maxClosedQueries of them.
func (a *App) recentClosedSprints(ctx context.Context, c *jira.Client, identity, project string, force bool,
	query func(sprintID int) (*jira.SearchResult, error)) ([]closedSprint, error) {
	candidates, err := a.closedSprintCandidates(ctx, c, identity, project, force)
	if err != nil {
		return nil, err
	}
	var out []closedSprint
	for i, s := range candidates {
		if len(out) == recentClosed || i == maxClosedQueries {
			break
		}
		res, err := query(s.ID)
		if err != nil {
			return nil, err
		}
		if len(res.Issues) > 0 {
			out = append(out, closedSprint{sprint: s, issues: res.Issues, truncated: res.Truncated()})
		}
	}
	return out, nil
}

// closedSprintCandidates returns the newest closed sprints (at most
// maxClosedQueries) of all closed sprints of the agile boards of project
// ("" = the tracked projects' boards), deduplicated, newest first. force
// re-reads the board and sprint lists.
// Boards without sprints are skipped; without the agile API there are none.
func (a *App) closedSprintCandidates(ctx context.Context, c *jira.Client, identity, project string, force bool) ([]SprintDTO, error) {
	var boardIDs []int
	if project != "" {
		boards, err := a.projectBoards(ctx, c, identity, project, force)
		if permanent(err) {
			log.Printf("closed sprints: agile boards of %s: %v", project, err)
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		for _, b := range boards {
			boardIDs = append(boardIDs, b.ID)
		}
	} else {
		boards, err := a.getAgileBoards(ctx, force)
		if err != nil {
			return nil, err
		}
		for _, b := range boards {
			boardIDs = append(boardIDs, b.ID)
		}
	}
	if len(boardIDs) > maxClosedBoards {
		log.Printf("closed sprints: reading the first %d of %d agile boards", maxClosedBoards, len(boardIDs))
		boardIDs = boardIDs[:maxClosedBoards]
	}

	type result struct {
		lists *sprintLists
		err   error
	}
	results := make([]result, len(boardIDs))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < agileWorkers && w < len(boardIDs); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				l, err := a.boardSprints(ctx, c, identity, boardIDs[i], force)
				results[i] = result{lists: l, err: err}
			}
		}()
	}
feed:
	for i := range boardIDs {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	seen := map[int]bool{}
	var all []closedCandidate
	for i, r := range results {
		switch {
		case permanent(r.err):
			log.Printf("closed sprints of board %d: %v", boardIDs[i], r.err)
			continue
		case r.err != nil:
			return nil, r.err
		}
		// All closed sprints of the board: Jira's listing order says nothing
		// about when a sprint was completed.
		for _, s := range r.lists.closed {
			if s.State == jira.SprintClosed && !seen[s.ID] {
				seen[s.ID] = true
				all = append(all, newClosedCandidate(s, boardIDs[i]))
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].newer(all[j]) })
	if len(all) > maxClosedQueries {
		all = all[:maxClosedQueries] // only these can be queried
	}
	out := make([]SprintDTO, 0, len(all))
	for _, cand := range all {
		out = append(out, sprintDTO(cand.s, cand.board))
	}
	return out, nil
}

// closedCandidate is a closed sprint with its completion time parsed once.
type closedCandidate struct {
	s     jira.Sprint
	board int
	at    time.Time
	dated bool
}

// newClosedCandidate dates a closed sprint by completeDate, else endDate.
func newClosedCandidate(s jira.Sprint, board int) closedCandidate {
	cand := closedCandidate{s: s, board: board}
	for _, v := range []string{s.CompleteDate, s.EndDate} {
		if t, err := jira.ParseTime(v); err == nil {
			cand.at, cand.dated = t, true
			break
		}
	}
	return cand
}

// newer orders closed sprints newest first: by date (dated ones first),
// then by id descending.
func (c closedCandidate) newer(o closedCandidate) bool {
	switch {
	case c.dated && o.dated && !c.at.Equal(o.at):
		return c.at.After(o.at)
	case c.dated != o.dated:
		return c.dated
	}
	return c.s.ID > o.s.ID
}

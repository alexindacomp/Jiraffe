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

// Layout sources (Board.layout.source).
const (
	LayoutAgile    = "agile"    // columns of an agile board configuration
	LayoutWorkflow = "workflow" // one column per status of the project workflow
	LayoutIssues   = "issues"   // nothing known: columns from the issues' statuses
)

const (
	layoutTTL = time.Hour
	// maxProjectBoards bounds the agile boards read for one project.
	maxProjectBoards = 50
	// maxStatusLookups bounds the GET /status/{id} calls of one layout.
	maxStatusLookups = 50
	// otherColumnID is the trailing column of issues in no layout column.
	otherColumnID = "other"
)

// BoardLayoutDTO is Board.layout.
type BoardLayoutDTO struct {
	Source  string `json:"source"` // agile | workflow | issues
	Project string `json:"project"`
	// BoardID / BoardName: the chosen agile board whenever the project has
	// one (also when its columns fell back to the workflow).
	BoardID   int             `json:"boardId,omitempty"`
	BoardName string          `json:"boardName,omitempty"`
	Boards    []AgileBoardDTO `json:"boards"` // agile boards of the project; [] when none or no Agile API
}

// layoutColumn is a column of a project layout, without issues.
type layoutColumn struct {
	id, name, category string
	statuses           []StatusDTO
}

// boardLayout is the (cached) column layout of a project board. It is never
// modified after it was built.
type boardLayout struct {
	dto     BoardLayoutDTO
	columns []layoutColumn
	at      time.Time
	ttl     time.Duration // layoutTTL, metaRetryTTL after transient failures
}

// projectLayout returns the layout of a project board, cached per project
// and requested board for layoutTTL (metaRetryTTL when it had to fall back
// because of a transient failure); force rebuilds it. Builds for the same
// project run one at a time: a caller waiting for another build uses its
// result (also when forced, since it is fresh).
func (a *App) projectLayout(ctx context.Context, c *jira.Client, identity, project string, boardID int, force bool) (*boardLayout, error) {
	key := cacheLayout + ":" + project + ":" + strconv.Itoa(boardID)
	prev, _ := a.cachedLayout(key, identity)
	if prev != nil && !force {
		return prev, nil
	}
	unlock, err := a.lockLayout(ctx, project)
	if err != nil {
		return nil, err
	}
	defer unlock()
	l, gen := a.cachedLayout(key, identity)
	if l != nil && (!force || l != prev) {
		return l, nil // built by the call we waited for
	}
	now := a.clock.Now()
	l, err = a.buildLayout(ctx, c, identity, project, boardID, force)
	if err != nil {
		return nil, err
	}
	l.at = now
	a.cachePut(key, identity, gen, now, l)
	return l, nil
}

// lockLayout waits until no layout of project is being built (or ctx is
// done) and marks one as being built; unlock ends it.
func (a *App) lockLayout(ctx context.Context, project string) (unlock func(), err error) {
	for {
		a.mu.Lock()
		busy, ok := a.layoutBuilds[project]
		if !ok {
			if a.layoutBuilds == nil {
				a.layoutBuilds = map[string]chan struct{}{}
			}
			done := make(chan struct{})
			a.layoutBuilds[project] = done
			a.mu.Unlock()
			return func() {
				a.mu.Lock()
				delete(a.layoutBuilds, project)
				a.mu.Unlock()
				close(done)
			}, nil
		}
		a.mu.Unlock()
		select {
		case <-busy:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (a *App) cachedLayout(key, identity string) (*boardLayout, uint64) {
	v, gen, ok := a.cacheGet(key, identity, layoutTTL)
	if !ok {
		return nil, gen
	}
	l := v.(*boardLayout)
	if age := a.clock.Now().Sub(l.at); age >= l.ttl || age < 0 {
		return nil, gen
	}
	return l, gen
}

// buildLayout reads the layout of a project board: the configuration of
// its agile board, else its workflow statuses, else nothing (columns from
// the issues). Failures only degrade the layout; a cancelled ctx fails.
func (a *App) buildLayout(ctx context.Context, c *jira.Client, identity, project string, boardID int, force bool) (*boardLayout, error) {
	l := &boardLayout{dto: BoardLayoutDTO{Project: project, Boards: []AgileBoardDTO{}}, ttl: layoutTTL}
	degraded := func(what string, err error) {
		log.Printf("board layout of %s: %s: %v", project, what, err)
		if !permanent(err) {
			l.ttl = metaRetryTTL
		}
	}
	types, statusErr := c.ProjectStatuses(ctx, project)
	if statusErr != nil {
		degraded("project statuses", statusErr)
	}
	known := projectStatuses(types)

	boards, err := a.projectBoards(ctx, c, identity, project, force)
	if err != nil {
		degraded("agile boards", err)
	}
	if len(boards) > 0 {
		chosen := chooseBoard(boards, project, boardID)
		// The board picker stays available whatever the columns come from.
		l.dto.BoardID, l.dto.BoardName, l.dto.Boards = chosen.ID, chosen.Name, boardDTOs(boards, project)
		cfg, err := c.BoardConfiguration(ctx, chosen.ID)
		if err != nil {
			degraded("configuration of board "+strconv.Itoa(chosen.ID), err)
		} else {
			// Known project statuses also tell which statuses of a shared
			// board belong to this project.
			cols, transient := agileColumns(ctx, c, cfg.ColumnConfig.Columns, known, statusErr == nil && len(known) > 0)
			if transient {
				l.ttl = metaRetryTTL
			}
			if len(cols) > 0 {
				l.dto.Source, l.columns = LayoutAgile, cols
			} else {
				log.Printf("board layout of %s: board %d has no columns with statuses of the project", project, chosen.ID)
			}
		}
	}
	if l.columns == nil {
		if cols := workflowColumns(types); len(cols) > 0 {
			l.dto.Source, l.columns = LayoutWorkflow, cols
		} else {
			l.dto.Source = LayoutIssues
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err // the fallbacks may stem from the cancellation
	}
	return l, nil
}

// projectBoards returns the agile boards of a project ordered by id (at
// most maxProjectBoards), cached like the other agile lists; force reads
// them again.
func (a *App) projectBoards(ctx context.Context, c *jira.Client, identity, project string, force bool) ([]jira.AgileBoard, error) {
	key := cacheAgile + ":boards:" + project
	cached, gen, ok := a.cacheGet(key, identity, agileTTL)
	if ok && !force {
		return cached.([]jira.AgileBoard), nil
	}
	now := a.clock.Now()
	boards, err := c.Boards(ctx, project)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(boards, func(i, j int) bool { return boards[i].ID < boards[j].ID })
	if len(boards) > maxProjectBoards {
		boards = boards[:maxProjectBoards]
	}
	a.cachePut(key, identity, gen, now, boards)
	return boards, nil
}

// boardDTOs renders the boards of a project; a board without a location
// is attributed to the project.
func boardDTOs(boards []jira.AgileBoard, project string) []AgileBoardDTO {
	out := make([]AgileBoardDTO, 0, len(boards))
	for _, b := range boards {
		pk := project
		if b.Location != nil && b.Location.ProjectKey != "" {
			pk = b.Location.ProjectKey
		}
		out = append(out, AgileBoardDTO{ID: b.ID, Name: b.Name, Type: b.Type, ProjectKey: pk})
	}
	return out
}

// chooseBoard picks the board of a project layout: the requested one if it
// belongs to the project, else the first one located in the project, else
// the first one (boards are ordered by id). boards must not be empty.
func chooseBoard(boards []jira.AgileBoard, project string, want int) jira.AgileBoard {
	if want > 0 {
		for _, b := range boards {
			if b.ID == want {
				return b
			}
		}
	}
	for _, b := range boards {
		if b.Location != nil && strings.EqualFold(b.Location.ProjectKey, project) {
			return b
		}
	}
	return boards[0]
}

// projectStatuses indexes the statuses of a project by id.
func projectStatuses(types []jira.IssueTypeStatuses) map[string]StatusDTO {
	m := map[string]StatusDTO{}
	for _, t := range types {
		for i := range t.Statuses {
			if s := t.Statuses[i]; s.ID != "" {
				if _, ok := m[s.ID]; !ok {
					m[s.ID] = statusDTO(&s)
				}
			}
		}
	}
	return m
}

// agileColumns turns board configuration columns into layout columns. With
// onlyKnown (the project statuses are known) a column keeps only the
// statuses of known: the statuses of other projects on a shared board are
// removed. Otherwise ids are resolved via known, else GET /status/{id} (once
// per id), else name = id and category indeterminate. Columns left without
// statuses are dropped. transient reports a lookup that failed for a
// transient reason.
func agileColumns(ctx context.Context, c *jira.Client, cols []jira.BoardColumn, known map[string]StatusDTO, onlyKnown bool) (out []layoutColumn, transient bool) {
	resolved := make(map[string]StatusDTO, len(known))
	for k, v := range known {
		resolved[k] = v
	}
	lookups := 0
	resolve := func(id string) StatusDTO {
		if s, ok := resolved[id]; ok {
			return s
		}
		s := StatusDTO{ID: id, Name: id, Category: "indeterminate"}
		if lookups < maxStatusLookups && ctx.Err() == nil {
			lookups++
			st, err := c.StatusByID(ctx, id)
			switch {
			case err != nil:
				log.Printf("board layout: status %s: %v", id, err)
				transient = transient || !permanent(err)
			default:
				s = statusDTO(st)
				s.ID = id
				if s.Name == "" {
					s.Name = id
				}
			}
		}
		resolved[id] = s
		return s
	}
	for i, col := range cols {
		var statuses []StatusDTO
		seen := map[string]bool{}
		for _, r := range col.Statuses {
			if r.ID == "" || seen[r.ID] {
				continue
			}
			if _, ok := known[r.ID]; onlyKnown && !ok {
				continue // another project's status on a shared board
			}
			seen[r.ID] = true
			statuses = append(statuses, resolve(r.ID))
		}
		if len(statuses) == 0 {
			continue
		}
		out = append(out, layoutColumn{id: "col-" + strconv.Itoa(i), name: col.Name, statuses: statuses})
	}
	for i := range out {
		out[i].category = agileColumnCategory(out[i].statuses, i == len(out)-1)
	}
	return out, transient
}

// agileColumnCategory is the category of an agile board column: the shared
// category of its statuses; else "done" for the last column (empty columns
// already dropped) with at least one done status; else "indeterminate".
func agileColumnCategory(statuses []StatusDTO, last bool) string {
	c := columnCategory(statuses)
	if c != "indeterminate" || !last {
		return c
	}
	for _, s := range statuses {
		if s.Category == "done" {
			return "done"
		}
	}
	return c
}

// columnCategory is the category of a multi-status column: the category of
// its statuses if they all share one, else "indeterminate".
func columnCategory(statuses []StatusDTO) string {
	for _, s := range statuses[1:] {
		if s.Category != statuses[0].Category {
			return "indeterminate"
		}
	}
	return statuses[0].Category
}

// workflowColumns is the fallback layout: one column per distinct status of
// the project (all issue types, sub-tasks included), ordered by category
// new → indeterminate → done, then by first appearance.
func workflowColumns(types []jira.IssueTypeStatuses) []layoutColumn {
	var statuses []StatusDTO
	seen := map[string]bool{}
	for _, t := range types {
		for i := range t.Statuses {
			s := statusDTO(&t.Statuses[i])
			id := statusColumn(s).ID
			if s.ID == "" && s.Name == "" || seen[id] {
				continue
			}
			seen[id] = true
			statuses = append(statuses, s)
		}
	}
	sort.SliceStable(statuses, func(i, j int) bool {
		return categoryRank(statuses[i].Category) < categoryRank(statuses[j].Category)
	})
	out := make([]layoutColumn, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, layoutColumn{id: statusColumn(s).ID, name: s.Name, category: s.Category, statuses: []StatusDTO{s}})
	}
	return out
}

// place distributes issues (in query order) over the layout columns. Every
// layout column is returned, empty ones included; issues whose status is in
// no column go to a trailing "other" column, present only when non-empty.
// The "issues" layout groups by status like the board without a project.
func (l *boardLayout) place(issues []IssueDTO) []Column {
	if l.dto.Source == LayoutIssues {
		return BuildColumns(issues)
	}
	cols := make([]Column, 0, len(l.columns)+1)
	byID, byName := map[string]int{}, map[string]int{}
	for i, lc := range l.columns {
		cols = append(cols, Column{ID: lc.id, Name: lc.name, Category: lc.category,
			Statuses: lc.statuses, Issues: []IssueDTO{}, Status: lc.statuses[0]})
		for _, s := range lc.statuses {
			if _, ok := byID[s.ID]; !ok && s.ID != "" {
				byID[s.ID] = i // a status in two columns: the first one wins
			}
			if _, ok := byName[s.Name]; !ok && s.Name != "" {
				byName[s.Name] = i
			}
		}
	}
	other := Column{ID: otherColumnID, Statuses: []StatusDTO{}, Issues: []IssueDTO{}}
	otherSeen := map[string]bool{}
	for _, is := range issues {
		i, ok := byID[is.Status.ID]
		if is.Status.ID == "" {
			i, ok = byName[is.Status.Name] // no id: match by name
		}
		if ok {
			cols[i].Issues = append(cols[i].Issues, is)
			continue
		}
		if id := statusColumn(is.Status).ID; !otherSeen[id] {
			otherSeen[id] = true
			other.Statuses = append(other.Statuses, is.Status)
		}
		other.Issues = append(other.Issues, is)
	}
	if len(other.Issues) > 0 {
		other.Category = columnCategory(other.Statuses)
		other.Status = other.Statuses[0]
		cols = append(cols, other)
	}
	return cols
}

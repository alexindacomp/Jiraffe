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
	maxOpenSprints   = 200
	maxClosedSprints = 2000 // read to find the newest closed ones
	lastClosed       = 5
	maxSprintIssues  = 1000
)

// AgileBoardDTO is the AgileBoard contract type.
type AgileBoardDTO struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"` // scrum | kanban
	ProjectKey string `json:"projectKey"`
}

// Progress counts issues by status category.
type Progress struct {
	Done       int `json:"done"`
	InProgress int `json:"inProgress"`
	Todo       int `json:"todo"`
	Total      int `json:"total"`
}

func (p *Progress) add(category string) {
	p.Total++
	switch category {
	case "done":
		p.Done++
	case "indeterminate":
		p.InProgress++
	default:
		p.Todo++
	}
}

// PointsDTO is SprintGroup.storyPoints: story points of done issues and
// of all issues.
type PointsDTO struct {
	Done  float64 `json:"done"`
	Total float64 `json:"total"`
}

// agileWorkers bounds the concurrent per-project board requests.
const agileWorkers = 4

func (a *App) getAgileBoards(ctx context.Context, force bool) ([]AgileBoardDTO, error) {
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	const key = cacheAgile + ":tracked"
	cached, gen, ok := a.cacheGet(key, id, agileTTL)
	if ok && !force {
		return cached.([]AgileBoardDTO), nil
	}
	now := a.clock.Now()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tracked, err := a.trackedForQuery(ctx, c, id)
	if err != nil {
		return nil, err
	}
	out := []AgileBoardDTO{}
	if len(tracked) == 0 {
		a.cachePut(key, id, gen, now, out)
		return out, nil
	}
	// Only a missing agile API (no Jira Software) means "no sprints"; a 404
	// for one project is skipped like 400/403.
	if avail, err := c.AgileAvailable(ctx); err != nil {
		return nil, err
	} else if !avail {
		log.Printf("agile boards: the agile REST API is not available")
		a.cachePut(key, id, gen, now, out)
		return out, nil
	}

	type result struct {
		boards []jira.AgileBoard
		err    error
		done   bool
	}
	results := make([]result, len(tracked))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < agileWorkers && w < len(tracked); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				boards, err := c.Boards(ctx, tracked[i])
				results[i] = result{boards: boards, err: err, done: true}
			}
		}()
	}
feed:
	for i := range tracked {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	complete := true
	seen := map[int]bool{}
	for i, r := range results {
		p := tracked[i]
		switch {
		case !r.done:
			complete = false
			continue
		case jira.IsCode(r.err, jira.CodeNotFound) || jira.IsCode(r.err, jira.CodeBadRequest) || jira.IsCode(r.err, jira.CodeForbidden):
			log.Printf("agile boards of %s: %v", p, r.err)
			continue
		case r.err != nil:
			if ctx.Err() == nil {
				return nil, r.err
			}
			complete = false // timed out: keep what was collected
			continue
		}
		for _, b := range r.boards {
			if seen[b.ID] {
				continue
			}
			seen[b.ID] = true
			pk := p
			if b.Location != nil && b.Location.ProjectKey != "" {
				pk = b.Location.ProjectKey
			}
			out = append(out, AgileBoardDTO{ID: b.ID, Name: b.Name, Type: b.Type, ProjectKey: pk})
		}
	}
	if !complete {
		if len(out) == 0 {
			return nil, ctx.Err()
		}
		log.Printf("agile boards: timed out, returning the boards of the projects read so far")
		return out, nil // partial: not cached
	}
	a.cachePut(key, id, gen, now, out)
	return out, nil
}

// getSprints returns active, then future sprints (Jira's order), then the
// last 5 closed ones, newest first. Boards without sprints (kanban) give [].
func (a *App) getSprints(ctx context.Context, boardID int, force bool) ([]SprintDTO, error) {
	if boardID <= 0 {
		return nil, a.newErr(CodeBadParams, "err_bad_params")
	}
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	l, err := a.boardSprints(ctx, c, id, boardID, force)
	if err != nil {
		return nil, err
	}
	return OrderSprints(l.open, l.closed, boardID), nil
}

// sprintLists are all sprints of a board in Jira's order (closed ones
// oldest first by Jira's listing).
type sprintLists struct {
	open, closed []jira.Sprint
}

// boardSprints reads the open and all closed sprints of a board (finding
// the newest closed ones walks all of them), cached per board. A board
// without sprints (kanban, HTTP 400) has none.
func (a *App) boardSprints(ctx context.Context, c *jira.Client, identity string, boardID int, force bool) (*sprintLists, error) {
	key := cacheAgile + ":sprints:" + strconv.Itoa(boardID)
	cached, gen, ok := a.cacheGet(key, identity, agileTTL)
	if ok && !force {
		return cached.(*sprintLists), nil
	}
	now := a.clock.Now()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	open, err := c.BoardSprints(ctx, boardID, jira.SprintActive+","+jira.SprintFuture, maxOpenSprints)
	if jira.IsCode(err, jira.CodeBadRequest) {
		out := &sprintLists{} // "the board does not support sprints"
		a.cachePut(key, identity, gen, now, out)
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	closed, err := c.BoardSprints(ctx, boardID, jira.SprintClosed, maxClosedSprints)
	if err != nil {
		return nil, err
	}
	out := &sprintLists{open: open, closed: closed}
	a.cachePut(key, identity, gen, now, out)
	return out, nil
}

// OrderSprints puts active sprints first, then future ones, then the last 5
// closed sprints (Jira lists closed sprints oldest first) newest first.
func OrderSprints(open, closed []jira.Sprint, boardID int) []SprintDTO {
	out := []SprintDTO{}
	for _, state := range []string{jira.SprintActive, jira.SprintFuture} {
		for _, s := range open {
			if s.State == state {
				out = append(out, sprintDTO(s, boardID))
			}
		}
	}
	n := 0
	for i := len(closed) - 1; i >= 0 && n < lastClosed; i-- {
		if closed[i].State == jira.SprintClosed {
			out = append(out, sprintDTO(closed[i], boardID))
			n++
		}
	}
	return out
}

// sortRefs orders issues: not-done first (in progress, then to do), done
// last, then by key descending.
func sortRefs(refs []IssueRef) {
	sort.SliceStable(refs, func(i, j int) bool { return refLess(refs[i], refs[j]) })
}

func refLess(a, b IssueRef) bool {
	if ra, rb := treeRank(a.Status.Category), treeRank(b.Status.Category); ra != rb {
		return ra < rb
	}
	return keyGreater(a.Key, b.Key)
}

func treeRank(category string) int {
	switch category {
	case "indeterminate":
		return 0
	case "done":
		return 2
	default:
		return 1
	}
}

// keyGreater compares issue keys by project, then numerically.
func keyGreater(a, b string) bool {
	pa, na := splitKey(a)
	pb, nb := splitKey(b)
	if pa != pb {
		return pa > pb
	}
	return na > nb
}

func splitKey(k string) (string, int) {
	for i := len(k) - 1; i > 0; i-- {
		if k[i] == '-' {
			n, _ := strconv.Atoi(k[i+1:])
			return k[:i], n
		}
	}
	return k, 0
}

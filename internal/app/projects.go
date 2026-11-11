package app

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"time"

	"jiraffe/internal/config"
	"jiraffe/internal/jira"
)

// MyOpenJQL selects my not-done issues: their projects initialise the
// tracked projects and give Project.myOpen.
const MyOpenJQL = "assignee = currentUser() AND statusCategory != Done"

const myOpenMax = 1000

// ProjectDTO is the Project contract type.
type ProjectDTO struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Category string `json:"category,omitempty"`
	Lead     string `json:"lead,omitempty"`
	Tracked  bool   `json:"tracked"`
	MyOpen   int    `json:"myOpen"`
	// Available is false for a tracked key Jira no longer lists (access
	// revoked, archived, renamed): shown only so it can be untracked.
	Available bool `json:"available"`
}

// projectsData is the cached raw project list with my open issue counts.
type projectsData struct {
	projects []jira.Project
	myOpen   map[string]int
}

// myOpenCounts counts my not-done issues per project key.
func myOpenCounts(ctx context.Context, c *jira.Client) (map[string]int, error) {
	issues, err := c.Search(ctx, MyOpenJQL, jira.SearchOptions{Fields: []string{"project"}, Limit: myOpenMax})
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, is := range issues {
		if p := projectOf(is); p != "" {
			counts[p]++
		}
	}
	return counts, nil
}

// trackedProjects returns the tracked project keys. When they were never
// initialised they are set to the projects of my not-done issues (counts, or
// a query when counts is nil) and persisted. nil means "not initialised and
// nothing to initialise from": callers then do not filter by project.
func (a *App) trackedProjects(ctx context.Context, c *jira.Client, identity string, counts map[string]int) ([]string, error) {
	if t := a.cfg.Get().TrackedProjects; t != nil {
		return t, nil
	}
	a.trackMu.Lock()
	defer a.trackMu.Unlock()
	if t := a.cfg.Get().TrackedProjects; t != nil {
		return t, nil // initialised meanwhile
	}
	if counts == nil {
		var err error
		if counts, err = myOpenCounts(ctx, c); err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		if jira.ValidProjectKey(k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, nil // stays uninitialised; retried on the next need
	}
	// Most of my issues first, so the cap keeps the relevant projects.
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > config.MaxTrackedProjects {
		keys = keys[:config.MaxTrackedProjects]
	}
	url := strings.SplitN(identity, "\x00", 2)[0]
	s, err := a.cfg.UpdateTrackedProjects(url, func(cur []string) []string {
		if cur != nil {
			return nil // keep what is there
		}
		return keys
	})
	if err != nil {
		// Still filter this request; the next need retries persisting.
		if !errors.Is(err, config.ErrConnectionChanged) {
			log.Printf("save tracked projects: %v", err)
		}
		return config.NormalizeProjectKeys(keys), nil
	}
	if s.TrackedProjects == nil {
		return config.NormalizeProjectKeys(keys), nil
	}
	log.Printf("tracked projects initialised: %s", strings.Join(s.TrackedProjects, ", "))
	return s.TrackedProjects, nil
}

// trackedSet is the post-filter set; nil = no filter.
func trackedSet(tracked []string) map[string]bool {
	if tracked == nil {
		return nil
	}
	m := make(map[string]bool, len(tracked))
	for _, k := range tracked {
		m[strings.ToUpper(k)] = true
	}
	return m
}

// filterTracked keeps the issues of tracked projects (all when tracked is nil).
func filterTracked(issues []jira.Issue, tracked []string) []jira.Issue {
	set := trackedSet(tracked)
	if set == nil {
		return issues
	}
	out := issues[:0:0]
	for _, is := range issues {
		if set[projectOf(is)] {
			out = append(out, is)
		}
	}
	return out
}

// accessibleProjects returns the projects I can browse. The list is cached
// apart from the issue counts: issue changes do not invalidate it.
func (a *App) accessibleProjects(ctx context.Context, c *jira.Client, identity string, force bool) ([]jira.Project, error) {
	const key = cacheProjectList + ":all"
	cached, gen, ok := a.cacheGet(key, identity, projectsTTL)
	if ok && !force {
		return cached.([]jira.Project), nil
	}
	now := a.clock.Now()
	projects, err := c.Projects(ctx)
	if err != nil {
		return nil, err
	}
	a.cachePut(key, identity, gen, now, projects)
	return projects, nil
}

// projectsData returns the accessible projects and my open counts (cached
// separately; the counts are dropped whenever issues change).
func (a *App) projectsData(ctx context.Context, c *jira.Client, identity string, force bool) (*projectsData, error) {
	projects, err := a.accessibleProjects(ctx, c, identity, force)
	if err != nil {
		return nil, err
	}
	const key = cacheProjects + ":counts"
	cached, gen, ok := a.cacheGet(key, identity, projectsTTL)
	if ok && !force {
		return &projectsData{projects: projects, myOpen: cached.(map[string]int)}, nil
	}
	now := a.clock.Now()
	counts, err := myOpenCounts(ctx, c)
	if err != nil {
		return nil, err
	}
	a.cachePut(key, identity, gen, now, counts)
	return &projectsData{projects: projects, myOpen: counts}, nil
}

// trackedForQuery returns the tracked projects usable in queries (see
// queryProjects), initialising them lazily.
func (a *App) trackedForQuery(ctx context.Context, c *jira.Client, identity string) ([]string, error) {
	tracked, err := a.trackedProjects(ctx, c, identity, nil)
	if err != nil {
		return nil, err
	}
	return a.queryProjects(ctx, c, identity, tracked)
}

// queryProjects returns the tracked keys that Jira still knows (projects I
// can browse): a stale key (access revoked, project archived or renamed)
// would make Jira reject a whole "project in (…)" query. The result is
// empty when no tracked project is accessible.
func (a *App) queryProjects(ctx context.Context, c *jira.Client, identity string, tracked []string) ([]string, error) {
	if len(tracked) == 0 {
		return nil, nil
	}
	projects, err := a.accessibleProjects(ctx, c, identity, false)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(projects))
	for _, p := range projects {
		known[strings.ToUpper(p.Key)] = true
	}
	out := make([]string, 0, len(tracked))
	for _, k := range tracked {
		if known[strings.ToUpper(k)] {
			out = append(out, k)
		}
	}
	return out, nil
}

func (a *App) getProjects(ctx context.Context, force bool) ([]ProjectDTO, error) {
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	d, err := a.projectsData(ctx, c, id, force)
	if err != nil {
		return nil, err
	}
	tracked, err := a.trackedProjects(ctx, c, id, d.myOpen)
	if err != nil {
		return nil, err
	}
	return renderProjects(d, tracked), nil
}

// renderProjects renders the projects: tracked first, then by name. Tracked
// keys Jira no longer lists are included (name = key) so they can be
// untracked.
func renderProjects(d *projectsData, tracked []string) []ProjectDTO {
	set := trackedSet(tracked)
	out := make([]ProjectDTO, 0, len(d.projects))
	listed := make(map[string]bool, len(d.projects))
	for _, p := range d.projects {
		listed[strings.ToUpper(p.Key)] = true
		key := strings.ToUpper(p.Key)
		dto := ProjectDTO{Key: p.Key, Name: p.Name, Tracked: set[key], MyOpen: d.myOpen[key], Available: true}
		if p.ProjectCategory != nil {
			dto.Category = p.ProjectCategory.Name
		}
		if p.Lead != nil {
			dto.Lead = p.Lead.DisplayName
			if dto.Lead == "" {
				dto.Lead = p.Lead.Name
			}
		}
		out = append(out, dto)
	}
	for _, k := range tracked {
		if k = strings.ToUpper(k); !listed[k] {
			listed[k] = true
			out = append(out, ProjectDTO{Key: k, Name: k, Tracked: true, MyOpen: d.myOpen[k]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Tracked != out[j].Tracked {
			return out[i].Tracked
		}
		ni, nj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if ni != nj {
			return ni < nj
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func (a *App) setProjectTracked(ctx context.Context, key string, tracked bool) ([]ProjectDTO, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	if !jira.ValidProjectKey(key) {
		return nil, a.newErr(CodeBadParams, "err_invalid_project")
	}
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	d, err := a.projectsData(ctx, c, id, false)
	if err != nil {
		return nil, err
	}
	if tracked && !hasProject(d.projects, key) {
		// Only projects I can browse: an unknown key would break the JQL of
		// the "all" board (Jira rejects unknown projects).
		return nil, a.newErr(CodeBadParams, "err_invalid_project")
	}
	base, err := a.trackedProjects(ctx, c, id, d.myOpen)
	if err != nil {
		return nil, err
	}
	url := strings.SplitN(id, "\x00", 2)[0]
	s, err := a.cfg.UpdateTrackedProjects(url, func(cur []string) []string {
		if cur == nil {
			cur = append([]string{}, base...)
		}
		next := make([]string, 0, len(cur)+1)
		for _, k := range cur {
			if k != key {
				next = append(next, k)
			}
		}
		if tracked {
			next = append(next, key)
		}
		return next
	})
	var ve *config.ValidationError
	if errors.As(err, &ve) {
		e := &Error{Code: CodeValidation, Message: T(a.Language(), "errInvalidSettings"), Fields: ve.Fields}
		if len(ve.Fields) > 0 {
			e.Message = ve.Fields[0].Message
		}
		return nil, e
	}
	if errors.Is(err, config.ErrConnectionChanged) {
		return nil, a.newErr(CodeConflict, "err_connection_changed")
	}
	if err != nil {
		log.Printf("save tracked projects: %v", err)
		return nil, &Error{Code: CodeInternal, Message: T(a.Language(), "err_save_failed") + ": " + err.Error()}
	}
	a.dropCache(cacheBoard, cacheEpics, cacheStories, cacheAgile)
	a.emit("board", struct{}{})
	return renderProjects(d, s.TrackedProjects), nil
}

func hasProject(ps []jira.Project, key string) bool {
	for _, p := range ps {
		if strings.EqualFold(p.Key, key) {
			return true
		}
	}
	return false
}

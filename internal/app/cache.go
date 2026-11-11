package app

import (
	"context"
	"log"
	"strings"
	"time"

	"jiraffe/internal/jira"
)

// Cache key prefixes; an entry key is prefix + ":" + variant.
const (
	cacheBoard    = "board"
	cacheEpics    = "epics"
	cacheStories  = "stories"
	cacheProjects = "projects" // my open issue counts per project
	// cacheProjectList is the accessible project list; it does not change
	// with issues, so invalidateBoard keeps it.
	cacheProjectList = "projlist"
	cacheAgile       = "agile"
	// cacheSprintGroups is the getSprintGroups result; dropped with the boards.
	cacheSprintGroups = "sprintgroups"
	// cacheLayout is the column layout of a project board (metadata: issue
	// changes do not drop it).
	cacheLayout = "layout"
)

const (
	metaTTL     = time.Hour
	epicInfoTTL = 10 * time.Minute
	listTTL     = 60 * time.Second // epics / stories
	projectsTTL = 10 * time.Minute
	agileTTL    = 10 * time.Minute
)

// cacheEntry is a cached result for one connection identity.
type cacheEntry struct {
	val      any
	at       time.Time
	identity string
}

// cacheGet returns the entry stored under key for identity if it is younger
// than ttl, and the current generation (pass it to cachePut).
func (a *App) cacheGet(key, identity string, ttl time.Duration) (any, uint64, bool) {
	now := a.clock.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.cache[key]
	if ok && e.identity == identity && now.Sub(e.at) < ttl && !now.Before(e.at) {
		return e.val, a.cacheGen, true
	}
	return nil, a.cacheGen, false
}

// cachePut stores val unless the caches were invalidated since gen was read
// (the value may then be stale: it is returned to the caller, not cached).
func (a *App) cachePut(key, identity string, gen uint64, at time.Time, val any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cacheGen != gen {
		return
	}
	if a.cache == nil {
		a.cache = map[string]cacheEntry{}
	}
	a.cache[key] = cacheEntry{val: val, at: at, identity: identity}
}

// dropCache removes the entries with the given key prefixes and bumps the
// generation so fetches in flight do not store stale results.
func (a *App) dropCache(prefixes ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k := range a.cache {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p+":") {
				delete(a.cache, k)
				break
			}
		}
	}
	a.cacheGen++
}

// --- Jira metadata ---------------------------------------------------------

// jiraMeta is what the app learned about one Jira instance. It is never
// modified after publication (copy on write), so readers need no lock.
type jiraMeta struct {
	fields     jira.FieldIDs
	epicTypes  map[string]bool // issue type ids
	storyTypes map[string]bool
}

type metaEntry struct {
	m        *jiraMeta
	at       time.Time
	ttl      time.Duration
	identity string
}

// metaRetryTTL is how long metadata from a discovery with transient
// failures is used before discovery is tried again.
const metaRetryTTL = time.Minute

// Kinds of issues (IssueType.kind).
const (
	KindEpic    = "epic"
	KindStory   = "story"
	KindTask    = "task"
	KindSubtask = "subtask"
)

// Learning queries for the Epic / Story issue type ids: JQL accepts the
// canonical names even where responses carry localized ones.
const (
	learnEpicJQL  = "issuetype = Epic ORDER BY created DESC"
	learnStoryJQL = "issuetype = Story ORDER BY created DESC"
)

// cachedMeta returns the cached metadata of identity if still fresh.
func (a *App) cachedMeta(identity string, now time.Time) *jiraMeta {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.meta; e != nil && e.identity == identity && now.Sub(e.at) < e.ttl && !now.Before(e.at) {
		return e.m
	}
	return nil
}

// permanent reports whether a discovery failure is a property of the Jira
// (feature absent or forbidden) rather than a transient problem.
func permanent(err error) bool {
	return jira.IsCode(err, jira.CodeBadRequest) || jira.IsCode(err, jira.CodeNotFound) || jira.IsCode(err, jira.CodeForbidden)
}

// metaFor returns the custom field ids and issue type ids of the connection,
// cached for metaTTL (metaRetryTTL after transient failures). Missing pieces
// degrade to "unknown" (empty ids). Concurrent first calls share one
// discovery.
func (a *App) metaFor(ctx context.Context, c *jira.Client, identity string) (*jiraMeta, error) {
	if m := a.cachedMeta(identity, a.clock.Now()); m != nil {
		return m, nil
	}
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	now := a.clock.Now()
	if m := a.cachedMeta(identity, now); m != nil {
		return m, nil // discovered by the call we waited for
	}

	m := &jiraMeta{epicTypes: map[string]bool{}, storyTypes: map[string]bool{}}
	ttl := metaTTL
	defs, err := c.Fields(ctx)
	switch {
	case err == nil:
		m.fields = jira.DiscoverFields(defs)
	case permanent(err): // 400 / 403 / 404: the field list is not offered here
		log.Printf("jira fields unavailable, epics/sprints/story points disabled: %v", err)
	default:
		return nil, err
	}
	if types, err := c.IssueTypes(ctx); err != nil {
		log.Printf("jira issue types: %v", err)
		if !permanent(err) {
			ttl = metaRetryTTL
		}
	} else {
		for _, t := range types {
			switch {
			case t.Subtask:
			case strings.EqualFold(t.Name, "Epic"):
				m.epicTypes[t.ID] = true
			case strings.EqualFold(t.Name, "Story"):
				m.storyTypes[t.ID] = true
			}
		}
	}
	for _, q := range []struct {
		jql  string
		into map[string]bool
	}{{learnEpicJQL, m.epicTypes}, {learnStoryJQL, m.storyTypes}} {
		// A Jira without the type rejects the query (HTTP 400): nothing to learn.
		issues, err := c.Search(ctx, q.jql, jira.SearchOptions{Fields: []string{"issuetype"}, Limit: 1})
		if err != nil {
			if !permanent(err) {
				log.Printf("learn issue types (%s): %v", q.jql, err)
				ttl = metaRetryTTL
			}
			continue
		}
		for _, is := range issues {
			if t := is.Fields.IssueType; t != nil && t.ID != "" && !t.Subtask {
				q.into[t.ID] = true
			}
		}
	}
	// Stored by identity: a connection change meanwhile leaves an entry no
	// lookup matches.
	a.mu.Lock()
	a.meta = &metaEntry{m: m, at: now, ttl: ttl, identity: identity}
	a.mu.Unlock()
	return m, nil
}

// learnTypes records the issue type ids of issues returned by an
// "issuetype = Epic|Story" query.
func (a *App) learnTypes(identity, kind string, issues []jira.Issue) *jiraMeta {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.meta
	if e == nil || e.identity != identity {
		return nil
	}
	cur := e.m.epicTypes
	if kind == KindStory {
		cur = e.m.storyTypes
	}
	var add []string
	for _, is := range issues {
		if t := is.Fields.IssueType; t != nil && t.ID != "" && !t.Subtask && !cur[t.ID] {
			add = append(add, t.ID)
		}
	}
	if len(add) == 0 {
		return e.m
	}
	next := &jiraMeta{fields: e.m.fields, epicTypes: e.m.epicTypes, storyTypes: e.m.storyTypes}
	grown := make(map[string]bool, len(cur)+len(add))
	for id := range cur {
		grown[id] = true
	}
	for _, id := range add {
		grown[id] = true
	}
	if kind == KindStory {
		next.storyTypes = grown
	} else {
		next.epicTypes = grown
	}
	a.meta = &metaEntry{m: next, at: e.at, ttl: e.ttl, identity: identity}
	return next
}

// --- epic names and colours --------------------------------------------------

// epicInfo is what an epic chip / epic reference needs.
type epicInfo struct {
	ref   IssueRef
	name  string
	color string
}

type epicCache struct {
	identity string
	at       time.Time
	m        map[string]epicInfo
}

// rememberEpics stores epic infos for later chips.
func (a *App) rememberEpics(identity string, infos map[string]epicInfo) {
	if len(infos) == 0 {
		return
	}
	now := a.clock.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.epics.identity != identity || now.Sub(a.epics.at) >= epicInfoTTL || now.Before(a.epics.at) {
		a.epics = epicCache{identity: identity, at: now, m: map[string]epicInfo{}}
	}
	for k, v := range infos {
		a.epics.m[k] = v
	}
}

// cachedEpics returns the known epic infos among keys and the missing keys.
func (a *App) cachedEpics(identity string, keys []string) (map[string]epicInfo, []string) {
	now := a.clock.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	fresh := a.epics.identity == identity && now.Sub(a.epics.at) < epicInfoTTL && !now.Before(a.epics.at)
	got := map[string]epicInfo{}
	var missing []string
	for _, k := range keys {
		if e, ok := a.epics.m[k]; fresh && ok {
			got[k] = e
		} else {
			missing = append(missing, k)
		}
	}
	return got, missing
}

// --- view: turning Jira issues into contract types ---------------------------

// view holds what is needed to render issues of one request.
type view struct {
	c           *jira.Client
	identity    string
	me          *jira.User
	meta        *jiraMeta
	hoursPerDay float64
	epics       map[string]epicInfo // Epic Link target → info
}

// newView fetches (or reuses) the current user and the Jira metadata.
func (a *App) newView(ctx context.Context, c *jira.Client, identity string) (*view, error) {
	me, err := a.me(ctx, c, identity)
	if err != nil {
		return nil, err
	}
	m, err := a.metaFor(ctx, c, identity)
	if err != nil {
		return nil, err
	}
	return &view{c: c, identity: identity, me: me, meta: m, hoursPerDay: a.cfg.Get().HoursPerDay, epics: map[string]epicInfo{}}, nil
}

// cachedView is newView without metadata discovery: it uses the cached
// metadata, or none (kinds by name only, no custom fields) when there is
// none yet. For quick single-issue paths such as transitions.
func (a *App) cachedView(ctx context.Context, c *jira.Client, identity string) (*view, error) {
	me, err := a.me(ctx, c, identity)
	if err != nil {
		return nil, err
	}
	m := &jiraMeta{}
	a.mu.Lock()
	if e := a.meta; e != nil && e.identity == identity {
		m = e.m
	}
	a.mu.Unlock()
	return &view{c: c, identity: identity, me: me, meta: m, hoursPerDay: a.cfg.Get().HoursPerDay, epics: map[string]epicInfo{}}, nil
}

// fields returns base plus the custom fields the view understands.
func (v *view) fields(base []string, withEpicInfo bool) []string {
	out := append([]string{}, base...)
	f := v.meta.fields
	for _, id := range []string{f.EpicLink, f.StoryPoints} {
		if id != "" {
			out = append(out, id)
		}
	}
	if withEpicInfo {
		for _, id := range []string{f.EpicName, f.EpicColor} {
			if id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

func (v *view) kind(t *jira.IssueType) string {
	switch {
	case t == nil:
		return KindTask
	case t.Subtask:
		return KindSubtask
	case v.meta.epicTypes[t.ID]:
		return KindEpic
	case v.meta.storyTypes[t.ID]:
		return KindStory
	// Ids not learned (yet): canonical English names still identify the type.
	case strings.EqualFold(t.Name, "Epic"):
		return KindEpic
	case strings.EqualFold(t.Name, "Story"):
		return KindStory
	}
	return KindTask
}

func (v *view) issueType(t *jira.IssueType) IssueTypeDTO {
	d := IssueTypeDTO{Kind: v.kind(t)}
	if t != nil {
		d.Name = t.Name
	}
	return d
}

func (v *view) epicLink(is *jira.Issue) string {
	k := strings.TrimSpace(is.Fields.CustomString(v.meta.fields.EpicLink))
	if !jira.ValidIssueKey(k) {
		return ""
	}
	return k
}

func (v *view) storyPoints(is *jira.Issue) *float64 {
	return is.Fields.CustomNumber(v.meta.fields.StoryPoints)
}

func userDTO(u *jira.User) *UserDTO {
	if u == nil {
		return nil
	}
	d := &UserDTO{Name: u.Name, DisplayName: u.DisplayName}
	if d.DisplayName == "" {
		d.DisplayName = u.Name
	}
	return d
}

// ref renders an IssueRef.
func (v *view) ref(is jira.Issue) IssueRef {
	f := is.Fields
	return IssueRef{
		Key:      is.Key,
		Summary:  f.Summary,
		URL:      v.c.BrowseURL(is.Key),
		Status:   statusDTO(f.Status),
		Type:     v.issueType(f.IssueType),
		Assignee: userDTO(f.Assignee),
		Mine:     v.me.Same(f.Assignee),
	}
}

// issue renders an Issue (the epic chip comes from v.epics).
func (v *view) issue(is jira.Issue) IssueDTO {
	f := is.Fields
	d := IssueDTO{
		ID:          is.ID,
		Key:         is.Key,
		Summary:     f.Summary,
		URL:         v.c.BrowseURL(is.Key),
		Project:     projectOf(is),
		Status:      statusDTO(f.Status),
		Type:        v.issueType(f.IssueType),
		Updated:     rfc3339(f.Updated),
		Assignee:    userDTO(f.Assignee),
		Mine:        v.me.Same(f.Assignee),
		StoryPoints: v.storyPoints(&is),
	}
	if f.Priority != nil {
		d.Priority.Name = f.Priority.Name
	}
	if k := v.epicLink(&is); k != "" {
		e := v.epics[k]
		d.Epic = &EpicChipDTO{Key: k, Name: e.name, Color: e.color}
	}
	return d
}

// epicInfoOf builds the epic info of an epic issue (fetched with the epic
// name and colour fields). The name falls back to the summary.
func (v *view) epicInfoOf(is jira.Issue) epicInfo {
	name := is.Fields.CustomString(v.meta.fields.EpicName)
	if name == "" {
		name = is.Fields.Summary
	}
	return epicInfo{ref: v.ref(is), name: name, color: is.Fields.CustomString(v.meta.fields.EpicColor)}
}

// loadEpics fills v.epics for keys from the cache and, for the rest, from
// Jira. Failures only cost the names and colours (logged).
func (a *App) loadEpics(ctx context.Context, v *view, keys []string) {
	keys = uniqueKeys(keys)
	if len(keys) == 0 {
		return
	}
	got, missing := a.cachedEpics(v.identity, keys)
	for k, e := range got {
		v.epics[k] = e
	}
	if len(missing) == 0 {
		return
	}
	fetched := map[string]epicInfo{}
	issues, _, err := searchKeys(ctx, v.c, missing, v.fields(epicFields, true), len(missing))
	if err != nil {
		log.Printf("load epic names: %v", err)
	}
	for _, is := range issues {
		fetched[is.Key] = v.epicInfoOf(is)
	}
	for k, e := range fetched {
		v.epics[k] = e
	}
	a.rememberEpics(v.identity, fetched)
}

var epicFields = []string{"summary", "status", "issuetype", "assignee"}

// keyChunk bounds the keys of one "issuekey in (…)" query (URL length).
const keyChunk = 100

// searchKeys fetches issues by key in chunks, up to limit issues; truncated
// reports that the limit cut the list. Keys Jira does not know or hides are
// skipped (validateQuery=false); a failing chunk does not stop the others.
// The first error is returned together with everything that was read.
func searchKeys(ctx context.Context, c *jira.Client, keys []string, fields []string, limit int) ([]jira.Issue, bool, error) {
	var out []jira.Issue
	var firstErr error
	for i := 0; i < len(keys); i += keyChunk {
		if len(out) >= limit {
			return out, true, firstErr
		}
		if ctx.Err() != nil {
			return out, false, ctx.Err()
		}
		end := i + keyChunk
		if end > len(keys) {
			end = len(keys)
		}
		list := quoteKeys(keys[i:end])
		if list == "" {
			continue
		}
		res, err := c.SearchAll(ctx, "issuekey in ("+list+")",
			jira.SearchOptions{Fields: fields, Limit: limit - len(out), NoValidateQuery: true})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, res.Issues...)
		if res.Truncated() {
			return out, true, firstErr
		}
	}
	return out, false, firstErr
}

// quoteKeys renders validated issue or project keys as a JQL list. Keys
// that are neither are dropped, so nothing else can reach the query.
func quoteKeys(keys []string) string {
	q := make([]string, 0, len(keys))
	for _, k := range keys {
		if jira.ValidIssueKey(k) || jira.ValidProjectKey(k) {
			q = append(q, `"`+k+`"`)
		}
	}
	return strings.Join(q, ",")
}

func uniqueKeys(keys []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// projectOf returns the project key of an issue (field, else key prefix).
func projectOf(is jira.Issue) string {
	if p := is.Fields.Project; p != nil && p.Key != "" {
		return strings.ToUpper(p.Key)
	}
	if i := strings.LastIndexByte(is.Key, '-'); i > 0 {
		return strings.ToUpper(is.Key[:i])
	}
	return ""
}

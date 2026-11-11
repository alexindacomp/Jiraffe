package app

import (
	"context"
	"log"
	"strconv"
	"time"

	"jiraffe/internal/jira"
)

// Limits of one getEpics / getStories call (SPEC v0.2).
const (
	maxTreeTop      = 300  // epics, stories
	maxTreeChildren = 1000 // issues below them
)

// treeFields are read for every issue of the epic/story tree.
var treeFields = []string{"summary", "status", "issuetype", "assignee", "project", "issuelinks", "subtasks"}

// StoryNode is the StoryNode contract type.
type StoryNode struct {
	Story       IssueRef    `json:"story"`
	Epic        *EpicRefDTO `json:"epic,omitempty"` // IssueRef + {name, color}
	Tasks       []IssueRef  `json:"tasks"`
	Progress    Progress    `json:"progress"`
	StoryPoints *float64    `json:"storyPoints,omitempty"`
	mine        bool        // the story or one of its tasks is mine
	taskKeys    []string    // distinct task keys (epic progress)
	categories  map[string]string
}

// EpicNode is the EpicNode contract type.
type EpicNode struct {
	Epic     EpicRefDTO  `json:"epic"`
	Stories  []StoryNode `json:"stories"`
	Tasks    []IssueRef  `json:"tasks"`
	Progress Progress    `json:"progress"`
}

// EpicsResult is the getEpics result.
type EpicsResult struct {
	Epics         []EpicNode  `json:"epics"`
	OrphanStories []StoryNode `json:"orphanStories"`
	FetchedAt     string      `json:"fetchedAt"`
	Truncated     bool        `json:"truncated"`
}

// StoriesResult is the getStories result.
type StoriesResult struct {
	Stories   []StoryNode `json:"stories"`
	FetchedAt string      `json:"fetchedAt"`
	Truncated bool        `json:"truncated"`
}

// treeNode is one issue of the tree with the relations the rules need.
type treeNode struct {
	is       jira.Issue
	ref      IssueRef
	epicLink string
	full     bool // read by a search (not embedded in another issue)
	// related are the keys of sub-tasks and linked issues that are neither
	// epics nor stories, in Jira's order (sub-tasks first).
	related []string
}

// treePool holds every issue of one tree call by key.
type treePool struct {
	v     *view
	nodes map[string]*treeNode
}

func newTreePool(v *view) *treePool { return &treePool{v: v, nodes: map[string]*treeNode{}} }

// add records an issue (a fuller copy replaces an embedded one).
func (p *treePool) add(is jira.Issue, full bool) *treeNode {
	if n, ok := p.nodes[is.Key]; ok && (n.full || !full) {
		return n
	}
	n := &treeNode{is: is, ref: p.v.ref(is), epicLink: p.v.epicLink(&is), full: full}
	seen := map[string]bool{}
	for _, st := range is.Fields.Subtasks {
		if st.Key != "" && !seen[st.Key] {
			seen[st.Key] = true
			n.related = append(n.related, st.Key)
		}
	}
	for _, l := range is.Fields.IssueLinks {
		other := l.OutwardIssue
		if other == nil {
			other = l.InwardIssue
		}
		if other == nil || other.Key == "" || seen[other.Key] {
			continue
		}
		if k := p.v.kind(other.Fields.IssueType); k == KindEpic || k == KindStory {
			continue
		}
		seen[other.Key] = true
		n.related = append(n.related, other.Key)
	}
	p.nodes[is.Key] = n
	return n
}

// addEmbedded records the sub-tasks and linked issues embedded in n (they
// lack the assignee) unless they are known already.
func (p *treePool) addEmbedded(n *treeNode) {
	for _, st := range n.is.Fields.Subtasks {
		if _, ok := p.nodes[st.Key]; !ok && st.Key != "" {
			p.add(st, false)
		}
	}
	for _, l := range n.is.Fields.IssueLinks {
		for _, other := range []*jira.Issue{l.OutwardIssue, l.InwardIssue} {
			if other != nil && other.Key != "" {
				if _, ok := p.nodes[other.Key]; !ok {
					p.add(*other, false)
				}
			}
		}
	}
}

// storyNode builds a story with its tasks: sub-tasks and linked issues that
// are neither epics nor stories, deduplicated.
func (p *treePool) storyNode(s *treeNode) StoryNode {
	sn := StoryNode{Story: s.ref, Tasks: []IssueRef{}, StoryPoints: p.v.storyPoints(&s.is), mine: s.ref.Mine,
		categories: map[string]string{}}
	for _, k := range s.related {
		t, ok := p.nodes[k]
		if !ok || t.ref.Type.Kind == KindEpic || t.ref.Type.Kind == KindStory {
			continue
		}
		sn.Tasks = append(sn.Tasks, t.ref)
		sn.taskKeys = append(sn.taskKeys, k)
		sn.categories[k] = t.ref.Status.Category
		sn.Progress.add(t.ref.Status.Category)
		sn.mine = sn.mine || t.ref.Mine
	}
	sortRefs(sn.Tasks)
	return sn
}

// epicNode builds an epic: its stories (Epic Link) and its other issues
// (Epic Link, not a story, not a task of one of the epic's stories).
// children are the pool nodes with this Epic Link, in any order.
func (p *treePool) epicNode(e *treeNode, children []*treeNode, info epicInfo) (EpicNode, bool) {
	en := EpicNode{Epic: EpicRefDTO{IssueRef: e.ref, Name: info.name, Color: info.color}, Stories: []StoryNode{}, Tasks: []IssueRef{}}
	mine := e.ref.Mine
	counted := map[string]bool{}
	count := func(key, category string) {
		if !counted[key] {
			counted[key] = true
			en.Progress.add(category)
		}
	}
	underStory := map[string]bool{}
	for _, ch := range children {
		if ch.ref.Type.Kind != KindStory {
			continue
		}
		sn := p.storyNode(ch)
		en.Stories = append(en.Stories, sn)
		mine = mine || sn.mine
		count(ch.ref.Key, ch.ref.Status.Category)
		for _, k := range sn.taskKeys {
			underStory[k] = true
			count(k, sn.categories[k])
		}
	}
	for _, ch := range children {
		k := ch.ref.Type.Kind
		if k == KindStory || k == KindEpic || underStory[ch.ref.Key] {
			continue
		}
		en.Tasks = append(en.Tasks, ch.ref)
		mine = mine || ch.ref.Mine
		count(ch.ref.Key, ch.ref.Status.Category)
	}
	sortStories(en.Stories)
	sortRefs(en.Tasks)
	return en, mine
}

func sortStories(s []StoryNode) {
	for i := 1; i < len(s); i++ { // insertion sort: stable, lists are short
		for j := i; j > 0 && refLess(s[j].Story, s[j-1].Story); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// fetchRelated reads the sub-tasks and linked issues of the stories that
// are not in the pool yet (for their assignee), up to budget issues. Those
// that cannot be read stay as embedded in the stories. It returns whether
// the budget cut the list.
func (p *treePool) fetchRelated(ctx context.Context, stories []*treeNode, budget int) bool {
	var missing []string
	for _, s := range stories {
		for _, k := range s.related {
			if n, ok := p.nodes[k]; !ok || !n.full {
				missing = append(missing, k)
			}
		}
	}
	missing = uniqueKeys(missing)
	truncated := false
	if len(missing) > budget {
		missing, truncated = missing[:budget], true
	}
	issues, cut, err := searchKeys(ctx, p.v.c, missing, p.v.fields(treeFields, false), budget)
	if err != nil {
		log.Printf("epics: read linked issues: %v", err)
	}
	for _, is := range issues {
		p.add(is, true)
	}
	for _, s := range stories {
		p.addEmbedded(s)
	}
	return truncated || cut
}

// projectClause is the JQL restriction to the tracked projects.
func projectClause(tracked []string) string { return "project in (" + quoteKeys(tracked) + ")" }

func notDone(includeDone bool) string {
	if includeDone {
		return ""
	}
	return " AND statusCategory != Done"
}

func treeCacheKey(prefix string, includeDone bool, scope string) string {
	return prefix + ":" + strconv.FormatBool(includeDone) + ":" + scope
}

func (a *App) getEpics(ctx context.Context, includeDone bool, scope string, force bool) (*EpicsResult, error) {
	scope, err := a.parseScope(scope, ScopeAll)
	if err != nil {
		return nil, err
	}
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	key := treeCacheKey(cacheEpics, includeDone, scope)
	cached, gen, ok := a.cacheGet(key, id, listTTL)
	if ok && !force {
		return cached.(*EpicsResult), nil
	}
	now := a.clock.Now()
	out := &EpicsResult{Epics: []EpicNode{}, OrphanStories: []StoryNode{}, FetchedAt: now.Format(time.RFC3339)}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tracked, err := a.trackedForQuery(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if len(tracked) == 0 {
		a.cachePut(key, id, gen, now, out)
		return out, nil
	}
	v, err := a.newView(ctx, c, id)
	if err != nil {
		return nil, err
	}
	fields := v.fields(treeFields, true)
	epics, err := c.SearchAll(ctx, "issuetype = Epic AND "+projectClause(tracked)+notDone(includeDone)+" ORDER BY key DESC",
		jira.SearchOptions{Fields: fields, Limit: maxTreeTop})
	if err != nil {
		return nil, err
	}
	out.Truncated = epics.Truncated()
	if m := a.learnTypes(id, KindEpic, epics.Issues); m != nil {
		v.meta = m
	}
	link := jira.ClauseRef(v.meta.fields.EpicLink)
	orphanJQL := "issuetype = Story AND " + projectClause(tracked)
	if link != "" {
		orphanJQL += " AND " + link + " is EMPTY"
	}
	orphans, err := c.SearchAll(ctx, orphanJQL+notDone(includeDone)+" ORDER BY key DESC",
		jira.SearchOptions{Fields: v.fields(treeFields, false), Limit: maxTreeTop})
	if err != nil {
		return nil, err
	}
	out.Truncated = out.Truncated || orphans.Truncated()
	if m := a.learnTypes(id, KindStory, orphans.Issues); m != nil {
		v.meta = m
	}

	// Everything with an Epic Link to these epics (any project, any status:
	// done children count for the progress).
	pool := newTreePool(v)
	var children []jira.Issue
	if link != "" && len(epics.Issues) > 0 {
		keys := make([]string, 0, len(epics.Issues))
		for _, e := range epics.Issues {
			keys = append(keys, e.Key)
		}
		for i := 0; i < len(keys); i += keyChunk {
			if len(children) >= maxTreeChildren {
				out.Truncated = true // epics left whose children were not read
				break
			}
			end := i + keyChunk
			if end > len(keys) {
				end = len(keys)
			}
			res, err := c.SearchAll(ctx, link+" in ("+quoteKeys(keys[i:end])+") ORDER BY key DESC",
				jira.SearchOptions{Fields: v.fields(treeFields, false), Limit: maxTreeChildren - len(children)})
			if err != nil {
				return nil, err
			}
			children = append(children, res.Issues...)
			out.Truncated = out.Truncated || res.Truncated()
		}
	}
	var stories []*treeNode
	for _, ch := range children {
		n := pool.add(ch, true)
		if n.ref.Type.Kind == KindStory {
			stories = append(stories, n)
		}
	}
	var orphanNodes []*treeNode
	for _, s := range orphans.Issues {
		n := pool.add(s, true)
		orphanNodes = append(orphanNodes, n)
		stories = append(stories, n)
	}
	if pool.fetchRelated(ctx, stories, maxTreeChildren-len(children)) {
		out.Truncated = true
	}

	byEpic := map[string][]*treeNode{}
	for _, ch := range children {
		n := pool.nodes[ch.Key]
		byEpic[n.epicLink] = append(byEpic[n.epicLink], n)
	}
	infos := map[string]epicInfo{}
	for _, e := range epics.Issues {
		info := v.epicInfoOf(e)
		infos[e.Key] = info
		en, mine := pool.epicNode(pool.add(e, true), byEpic[e.Key], info)
		if scope == ScopeMine && !mine {
			continue
		}
		out.Epics = append(out.Epics, en)
	}
	a.rememberEpics(id, infos)
	for _, s := range orphanNodes {
		sn := pool.storyNode(s)
		if scope == ScopeMine && !sn.mine {
			continue
		}
		out.OrphanStories = append(out.OrphanStories, sn)
	}
	sortEpics(out.Epics)
	sortStories(out.OrphanStories)
	a.cachePut(key, id, gen, now, out)
	return out, nil
}

func sortEpics(e []EpicNode) {
	for i := 1; i < len(e); i++ {
		for j := i; j > 0 && refLess(e[j].Epic.IssueRef, e[j-1].Epic.IssueRef); j-- {
			e[j], e[j-1] = e[j-1], e[j]
		}
	}
}

func (a *App) getStories(ctx context.Context, includeDone bool, scope string, force bool) (*StoriesResult, error) {
	scope, err := a.parseScope(scope, ScopeAll)
	if err != nil {
		return nil, err
	}
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	key := treeCacheKey(cacheStories, includeDone, scope)
	cached, gen, ok := a.cacheGet(key, id, listTTL)
	if ok && !force {
		return cached.(*StoriesResult), nil
	}
	now := a.clock.Now()
	out := &StoriesResult{Stories: []StoryNode{}, FetchedAt: now.Format(time.RFC3339)}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tracked, err := a.trackedForQuery(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if len(tracked) == 0 {
		a.cachePut(key, id, gen, now, out)
		return out, nil
	}
	v, err := a.newView(ctx, c, id)
	if err != nil {
		return nil, err
	}
	res, err := c.SearchAll(ctx, "issuetype = Story AND "+projectClause(tracked)+notDone(includeDone)+" ORDER BY key DESC",
		jira.SearchOptions{Fields: v.fields(treeFields, false), Limit: maxTreeTop})
	if err != nil {
		return nil, err
	}
	out.Truncated = res.Truncated()
	if m := a.learnTypes(id, KindStory, res.Issues); m != nil {
		v.meta = m
	}
	pool := newTreePool(v)
	var stories []*treeNode
	var epicKeys []string
	for _, s := range res.Issues {
		n := pool.add(s, true)
		stories = append(stories, n)
		epicKeys = append(epicKeys, n.epicLink)
	}
	if pool.fetchRelated(ctx, stories, maxTreeChildren) {
		out.Truncated = true
	}
	a.loadEpics(ctx, v, epicKeys)
	for _, s := range stories {
		sn := pool.storyNode(s)
		if scope == ScopeMine && !sn.mine {
			continue
		}
		if s.epicLink != "" {
			e, ok := v.epics[s.epicLink]
			if !ok { // epic not readable: a bare reference
				e = epicInfo{ref: IssueRef{Key: s.epicLink, URL: c.BrowseURL(s.epicLink), Type: IssueTypeDTO{Kind: KindEpic}, Status: StatusDTO{Category: "new"}}}
			}
			sn.Epic = &EpicRefDTO{IssueRef: e.ref, Name: e.name, Color: e.color}
		}
		out.Stories = append(out.Stories, sn)
	}
	sortStories(out.Stories)
	a.cachePut(key, id, gen, now, out)
	return out, nil
}

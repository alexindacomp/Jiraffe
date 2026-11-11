package app

import (
	"context"
	"log"
	"time"

	"jiraffe/internal/jira"
	"jiraffe/internal/worklog"
)

const (
	maxDetailComments  = 200
	maxDetailChanges   = 500
	maxDetailChildren  = 1000
	maxDetailRelations = 500
)

// EpicRefDTO is IssueRef + { name, color } (IssueDetail.epic, EpicNode.epic).
type EpicRefDTO struct {
	IssueRef
	Name  string `json:"name"`
	Color string `json:"color"`
}

// CommentDTO is the Comment contract type (body = raw Jira wiki markup).
type CommentDTO struct {
	ID      string   `json:"id"`
	Author  *UserDTO `json:"author"`
	Created string   `json:"created"`
	Updated string   `json:"updated"`
	Body    string   `json:"body"`
}

// ChangeItemDTO is the ChangeItem contract type (display strings).
type ChangeItemDTO struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// ChangeDTO is the Change contract type.
type ChangeDTO struct {
	ID      string          `json:"id"`
	Author  *UserDTO        `json:"author"`
	Created string          `json:"created"`
	Items   []ChangeItemDTO `json:"items"`
}

// LinkDTO is the Link contract type: relation as seen from the issue.
type LinkDTO struct {
	Relation string   `json:"relation"`
	Issue    IssueRef `json:"issue"`
}

// TimeTrackingDTO is IssueDetail.timeTracking (formatted with hoursPerDay).
type TimeTrackingDTO struct {
	OriginalSeconds  *int64 `json:"originalSeconds,omitempty"`
	RemainingSeconds *int64 `json:"remainingSeconds,omitempty"`
	SpentSeconds     *int64 `json:"spentSeconds,omitempty"`
	Original         string `json:"original,omitempty"`
	Remaining        string `json:"remaining,omitempty"`
	Spent            string `json:"spent,omitempty"`
}

// SprintDTO is the Sprint contract type.
type SprintDTO struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	State        string `json:"state"`
	StartDate    string `json:"startDate,omitempty"`
	EndDate      string `json:"endDate,omitempty"`
	CompleteDate string `json:"completeDate,omitempty"`
	Goal         string `json:"goal,omitempty"`
	BoardID      int    `json:"boardId"`
}

// IssueDetail is the getIssue result.
type IssueDetail struct {
	Issue        IssueDTO        `json:"issue"`
	Description  string          `json:"description"`
	Reporter     *UserDTO        `json:"reporter,omitempty"`
	Creator      *UserDTO        `json:"creator,omitempty"`
	Created      string          `json:"created"`
	Updated      string          `json:"updated"`
	DueDate      string          `json:"dueDate,omitempty"`
	Resolution   string          `json:"resolution,omitempty"`
	Labels       []string        `json:"labels"`
	Components   []string        `json:"components"`
	FixVersions  []string        `json:"fixVersions"`
	Priority     *NameDTO        `json:"priority,omitempty"`
	TimeTracking TimeTrackingDTO `json:"timeTracking"`
	StoryPoints  *float64        `json:"storyPoints,omitempty"`
	Sprints      []SprintDTO     `json:"sprints"`
	Epic         *EpicRefDTO     `json:"epic,omitempty"`
	Parent       *IssueRef       `json:"parent,omitempty"`
	Stories      []IssueRef      `json:"stories"`
	Children     []IssueRef      `json:"children"`
	Links        []LinkDTO       `json:"links"`
	Comments     []CommentDTO    `json:"comments"`
	Changelog    []ChangeDTO     `json:"changelog"`
	Watchers     *int            `json:"watchers,omitempty"`
}

var detailFields = []string{"summary", "status", "issuetype", "priority", "updated", "created", "assignee",
	"reporter", "creator", "project", "description", "duedate", "resolution", "labels", "components",
	"fixVersions", "timetracking", "issuelinks", "subtasks", "parent", "comment", "watches"}

func (a *App) getIssue(ctx context.Context, key string) (*IssueDetail, error) {
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	if !jira.ValidIssueKey(key) {
		return nil, a.newErr(CodeBadParams, "err_invalid_key")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	v, err := a.newView(ctx, c, id)
	if err != nil {
		return nil, err
	}
	fields := v.fields(detailFields, false)
	if v.meta.fields.Sprint != "" {
		fields = append(fields, v.meta.fields.Sprint)
	}
	is, err := c.IssueDetail(ctx, key, fields)
	if err != nil {
		return nil, err
	}
	comments := []jira.Comment(nil)
	if cp := is.Fields.Comment; cp != nil {
		comments = cp.Comments
		if cp.Total > len(cp.Comments) {
			if all, err := c.Comments(ctx, key, maxDetailComments); err != nil {
				log.Printf("issue %s: read all comments: %v", key, err)
			} else {
				comments = all
			}
		}
	}
	epicKey := v.epicLink(is)
	a.loadEpics(ctx, v, []string{epicKey})
	d := v.detail(*is, comments)
	if epicKey != "" {
		e, ok := v.epics[epicKey]
		if !ok { // epic not readable: a bare reference
			e = epicInfo{ref: IssueRef{Key: epicKey, URL: c.BrowseURL(epicKey), Type: IssueTypeDTO{Kind: KindEpic}, Status: StatusDTO{Category: "new"}}}
		}
		d.Epic = &EpicRefDTO{IssueRef: e.ref, Name: e.name, Color: e.color}
	}
	if d.Issue.Type.Kind == KindEpic && v.meta.fields.EpicLink != "" {
		ek := is.Key // the current key (the issue may have been moved)
		if !jira.ValidIssueKey(ek) {
			ek = key
		}
		jql := jira.ClauseRef(v.meta.fields.EpicLink) + ` = "` + ek + `" ORDER BY key DESC`
		children, err := c.Search(ctx, jql, jira.SearchOptions{Fields: v.fields(treeFields, false), Limit: maxDetailChildren})
		if err != nil {
			log.Printf("issue %s: epic children: %v", key, err)
		}
		for _, ch := range children {
			d.Children = append(d.Children, v.ref(ch))
		}
	}
	return d, nil
}

// detail maps an issue (fetched with detailFields and its changelog) to
// IssueDetail, without the epic and the epic's children.
func (v *view) detail(is jira.Issue, comments []jira.Comment) *IssueDetail {
	f := is.Fields
	d := &IssueDetail{
		Issue:       v.issue(is),
		Description: f.Description,
		Reporter:    userDTO(f.Reporter),
		Creator:     userDTO(f.Creator),
		Created:     rfc3339(f.Created),
		Updated:     rfc3339(f.Updated),
		DueDate:     f.DueDate,
		Labels:      append([]string{}, f.Labels...),
		Components:  names(f.Components),
		FixVersions: names(f.FixVersions),
		StoryPoints: v.storyPoints(&is),
		Sprints:     []SprintDTO{},
		Stories:     []IssueRef{},
		Children:    []IssueRef{},
		Links:       []LinkDTO{},
		Comments:    []CommentDTO{},
		Changelog:   []ChangeDTO{},
	}
	if f.Resolution != nil {
		d.Resolution = f.Resolution.Name
	}
	if f.Priority != nil {
		d.Priority = &NameDTO{Name: f.Priority.Name}
	}
	if f.Watches != nil {
		n := f.Watches.WatchCount
		d.Watchers = &n
	}
	if tt := f.TimeTracking; tt != nil {
		d.TimeTracking = TimeTrackingDTO{
			OriginalSeconds:  tt.OriginalEstimateSeconds,
			RemainingSeconds: tt.RemainingEstimateSeconds,
			SpentSeconds:     tt.TimeSpentSeconds,
			Original:         v.duration(tt.OriginalEstimateSeconds),
			Remaining:        v.duration(tt.RemainingEstimateSeconds),
			Spent:            v.duration(tt.TimeSpentSeconds),
		}
	}
	for _, s := range jira.ParseSprints(f.Custom[v.meta.fields.Sprint]) {
		d.Sprints = append(d.Sprints, sprintDTO(s, 0))
	}
	if f.Parent != nil {
		p := v.ref(*f.Parent)
		d.Parent = &p
	}

	kind := d.Issue.Type.Kind
	seenChild := map[string]bool{}
	addChild := func(r IssueRef) {
		if !seenChild[r.Key] && len(d.Children) < maxDetailRelations {
			seenChild[r.Key] = true
			d.Children = append(d.Children, r)
		}
	}
	if kind != KindEpic {
		for _, st := range f.Subtasks {
			addChild(v.ref(st))
		}
	}
	for _, l := range f.IssueLinks {
		rel, other := l.Type.Outward, l.OutwardIssue
		if other == nil {
			rel, other = l.Type.Inward, l.InwardIssue
		}
		if other == nil || len(d.Links) >= maxDetailRelations {
			continue
		}
		r := v.ref(*other)
		d.Links = append(d.Links, LinkDTO{Relation: rel, Issue: r})
		switch {
		case kind == KindStory && r.Type.Kind != KindEpic && r.Type.Kind != KindStory:
			addChild(r)
		case (kind == KindTask || kind == KindSubtask) && r.Type.Kind == KindStory:
			if !containsRef(d.Stories, r.Key) {
				d.Stories = append(d.Stories, r)
			}
		}
	}

	if len(comments) > maxDetailComments {
		comments = comments[len(comments)-maxDetailComments:]
	}
	for _, cm := range comments {
		d.Comments = append(d.Comments, CommentDTO{ID: cm.ID, Author: userDTO(cm.Author),
			Created: rfc3339(cm.Created), Updated: rfc3339(cm.Updated), Body: cm.Body})
	}
	if is.Changelog != nil {
		hs := is.Changelog.Histories
		for i := len(hs) - 1; i >= 0 && len(d.Changelog) < maxDetailChanges; i-- { // newest first
			h := hs[i]
			ch := ChangeDTO{ID: h.ID, Author: userDTO(h.Author), Created: rfc3339(h.Created), Items: []ChangeItemDTO{}}
			for _, it := range h.Items {
				ch.Items = append(ch.Items, ChangeItemDTO{Field: it.Field, From: orElse(it.FromString, it.From), To: orElse(it.ToString, it.To)})
			}
			d.Changelog = append(d.Changelog, ch)
		}
	}
	return d
}

func (v *view) duration(sec *int64) string {
	if sec == nil {
		return ""
	}
	return worklog.FormatDuration(float64(*sec), v.hoursPerDay)
}

func sprintDTO(s jira.Sprint, board int) SprintDTO {
	if s.BoardID != 0 {
		board = s.BoardID
	}
	d := SprintDTO{ID: s.ID, Name: s.Name, State: s.State, Goal: s.Goal, BoardID: board}
	for _, p := range []struct {
		dst *string
		src string
	}{{&d.StartDate, s.StartDate}, {&d.EndDate, s.EndDate}, {&d.CompleteDate, s.CompleteDate}} {
		if p.src != "" {
			*p.dst = rfc3339(p.src)
		}
	}
	return d
}

func names(ns []jira.Named) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Name)
	}
	return out
}

func orElse(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

func containsRef(refs []IssueRef, key string) bool {
	for _, r := range refs {
		if r.Key == key {
			return true
		}
	}
	return false
}

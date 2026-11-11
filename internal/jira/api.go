package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// User is a Jira user.
type User struct {
	Name         string `json:"name"`
	Key          string `json:"key"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress,omitempty"`
}

// Same reports whether u and o are the same Jira user.
func (u *User) Same(o *User) bool {
	if u == nil || o == nil {
		return false
	}
	if u.Key != "" && o.Key != "" {
		return u.Key == o.Key
	}
	return u.Name != "" && strings.EqualFold(u.Name, o.Name)
}

// StatusCategory of a status.
type StatusCategory struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Status of an issue.
type Status struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	StatusCategory StatusCategory `json:"statusCategory"`
}

// Named is a field with a name (issue type, priority).
type Named struct {
	Name string `json:"name"`
}

// Project of an issue (also an entry of GET /rest/api/2/project, where the
// category and, with expand=lead, the lead are filled).
type Project struct {
	ID              string `json:"id"`
	Key             string `json:"key"`
	Name            string `json:"name"`
	ProjectCategory *Named `json:"projectCategory"`
	Lead            *User  `json:"lead"`
}

// IssueType of an issue. Names are localized; classify by ID.
type IssueType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Subtask bool   `json:"subtask"`
}

// TimeTracking is the "timetracking" field (absent values stay nil).
type TimeTracking struct {
	OriginalEstimateSeconds  *int64 `json:"originalEstimateSeconds"`
	RemainingEstimateSeconds *int64 `json:"remainingEstimateSeconds"`
	TimeSpentSeconds         *int64 `json:"timeSpentSeconds"`
}

// IssueLinkType names the two directions of a link type.
type IssueLinkType struct {
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

// IssueLink is one entry of the "issuelinks" field. Exactly one of
// InwardIssue / OutwardIssue is set: OutwardIssue means "this issue
// <Type.Outward> OutwardIssue", InwardIssue means "this issue <Type.Inward>
// InwardIssue".
type IssueLink struct {
	ID           string        `json:"id"`
	Type         IssueLinkType `json:"type"`
	InwardIssue  *Issue        `json:"inwardIssue"`
	OutwardIssue *Issue        `json:"outwardIssue"`
}

// Watches is the "watches" field.
type Watches struct {
	WatchCount int `json:"watchCount"`
}

// Comment on an issue.
type Comment struct {
	ID      string `json:"id"`
	Author  *User  `json:"author"`
	Body    string `json:"body"`
	Created string `json:"created"`
	Updated string `json:"updated"`
}

// CommentPage is the "comment" field of an issue.
type CommentPage struct {
	Comments []Comment `json:"comments"`
	Total    int       `json:"total"`
}

// Fields of an issue (only those the app requests).
type Fields struct {
	Summary   string       `json:"summary"`
	Status    *Status      `json:"status"`
	Created   string       `json:"created"`
	Updated   string       `json:"updated"`
	Assignee  *User        `json:"assignee"`
	IssueType *IssueType   `json:"issuetype"`
	Priority  *Named       `json:"priority"`
	Project   *Project     `json:"project"`
	Comment   *CommentPage `json:"comment"`
	Worklog   *WorklogPage `json:"worklog"`

	Description  string        `json:"description"`
	Reporter     *User         `json:"reporter"`
	Creator      *User         `json:"creator"`
	DueDate      string        `json:"duedate"`
	Resolution   *Named        `json:"resolution"`
	Labels       []string      `json:"labels"`
	Components   []Named       `json:"components"`
	FixVersions  []Named       `json:"fixVersions"`
	TimeTracking *TimeTracking `json:"timetracking"`
	IssueLinks   []IssueLink   `json:"issuelinks"`
	Subtasks     []Issue       `json:"subtasks"`
	Parent       *Issue        `json:"parent"`
	Watches      *Watches      `json:"watches"`

	// Custom holds the raw values of customfield_* fields (ids differ
	// between Jira instances, see DiscoverFields).
	Custom map[string]json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes the known fields and keeps customfield_* raw.
func (f *Fields) UnmarshalJSON(b []byte) error {
	type plain Fields
	if err := json.Unmarshal(b, (*plain)(f)); err != nil {
		return err
	}
	if !bytes.Contains(b, []byte(`"customfield_`)) {
		return nil // nothing custom requested: skip the second pass
	}
	// Walk the top-level keys only; values are copied raw, not decoded.
	dec := json.NewDecoder(bytes.NewReader(b))
	if _, err := dec.Token(); err != nil { // {
		return err
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return err
		}
		if k, _ := tok.(string); strings.HasPrefix(k, "customfield_") {
			if f.Custom == nil {
				f.Custom = map[string]json.RawMessage{}
			}
			f.Custom[k] = v
		}
	}
	return nil
}

// CustomString returns a custom field holding a string (or an object with a
// "key"/"value"/"name"), "" when absent, null or of another type.
func (f *Fields) CustomString(id string) string {
	raw, ok := f.Custom[id]
	if id == "" || !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Key   string `json:"key"`
		Value string `json:"value"`
		Name  string `json:"name"`
	}
	if json.Unmarshal(raw, &o) == nil {
		for _, v := range []string{o.Key, o.Value, o.Name} {
			if v != "" {
				return v
			}
		}
	}
	return ""
}

// CustomNumber returns a numeric custom field, nil when absent or null.
func (f *Fields) CustomNumber(id string) *float64 {
	raw, ok := f.Custom[id]
	if id == "" || !ok {
		return nil
	}
	var n *float64
	if json.Unmarshal(raw, &n) != nil {
		return nil
	}
	return n
}

// ChangeItem is one changed field of a changelog history entry.
type ChangeItem struct {
	Field      string `json:"field"`
	FieldType  string `json:"fieldtype"`
	From       string `json:"from"`
	FromString string `json:"fromString"`
	To         string `json:"to"`
	ToString   string `json:"toString"`
}

// History is one changelog entry.
type History struct {
	ID      string       `json:"id"`
	Author  *User        `json:"author"`
	Created string       `json:"created"`
	Items   []ChangeItem `json:"items"`
}

// Changelog of an issue (expand=changelog).
type Changelog struct {
	StartAt    int       `json:"startAt"`
	MaxResults int       `json:"maxResults"`
	Total      int       `json:"total"`
	Histories  []History `json:"histories"`
}

// Issue as returned by search / issue endpoints.
type Issue struct {
	ID        string     `json:"id"`
	Key       string     `json:"key"`
	Fields    Fields     `json:"fields"`
	Changelog *Changelog `json:"changelog"`
}

// Worklog entry.
type Worklog struct {
	ID               string `json:"id"`
	Author           *User  `json:"author"`
	Started          string `json:"started"`
	TimeSpentSeconds int64  `json:"timeSpentSeconds"`
}

// Transition available for an issue.
type Transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   Status `json:"to"`
}

// Myself returns the user owning the token (GET /rest/api/2/myself).
func (c *Client) Myself(ctx context.Context) (*User, error) {
	var u User
	if err := c.do(ctx, "GET", "/rest/api/2/myself", nil, nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// SearchResult is a (possibly truncated) search: Total is what Jira matched.
type SearchResult struct {
	Issues []Issue
	Total  int
}

// Truncated reports whether Jira matched more issues than were fetched.
func (r *SearchResult) Truncated() bool { return r.Total > len(r.Issues) }

type searchResult struct {
	StartAt    int     `json:"startAt"`
	MaxResults int     `json:"maxResults"`
	Total      int     `json:"total"`
	Issues     []Issue `json:"issues"`
}

// SearchOptions controls Search.
type SearchOptions struct {
	Fields []string
	Expand string
	Limit  int // max issues to return in total; <= 0 means 500
	// NoValidateQuery sends validateQuery=false: Jira then ignores unknown
	// or hidden values (e.g. issue keys in "issuekey in (…)") instead of
	// rejecting the whole query with HTTP 400.
	NoValidateQuery bool
}

const searchPage = 100

// Search runs a JQL query and follows pagination up to opts.Limit issues.
func (c *Client) Search(ctx context.Context, jql string, opts SearchOptions) ([]Issue, error) {
	res, err := c.SearchAll(ctx, jql, opts)
	if err != nil {
		return nil, err
	}
	return res.Issues, nil
}

// SearchAll is Search that also reports how many issues Jira matched.
func (c *Client) SearchAll(ctx context.Context, jql string, opts SearchOptions) (*SearchResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 500
	}
	out := &SearchResult{}
	for start := 0; start < limit; {
		q := url.Values{}
		q.Set("jql", jql)
		if len(opts.Fields) > 0 {
			q.Set("fields", strings.Join(opts.Fields, ","))
		}
		if opts.Expand != "" {
			q.Set("expand", opts.Expand)
		}
		if opts.NoValidateQuery {
			q.Set("validateQuery", "false")
		}
		page := searchPage
		if limit-start < page {
			page = limit - start
		}
		q.Set("maxResults", strconv.Itoa(page))
		q.Set("startAt", strconv.Itoa(start))
		var res searchResult
		if err := c.do(ctx, "GET", "/rest/api/2/search", q, nil, &res); err != nil {
			return nil, err
		}
		out.Issues = append(out.Issues, res.Issues...)
		if res.Total > out.Total {
			out.Total = res.Total
		}
		start += len(res.Issues)
		if len(res.Issues) == 0 || start >= res.Total {
			break
		}
	}
	return out, nil
}

// GetIssue fetches one issue with the given fields.
func (c *Client) GetIssue(ctx context.Context, key string, fields []string) (*Issue, error) {
	if !ValidIssueKey(key) {
		return nil, &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue key " + strconv.Quote(key)}
	}
	q := url.Values{}
	if len(fields) > 0 {
		q.Set("fields", strings.Join(fields, ","))
	}
	var is Issue
	if err := c.do(ctx, "GET", "/rest/api/2/issue/"+url.PathEscape(key), q, nil, &is); err != nil {
		return nil, err
	}
	return &is, nil
}

// IssueWithChangelog fetches the full changelog of an issue (used when the
// changelog embedded in search results is truncated).
func (c *Client) IssueWithChangelog(ctx context.Context, key string) (*Issue, error) {
	if !ValidIssueKey(key) {
		return nil, &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue key " + strconv.Quote(key)}
	}
	q := url.Values{}
	q.Set("fields", "status,created")
	q.Set("expand", "changelog")
	var is Issue
	if err := c.do(ctx, "GET", "/rest/api/2/issue/"+url.PathEscape(key), q, nil, &is); err != nil {
		return nil, err
	}
	return &is, nil
}

// WorklogPage is a page of worklogs (also the "worklog" issue field).
type WorklogPage struct {
	StartAt    int       `json:"startAt"`
	MaxResults int       `json:"maxResults"`
	Total      int       `json:"total"`
	Worklogs   []Worklog `json:"worklogs"`
}

// Complete reports whether the page holds all worklogs of the issue.
func (p *WorklogPage) Complete() bool {
	return p.StartAt == 0 && p.Total <= p.MaxResults && len(p.Worklogs) >= p.Total
}

// Worklogs returns the worklogs of an issue.
func (c *Client) Worklogs(ctx context.Context, key string) ([]Worklog, error) {
	if !ValidIssueKey(key) {
		return nil, &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue key " + strconv.Quote(key)}
	}
	var out []Worklog
	for start := 0; ; {
		q := url.Values{}
		q.Set("startAt", strconv.Itoa(start))
		q.Set("maxResults", "1000")
		var page WorklogPage
		if err := c.do(ctx, "GET", "/rest/api/2/issue/"+url.PathEscape(key)+"/worklog", q, nil, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Worklogs...)
		start = page.StartAt + len(page.Worklogs)
		// Older Jira versions ignore paging and return everything at once.
		if len(page.Worklogs) == 0 || start >= page.Total || page.MaxResults == 0 || start > 20000 {
			return out, nil
		}
	}
}

// AddWorklog logs seconds on an issue starting at started.
func (c *Client) AddWorklog(ctx context.Context, key string, seconds int64, started time.Time) error {
	if !ValidIssueKey(key) {
		return &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue key " + strconv.Quote(key)}
	}
	body := map[string]any{"timeSpentSeconds": seconds, "started": FormatTime(started)}
	return c.do(ctx, "POST", "/rest/api/2/issue/"+url.PathEscape(key)+"/worklog", nil, body, nil)
}

// Transitions lists the transitions available for an issue.
func (c *Client) Transitions(ctx context.Context, key string) ([]Transition, error) {
	if !ValidIssueKey(key) {
		return nil, &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue key " + strconv.Quote(key)}
	}
	var res struct {
		Transitions []Transition `json:"transitions"`
	}
	if err := c.do(ctx, "GET", "/rest/api/2/issue/"+url.PathEscape(key)+"/transitions", nil, nil, &res); err != nil {
		return nil, err
	}
	return res.Transitions, nil
}

// DoTransition performs a transition on an issue.
func (c *Client) DoTransition(ctx context.Context, key, transitionID string) error {
	if !ValidIssueKey(key) {
		return &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue key " + strconv.Quote(key)}
	}
	body := map[string]any{"transition": map[string]string{"id": transitionID}}
	return c.do(ctx, "POST", "/rest/api/2/issue/"+url.PathEscape(key)+"/transitions", nil, body, nil)
}

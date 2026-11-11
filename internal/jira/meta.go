package jira

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// Schema custom types of the Jira Software (greenhopper) fields.
const (
	SchemaEpicLink  = "com.pyxis.greenhopper.jira:gh-epic-link"
	SchemaEpicName  = "com.pyxis.greenhopper.jira:gh-epic-label"
	SchemaEpicColor = "com.pyxis.greenhopper.jira:gh-epic-color"
	SchemaSprint    = "com.pyxis.greenhopper.jira:gh-sprint"
)

// FieldSchema describes the value type of a field.
type FieldSchema struct {
	Type   string `json:"type"`
	Custom string `json:"custom"`
}

// FieldDef is an entry of GET /rest/api/2/field.
type FieldDef struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Custom      bool         `json:"custom"`
	ClauseNames []string     `json:"clauseNames"`
	Schema      *FieldSchema `json:"schema"`
}

// FieldIDs are the ids of the custom fields the app uses on one Jira
// instance; "" means the field does not exist there.
type FieldIDs struct {
	EpicLink    string
	EpicName    string
	EpicColor   string
	Sprint      string
	StoryPoints string
}

// List returns the ids that exist (for the "fields" query parameter).
func (f FieldIDs) List() []string {
	var out []string
	for _, id := range []string{f.EpicLink, f.EpicName, f.EpicColor, f.Sprint, f.StoryPoints} {
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

// Fields returns all field definitions (GET /rest/api/2/field).
func (c *Client) Fields(ctx context.Context) ([]FieldDef, error) {
	var out []FieldDef
	if err := c.do(ctx, "GET", "/rest/api/2/field", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DiscoverFields finds the Epic Link / Epic Name / Epic Color / Sprint fields
// by their schema and Story Points as a number field whose clause names
// contain "Story Points". The first match wins.
func DiscoverFields(defs []FieldDef) FieldIDs {
	var f FieldIDs
	set := func(dst *string, id string) {
		if *dst == "" {
			*dst = id
		}
	}
	for _, d := range defs {
		if d.ID == "" || d.Schema == nil {
			continue
		}
		switch d.Schema.Custom {
		case SchemaEpicLink:
			set(&f.EpicLink, d.ID)
		case SchemaEpicName:
			set(&f.EpicName, d.ID)
		case SchemaEpicColor:
			set(&f.EpicColor, d.ID)
		case SchemaSprint:
			set(&f.Sprint, d.ID)
		}
		if d.Schema.Type == "number" {
			for _, cn := range d.ClauseNames {
				if strings.EqualFold(strings.TrimSpace(cn), "Story Points") {
					set(&f.StoryPoints, d.ID)
					break
				}
			}
		}
	}
	return f
}

// ClauseRef returns the JQL reference of a custom field id
// ("customfield_10103" → "cf[10103]"), "" if id is not a custom field id.
func ClauseRef(id string) string {
	n := strings.TrimPrefix(id, "customfield_")
	if n == id || n == "" {
		return ""
	}
	for i := 0; i < len(n); i++ {
		if n[i] < '0' || n[i] > '9' {
			return ""
		}
	}
	return "cf[" + n + "]"
}

// IssueTypes returns all issue types (GET /rest/api/2/issuetype).
func (c *Client) IssueTypes(ctx context.Context) ([]IssueType, error) {
	var out []IssueType
	if err := c.do(ctx, "GET", "/rest/api/2/issuetype", nil, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Projects returns the projects the user can browse, with their leads.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	q := url.Values{}
	q.Set("expand", "lead")
	var out []Project
	if err := c.do(ctx, "GET", "/rest/api/2/project", q, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ValidProjectKey reports whether key looks like a Jira project key, so it
// can be used in JQL and request parameters.
func ValidProjectKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		letter := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
		if i == 0 && !letter || !(letter || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// IssueDetail fetches one issue with the given fields and its changelog.
func (c *Client) IssueDetail(ctx context.Context, key string, fields []string) (*Issue, error) {
	if !ValidIssueKey(key) {
		return nil, &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue key " + strconv.Quote(key)}
	}
	q := url.Values{}
	if len(fields) > 0 {
		q.Set("fields", strings.Join(fields, ","))
	}
	q.Set("expand", "changelog")
	var is Issue
	if err := c.do(ctx, "GET", "/rest/api/2/issue/"+url.PathEscape(key), q, nil, &is); err != nil {
		return nil, err
	}
	return &is, nil
}

// Comments returns the newest max comments of an issue, oldest first
// (GET /rest/api/2/issue/{key}/comment, paginated).
func (c *Client) Comments(ctx context.Context, key string, max int) ([]Comment, error) {
	if !ValidIssueKey(key) {
		return nil, &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue key " + strconv.Quote(key)}
	}
	type page struct {
		StartAt    int       `json:"startAt"`
		MaxResults int       `json:"maxResults"`
		Total      int       `json:"total"`
		Comments   []Comment `json:"comments"`
	}
	get := func(start, n int) (*page, error) {
		q := url.Values{}
		q.Set("startAt", strconv.Itoa(start))
		q.Set("maxResults", strconv.Itoa(n))
		q.Set("orderBy", "created")
		var p page
		if err := c.do(ctx, "GET", "/rest/api/2/issue/"+url.PathEscape(key)+"/comment", q, nil, &p); err != nil {
			return nil, err
		}
		return &p, nil
	}
	first, err := get(0, max)
	if err != nil {
		return nil, err
	}
	if first.Total <= len(first.Comments) {
		return keepLast(first.Comments, max), nil
	}
	// More than one page: read the newest max comments.
	var out []Comment
	for start := first.Total - max; start < first.Total && len(out) < max; {
		if start < 0 {
			start = 0
		}
		p, err := get(start, max-len(out))
		if err != nil {
			return nil, err
		}
		if len(p.Comments) == 0 {
			break
		}
		out = append(out, p.Comments...)
		start += len(p.Comments)
	}
	return keepLast(out, max), nil
}

func keepLast(cs []Comment, max int) []Comment {
	if len(cs) > max {
		return cs[len(cs)-max:]
	}
	return cs
}

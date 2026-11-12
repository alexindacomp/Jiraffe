package jira

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// MetaField is a field of the create or edit meta: how it is written and
// what values it accepts.
type MetaField struct {
	ID              string            `json:"fieldId"` // new create meta; legacy and edit meta: the map key
	Key             string            `json:"key"`
	Name            string            `json:"name"`
	Required        bool              `json:"required"`
	Schema          *FieldSchema      `json:"schema"`
	AllowedValues   []json.RawMessage `json:"allowedValues"`
	HasDefaultValue bool              `json:"hasDefaultValue"`
	DefaultValue    json.RawMessage   `json:"defaultValue"`
	AutoCompleteURL string            `json:"autoCompleteUrl"`
	Operations      []string          `json:"operations"`
}

// Limits of the create meta lists.
const (
	maxMetaTypes  = 200
	maxMetaFields = 2000
	metaPageSize  = 100
)

// metaPage is the paged envelope of the Jira ≥ 8.4 create meta endpoints.
type metaPage[T any] struct {
	StartAt    int  `json:"startAt"`
	MaxResults int  `json:"maxResults"`
	Total      int  `json:"total"`
	IsLast     bool `json:"isLast"`
	Values     []T  `json:"values"`
}

// metaList follows the paging (isLast / total) of a create meta endpoint.
// A server that ignores startAt (answering the same page again) ends the
// list instead of repeating it up to the limit.
func metaList[T any](ctx context.Context, c *Client, path string, limit int) ([]T, error) {
	var out []T
	seen := map[string]bool{} // first value of every page read
	for start := 0; len(out) < limit; {
		q := url.Values{}
		q.Set("startAt", strconv.Itoa(start))
		q.Set("maxResults", strconv.Itoa(metaPageSize))
		var p metaPage[json.RawMessage]
		if err := c.do(ctx, "GET", path, q, nil, &p); err != nil {
			return nil, err
		}
		if len(p.Values) > 0 {
			first := string(p.Values[0])
			if seen[first] {
				break
			}
			seen[first] = true
		}
		for _, raw := range p.Values {
			var v T
			if err := json.Unmarshal(raw, &v); err != nil {
				return nil, &Error{Code: CodeBadResponse, Kind: KindJSON, Detail: err.Error(),
					Message: "unexpected response from Jira: " + err.Error()}
			}
			out = append(out, v)
		}
		start += len(p.Values)
		if p.IsLast || len(p.Values) == 0 || (p.Total > 0 && start >= p.Total) {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// legacyCreateMeta is GET /rest/api/2/issue/createmeta (Jira < 8.4).
type legacyCreateMeta struct {
	Projects []struct {
		Key        string `json:"key"`
		IssueTypes []struct {
			IssueType
			Fields map[string]MetaField `json:"fields"`
		} `json:"issuetypes"`
	} `json:"projects"`
}

func (c *Client) legacyCreateMeta(ctx context.Context, projectKey, typeID string) (*legacyCreateMeta, error) {
	q := url.Values{}
	q.Set("projectKeys", projectKey)
	if typeID != "" {
		q.Set("issuetypeIds", typeID)
		q.Set("expand", "projects.issuetypes.fields")
	}
	var m legacyCreateMeta
	if err := c.do(ctx, "GET", "/rest/api/2/issue/createmeta", q, nil, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func validMetaArgs(projectKey, typeID string) error {
	if !ValidProjectKey(projectKey) {
		return &Error{Code: CodeBadRequest, Message: "invalid project key " + strconv.Quote(projectKey)}
	}
	if typeID != "" && !numericID(typeID) {
		return &Error{Code: CodeBadRequest, Message: "invalid issue type id " + strconv.Quote(typeID)}
	}
	return nil
}

// CreateMetaIssueTypes returns the issue types a project can create
// (GET …/createmeta/{project}/issuetypes, paged; legacy createmeta on 404).
func (c *Client) CreateMetaIssueTypes(ctx context.Context, projectKey string) ([]IssueType, error) {
	if err := validMetaArgs(projectKey, ""); err != nil {
		return nil, err
	}
	types, err := metaList[IssueType](ctx, c, "/rest/api/2/issue/createmeta/"+projectKey+"/issuetypes", maxMetaTypes)
	if !IsCode(err, CodeNotFound) {
		return types, err
	}
	m, err := c.legacyCreateMeta(ctx, projectKey, "")
	if err != nil {
		return nil, err
	}
	var out []IssueType
	for _, p := range m.Projects {
		if strings.EqualFold(p.Key, projectKey) {
			for _, t := range p.IssueTypes {
				out = append(out, t.IssueType)
			}
		}
	}
	return out, nil
}

// CreateMetaFields returns the fields of the create screen of a project
// and issue type, in Jira's order for the paged endpoint (legacy createmeta
// on 404; its map is ordered by field id).
func (c *Client) CreateMetaFields(ctx context.Context, projectKey, typeID string) ([]MetaField, error) {
	if err := validMetaArgs(projectKey, typeID); err != nil {
		return nil, err
	}
	if typeID == "" {
		return nil, &Error{Code: CodeBadRequest, Message: "missing issue type id"}
	}
	fields, err := metaList[MetaField](ctx, c, "/rest/api/2/issue/createmeta/"+projectKey+"/issuetypes/"+typeID, maxMetaFields)
	if !IsCode(err, CodeNotFound) {
		return fields, err
	}
	m, err := c.legacyCreateMeta(ctx, projectKey, typeID)
	if err != nil {
		return nil, err
	}
	for _, p := range m.Projects {
		if !strings.EqualFold(p.Key, projectKey) {
			continue
		}
		for _, t := range p.IssueTypes {
			if t.ID == typeID {
				return sortedMetaFields(t.Fields), nil
			}
		}
	}
	return nil, &Error{Status: 404, Code: CodeNotFound, Message: "issue type " + typeID + " cannot be created in " + projectKey}
}

// sortedMetaFields turns a field map into a list ordered by id (system
// fields first, then custom fields by number), filling ids from the keys.
func sortedMetaFields(m map[string]MetaField) []MetaField {
	out := make([]MetaField, 0, len(m))
	for id, f := range m {
		if f.ID == "" {
			f.ID = id
		}
		out = append(out, f)
	}
	less := func(a, b string) bool {
		na, ca := customNumber(a)
		nb, cb := customNumber(b)
		if ca != cb {
			return !ca
		}
		if ca {
			return na < nb
		}
		return a < b
	}
	for i := 1; i < len(out); i++ { // insertion sort: small lists
		for j := i; j > 0 && less(out[j].ID, out[j-1].ID); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func customNumber(id string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "customfield_"))
	return n, err == nil && strings.HasPrefix(id, "customfield_")
}

// EditMeta returns the editable fields of an issue (GET …/editmeta); none
// means the user may not edit it.
func (c *Client) EditMeta(ctx context.Context, key string) ([]MetaField, error) {
	if !ValidIssueKey(key) {
		return nil, &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue key " + strconv.Quote(key)}
	}
	var m struct {
		Fields map[string]MetaField `json:"fields"`
	}
	if err := c.do(ctx, "GET", "/rest/api/2/issue/"+url.PathEscape(key)+"/editmeta", nil, nil, &m); err != nil {
		return nil, err
	}
	return sortedMetaFields(m.Fields), nil
}

// IssueFields returns the raw "fields" object of an issue (only the given
// fields), for mapping values of arbitrary fields.
func (c *Client) IssueFields(ctx context.Context, key string, fields []string) (map[string]json.RawMessage, error) {
	if !ValidIssueKey(key) {
		return nil, &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue key " + strconv.Quote(key)}
	}
	q := url.Values{}
	q.Set("fields", strings.Join(fields, ","))
	var is struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := c.do(ctx, "GET", "/rest/api/2/issue/"+url.PathEscape(key), q, nil, &is); err != nil {
		return nil, err
	}
	if is.Fields == nil {
		is.Fields = map[string]json.RawMessage{}
	}
	return is.Fields, nil
}

// CreatedIssue is the answer of POST /rest/api/2/issue.
type CreatedIssue struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// CreateIssue creates an issue from Jira field values.
func (c *Client) CreateIssue(ctx context.Context, fields map[string]any) (*CreatedIssue, error) {
	var out CreatedIssue
	if err := c.do(ctx, "POST", "/rest/api/2/issue", nil, map[string]any{"fields": fields}, &out); err != nil {
		return nil, err
	}
	if !ValidIssueKey(out.Key) {
		return nil, &Error{Code: CodeBadResponse, Kind: KindJSON, Message: "unexpected response from Jira: no issue key"}
	}
	return &out, nil
}

// UpdateIssue sets fields of an issue (PUT, answered with 204).
func (c *Client) UpdateIssue(ctx context.Context, key string, fields map[string]any) error {
	if !ValidIssueKey(key) {
		return &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue key " + strconv.Quote(key)}
	}
	return c.do(ctx, "PUT", "/rest/api/2/issue/"+url.PathEscape(key), nil, map[string]any{"fields": fields}, nil)
}

// AssignableUsers searches users assignable in a project (create) or to an
// issue (edit); query matches user name, display name or e-mail.
func (c *Client) AssignableUsers(ctx context.Context, projectKey, issueKey, query string, max int) ([]User, error) {
	q := url.Values{}
	switch {
	case issueKey != "":
		if !ValidIssueKey(issueKey) {
			return nil, &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue key " + strconv.Quote(issueKey)}
		}
		q.Set("issueKey", issueKey)
	case ValidProjectKey(projectKey):
		q.Set("project", projectKey)
	default:
		return nil, &Error{Code: CodeBadRequest, Message: "invalid project key " + strconv.Quote(projectKey)}
	}
	q.Set("username", query)
	q.Set("maxResults", strconv.Itoa(max))
	var out []User
	if err := c.do(ctx, "GET", "/rest/api/2/user/assignable/search", q, nil, &out); err != nil {
		return nil, err
	}
	if len(out) > max {
		out = out[:max]
	}
	return out, nil
}

// MoveToSprint moves issues into a sprint (agile API).
func (c *Client) MoveToSprint(ctx context.Context, sprintID int, keys []string) error {
	if sprintID <= 0 {
		return &Error{Code: CodeBadRequest, Message: "invalid sprint id"}
	}
	return c.do(ctx, "POST", "/rest/agile/1.0/sprint/"+strconv.Itoa(sprintID)+"/issue", nil, map[string]any{"issues": keys}, nil)
}

// MoveToBacklog removes issues from their sprints (agile API).
func (c *Client) MoveToBacklog(ctx context.Context, keys []string) error {
	return c.do(ctx, "POST", "/rest/agile/1.0/backlog/issue", nil, map[string]any{"issues": keys}, nil)
}

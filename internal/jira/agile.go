package jira

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// Sprint states as returned by Jira (lower-cased).
const (
	SprintActive = "active"
	SprintFuture = "future"
	SprintClosed = "closed"
)

// Sprint of Jira Software. Dates are Jira timestamps as received ("" when
// unknown); BoardID is the sprint's origin board (0 when unknown).
type Sprint struct {
	ID           int
	Name         string
	State        string // active | future | closed (lower case)
	StartDate    string
	EndDate      string
	CompleteDate string
	Goal         string
	BoardID      int
}

// sprintJSON is the object form of a sprint: the agile REST API and newer
// Jira versions in the Sprint issue field.
type sprintJSON struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	State         string `json:"state"`
	StartDate     string `json:"startDate"`
	EndDate       string `json:"endDate"`
	CompleteDate  string `json:"completeDate"`
	Goal          string `json:"goal"`
	OriginBoardID int    `json:"originBoardId"`
	BoardID       int    `json:"boardId"`
	RapidViewID   int    `json:"rapidViewId"`
}

func (s sprintJSON) sprint() Sprint {
	board := s.OriginBoardID
	if board == 0 {
		board = s.BoardID
	}
	if board == 0 {
		board = s.RapidViewID
	}
	return Sprint{ID: s.ID, Name: s.Name, State: strings.ToLower(s.State), StartDate: s.StartDate,
		EndDate: s.EndDate, CompleteDate: s.CompleteDate, Goal: s.Goal, BoardID: board}
}

// ParseSprints decodes the value of the Sprint custom field: an array of
// legacy strings ("com.atlassian.greenhopper.service.sprint.Sprint@1a2b[id=6338,
// rapidViewId=880,state=CLOSED,name=Sprint 1,…]") and/or sprint objects.
// Unparsable entries are skipped; null or absent gives nil.
func ParseSprints(raw json.RawMessage) []Sprint {
	var items []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []Sprint
	for _, it := range items {
		var s string
		if json.Unmarshal(it, &s) == nil {
			if sp, ok := ParseLegacySprint(s); ok {
				out = append(out, sp)
			}
			continue
		}
		var o sprintJSON
		if json.Unmarshal(it, &o) == nil && o.ID != 0 {
			out = append(out, o.sprint())
		}
	}
	return out
}

// ParseLegacySprint parses the legacy string form of a sprint. Values may
// contain commas (names, goals): a new attribute only starts at
// ",<identifier>=", so "Sprint 1, part 2" stays one value.
func ParseLegacySprint(s string) (Sprint, bool) {
	open := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if open < 0 || end <= open {
		return Sprint{}, false
	}
	body := s[open+1 : end]
	type attr struct {
		key        string
		begin, val int // start of "key=" and of the value
	}
	var attrs []attr
	for i := 0; i < len(body); i++ {
		if i != 0 && body[i-1] != ',' {
			continue
		}
		eq := strings.IndexByte(body[i:], '=')
		if eq > 0 && isIdentifier(body[i:i+eq]) {
			attrs = append(attrs, attr{key: body[i : i+eq], begin: i, val: i + eq + 1})
		}
	}
	vals := map[string]string{}
	for j, a := range attrs {
		stop := len(body)
		if j+1 < len(attrs) {
			stop = attrs[j+1].begin - 1 // the separating comma
		}
		v := body[a.val:stop]
		if v == "<null>" {
			v = ""
		}
		vals[a.key] = v
	}
	id, err := strconv.Atoi(vals["id"])
	if err != nil || id == 0 {
		return Sprint{}, false
	}
	board, _ := strconv.Atoi(vals["rapidViewId"])
	return Sprint{ID: id, Name: vals["name"], State: strings.ToLower(vals["state"]), StartDate: vals["startDate"],
		EndDate: vals["endDate"], CompleteDate: vals["completeDate"], Goal: vals["goal"], BoardID: board}, true
}

func isIdentifier(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return s != ""
}

// AgileBoard is a Jira Software board.
type AgileBoard struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"` // scrum | kanban
	Location *struct {
		ProjectKey string `json:"projectKey"`
	} `json:"location"`
}

// agilePage is the paginated envelope of the agile REST API.
type agilePage[T any] struct {
	StartAt    int  `json:"startAt"`
	MaxResults int  `json:"maxResults"`
	IsLast     bool `json:"isLast"`
	Values     []T  `json:"values"`
}

const agilePageSize = 50

// agileList follows isLast pagination of an agile endpoint up to limit values.
func agileList[T any](ctx context.Context, c *Client, path string, q url.Values, limit int) ([]T, error) {
	var out []T
	for start := 0; len(out) < limit; {
		q.Set("startAt", strconv.Itoa(start))
		q.Set("maxResults", strconv.Itoa(agilePageSize))
		var p agilePage[T]
		if err := c.do(ctx, "GET", path, q, nil, &p); err != nil {
			return nil, err
		}
		out = append(out, p.Values...)
		start += len(p.Values)
		if p.IsLast || len(p.Values) == 0 {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// AgileAvailable reports whether the Jira Software agile REST API exists
// (false when GET /rest/agile/1.0/board answers 404).
func (c *Client) AgileAvailable(ctx context.Context) (bool, error) {
	q := url.Values{}
	q.Set("maxResults", "1")
	var p agilePage[AgileBoard]
	err := c.do(ctx, "GET", "/rest/agile/1.0/board", q, nil, &p)
	if IsCode(err, CodeNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Boards returns the agile boards of a project.
func (c *Client) Boards(ctx context.Context, projectKey string) ([]AgileBoard, error) {
	if !ValidProjectKey(projectKey) {
		return nil, &Error{Code: CodeBadRequest, Message: "invalid project key " + strconv.Quote(projectKey)}
	}
	q := url.Values{}
	q.Set("projectKeyOrId", projectKey)
	return agileList[AgileBoard](ctx, c, "/rest/agile/1.0/board", q, 200)
}

// BoardSprints returns the sprints of a board in the given states
// (comma-separated, e.g. "active,future"), in Jira's order (oldest first).
// At most limit sprints are read.
func (c *Client) BoardSprints(ctx context.Context, boardID int, states string, limit int) ([]Sprint, error) {
	q := url.Values{}
	q.Set("state", states)
	raw, err := agileList[sprintJSON](ctx, c, "/rest/agile/1.0/board/"+strconv.Itoa(boardID)+"/sprint", q, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Sprint, 0, len(raw))
	for _, s := range raw {
		out = append(out, s.sprint())
	}
	return out, nil
}

// BoardColumn is a column of an agile board configuration. Statuses carry
// only the status ids.
type BoardColumn struct {
	Name     string      `json:"name"`
	Statuses []StatusRef `json:"statuses"`
}

// StatusRef is a reference to a status by id.
type StatusRef struct {
	ID string `json:"id"`
}

// UnmarshalJSON accepts the id as a JSON string or number.
func (r *StatusRef) UnmarshalJSON(b []byte) error {
	var o struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	r.ID = ""
	if len(o.ID) == 0 || string(o.ID) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(o.ID, &s) == nil {
		r.ID = s
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(o.ID, &n); err != nil {
		return err
	}
	r.ID = n.String()
	return nil
}

// BoardConfig is the configuration of an agile board.
type BoardConfig struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	ColumnConfig struct {
		Columns []BoardColumn `json:"columns"`
	} `json:"columnConfig"`
}

// BoardConfiguration returns the column configuration of an agile board
// (GET /rest/agile/1.0/board/{id}/configuration).
func (c *Client) BoardConfiguration(ctx context.Context, boardID int) (*BoardConfig, error) {
	if boardID <= 0 {
		return nil, &Error{Code: CodeBadRequest, Message: "invalid board id " + strconv.Itoa(boardID)}
	}
	var out BoardConfig
	if err := c.do(ctx, "GET", "/rest/agile/1.0/board/"+strconv.Itoa(boardID)+"/configuration", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

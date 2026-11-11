package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"jiraffe/internal/jira"
	"jiraffe/internal/scheduler"
)

// The JQL the app is expected to send, written out from SPEC.md rather than
// taken from the production constants, so a change to a query fails the
// tests instead of silently changing both sides.
const (
	specWorklogJQL = `(assignee = currentUser() OR status changed BY currentUser() DURING (startOfDay(), now())) AND (status changed DURING (startOfDay(), now()) OR statusCategory = "In Progress")`
	specBoardJQL   = `assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC`
	specMyOpenJQL  = `assignee = currentUser() AND statusCategory != Done`
)

var (
	// A run computing a past day widens startOfDay() to startOfDay(-Nd).
	specWorklogJQLWidened = regexp.MustCompile(`^\(assignee = currentUser\(\) OR status changed BY currentUser\(\) DURING \(startOfDay\(-(\d+)d\), now\(\)\)\) AND \(status changed DURING \(startOfDay\(-(\d+)d\), now\(\)\) OR statusCategory = "In Progress"\)$`)
	specCommentJQL        = regexp.MustCompile(`^assignee = currentUser\(\) AND updated >= -(\d+)m ORDER BY updated ASC$`)
	specKeysJQL           = regexp.MustCompile(`^issuekey in \(("[A-Z0-9_]+-\d+"(?:,"[A-Z0-9_]+-\d+")*)\)$`)
	specCapJQL            = regexp.MustCompile(`^worklogAuthor = currentUser\(\) AND worklogDate >= "(\d{4}-\d\d-\d\d)" AND worklogDate <= "(\d{4}-\d\d-\d\d)"$`)
)

type pollComment struct {
	id, author string
	created    time.Time
}

type pollIssue struct {
	key      string
	updated  time.Time
	comments []pollComment
}

// fakeJira is the in-memory Jira used by all app tests. Searches are routed
// by their exact JQL (unexpected JQL gets HTTP 400, like a real Jira), every
// result list is paginated by startAt/maxResults, and the comment search
// evaluates "updated >= -Nm" against the clock when pollIssues is used.
type fakeJira struct {
	mu    sync.Mutex
	clock scheduler.Clock // set by newTestApp/openApp when nil

	issues      string      // JSON array: worklog search and board
	capIssues   string      // JSON array: "my worklogs around the day" search ("" = none)
	capDay      string      // day the cap search must cover ±1; "" = the clock's date
	comments    string      // JSON array: comment search (when pollIssues is nil)
	pollIssues  []pollIssue // comment search filtered by the window, oldest first
	issueBodies map[string]string
	worklogs    map[string][]jira.Worklog

	posted         []string
	jqls           []string
	lastToken      string
	searches       int
	issueCalls     map[string]int
	worklogFetches map[string]int
	transitions    []string // transition POSTs

	// v0.2: routes by exact JQL (checked first), my open issues (MyOpenJQL;
	// nil = issues), metadata, projects, agile boards and sprints.
	routes       map[string]string
	myOpen       *string
	fieldsJSON   string            // GET /field; "" = realFields
	typesJSON    string            // GET /issuetype; "" = []
	projectsJSON string            // GET /project; "" = []
	agileBoards  map[string]string // project key → JSON array of boards
	agileMissing bool              // the agile API answers 404
	agileStatus  map[string]int    // project key → forced HTTP status of its board list
	sprints      map[int][]string  // board id → sprint objects, Jira order
	kanban       map[int]bool      // boards that do not support sprints (400)
	comments2    map[string]int    // issue key → number of comments of GET …/comment
	byKey        map[string]string // issue JSON served for "issuekey in (…)"
	paths        []string          // every request path with its query

	searchHook func()                    // called (unlocked) before answering a search
	transErr   error                     // returned by issue GETs after a transition POST
	fail       func(*jira.Request) error // optional transport failure
	status     func(*jira.Request) int   // optional forced HTTP status
}

func (f *fakeJira) Do(_ context.Context, r *jira.Request) (*jira.Response, error) {
	u, _ := url.Parse(r.URL)
	if u.Path == "/rest/api/2/search" && f.searchHook != nil {
		f.searchHook()
	}
	if f.fail != nil {
		if err := f.fail(r); err != nil {
			return nil, err
		}
	}
	if f.status != nil {
		if code := f.status(r); code != 0 {
			return &jira.Response{StatusCode: code, Body: []byte(`{"errorMessages":["from jira"]}`)}, nil
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastToken = r.Headers["Authorization"]
	f.paths = append(f.paths, u.RequestURI())
	ok := func(body string) (*jira.Response, error) {
		return &jira.Response{StatusCode: 200, Body: []byte(body)}, nil
	}
	path := u.Path
	if resp, handled := f.v2(r, u); handled {
		return resp, nil
	}
	switch {
	case path == "/rest/api/2/myself":
		return ok(`{"name":"alex","key":"alex","displayName":"Alex"}`)
	case path == "/rest/api/2/search":
		return f.search(u.Query())
	case strings.HasSuffix(path, "/transitions"):
		if r.Method == "POST" {
			f.transitions = append(f.transitions, string(r.Body))
			return &jira.Response{StatusCode: 204}, nil
		}
		return ok(`{"transitions":[{"id":"31","name":"To review","to":{"id":"6","name":"ForReview","statusCategory":{"key":"indeterminate"}}}]}`)
	case strings.HasSuffix(path, "/worklog"):
		key := strings.TrimSuffix(strings.TrimPrefix(path, "/rest/api/2/issue/"), "/worklog")
		if r.Method == "POST" {
			var body struct {
				TimeSpentSeconds int64  `json:"timeSpentSeconds"`
				Started          string `json:"started"`
			}
			_ = json.Unmarshal(r.Body, &body)
			f.posted = append(f.posted, fmt.Sprintf("%s %d %s", key, body.TimeSpentSeconds, body.Started))
			if f.worklogs == nil {
				f.worklogs = map[string][]jira.Worklog{}
			}
			f.worklogs[key] = append(f.worklogs[key], jira.Worklog{Author: &jira.User{Name: "alex", Key: "alex"},
				Started: body.Started, TimeSpentSeconds: body.TimeSpentSeconds})
			return &jira.Response{StatusCode: 201, Body: []byte(`{}`)}, nil
		}
		if f.worklogFetches == nil {
			f.worklogFetches = map[string]int{}
		}
		f.worklogFetches[key]++
		b, _ := json.Marshal(f.worklogs[key])
		n := len(f.worklogs[key])
		return ok(fmt.Sprintf(`{"startAt":0,"maxResults":%d,"total":%d,"worklogs":%s}`, n, n, b))
	case strings.HasPrefix(path, "/rest/api/2/issue/"):
		key := strings.TrimPrefix(path, "/rest/api/2/issue/")
		if f.issueCalls == nil {
			f.issueCalls = map[string]int{}
		}
		f.issueCalls[key]++
		if body, found := f.issueBodies[key]; found {
			if u.Query().Get("expand") != "changelog" {
				return &jira.Response{StatusCode: 400, Body: []byte(`{"errorMessages":["changelog not expanded"]}`)}, nil
			}
			return ok(string(projectFields(json.RawMessage(body), u.Query().Get("fields"))))
		}
		if len(f.transitions) > 0 && f.transErr != nil {
			return nil, f.transErr
		}
		return ok(`{"key":"A-1","fields":{"summary":"Fresh","status":{"id":"6","name":"ForReview","statusCategory":{"key":"indeterminate"}}}}`)
	}
	return &jira.Response{StatusCode: 404, Body: []byte(`{"errorMessages":["no route"]}`)}, nil
}

// realFields mirrors the custom fields of the real Jira (sd.local).
const realFields = `[
 {"id":"summary","name":"Summary","custom":false,"clauseNames":["summary"],"schema":{"type":"string","system":"summary"}},
 {"id":"customfield_10103","name":"Epic Link","custom":true,"clauseNames":["cf[10103]","Epic Link"],"schema":{"type":"any","custom":"com.pyxis.greenhopper.jira:gh-epic-link","customId":10103}},
 {"id":"customfield_10105","name":"Epic Name","custom":true,"clauseNames":["cf[10105]","Epic Name"],"schema":{"type":"string","custom":"com.pyxis.greenhopper.jira:gh-epic-label","customId":10105}},
 {"id":"customfield_10106","name":"Epic Color","custom":true,"clauseNames":["cf[10106]","Epic Color"],"schema":{"type":"string","custom":"com.pyxis.greenhopper.jira:gh-epic-color","customId":10106}},
 {"id":"customfield_10107","name":"Sprint","custom":true,"clauseNames":["cf[10107]","Sprint"],"schema":{"type":"array","items":"string","custom":"com.pyxis.greenhopper.jira:gh-sprint","customId":10107}},
 {"id":"customfield_10130","name":"Story Points","custom":true,"clauseNames":["cf[10130]","Story Points"],"schema":{"type":"number","custom":"com.atlassian.jira.plugin.system.customfieldtypes:float","customId":10130}}
]`

// v2 answers the v0.2 endpoints; handled is false for everything else.
func (f *fakeJira) v2(r *jira.Request, u *url.URL) (*jira.Response, bool) {
	ok := func(body string) (*jira.Response, bool) {
		return &jira.Response{StatusCode: 200, Body: []byte(body)}, true
	}
	orDefault := func(s, def string) string {
		if s == "" {
			return def
		}
		return s
	}
	path, q := u.Path, u.Query()
	switch {
	case path == "/rest/api/2/field":
		return ok(orDefault(f.fieldsJSON, realFields))
	case path == "/rest/api/2/issuetype":
		return ok(orDefault(f.typesJSON, "[]"))
	case path == "/rest/api/2/project":
		return ok(orDefault(f.projectsJSON, "[]"))
	case path == "/rest/agile/1.0/board":
		if f.agileMissing {
			return &jira.Response{StatusCode: 404, Body: []byte(`<html>404</html>`)}, true
		}
		if code := f.agileStatus[q.Get("projectKeyOrId")]; code != 0 {
			return &jira.Response{StatusCode: code, Body: []byte(`{"errorMessages":["forced"]}`)}, true
		}
		return f.agilePage(rawList(f.agileBoards[q.Get("projectKeyOrId")]), q), true
	case strings.HasPrefix(path, "/rest/agile/1.0/board/") && strings.HasSuffix(path, "/sprint"):
		id, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(path, "/rest/agile/1.0/board/"), "/sprint"))
		if f.kanban[id] {
			return &jira.Response{StatusCode: 400, Body: []byte(`{"errorMessages":["The board does not support sprints"]}`)}, true
		}
		states := map[string]bool{}
		for _, s := range strings.Split(q.Get("state"), ",") {
			states[s] = true
		}
		var list []json.RawMessage
		for _, sp := range f.sprints[id] {
			var o struct {
				State string `json:"state"`
			}
			_ = json.Unmarshal([]byte(sp), &o)
			if states[o.State] {
				list = append(list, json.RawMessage(sp))
			}
		}
		return f.agilePage(list, q), true
	case strings.HasPrefix(path, "/rest/agile/1.0/sprint/"):
		id := strings.TrimPrefix(path, "/rest/agile/1.0/sprint/")
		for _, list := range f.sprints {
			for _, sp := range list {
				var o struct {
					ID int `json:"id"`
				}
				_ = json.Unmarshal([]byte(sp), &o)
				if strconv.Itoa(o.ID) == id {
					return ok(sp)
				}
			}
		}
		return &jira.Response{StatusCode: 404, Body: []byte(`{"errorMessages":["no sprint"]}`)}, true
	case strings.HasPrefix(path, "/rest/api/2/issue/") && strings.HasSuffix(path, "/comment"):
		key := strings.TrimSuffix(strings.TrimPrefix(path, "/rest/api/2/issue/"), "/comment")
		n := f.comments2[key]
		start, _ := strconv.Atoi(q.Get("startAt"))
		max, _ := strconv.Atoi(q.Get("maxResults"))
		if max > 50 {
			max = 50 // like Jira, serve smaller pages than asked
		}
		var cs []string
		for i := start; i < n && i < start+max; i++ {
			cs = append(cs, fmt.Sprintf(`{"id":"%d","author":{"name":"bob","displayName":"Bob"},"body":"c%d","created":"2026-11-01T10:00:00.000+0200"}`, i+1, i+1))
		}
		return ok(fmt.Sprintf(`{"startAt":%d,"maxResults":%d,"total":%d,"comments":[%s]}`, start, max, n, strings.Join(cs, ",")))
	}
	return nil, false
}

// agilePage paginates an agile list with startAt/maxResults/isLast.
func (f *fakeJira) agilePage(list []json.RawMessage, q url.Values) *jira.Response {
	start, _ := strconv.Atoi(q.Get("startAt"))
	max, _ := strconv.Atoi(q.Get("maxResults"))
	if max <= 0 || max > 50 {
		max = 50
	}
	if start > len(list) {
		start = len(list)
	}
	end := start + max
	if end > len(list) {
		end = len(list)
	}
	page, _ := json.Marshal(list[start:end])
	if end == start {
		page = []byte("[]")
	}
	return &jira.Response{StatusCode: 200, Body: []byte(fmt.Sprintf(`{"startAt":%d,"maxResults":%d,"isLast":%v,"values":%s}`,
		start, max, end >= len(list), page))}
}

// requests returns the request URIs (path?query) seen so far with the prefix.
func (f *fakeJira) requests(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.paths {
		if strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeJira) badJQL(jql string) (*jira.Response, error) {
	return &jira.Response{StatusCode: 400, Body: []byte(fmt.Sprintf(`{"errorMessages":[%q]}`, "unexpected jql: "+jql))}, nil
}

func (f *fakeJira) search(q url.Values) (*jira.Response, error) {
	f.searches++
	jql := q.Get("jql")
	f.jqls = append(f.jqls, jql)
	var list []json.RawMessage
	route, routed := f.routes[jql]
	switch {
	case routed:
		list = rawList(route)
	case specKeysJQL.MatchString(jql):
		for _, k := range strings.Split(specKeysJQL.FindStringSubmatch(jql)[1], ",") {
			is, ok := f.byKey[strings.Trim(k, `"`)]
			if !ok && q.Get("validateQuery") != "false" {
				// Like Jira: an unknown or hidden key fails the whole query.
				return f.badJQL(jql)
			}
			if ok {
				list = append(list, json.RawMessage(is))
			}
		}
	case jql == specMyOpenJQL:
		if f.myOpen != nil {
			list = rawList(*f.myOpen)
		} else {
			list = rawList(f.issues)
		}
	case jql == specWorklogJQL || jql == specBoardJQL:
		list = rawList(f.issues)
	case specWorklogJQLWidened.MatchString(jql):
		m := specWorklogJQLWidened.FindStringSubmatch(jql)
		if m[1] != m[2] {
			return f.badJQL(jql)
		}
		list = rawList(f.issues)
	case specCapJQL.MatchString(jql):
		m := specCapJQL.FindStringSubmatch(jql)
		day := f.capDay
		if day == "" {
			day = f.clock.Now().Format("2006-01-02")
		}
		d, _ := time.Parse("2006-01-02", day)
		if m[1] != d.AddDate(0, 0, -1).Format("2006-01-02") || m[2] != d.AddDate(0, 0, 1).Format("2006-01-02") || q.Get("fields") != "worklog" {
			return f.badJQL(jql)
		}
		list = rawList(f.capIssues)
	case specCommentJQL.MatchString(jql):
		if f.pollIssues == nil {
			list = rawList(f.comments)
			break
		}
		mins, _ := strconv.Atoi(specCommentJQL.FindStringSubmatch(jql)[1])
		list = f.pollResults(f.clock.Now().Add(-time.Duration(mins) * time.Minute))
	default:
		return f.badJQL(jql)
	}
	start, _ := strconv.Atoi(q.Get("startAt"))
	max, _ := strconv.Atoi(q.Get("maxResults"))
	if start > len(list) {
		start = len(list)
	}
	end := start + max
	if end > len(list) {
		end = len(list)
	}
	sel := make([]json.RawMessage, 0, end-start)
	for _, is := range list[start:end] {
		sel = append(sel, projectFields(is, q.Get("fields")))
	}
	page, _ := json.Marshal(sel)
	if end == start {
		page = []byte("[]")
	}
	return &jira.Response{StatusCode: 200, Body: []byte(fmt.Sprintf(`{"startAt":%d,"maxResults":%d,"total":%d,"issues":%s}`, start, max, len(list), page))}, nil
}

// projectFields keeps only the requested fields of an issue, like Jira:
// a field the app does not ask for is not in the response. All fields when
// the parameter is absent or "*all".
func projectFields(issue json.RawMessage, fields string) json.RawMessage {
	if fields == "" || fields == "*all" {
		return issue
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(issue, &top) != nil || top["fields"] == nil {
		return issue
	}
	var all map[string]json.RawMessage
	if json.Unmarshal(top["fields"], &all) != nil {
		return issue
	}
	kept := map[string]json.RawMessage{}
	for _, f := range strings.Split(fields, ",") {
		if v, ok := all[strings.TrimSpace(f)]; ok {
			kept[strings.TrimSpace(f)] = v
		}
	}
	top["fields"], _ = json.Marshal(kept)
	out, _ := json.Marshal(top)
	return out
}

func rawList(s string) []json.RawMessage {
	var arr []json.RawMessage
	if strings.TrimSpace(s) != "" {
		if err := json.Unmarshal([]byte(s), &arr); err != nil {
			panic("bad fixture: " + err.Error())
		}
	}
	return arr
}

func (f *fakeJira) pollResults(from time.Time) []json.RawMessage {
	var hits []pollIssue
	for _, is := range f.pollIssues {
		if !is.updated.Before(from) {
			hits = append(hits, is)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].updated.Before(hits[j].updated) })
	out := make([]json.RawMessage, 0, len(hits))
	for _, is := range hits {
		var cs []string
		for _, c := range is.comments {
			cs = append(cs, fmt.Sprintf(`{"id":%q,"author":{"name":%q,"key":%q,"displayName":%q},"body":"from %s","created":%q}`,
				c.id, c.author, c.author, strings.ToUpper(c.author[:1])+c.author[1:], c.author, jira.FormatTime(c.created)))
		}
		out = append(out, json.RawMessage(fmt.Sprintf(`{"key":%q,"fields":{"summary":"S","updated":%q,"comment":{"total":%d,"comments":[%s]}}}`,
			is.key, jira.FormatTime(is.updated), len(cs), strings.Join(cs, ","))))
	}
	return out
}

// setPoll replaces the issues the comment search sees.
func (f *fakeJira) setPoll(issues ...pollIssue) {
	f.mu.Lock()
	f.pollIssues = append([]pollIssue{}, issues...)
	f.mu.Unlock()
}

// searched returns the JQL of every search so far.
func (f *fakeJira) searched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.jqls...)
}

// lastCommentWindow returns N of the last comment search "updated >= -Nm".
func (f *fakeJira) lastCommentWindow() int {
	jqls := f.searched()
	for i := len(jqls) - 1; i >= 0; i-- {
		if m := specCommentJQL.FindStringSubmatch(jqls[i]); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	return -1
}

// lostResponse records the worklog in Jira but reports a transport timeout
// for the first worklog POST, as when the connection drops after the server
// committed it.
type lostResponse struct {
	*fakeJira
	once sync.Once
}

func (l *lostResponse) Do(ctx context.Context, r *jira.Request) (*jira.Response, error) {
	resp, err := l.fakeJira.Do(ctx, r)
	lost := false
	if r.Method == "POST" && strings.HasSuffix(r.URL, "/worklog") {
		l.once.Do(func() { lost = true })
	}
	if lost {
		return nil, &jira.TransportError{Kind: jira.NetTimeout, Message: "timed out"}
	}
	return resp, err
}

// attachClock gives a fakeJira (possibly wrapped) the app's clock.
func attachClock(t jira.Transport, c scheduler.Clock) {
	switch f := t.(type) {
	case *fakeJira:
		if f.clock == nil {
			f.clock = c
		}
	case *lostResponse:
		attachClock(f.fakeJira, c)
	}
}

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"jiraffe/internal/jira"
)

// --- fixtures ------------------------------------------------------------------------

const (
	v5Updated  = "2026-11-12T10:00:00.000+0200"
	v5Updated2 = "2026-11-12T10:05:00.000+0200"
)

// rbiStoryFields is the create screen of RBI stories (Jira's order).
var rbiStoryFields = []string{
	`{"fieldId":"summary","name":"Summary","required":true,"schema":{"type":"string","system":"summary"}}`,
	`{"fieldId":"issuetype","name":"Issue Type","required":true,"schema":{"type":"issuetype","system":"issuetype"}}`,
	`{"fieldId":"project","name":"Project","required":true,"schema":{"type":"project","system":"project"}}`,
	`{"fieldId":"customfield_10301","name":"Region","required":true,"schema":{"type":"option-with-child","custom":"com.atlassian.jira.plugin.system.customfieldtypes:cascadingselect"}}`,
	`{"fieldId":"description","name":"Description","required":false,"schema":{"type":"string","system":"description"}}`,
	`{"fieldId":"priority","name":"Priority","required":false,"schema":{"type":"priority","system":"priority"},
	  "allowedValues":[{"id":"2","name":"High"},{"id":"3","name":"Medium"}],"hasDefaultValue":true,"defaultValue":{"id":"3","name":"Medium"}}`,
	`{"fieldId":"assignee","name":"Assignee","required":false,"schema":{"type":"user","system":"assignee"}}`,
	`{"fieldId":"labels","name":"Labels","required":false,"schema":{"type":"array","items":"string","system":"labels"}}`,
	`{"fieldId":"customfield_10103","name":"Epic Link","required":false,"schema":{"type":"any","custom":"com.pyxis.greenhopper.jira:gh-epic-link"}}`,
	`{"fieldId":"customfield_10130","name":"Story Points","required":false,"schema":{"type":"number","custom":"com.atlassian.jira.plugin.system.customfieldtypes:float"}}`,
	`{"fieldId":"customfield_10204","name":"Dev Story Points","required":false,"schema":{"type":"option","custom":"com.atlassian.jira.plugin.system.customfieldtypes:select"},
	  "allowedValues":[{"id":"10601","value":"8"},{"id":"10602","value":"13","disabled":true},{"id":"10603","value":"5"}]}`,
	`{"fieldId":"customfield_10205","name":"QA Story Points","required":false,"schema":{"type":"option","custom":"com.atlassian.jira.plugin.system.customfieldtypes:select"},
	  "allowedValues":[{"id":"10701","value":"3"}]}`,
	`{"fieldId":"components","name":"Component/s","required":false,"schema":{"type":"array","items":"component","system":"components"},"allowedValues":[{"id":"100","name":"API"}]}`,
	`{"fieldId":"fixVersions","name":"Fix Version/s","required":false,"schema":{"type":"array","items":"version","system":"fixVersions"},
	  "allowedValues":[{"id":"200","name":"1.0"},{"id":"201","name":"0.9","archived":true}]}`,
	`{"fieldId":"duedate","name":"Due Date","required":false,"schema":{"type":"date","system":"duedate"}}`,
	`{"fieldId":"timetracking","name":"Time tracking","required":false,"schema":{"type":"timetracking","system":"timetracking"}}`,
	`{"fieldId":"customfield_10300","name":"Team","required":true,"schema":{"type":"option","custom":"com.atlassian.jira.plugin.system.customfieldtypes:select"},
	  "allowedValues":[{"id":"500","value":"Core"},{"id":"501","value":"BI"}]}`,
	`{"fieldId":"customfield_10302","name":"Notes","required":false,"schema":{"type":"string","custom":"com.atlassian.jira.plugin.system.customfieldtypes:textfield"}}`,
	`{"fieldId":"environment","name":"Environment","required":false,"schema":{"type":"string","system":"environment"}}`,
	`{"fieldId":"customfield_10200","name":"Dev Points","required":false,"schema":{"type":"number"}}`,
}

// rbiTaskFields are the story fields without the unsupported required one.
func rbiTaskFields() []string {
	var out []string
	for _, f := range rbiStoryFields {
		if !strings.Contains(f, "customfield_10301") {
			out = append(out, f)
		}
	}
	return out
}

var rbiTypes = []string{fxTypes["epic"], fxTypes["story"], fxTypes["task"], fxTypes["sub"]}

// rbiEditMeta are the editable fields of RBI-12 (no sprint field).
var rbiEditMeta = `{"fields":{
 "summary":{"required":true,"schema":{"type":"string","system":"summary"},"name":"Summary"},
 "description":{"required":false,"schema":{"type":"string","system":"description"},"name":"Description"},
 "assignee":{"required":false,"schema":{"type":"user","system":"assignee"},"name":"Assignee"},
 "priority":{"required":false,"schema":{"type":"priority","system":"priority"},"name":"Priority","allowedValues":[{"id":"2","name":"High"},{"id":"3","name":"Medium"}]},
 "labels":{"required":false,"schema":{"type":"array","items":"string","system":"labels"},"name":"Labels"},
 "customfield_10103":{"required":false,"schema":{"type":"any","custom":"com.pyxis.greenhopper.jira:gh-epic-link"},"name":"Epic Link"},
 "customfield_10204":{"required":false,"schema":{"type":"option"},"name":"Dev Story Points","allowedValues":[{"id":"10601","value":"8"},{"id":"10603","value":"5"}]},
 "duedate":{"required":false,"schema":{"type":"date","system":"duedate"},"name":"Due Date"},
 "timetracking":{"required":false,"schema":{"type":"timetracking","system":"timetracking"},"name":"Time tracking"},
 "customfield_10300":{"required":true,"schema":{"type":"option"},"name":"Team","allowedValues":[{"id":"500","value":"Core"},{"id":"501","value":"BI"}]},
 "components":{"required":false,"schema":{"type":"array","items":"component","system":"components"},"name":"Component/s","allowedValues":[{"id":"100","name":"API"}]},
 "customfield_10500":{"required":true,"schema":{"type":"array","items":"user"},"name":"Reviewers"},
 "customfield_10600":{"required":false,"schema":{"type":"datetime"},"name":"Deployed"}
}}`

func rbi12Fields(updated, desc string) string {
	return fmt.Sprintf(`{"updated":%q,"issuetype":%s,"project":{"key":"RBI","name":"Project RBI"},"summary":"Old","description":%q,
 "assignee":{"name":"alex","displayName":"Alex"},"priority":{"id":"3","name":"Medium"},"labels":["x"],
 "customfield_10103":"RBI-1","customfield_10204":{"id":"10603","value":"5"},"duedate":"2026-12-01",
 "timetracking":{"originalEstimate":"1d","remainingEstimate":"4h","originalEstimateSeconds":32400},
 "customfield_10300":{"id":"501","value":"BI"},"components":[{"id":"100","name":"API"}],
 "customfield_10107":[{"id":12,"name":"S12","state":"ACTIVE","boardId":880},{"id":11,"name":"S11","state":"CLOSED"}],
 "customfield_10500":[{"name":"bob","displayName":"Bob"}],"customfield_10600":null}`, updated, fxTypes["task"], desc)
}

// legFields is the legacy create meta of LEG tasks (map keyed by id).
const legLegacy = `{"projects":[{"key":"LEG","issuetypes":[{"id":"3","name":"Задача","subtask":false,"fields":{
 "summary":{"required":true,"schema":{"type":"string","system":"summary"},"name":"Summary"},
 "issuetype":{"required":true,"schema":{"type":"issuetype","system":"issuetype"},"name":"Issue Type"},
 "project":{"required":true,"schema":{"type":"project","system":"project"},"name":"Project"},
 "customfield_10400":{"required":true,"schema":{"type":"group","custom":"com.atlassian.jira.plugin.system.customfieldtypes:grouppicker"},"name":"Group"},
 "description":{"required":false,"schema":{"type":"string","system":"description"},"name":"Description"},
 "customfield_10103":{"required":false,"schema":{"type":"any","custom":"com.pyxis.greenhopper.jira:gh-epic-link"},"name":"Epic Link"},
 "duedate":{"required":false,"schema":{"type":"date","system":"duedate"},"name":"Due Date"}}}]}]}`

const legLegacyTypes = `{"projects":[{"key":"LEG","issuetypes":[{"id":"3","name":"Задача","subtask":false},{"id":"5","name":"Подзадача","subtask":true}]}]}`

const descSampleJQL = `project = "RBI" AND description is not EMPTY ORDER BY updated DESC`

// write is a recorded POST/PUT.
type write struct {
	method, path, body string
}

// v5Jira routes the v0.5 endpoints of the fake.
type v5Jira struct {
	writes      []write
	editMeta    map[string]string // issue key → editmeta JSON
	issue       map[string]string // issue key → fields JSON
	createResp  *jira.Response    // answer of POST /issue (nil = 201 RBI-77)
	putResp     *jira.Response    // answer of PUT /issue/{key} (nil = 204)
	afterPut    func()            // e.g. bump "updated"
	agileResp   *jira.Response    // answer of the agile moves (nil = 204)
	failReread  bool              // GETs of the issue fail after a PUT
	assignable  string
	metaRequest map[string]int // path → count
	issueGets   []string       // "fields" of the issue GETs
}

func page(values []string, q url.Values, size int) string {
	start, _ := strconv.Atoi(q.Get("startAt"))
	if start > len(values) {
		start = len(values)
	}
	end := start + size
	if end > len(values) {
		end = len(values)
	}
	return fmt.Sprintf(`{"startAt":%d,"maxResults":%d,"total":%d,"isLast":%v,"values":[%s]}`,
		start, size, len(values), end >= len(values), strings.Join(values[start:end], ","))
}

func (v *v5Jira) handle(r *jira.Request, u *url.URL) (*jira.Response, bool) {
	ok := func(body string) (*jira.Response, bool) {
		return &jira.Response{StatusCode: 200, Body: []byte(body)}, true
	}
	notFound := func() (*jira.Response, bool) {
		return &jira.Response{StatusCode: 404, Body: []byte(`{"errorMessages":["not found"]}`)}, true
	}
	p, q := u.Path, u.Query()
	if v.metaRequest == nil {
		v.metaRequest = map[string]int{}
	}
	if r.Method != "GET" {
		v.writes = append(v.writes, write{r.Method, p, string(r.Body)})
	}
	switch {
	case p == "/rest/api/2/issue/createmeta/RBI/issuetypes":
		v.metaRequest[p]++
		return ok(page(rbiTypes, q, 2))
	case p == "/rest/api/2/issue/createmeta/RBI/issuetypes/10001":
		v.metaRequest[p]++
		return ok(page(rbiStoryFields, q, 5))
	case p == "/rest/api/2/issue/createmeta/RBI/issuetypes/3":
		v.metaRequest[p]++
		return ok(page(rbiTaskFields(), q, 50))
	case p == "/rest/api/2/issue/createmeta/RBI/issuetypes/5":
		return ok(page(append(rbiTaskFields(), `{"fieldId":"parent","name":"Parent","required":true,"schema":{"type":"issuelink","system":"parent"}}`), q, 50))
	case p == "/rest/api/2/issue/createmeta/RBI/issuetypes/10000":
		v.metaRequest[p]++
		return ok(page([]string{rbiStoryFields[0], `{"fieldId":"reporter","name":"Reporter","required":true,"schema":{"type":"user","system":"reporter"}}`}, q, 50))
	case strings.HasPrefix(p, "/rest/api/2/issue/createmeta/LEG"):
		return notFound()
	case p == "/rest/api/2/issue/createmeta":
		v.metaRequest[p+"?"+q.Get("issuetypeIds")]++
		if q.Get("projectKeys") != "LEG" {
			return notFound()
		}
		if q.Get("issuetypeIds") == "3" && q.Get("expand") == "projects.issuetypes.fields" {
			return ok(legLegacy)
		}
		return ok(legLegacyTypes)
	case p == "/rest/api/2/issue" && r.Method == "POST":
		if v.createResp != nil {
			return v.createResp, true
		}
		return &jira.Response{StatusCode: 201, Body: []byte(`{"id":"9077","key":"RBI-77","self":"https://sd.local/rest/api/2/issue/9077"}`)}, true
	case strings.HasSuffix(p, "/editmeta"):
		key := strings.TrimSuffix(strings.TrimPrefix(p, "/rest/api/2/issue/"), "/editmeta")
		if m, found := v.editMeta[key]; found {
			return ok(m)
		}
		return notFound()
	case strings.HasPrefix(p, "/rest/api/2/issue/") && r.Method == "PUT":
		resp := v.putResp
		if resp == nil {
			resp = &jira.Response{StatusCode: 204}
			if v.afterPut != nil {
				v.afterPut()
			}
		}
		return resp, true
	case strings.HasPrefix(p, "/rest/api/2/issue/") && r.Method == "GET" && q.Get("expand") == "":
		key := strings.TrimPrefix(p, "/rest/api/2/issue/")
		v.issueGets = append(v.issueGets, q.Get("fields"))
		if v.failReread && len(v.writes) > 0 {
			return &jira.Response{StatusCode: 500, Body: []byte(`{}`)}, true
		}
		if f, found := v.issue[key]; found {
			return ok(string(projectFields(json.RawMessage(`{"key":"`+key+`","fields":`+f+`}`), q.Get("fields"))))
		}
		return nil, false
	case p == "/rest/api/2/user/assignable/search":
		return ok(v.assignable)
	case strings.HasPrefix(p, "/rest/agile/1.0/sprint/") || p == "/rest/agile/1.0/backlog/issue":
		if v.agileResp != nil {
			return v.agileResp, true
		}
		return &jira.Response{StatusCode: 204}, true
	}
	return nil, false
}

// v5App is an app tracking RBI and LEG with the v0.5 endpoints, Dev/QA
// story point fields, an agile board with sprints and a description sample.
func v5App(t *testing.T) (*App, *fakeJira, *v5Jira, *recorder) {
	t.Helper()
	v := &v5Jira{editMeta: map[string]string{"RBI-12": rbiEditMeta}, issue: map[string]string{"RBI-12": rbi12Fields(v5Updated, "h1. Wiki")},
		assignable: `[{"name":"bob","key":"bob","displayName":"Bob"},{"name":"carol","displayName":""}]`}
	fj := &fakeJira{
		fieldsJSON:  v4Fields,
		byKey:       fxPool(fxRBI1),
		agileBoards: map[string]string{"RBI": `[{"id":880,"name":"BI","type":"scrum"}]`},
		sprints: map[int][]string{880: {`{"id":11,"name":"S11","state":"closed"}`, `{"id":12,"name":"S12","state":"active"}`,
			`{"id":13,"name":"S13","state":"future"}`}},
		routes: map[string]string{descSampleJQL: `[
		 {"key":"RBI-3","fields":{"description":"<p dir=\"auto\">a</p>"}},
		 {"key":"RBI-4","fields":{"description":"<div class=\"jePanel_blue\">b</div>"}},
		 {"key":"RBI-5","fields":{"description":"*wiki*"}}]`},
	}
	fj.handle = v.handle
	a := v2App(t, fj, []string{"LEG", "RBI"})
	rec := &recorder{}
	a.emit = rec.emit
	return a, fj, v, rec
}

func call(t *testing.T, a *App, method, params string) (any, error) {
	t.Helper()
	return a.Call(context.Background(), method, json.RawMessage(params))
}

func fieldSummary(fs []FieldMetaDTO) string {
	var parts []string
	for _, f := range fs {
		s := f.ID + ":" + f.Kind
		if f.Required {
			s += "!"
		}
		if !f.Main {
			s += "(extra)"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func findField(fs []FieldMetaDTO, id string) *FieldMetaDTO {
	for i := range fs {
		if fs[i].ID == id {
			return &fs[i]
		}
	}
	return nil
}

// --- meta ------------------------------------------------------------------------------

func TestCreateMetaPagedEndpoints(t *testing.T) {
	a, _, v, _ := v5App(t)
	res, err := call(t, a, "getCreateMeta", `{"project":"rbi"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(*CreateMetaDTO)
	if m.Project != (ProjectRefDTO{Key: "RBI", Name: "Project RBI"}) || m.DescriptionFormat != DescHTML {
		t.Errorf("project %+v format %s", m.Project, m.DescriptionFormat)
	}
	// Types from two pages; without issueTypeId the first non-subtask type.
	if len(m.IssueTypes) != 4 || m.IssueTypeID != "10000" || v.metaRequest["/rest/api/2/issue/createmeta/RBI/issuetypes"] != 2 {
		t.Fatalf("types %+v chosen %s, %d pages", m.IssueTypes, m.IssueTypeID, v.metaRequest["/rest/api/2/issue/createmeta/RBI/issuetypes"])
	}
	if m.IssueTypes[3] != (CreateTypeDTO{ID: "5", Name: "Подзадача", Kind: KindSubtask, Subtask: true}) || m.IssueTypes[1].Kind != KindStory {
		t.Errorf("types %+v", m.IssueTypes)
	}

	res, err = call(t, a, "getCreateMeta", `{"project":"RBI","issueTypeId":"10001"}`)
	if err != nil {
		t.Fatal(err)
	}
	m = res.(*CreateMetaDTO)
	// Fields from 4 pages of 5; main fields in the SPEC order, then the
	// required extras in Jira's order; optional non-main fields left out.
	want := "summary:summary! description:description assignee:user priority:priority customfield_10103:epic " +
		"customfield_10107:sprint customfield_10130:storyPoints customfield_10204:storyPoints customfield_10205:storyPoints " +
		"labels:labels components:components fixVersions:versions duedate:date timetracking:estimate " +
		"customfield_10301:unsupported!(extra) customfield_10300:select!(extra)"
	if got := fieldSummary(m.Fields); got != want || m.IssueTypeID != "10001" {
		t.Errorf("fields\n%s\nwant\n%s", got, want)
	}
	if n := v.metaRequest["/rest/api/2/issue/createmeta/RBI/issuetypes/10001"]; n != 4 {
		t.Errorf("%d field pages, want 4", n)
	}
	if f := findField(m.Fields, "summary"); f.Max != 255 || f.Name != "Summary" {
		t.Errorf("summary %+v", f)
	}
	if f := findField(m.Fields, "description"); f.Max != 32000 {
		t.Errorf("description %+v", f)
	}
	if f := findField(m.Fields, "priority"); !reflect.DeepEqual(f.Default, map[string]string{"id": "3"}) || len(f.Options) != 2 {
		t.Errorf("priority %+v", f)
	}
	if f := findField(m.Fields, "customfield_10204"); !reflect.DeepEqual(f.Options, []OptionDTO{{ID: "10601", Name: "8"}, {ID: "10603", Name: "5"}}) {
		t.Errorf("dev SP options %+v (disabled left out)", f.Options)
	}
	if f := findField(m.Fields, "customfield_10130"); len(f.Options) != 0 {
		t.Errorf("number story points %+v", f)
	}
	if f := findField(m.Fields, "fixVersions"); len(f.Options) != 1 || f.Options[0].ID != "200" {
		t.Errorf("versions %+v (archived left out)", f.Options)
	}
	// Sprint: not in Jira's create meta, set via the agile API; options are
	// the open sprints of the project's boards, active first.
	f := findField(m.Fields, "customfield_10107")
	if f == nil || !f.agile || f.Name != T("ru", "field_sprint") ||
		!reflect.DeepEqual(f.Options, []OptionDTO{{ID: "12", Name: "S12", State: "active"}, {ID: "13", Name: "S13", State: "future"}}) {
		t.Errorf("sprint %+v", f)
	}
	js, _ := json.Marshal(m)
	for _, s := range []string{`"issueTypeId":"10001"`, `"descriptionFormat":"html"`, `{"id":"priority","name":"Priority","kind":"priority","required":false,"main":true,"options":[{"id":"2","name":"High"},{"id":"3","name":"Medium"}],"default":{"id":"3"}}`,
		`{"id":"customfield_10107","name":"Спринт","kind":"sprint","required":false,"main":true,"options":[{"id":"12","name":"S12","state":"active"}`} {
		if !strings.Contains(string(js), s) {
			t.Errorf("JSON lacks %s", s)
		}
	}
	// Cached: no more meta requests.
	before := v.metaRequest["/rest/api/2/issue/createmeta/RBI/issuetypes/10001"]
	if _, err := call(t, a, "getCreateMeta", `{"project":"RBI","issueTypeId":"10001"}`); err != nil ||
		v.metaRequest["/rest/api/2/issue/createmeta/RBI/issuetypes/10001"] != before {
		t.Errorf("create meta not cached: %v", err)
	}
	// Unknown type id ⇒ the default type; bad parameters.
	if res, err := call(t, a, "getCreateMeta", `{"project":"RBI","issueTypeId":"999"}`); err != nil || res.(*CreateMetaDTO).IssueTypeID != "10000" {
		t.Errorf("unknown type: %v", err)
	}
	for params, code := range map[string]string{`{}`: CodeBadParams, `{"project":"RBI","issueTypeId":"x"}`: CodeBadParams,
		`{"project":"NOPE"}`: CodeProjectUnavailable} {
		if _, err := call(t, a, "getCreateMeta", params); ToError(err).Code != code {
			t.Errorf("%s: %v", params, err)
		}
	}
}

func TestCreateMetaLegacyFallback(t *testing.T) {
	a, _, v, _ := v5App(t)
	res, err := call(t, a, "getCreateMeta", `{"project":"LEG"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(*CreateMetaDTO)
	if m.IssueTypeID != "3" || len(m.IssueTypes) != 2 || m.DescriptionFormat != DescWiki {
		t.Errorf("legacy meta %+v", m)
	}
	if got := fieldSummary(m.Fields); got != "summary:summary! description:description customfield_10103:epic duedate:date customfield_10400:unsupported!(extra)" {
		t.Errorf("legacy fields %s", got)
	}
	if v.metaRequest["/rest/api/2/issue/createmeta?"] != 1 || v.metaRequest["/rest/api/2/issue/createmeta?3"] != 1 {
		t.Errorf("legacy requests %v", v.metaRequest)
	}
}

func TestKindOf(t *testing.T) {
	ids := jira.FieldIDs{EpicLink: "customfield_10103", EpicName: "customfield_10105", Sprint: "customfield_10107",
		StoryPoints: "customfield_10130", DevStoryPoints: "customfield_10204", QAStoryPoints: "customfield_10205"}
	opts := []json.RawMessage{json.RawMessage(`{"id":"1","value":"a"}`)}
	sc := func(typ, items, custom string) *jira.FieldSchema {
		return &jira.FieldSchema{Type: typ, Items: items, Custom: "com.atlassian.jira.plugin.system.customfieldtypes:" + custom}
	}
	tests := []struct {
		f    jira.MetaField
		kind string
		max  int
	}{
		{jira.MetaField{ID: "issuetype"}, "", 0},
		{jira.MetaField{ID: "project"}, "", 0},
		{jira.MetaField{ID: "summary"}, FKSummary, 255},
		{jira.MetaField{ID: "description"}, FKDescription, 32000},
		{jira.MetaField{ID: "environment"}, FKTextarea, 32000},
		{jira.MetaField{ID: "reporter"}, FKUser, 0},
		{jira.MetaField{ID: "versions"}, FKVersions, 0},
		{jira.MetaField{ID: "customfield_10103"}, FKEpic, 0},
		{jira.MetaField{ID: "customfield_10105"}, FKEpicName, 255},
		{jira.MetaField{ID: "customfield_10107"}, FKSprint, 0},
		{jira.MetaField{ID: "customfield_10130", Schema: &jira.FieldSchema{Type: "number"}}, FKStoryPoints, 0},
		{jira.MetaField{ID: "customfield_10204", Schema: &jira.FieldSchema{Type: "option"}}, FKStoryPoints, 0},
		{jira.MetaField{ID: "customfield_10205", Schema: &jira.FieldSchema{Type: "string"}}, FKUnsupported, 0},
		{jira.MetaField{ID: "customfield_1", Schema: sc("string", "", "textfield")}, FKText, 255},
		{jira.MetaField{ID: "customfield_2", Schema: sc("string", "", "textarea")}, FKTextarea, 32000},
		{jira.MetaField{ID: "customfield_3", Schema: sc("string", "", "url")}, FKText, 255},
		{jira.MetaField{ID: "customfield_4", Schema: sc("string", "", "readonlyfield")}, FKUnsupported, 0},
		{jira.MetaField{ID: "customfield_5", Schema: sc("number", "", "float")}, FKNumber, 0},
		{jira.MetaField{ID: "customfield_6", Schema: sc("date", "", "datepicker")}, FKDate, 0},
		{jira.MetaField{ID: "customfield_7", Schema: sc("datetime", "", "datetime")}, FKDateTime, 0},
		{jira.MetaField{ID: "customfield_8", Schema: sc("option", "", "select"), AllowedValues: opts}, FKSelect, 0},
		{jira.MetaField{ID: "customfield_9", Schema: sc("option", "", "radiobuttons"), AllowedValues: opts}, FKSelect, 0},
		{jira.MetaField{ID: "customfield_10", Schema: sc("option", "", "select")}, FKUnsupported, 0}, // no values to offer
		{jira.MetaField{ID: "customfield_11", Schema: sc("array", "option", "multiselect"), AllowedValues: opts}, FKMultiselect, 0},
		{jira.MetaField{ID: "customfield_12", Schema: sc("array", "option", "multicheckboxes"), AllowedValues: opts}, FKMultiselect, 0},
		{jira.MetaField{ID: "customfield_13", Schema: sc("user", "", "userpicker")}, FKUser, 0},
		{jira.MetaField{ID: "customfield_14", Schema: sc("array", "user", "multiuserpicker")}, FKUsers, 0},
		{jira.MetaField{ID: "customfield_15", Schema: sc("array", "string", "labels")}, FKLabels, 0},
		{jira.MetaField{ID: "customfield_16", Schema: sc("array", "string", "multitextfield")}, FKUnsupported, 0},
		{jira.MetaField{ID: "customfield_17", Schema: sc("version", "", "version"), AllowedValues: opts}, FKSelect, 0},
		{jira.MetaField{ID: "customfield_18", Schema: sc("array", "version", "multiversion")}, FKVersions, 0},
		{jira.MetaField{ID: "customfield_19", Schema: sc("option-with-child", "", "cascadingselect"), AllowedValues: opts}, FKUnsupported, 0},
		{jira.MetaField{ID: "customfield_20", Schema: sc("group", "", "grouppicker")}, FKUnsupported, 0},
		{jira.MetaField{ID: "customfield_21", Schema: sc("project", "", "project")}, FKUnsupported, 0},
		{jira.MetaField{ID: "customfield_22", Schema: sc("any", "", "something")}, FKUnsupported, 0},
		{jira.MetaField{ID: "customfield_23"}, FKUnsupported, 0},
		{jira.MetaField{ID: "issuelinks", Schema: &jira.FieldSchema{Type: "array", Items: "issuelinks", System: "issuelinks"}}, FKUnsupported, 0},
		{jira.MetaField{ID: "security", Schema: &jira.FieldSchema{Type: "securitylevel", System: "security"}, AllowedValues: opts}, FKSelect, 0},
	}
	for _, tc := range tests {
		kind, max := kindOf(tc.f, ids)
		if kind != tc.kind || max != tc.max {
			t.Errorf("%s %+v: %q %d, want %q %d", tc.f.ID, tc.f.Schema, kind, max, tc.kind, tc.max)
		}
	}
	// The main field set depends on the issue type.
	for typeKind, want := range map[string]string{
		KindEpic:    "summary description assignee priority customfield_10105 customfield_10107",
		KindSubtask: "summary description assignee priority parent customfield_10107",
		KindTask:    "summary description assignee priority customfield_10103 customfield_10107",
	} {
		if got := strings.Join(mainOrder(ids, typeKind)[:6], " "); got != want {
			t.Errorf("%s: %s", typeKind, got)
		}
	}
}

// --- values ----------------------------------------------------------------------------

func TestMapValue(t *testing.T) {
	opts := []OptionDTO{{ID: "1", Name: "a"}, {ID: "2", Name: "b"}}
	f := func(kind string, o ...OptionDTO) FieldMetaDTO {
		return FieldMetaDTO{ID: "f", Kind: kind, Options: o}
	}
	long := strings.Repeat("я", 256)
	tests := []struct {
		name  string
		field FieldMetaDTO
		in    string
		want  string // Jira JSON, or "!" + error key
		empty bool
	}{
		{"summary trimmed", FieldMetaDTO{ID: "f", Kind: FKSummary, Max: 255}, `"  Fix it  "`, `"Fix it"`, false},
		{"summary too long", FieldMetaDTO{ID: "f", Kind: FKSummary, Max: 255}, `"` + long + `"`, "!fe_too_long", false},
		{"summary multi-line", FieldMetaDTO{ID: "f", Kind: FKSummary, Max: 255}, `"a\nb"`, "!fe_single_line", false},
		{"summary not a string", FieldMetaDTO{ID: "f", Kind: FKSummary, Max: 255}, `5`, "!fe_invalid", false},
		{"summary blank", FieldMetaDTO{ID: "f", Kind: FKSummary, Max: 255}, `"  "`, `null`, true},
		{"description kept as is", FieldMetaDTO{ID: "f", Kind: FKDescription, Max: 32000}, `"<p dir=\"auto\">x</p>\n"`, `"<p dir=\"auto\">x</p>\n"`, false},
		{"description too long", FieldMetaDTO{ID: "f", Kind: FKDescription, Max: 32000}, `"` + strings.Repeat("a", 32001) + `"`, "!fe_too_long", false},
		{"text", FieldMetaDTO{ID: "f", Kind: FKText}, `"x"`, `"x"`, false},
		{"text default max", FieldMetaDTO{ID: "f", Kind: FKText}, `"` + long + `"`, "!fe_too_long", false},
		{"textarea multi-line", FieldMetaDTO{ID: "f", Kind: FKTextarea, Max: 32000}, `"a\nb"`, `"a\nb"`, false},
		{"epic name", FieldMetaDTO{ID: "f", Kind: FKEpicName, Max: 255}, `"Platform"`, `"Platform"`, false},
		{"number", f(FKNumber), `2.5`, `2.5`, false},
		{"number null", f(FKNumber), `null`, `null`, true},
		{"number as string", f(FKNumber), `"2"`, "!fe_number", false},
		{"story points number", f(FKStoryPoints), `3`, `3`, false},
		{"story points select", f(FKStoryPoints, opts...), `{"id":"2"}`, `{"id":"2"}`, false},
		{"story points select bad id", f(FKStoryPoints, opts...), `{"id":"9"}`, "!fe_option", false},
		{"date", f(FKDate), `"2026-12-01"`, `"2026-12-01"`, false},
		{"date bad", f(FKDate), `"01.12.2026"`, "!fe_date", false},
		{"date empty", f(FKDate), `""`, `null`, true},
		{"datetime RFC 3339", f(FKDateTime), `"2026-12-01T10:30:00+02:00"`, `"2026-12-01T10:30:00.000+0200"`, false},
		{"datetime bad", f(FKDateTime), `"tomorrow"`, "!fe_datetime", false},
		{"datetime local minutes", f(FKDateTime), `"2026-12-01T10:30"`, strconv.Quote(jira.FormatTime(time.Date(2026, 12, 1, 10, 30, 0, 0, time.Local))), false},
		{"datetime local seconds", f(FKDateTime), `"2026-12-01T10:30:45"`, strconv.Quote(jira.FormatTime(time.Date(2026, 12, 1, 10, 30, 45, 0, time.Local))), false},
		{"datetime local without minutes", f(FKDateTime), `"2026-12-01T10"`, "!fe_datetime", false},
		{"select", f(FKSelect, opts...), `{"id":"1"}`, `{"id":"1"}`, false},
		{"select numeric id", f(FKSelect, opts...), `{"id":2}`, `{"id":"2"}`, false},
		{"select not offered", f(FKSelect, opts...), `{"id":"3"}`, "!fe_option", false},
		{"select plain string", f(FKSelect, opts...), `"1"`, "!fe_invalid", false},
		{"select cleared", f(FKSelect, opts...), `null`, `null`, true},
		{"priority", f(FKPriority, opts...), `{"id":"2","name":"b"}`, `{"id":"2"}`, false},
		{"multiselect", f(FKMultiselect, opts...), `[{"id":"1"},{"id":"2"},{"id":"1"}]`, `[{"id":"1"},{"id":"2"}]`, false},
		{"multiselect ids as read", f(FKMultiselect, opts...), `["1","2"]`, `[{"id":"1"},{"id":"2"}]`, false},
		{"multiselect garbage", f(FKMultiselect, opts...), `[true]`, "!fe_invalid", false},
		{"multiselect not a list", f(FKMultiselect, opts...), `{"id":"1"}`, "!fe_invalid", false},
		{"components", f(FKComponents, opts...), `[{"id":"2"}]`, `[{"id":"2"}]`, false},
		{"versions not offered", f(FKVersions, opts...), `[{"id":"7"}]`, "!fe_option", false},
		{"versions cleared", f(FKVersions, opts...), `[]`, `[]`, true},
		{"labels", f(FKLabels), `["a"," b ","a",""]`, `["a","b"]`, false},
		{"labels with space", f(FKLabels), `["a b"]`, "!fe_label_space", false},
		{"labels not strings", f(FKLabels), `[1]`, "!fe_invalid", false},
		{"user", f(FKUser), `{"name":"bob","displayName":"Bob"}`, `{"name":"bob"}`, false},
		{"user unassigned", f(FKUser), `null`, `null`, true},
		{"user empty name", f(FKUser), `{"name":""}`, "!fe_invalid", false},
		{"users", f(FKUsers), `[{"name":"bob"},"carol"]`, `[{"name":"bob"},{"name":"carol"}]`, false},
		{"users empty name", f(FKUsers), `[{"name":""}]`, "!fe_invalid", false},
		{"users not names", f(FKUsers), `[5]`, "!fe_invalid", false},
		{"users cleared", f(FKUsers), `[]`, `[]`, true},
		{"user name with a line break", f(FKUser), `{"name":"a\nb"}`, "!fe_invalid", false},
		{"user name string with a line break", f(FKUser), `"a\rb"`, "!fe_invalid", false},
		{"labels over the list limit", f(FKLabels), `[` + strings.Repeat(`"x",`, maxListValues) + `"y"]`, "!fe_invalid", false},
		{"labels at the list limit", f(FKLabels), `[` + strings.TrimSuffix(strings.Repeat(`"x",`, maxListValues), ",") + `]`, `["x"]`, false},
		{"multiselect over the list limit", f(FKMultiselect, opts...), `[` + strings.Repeat(`"1",`, maxListValues) + `"2"]`, "!fe_invalid", false},
		{"users over the list limit", f(FKUsers), `[` + strings.Repeat(`"bob",`, maxListValues) + `"carol"]`, "!fe_invalid", false},
		{"epic key", f(FKEpic), `"rbi-1"`, `"RBI-1"`, false},
		{"epic object", f(FKEpic), `{"key":"RBI-1","summary":"x"}`, `"RBI-1"`, false},
		{"epic removed", f(FKEpic), `null`, `null`, true},
		{"epic bad key", f(FKEpic), `"RBI 1"`, "!fe_key", false},
		{"parent", f(FKParent), `{"key":"RBI-2"}`, `{"key":"RBI-2"}`, false},
		{"parent null", f(FKParent), `null`, "!fe_required", false},
		{"sprint", f(FKSprint, OptionDTO{ID: "12"}), `12`, `12`, false},
		{"sprint not offered", f(FKSprint, OptionDTO{ID: "12"}), `11`, "!fe_option", false},
		{"sprint as string", f(FKSprint, OptionDTO{ID: "12"}), `"12"`, "!fe_invalid", false},
		{"sprint fraction", f(FKSprint, OptionDTO{ID: "12"}), `12.5`, "!fe_invalid", false},
		{"sprint backlog", f(FKSprint, OptionDTO{ID: "12"}), `null`, `null`, true},
		{"estimate", f(FKEstimate), `{"originalEstimate":"1D 2h","remainingEstimate":"30m"}`, `{"originalEstimate":"1d 2h","remainingEstimate":"30m"}`, false},
		{"estimate one", f(FKEstimate), `{"originalEstimate":"2h","remainingEstimate":""}`, `{"originalEstimate":"2h"}`, false},
		{"estimate bad", f(FKEstimate), `{"originalEstimate":"2 hours"}`, "!fe_duration", false},
		{"estimate not an object", f(FKEstimate), `"2h"`, "!fe_invalid", false},
		{"unsupported", f(FKUnsupported), `"x"`, "!fe_unsupported", false},
	}
	for _, tc := range tests {
		errs := &fieldErrs{lang: "en"}
		val, empty, ok := mapValue(tc.field, json.RawMessage(tc.in), writeCtx{hoursPerDay: 9, html: htmlRules{jiraURL: "https://sd.local"}}, errs)
		if strings.HasPrefix(tc.want, "!") {
			key, msg := tc.want[1:], ""
			if len(errs.errs) == 1 {
				msg = errs.errs[0].Message
			}
			if ok || msg != T("en", key) && msg != T("en", key, 255) && msg != T("en", key, 32000) {
				t.Errorf("%s: ok=%v errs=%+v, want %s", tc.name, ok, errs.errs, tc.want)
			}
			continue
		}
		var buf strings.Builder
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(val)
		got := strings.TrimSuffix(buf.String(), "\n")
		if !ok || got != tc.want || empty != tc.empty {
			t.Errorf("%s: %s empty=%v ok=%v errs=%+v, want %s empty=%v", tc.name, got, empty, ok, errs.errs, tc.want, tc.empty)
		}
	}
}

// --- create ------------------------------------------------------------------------------

func canon(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("%v: %s", err, s)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func TestCreateIssue(t *testing.T) {
	a, _, v, rec := v5App(t)
	c, id, _ := a.client()
	_, gen, _ := a.cacheGet("board:mine::0", id, 0)
	a.cachePut("board:mine::0", id, gen, a.clock.Now(), "stale")
	_ = c
	res, err := call(t, a, "createIssue", `{"project":"RBI","issueTypeId":"3","values":{
	 "summary":"  New  ","description":"<p>x</p>","assignee":{"name":"bob","displayName":"Bob"},"priority":{"id":"2"},
	 "customfield_10103":"rbi-1","customfield_10107":12,"customfield_10130":3,"customfield_10204":{"id":"10601"},
	 "labels":["a","b","a"],"components":[{"id":"100"}],"fixVersions":[{"id":"200"}],"duedate":"2026-12-01",
	 "timetracking":{"originalEstimate":"1D 2h","remainingEstimate":""},"customfield_10300":{"id":"500"},"customfield_10205":null}}`)
	if err != nil {
		t.Fatal(err)
	}
	if d := res.(*CreatedDTO); *d != (CreatedDTO{Key: "RBI-77", URL: "https://sd.local/browse/RBI-77"}) {
		t.Errorf("result %+v", d)
	}
	if len(v.writes) != 2 {
		t.Fatalf("writes %+v", v.writes)
	}
	want := `{"fields":{"assignee":{"name":"bob"},"components":[{"id":"100"}],"customfield_10103":"RBI-1","customfield_10130":3,
	 "customfield_10204":{"id":"10601"},"customfield_10300":{"id":"500"},"description":"<p>x</p>","duedate":"2026-12-01",
	 "fixVersions":[{"id":"200"}],"issuetype":{"id":"3"},"labels":["a","b"],"priority":{"id":"2"},"project":{"key":"RBI"},
	 "summary":"New","timetracking":{"originalEstimate":"1d 2h"}}}`
	if w := v.writes[0]; w.method != "POST" || w.path != "/rest/api/2/issue" || canon(t, w.body) != canon(t, want) {
		t.Errorf("create request %s %s\n%s", w.method, w.path, w.body)
	}
	// The sprint is not on the create screen: set through the agile API.
	if w := v.writes[1]; w.path != "/rest/agile/1.0/sprint/12/issue" || canon(t, w.body) != `{"issues":["RBI-77"]}` {
		t.Errorf("sprint move %+v", w)
	}
	if _, _, ok := a.cacheGet("board:mine::0", id, time.Hour); ok {
		t.Error("board cache kept after create")
	}
	if len(rec.of("board")) != 1 {
		t.Errorf("events %+v", rec.events)
	}
}

func TestCreateIssueValidation(t *testing.T) {
	a, _, v, _ := v5App(t)
	// Story: the unsupported required field blocks creation; required fields
	// without a value are listed; nothing is sent.
	_, err := call(t, a, "createIssue", `{"project":"RBI","issueTypeId":"10001","values":{"summary":" ","labels":["a b"]}}`)
	e := ToError(err)
	var got []string
	for _, f := range e.Fields {
		got = append(got, f.Field+"="+f.Message)
	}
	want := []string{"labels=" + T("ru", "fe_label_space"), "summary=" + T("ru", "fe_required"),
		"customfield_10301=" + T("ru", "fe_unsupported"), "customfield_10300=" + T("ru", "fe_required")}
	if e.Code != CodeValidation || e.Message != T("ru", "err_validation_fields") || !reflect.DeepEqual(got, want) {
		t.Errorf("validation %+v\n%v", e, got)
	}
	for params, code := range map[string]string{
		`{"project":"RBI","issueTypeId":"3","values":{"customfield_10302":"x"}}`:     CodeBadParams, // optional field not in the form
		`{"project":"RBI","issueTypeId":"3","values":{"issuetype":{"id":"5"}}}`:      CodeBadParams,
		`{"project":"RBI","issueTypeId":"3","values":{"customfield_99999":1}}`:       CodeBadParams,
		`{"project":"RBI","values":{"summary":"x"}}`:                                 CodeBadParams,
		`{"project":"RBI","issueTypeId":"999","values":{"summary":"x"}}`:             CodeBadParams,
		`{"project":"RBI","issueTypeId":"3","values":{"summary":"x"},"extra":[}`:     CodeBadParams,
		`{"project":"RBI","issueTypeId":"3","values":{"summary":"x","duedate":"x"}}`: CodeValidation,
	} {
		if _, err := call(t, a, "createIssue", params); ToError(err).Code != code {
			t.Errorf("%s: %v, want %s", params, err, code)
		}
	}
	if len(v.writes) != 0 {
		t.Errorf("writes after failed validation: %+v", v.writes)
	}
	if e := ToError(err); !strings.Contains(T("ru", "err_unknown_field", "x"), "x") || e == nil {
		t.Error("unknown field message")
	}
}

func TestCreateIssueJiraErrors(t *testing.T) {
	tests := []struct {
		name   string
		resp   *jira.Response
		code   string
		fields map[string]string
		msg    string
	}{
		{"field errors", &jira.Response{StatusCode: 400, Body: []byte(`{"errorMessages":["Team is wrong"],
		  "errors":{"customfield_10300":"Bad team","timeoriginalestimate":"Bad estimate","foo":"Bar"}}`)},
			CodeValidation, map[string]string{"customfield_10300": "Bad team", "timetracking": "Bad estimate"}, "foo: Bar; Team is wrong"},
		{"no permission", &jira.Response{StatusCode: 403, Body: []byte(`{"errorMessages":["no"]}`)}, CodeForbiddenEdit, nil, ""},
		{"server error", &jira.Response{StatusCode: 500, Body: []byte(`{}`)}, jira.CodeServer, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, _, v, rec := v5App(t)
			v.createResp = tc.resp
			_, err := call(t, a, "createIssue", `{"project":"RBI","issueTypeId":"3","values":{"summary":"x","customfield_10300":{"id":"500"}}}`)
			e := ToError(err)
			if e.Code != tc.code {
				t.Fatalf("%+v, want %s", e, tc.code)
			}
			if tc.fields != nil {
				got := map[string]string{}
				for _, f := range e.Fields {
					got[f.Field] = f.Message
				}
				if !reflect.DeepEqual(got, tc.fields) || e.Message != T("ru", "err_validation_fields")+": "+tc.msg {
					t.Errorf("fields %v message %q", got, e.Message)
				}
			}
			if len(rec.of("board")) != 0 {
				t.Error("refresh event after a failed create")
			}
		})
	}
}

// --- edit ---------------------------------------------------------------------------------

func TestEditMeta(t *testing.T) {
	a, _, _, _ := v5App(t)
	res, err := call(t, a, "getEditMeta", `{"key":"RBI-12"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(*EditMetaDTO)
	if m.Key != "RBI-12" || m.IssueTypeID != "3" || m.Project != (ProjectRefDTO{Key: "RBI", Name: "Project RBI"}) ||
		m.Updated != v5Updated || m.DescriptionFormat != DescWiki {
		t.Errorf("meta %+v", m)
	}
	if got := fieldSummary(m.Fields); got != "summary:summary! description:description assignee:user priority:priority customfield_10103:epic "+
		"customfield_10107:sprint customfield_10204:storyPoints labels:labels components:components duedate:date timetracking:estimate "+
		"customfield_10300:select!(extra) customfield_10500:users!(extra)" {
		t.Errorf("fields %s", got)
	}
	js, _ := json.Marshal(m.Values)
	want := `{"assignee":{"name":"alex","displayName":"Alex"},"components":["100"],"customfield_10103":{"key":"RBI-1","summary":"S RBI-1"},
	 "customfield_10107":12,"customfield_10204":{"id":"10603"},"customfield_10300":{"id":"501"},"customfield_10500":[{"name":"bob","displayName":"Bob"}],
	 "description":"h1. Wiki","duedate":"2026-12-01","labels":["x"],"priority":{"id":"3"},"summary":"Old",
	 "timetracking":{"originalEstimate":"1d","remainingEstimate":"4h"}}`
	if canon(t, string(js)) != canon(t, want) {
		t.Errorf("values\n%s\nwant\n%s", js, canon(t, want))
	}
	if _, err := call(t, a, "getEditMeta", `{"key":"RBI 12"}`); ToError(err).Code != CodeBadParams {
		t.Errorf("bad key %v", err)
	}
}

func TestEditMetaDescriptionFormat(t *testing.T) {
	for desc, want := range map[string]string{"<p dir=\"auto\">x</p>": DescHTML, "*wiki*": DescWiki, "": DescHTML /* project default */} {
		a, _, v, _ := v5App(t)
		v.issue["RBI-12"] = rbi12Fields(v5Updated, desc)
		res, err := call(t, a, "getEditMeta", `{"key":"RBI-12"}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := res.(*EditMetaDTO).DescriptionFormat; got != want {
			t.Errorf("%q: %s, want %s", desc, got, want)
		}
	}
}

func TestProjectDescriptionFormatCached(t *testing.T) {
	a, fj, _, _ := v5App(t)
	c, id, _ := a.client()
	clock := a.clock.(*fakeClock)
	for i, step := range []struct {
		after    time.Duration
		searches int
	}{{0, 1}, {29 * time.Minute, 1}, {2 * time.Minute, 2}} {
		clock.t = clock.t.Add(step.after)
		if f := a.projectDescriptionFormat(context.Background(), c, id, "RBI"); f != DescHTML {
			t.Errorf("step %d: %s", i, f)
		}
		if n := countJQL(fj, descSampleJQL); n != step.searches {
			t.Errorf("step %d: %d searches, want %d", i, n, step.searches)
		}
	}
	// Mostly wiki ⇒ wiki; no sample (or a failing search) ⇒ wiki.
	fj.routes[`project = "LEG" AND description is not EMPTY ORDER BY updated DESC`] = `[
	 {"key":"LEG-1","fields":{"description":"<p>a</p>"}},{"key":"LEG-2","fields":{"description":"h1. b"}}]`
	if f := a.projectDescriptionFormat(context.Background(), c, id, "LEG"); f != DescWiki {
		t.Errorf("tie: %s", f)
	}
}

func TestUpdateIssue(t *testing.T) {
	tests := []struct {
		name     string
		values   string
		expected string
		putResp  *jira.Response
		code     string  // "" = success
		writes   []write // expected requests (bodies canonical)
	}{
		{"only the given fields", `{"summary":"New","labels":[]}`, v5Updated, nil, "",
			[]write{{"PUT", "/rest/api/2/issue/RBI-12", `{"fields":{"labels":[],"summary":"New"}}`}}},
		{"sprint via the agile API", `{"customfield_10107":13,"assignee":null}`, v5Updated, nil, "",
			[]write{{"PUT", "/rest/api/2/issue/RBI-12", `{"fields":{"assignee":null}}`},
				{"POST", "/rest/agile/1.0/sprint/13/issue", `{"issues":["RBI-12"]}`}}},
		{"to the backlog", `{"customfield_10107":null}`, v5Updated, nil, "",
			[]write{{"POST", "/rest/agile/1.0/backlog/issue", `{"issues":["RBI-12"]}`}}},
		{"changed meanwhile", `{"summary":"New"}`, "2026-11-12T09:00:00.000+0200", nil, CodeEditConflict, nil},
		{"same instant in RFC 3339", `{"summary":"New"}`, "2026-11-12T10:00:00+02:00", nil, "",
			[]write{{"PUT", "/rest/api/2/issue/RBI-12", `{"fields":{"summary":"New"}}`}}},
		{"field not in the edit meta", `{"customfield_10600":"2026-12-01T10:00"}`, v5Updated, nil, CodeBadParams, nil},
		{"required field emptied", `{"customfield_10300":null}`, v5Updated, nil, CodeValidation, nil},
		{"closed sprint", `{"customfield_10107":11}`, v5Updated, nil, CodeValidation, nil},
		{"no permission", `{"summary":"New"}`, v5Updated, &jira.Response{StatusCode: 403, Body: []byte(`{}`)}, CodeForbiddenEdit,
			[]write{{"PUT", "/rest/api/2/issue/RBI-12", `{"fields":{"summary":"New"}}`}}},
		{"Jira rejects a value", `{"duedate":"2026-12-01"}`, v5Updated,
			&jira.Response{StatusCode: 400, Body: []byte(`{"errorMessages":[],"errors":{"duedate":"Due date must be later"}}`)}, CodeValidation,
			[]write{{"PUT", "/rest/api/2/issue/RBI-12", `{"fields":{"duedate":"2026-12-01"}}`}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, _, v, rec := v5App(t)
			v.putResp = tc.putResp
			v.afterPut = func() { v.issue["RBI-12"] = rbi12Fields(v5Updated2, "h1. Wiki") }
			b, _ := json.Marshal(tc.expected)
			res, err := call(t, a, "updateIssue", `{"key":"RBI-12","values":`+tc.values+`,"expectedUpdated":`+string(b)+`}`)
			if tc.code != "" {
				if ToError(err).Code != tc.code {
					t.Fatalf("%v, want %s", err, tc.code)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(v.writes) != len(tc.writes) {
				t.Fatalf("writes %+v, want %+v", v.writes, tc.writes)
			}
			for i, w := range tc.writes {
				if g := v.writes[i]; g.method != w.method || g.path != w.path || canon(t, g.body) != canon(t, w.body) {
					t.Errorf("write %d: %+v, want %+v", i, g, w)
				}
			}
			if tc.code == "" {
				d := res.(*UpdatedIssueDTO)
				wantUpdated := v5Updated
				if v.writes[0].method == "PUT" {
					wantUpdated = v5Updated2
				}
				if d.Key != "RBI-12" || d.Updated != wantUpdated {
					t.Errorf("result %+v", d)
				}
				if len(rec.of("board")) != 1 {
					t.Errorf("events %+v", rec.events)
				}
			} else if len(rec.of("board")) != 0 {
				t.Errorf("refresh after a failed update: %+v", rec.events)
			}
			if tc.code == CodeEditConflict && ToError(err).Message != T("ru", "err_edit_conflict") {
				t.Errorf("conflict message %q", ToError(err).Message)
			}
		})
	}
}

func TestEditWithoutPermission(t *testing.T) {
	a, _, v, _ := v5App(t)
	v.editMeta["RBI-12"] = `{"fields":{}}`
	res, err := call(t, a, "getEditMeta", `{"key":"RBI-12"}`)
	if err != nil {
		t.Fatal(err)
	}
	if m := res.(*EditMetaDTO); len(m.Fields) != 0 || m.Fields == nil {
		t.Errorf("fields %+v (want [])", m.Fields)
	}
	if js, _ := json.Marshal(res); !strings.Contains(string(js), `"fields":[]`) {
		t.Errorf("JSON %s", js)
	}
	_, err = call(t, a, "updateIssue", `{"key":"RBI-12","values":{"summary":"x"},"expectedUpdated":"`+v5Updated+`"}`)
	if ToError(err).Code != CodeForbiddenEdit || len(v.writes) != 0 {
		t.Errorf("%v, writes %+v", err, v.writes)
	}
	for _, params := range []string{`{"key":"RBI-12","values":{}}`, `{"key":"x","values":{},"expectedUpdated":"y"}`} {
		if _, err := call(t, a, "updateIssue", params); ToError(err).Code != CodeBadParams {
			t.Errorf("%s: %v", params, err)
		}
	}
}

// --- pickers -----------------------------------------------------------------------------

func TestSearchAssignable(t *testing.T) {
	a, fj, _, _ := v5App(t)
	res, err := call(t, a, "searchAssignable", `{"project":"RBI","query":" bo "}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.([]UserDTO); !reflect.DeepEqual(got, []UserDTO{{Name: "bob", DisplayName: "Bob"}, {Name: "carol", DisplayName: "carol"}}) {
		t.Errorf("users %+v", got)
	}
	if _, err := call(t, a, "searchAssignable", `{"key":"RBI-12","query":""}`); err != nil {
		t.Fatal(err)
	}
	reqs := fj.requests("/rest/api/2/user/assignable/search")
	if len(reqs) != 2 || reqs[0] != "/rest/api/2/user/assignable/search?maxResults=20&project=RBI&username=bo" ||
		reqs[1] != "/rest/api/2/user/assignable/search?issueKey=RBI-12&maxResults=20&username=" {
		t.Errorf("requests %v", reqs)
	}
	for _, params := range []string{`{"query":"x"}`, `{"project":"a b"}`, `{"key":"RBI 1"}`, `{"project":"RBI","query":"` + strings.Repeat("x", 101) + `"}`} {
		if _, err := call(t, a, "searchAssignable", params); ToError(err).Code != CodeBadParams {
			t.Errorf("%s: %v", params, err)
		}
	}
}

func TestSearchIssuesFor(t *testing.T) {
	a, fj, _, _ := v5App(t)
	tests := []struct {
		params string
		jql    string
	}{
		{`{"purpose":"epic","project":"RBI","query":""}`, `project = "RBI" AND issuetype = Epic AND statusCategory != Done ORDER BY updated DESC`},
		{`{"purpose":"epic","project":"rbi","query":"fix \"it\" OR key = X*"}`, `project = "RBI" AND issuetype = Epic AND statusCategory != Done AND (summary ~ "fix it or key x*") ORDER BY updated DESC`},
		{`{"purpose":"parent","project":"RBI","query":"rbi-1"}`, `project = "RBI" AND issuetype not in subTaskIssueTypes() AND statusCategory != Done AND (summary ~ "rbi-1*" OR key = "RBI-1") ORDER BY updated DESC`},
		{`{"purpose":"parent","project":"RBI","query":"Загрузка"}`, `project = "RBI" AND issuetype not in subTaskIssueTypes() AND statusCategory != Done AND (summary ~ "загрузка*") ORDER BY updated DESC`},
		{`{"purpose":"epic","project":"RBI","query":"a-b ..c_d. (x) +y -z NOT w*?"}`, `project = "RBI" AND issuetype = Epic AND statusCategory != Done AND (summary ~ "a-b c_d x y z not w*") ORDER BY updated DESC`},
	}
	for _, tc := range tests {
		fj.routes[tc.jql] = fxList(fxRBI1)
		res, err := call(t, a, "searchIssuesFor", tc.params)
		if err != nil {
			t.Errorf("%s: %v (searched %v)", tc.params, err, fj.searched()[len(fj.searched())-1])
			continue
		}
		if refs := res.([]IssueRef); len(refs) != 1 || refs[0].Key != "RBI-1" || refs[0].Type.Kind != KindEpic {
			t.Errorf("%s: %+v", tc.params, refs)
		}
	}
	// A key-shaped query of an issue that does not exist must not fail the
	// search: validateQuery=false.
	for _, p := range fj.requests("/rest/api/2/search?") {
		if u, _ := url.Parse(p); strings.Contains(u.Query().Get("jql"), "statusCategory != Done") && u.Query().Get("validateQuery") != "false" {
			t.Errorf("picker search without validateQuery=false: %s", p)
		}
	}
	for _, params := range []string{`{"purpose":"x","project":"RBI"}`, `{"purpose":"epic"}`, `{"purpose":"epic","project":"RBI","query":"` + strings.Repeat("x", 101) + `"}`} {
		if _, err := call(t, a, "searchIssuesFor", params); ToError(err).Code != CodeBadParams {
			t.Errorf("%s: %v", params, err)
		}
	}
}

// --- review round: CR-18…CR-21, security ---------------------------------------------

func TestUpdateSprintMoveFailureIsAWarning(t *testing.T) {
	tests := []struct {
		name        string
		values      string
		failReread  bool
		wantErr     bool
		wantUpdated string
		warning     bool
	}{
		{"fields saved, sprint not", `{"summary":"New","customfield_10107":13}`, false, false, v5Updated2, true},
		{"only the sprint, which fails", `{"customfield_10107":13}`, false, true, "", false},
		{"saved, updated not re-read", `{"summary":"New"}`, true, false, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, _, v, rec := v5App(t)
			v.agileResp = &jira.Response{StatusCode: 400, Body: []byte(`{"errorMessages":["Sprint is closed"]}`)}
			v.failReread = tc.failReread
			v.afterPut = func() { v.issue["RBI-12"] = rbi12Fields(v5Updated2, "h1. Wiki") }
			res, err := call(t, a, "updateIssue", `{"key":"RBI-12","values":`+tc.values+`,"expectedUpdated":"`+v5Updated+`"}`)
			if tc.wantErr {
				if err == nil || len(rec.of("board")) != 0 {
					t.Fatalf("want an error without refresh: %v %+v", err, rec.events)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			d := res.(*UpdatedIssueDTO)
			if d.Updated != tc.wantUpdated || (d.Warning != "") != tc.warning || len(rec.of("board")) != 1 {
				t.Errorf("result %+v events %+v", d, rec.events)
			}
			if tc.warning && !strings.HasPrefix(d.Warning, strings.Split(T("ru", "warn_sprint_not_changed", ""), "%")[0][:10]) {
				t.Errorf("warning %q", d.Warning)
			}
			if js, _ := json.Marshal(d); tc.warning != strings.Contains(string(js), `"warning":`) {
				t.Errorf("JSON %s", js)
			}
		})
	}
}

func TestCreateSprintMoveFailureIsAWarning(t *testing.T) {
	a, _, v, _ := v5App(t)
	v.agileResp = &jira.Response{StatusCode: 400, Body: []byte(`{"errorMessages":["Sprint is closed"]}`)}
	res, err := call(t, a, "createIssue", `{"project":"RBI","issueTypeId":"3","values":{"summary":"x","customfield_10300":{"id":"500"},"customfield_10107":12}}`)
	if err != nil {
		t.Fatal(err)
	}
	if d := res.(*CreatedDTO); d.Key != "RBI-77" || d.Warning == "" || !strings.Contains(d.Warning, "Sprint is closed") {
		t.Errorf("result %+v", d)
	}
}

func TestSubtaskHasNoAgileSprint(t *testing.T) {
	a, _, _, _ := v5App(t)
	res, err := call(t, a, "getCreateMeta", `{"project":"RBI","issueTypeId":"5"}`)
	if err != nil {
		t.Fatal(err)
	}
	fs := res.(*CreateMetaDTO).Fields
	if findField(fs, "customfield_10107") != nil || findField(fs, "customfield_10103") != nil {
		t.Errorf("sub-task fields %s", fieldSummary(fs))
	}
	if f := findField(fs, "parent"); f == nil || f.Kind != FKParent || !f.Main || !f.Required {
		t.Errorf("parent %+v", f)
	}
}

func TestRequiredReporterDefaultsToMe(t *testing.T) {
	a, _, v, _ := v5App(t)
	res, err := call(t, a, "getCreateMeta", `{"project":"RBI","issueTypeId":"10000"}`)
	if err != nil {
		t.Fatal(err)
	}
	f := findField(res.(*CreateMetaDTO).Fields, "reporter")
	if f == nil || f.Main || !f.Required || !reflect.DeepEqual(f.Default, UserDTO{Name: "alex", DisplayName: "Alex"}) {
		t.Fatalf("reporter %+v", f)
	}
	// Absent on create: not "missing", and not sent (Jira fills it in).
	if _, err := call(t, a, "createIssue", `{"project":"RBI","issueTypeId":"10000","values":{"summary":"Epic"}}`); err != nil {
		t.Fatal(err)
	}
	if body := v.writes[0].body; strings.Contains(body, "reporter") {
		t.Errorf("reporter sent: %s", body)
	}
}

func TestEditStateRequestsOnlyFormFields(t *testing.T) {
	a, _, v, _ := v5App(t)
	if _, err := call(t, a, "getEditMeta", `{"key":"RBI-12"}`); err != nil {
		t.Fatal(err)
	}
	got := strings.Split(v.issueGets[0], ",")
	for _, want := range []string{"updated", "issuetype", "project", "description", "summary", "customfield_10500", "customfield_10107"} {
		if !contains(got, want) {
			t.Errorf("getEditMeta does not read %s: %v", want, got)
		}
	}
	if contains(got, "customfield_10600") {
		t.Errorf("an optional field outside the form is read: %v", got)
	}
	v.issueGets = nil
	if _, err := call(t, a, "updateIssue", `{"key":"RBI-12","values":{"summary":"x"},"expectedUpdated":"`+v5Updated+`"}`); err != nil {
		t.Fatal(err)
	}
	if v.issueGets[0] != "updated,issuetype,project,description" {
		t.Errorf("updateIssue reads %q", v.issueGets[0])
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestJiraValidationIsCapped(t *testing.T) {
	a, _, _, _ := v5App(t)
	je := &jira.Error{Code: jira.CodeBadRequest, FieldErrors: map[string]string{}, Messages: []string{"m\x07sg"}}
	for i := 0; i < 30; i++ {
		je.FieldErrors[fmt.Sprintf("customfield_%02d", i)] = strings.Repeat("ж", 400) + "\x00\n"
	}
	je.FieldErrors[strings.Repeat("x", 100)] = "long id"
	fields := []FieldMetaDTO{{ID: "customfield_00", Kind: FKText}}
	e := a.jiraValidation(je, fields)
	if len(e.Fields) != 1 || utf8RuneCount(e.Fields[0].Message) != 300 || strings.ContainsAny(e.Fields[0].Message, "\x00\n") {
		t.Errorf("field errors %d, first %d runes", len(e.Fields), utf8RuneCount(e.Fields[0].Message))
	}
	if n := utf8RuneCount(e.Message); n > 2000 || strings.Count(e.Message, ";") > 19 || strings.Contains(e.Message, strings.Repeat("x", 65)) {
		t.Errorf("message %d runes, %d entries", n, strings.Count(e.Message, ";")+1)
	}
	if _, err := call(t, a, "createIssue", `{"project":"RBI","issueTypeId":"3","values":{"`+strings.Repeat("y", 200)+`":1}}`); ToError(err).Code != CodeBadParams ||
		strings.Contains(ToError(err).Message, strings.Repeat("y", 65)) {
		t.Errorf("unknown field echo: %v", err)
	}
}

func utf8RuneCount(s string) int { return len([]rune(s)) }

func TestCreateMetaCacheIsBounded(t *testing.T) {
	a, _, v, _ := v5App(t)
	for _, id := range []string{"999", "998", "997", "", "10000"} {
		if res, err := call(t, a, "getCreateMeta", `{"project":"RBI","issueTypeId":"`+id+`"}`); err != nil || res.(*CreateMetaDTO).IssueTypeID != "10000" {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if n := v.metaRequest["/rest/api/2/issue/createmeta/RBI/issuetypes/10000"]; n != 1 {
		t.Errorf("fields of the resolved type read %d times", n)
	}
	if n := v.metaRequest["/rest/api/2/issue/createmeta/RBI/issuetypes"]; n != 2 { // 2 pages, once
		t.Errorf("type list read %d pages", n)
	}
	a.mu.Lock()
	entries := 0
	for k := range a.cache {
		if strings.HasPrefix(k, cacheCreateMeta+":") {
			entries++
		}
	}
	a.mu.Unlock()
	if entries != 2 {
		t.Errorf("%d create meta cache entries, want 2 (types + one resolved type)", entries)
	}
}

func TestCreateIsNotDoubled(t *testing.T) {
	a, _, v, _ := v5App(t)
	clock := a.clock.(*fakeClock)
	params := `{"project":"RBI","issueTypeId":"3","values":{"summary":"Same","customfield_10300":{"id":"500"}}}`
	if _, err := call(t, a, "createIssue", params); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, a, "createIssue", params); ToError(err).Code != CodeBusy || ToError(err).Message != T("ru", "err_busy_create") {
		t.Errorf("second create: %v", err)
	}
	// Other values are another create.
	if _, err := call(t, a, "createIssue", `{"project":"RBI","issueTypeId":"3","values":{"summary":"Other","customfield_10300":{"id":"500"}}}`); err != nil {
		t.Errorf("other create: %v", err)
	}
	clock.t = clock.t.Add(11 * time.Second)
	if _, err := call(t, a, "createIssue", params); err != nil {
		t.Errorf("after 11 s: %v", err)
	}
	if n := len(v.writes); n != 3 {
		t.Errorf("%d creates sent", n)
	}
	// A failed create does not block a retry.
	v.createResp = &jira.Response{StatusCode: 500, Body: []byte(`{}`)}
	p2 := `{"project":"RBI","issueTypeId":"3","values":{"summary":"Retry","customfield_10300":{"id":"500"}}}`
	_, _ = call(t, a, "createIssue", p2)
	v.createResp = nil
	if _, err := call(t, a, "createIssue", p2); err != nil {
		t.Errorf("retry after a failure: %v", err)
	}
}

// The description is checked against the HTML contract; images must be on
// the Jira origin or (edit) already in the issue.
func TestDescriptionHTMLIsValidated(t *testing.T) {
	a, _, v, _ := v5App(t)
	_, err := call(t, a, "createIssue", `{"project":"RBI","issueTypeId":"3","values":{"summary":"x","customfield_10300":{"id":"500"},
	 "description":"<p dir=\"auto\">hi</p><script>alert(1)</script>"}}`)
	if e := ToError(err); e.Code != CodeValidation || len(e.Fields) != 1 || e.Fields[0].Field != "description" ||
		!strings.HasPrefix(e.Fields[0].Message, strings.SplitN(T("ru", "fe_html", ""), ":", 2)[0]) {
		t.Errorf("script: %+v", e)
	}
	ext := `<p dir=\"auto\"><span class=\"image-wrap\"><img src=\"https://cdn.example/x.png\"></span></p>`
	if _, err := call(t, a, "createIssue", `{"project":"RBI","issueTypeId":"3","values":{"summary":"x","customfield_10300":{"id":"500"},"description":"`+ext+`"}}`); ToError(err).Code != CodeValidation {
		t.Errorf("foreign image on create: %v", err)
	}
	// Edit: an image already in the description may stay.
	v.issue["RBI-12"] = rbi12Fields(v5Updated, `<p dir="auto"><span class="image-wrap"><img src="https://cdn.example/x.png"></span></p>`)
	if _, err := call(t, a, "updateIssue", `{"key":"RBI-12","values":{"description":"`+ext+`<p dir=\"auto\">more</p>"},"expectedUpdated":"`+v5Updated+`"}`); err != nil {
		t.Errorf("kept image on edit: %v", err)
	}
	if len(v.writes) != 1 {
		t.Errorf("writes %+v", v.writes)
	}
	// In an HTML issue/project the description is always checked as HTML…
	if _, err := call(t, a, "updateIssue", `{"key":"RBI-12","values":{"description":"a <T> generic, x < y"},"expectedUpdated":"`+v.lastUpdated()+`"}`); ToError(err).Code != CodeValidation {
		t.Errorf("non-HTML text in an HTML description: %v", err)
	}
	// (wiki text with "<" in a wiki issue: TestWikiDescriptionWithAngleBrackets)
}

func TestWikiDescriptionWithAngleBrackets(t *testing.T) {
	a, fj, v, _ := v5App(t)
	fj.routes[descSampleJQL] = `[{"key":"RBI-5","fields":{"description":"*wiki*"}}]`
	if _, err := call(t, a, "updateIssue", `{"key":"RBI-12","values":{"description":"a <T> generic, x < y & z"},"expectedUpdated":"`+v5Updated+`"}`); err != nil {
		t.Errorf("wiki with <: %v", err)
	}
	if _, err := call(t, a, "updateIssue", `{"key":"RBI-12","values":{"description":"x <script>alert(1)</script>"},"expectedUpdated":"`+v.lastUpdated()+`"}`); ToError(err).Code != CodeValidation {
		t.Errorf("script in wiki text: %v", err)
	}
}

func (v *v5Jira) lastUpdated() string {
	var f struct {
		Updated string `json:"updated"`
	}
	_ = json.Unmarshal([]byte(v.issue["RBI-12"]), &f)
	return f.Updated
}

// A wiki issue in an HTML project keeps the wiki rules (its own format, as
// getEditMeta reports it); an empty description uses the project's format.
func TestWikiIssueInHTMLProject(t *testing.T) {
	a, _, v, _ := v5App(t) // the RBI sample is mostly HTML; RBI-12 is "h1. Wiki"
	res, err := call(t, a, "getEditMeta", `{"key":"RBI-12"}`)
	if err != nil || res.(*EditMetaDTO).DescriptionFormat != DescWiki {
		t.Fatalf("format %v", err)
	}
	if _, err := call(t, a, "updateIssue", `{"key":"RBI-12","values":{"description":"h1. New\n* a < b & c"},"expectedUpdated":"`+v5Updated+`"}`); err != nil {
		t.Errorf("plain wiki edit: %v", err)
	}
	if _, err := call(t, a, "updateIssue", `{"key":"RBI-12","values":{"description":"x <script>alert(1)</script>"},"expectedUpdated":"`+v.lastUpdated()+`"}`); ToError(err).Code != CodeValidation {
		t.Errorf("script in a wiki issue: %v", err)
	}
	// Empty description ⇒ the project's (HTML) format ⇒ strict.
	v.issue["RBI-12"] = rbi12Fields(v5Updated, "")
	if _, err := call(t, a, "updateIssue", `{"key":"RBI-12","values":{"description":"plain & text"},"expectedUpdated":"`+v5Updated+`"}`); ToError(err).Code != CodeValidation {
		t.Errorf("empty description in an HTML project: %v", err)
	}
}

// CR-25: createIssue validates against the description format that
// getCreateMeta returned, even if the project's format changes meanwhile.
func TestCreateUsesTheMetaDescriptionFormat(t *testing.T) {
	a, fj, _, _ := v5App(t)
	c, id, _ := a.client()
	clock := a.clock.(*fakeClock)
	if f := a.projectDescriptionFormat(context.Background(), c, id, "RBI"); f != DescHTML {
		t.Fatalf("format %s", f)
	}
	clock.t = clock.t.Add(25 * time.Minute)
	res, err := call(t, a, "getCreateMeta", `{"project":"RBI","issueTypeId":"3"}`)
	if err != nil || res.(*CreateMetaDTO).DescriptionFormat != DescHTML {
		t.Fatalf("meta %v", err)
	}
	// The project's sample turns wiki and its cached format expires…
	fj.routes[descSampleJQL] = `[{"key":"RBI-5","fields":{"description":"*wiki*"}}]`
	clock.t = clock.t.Add(6 * time.Minute)
	if f := a.projectDescriptionFormat(context.Background(), c, id, "RBI"); f != DescWiki {
		t.Fatalf("format now %s", f)
	}
	// …but the create still follows the format of its meta (HTML: strict).
	_, err = call(t, a, "createIssue", `{"project":"RBI","issueTypeId":"3","values":{"summary":"x","customfield_10300":{"id":"500"},"description":"plain & text"}}`)
	if ToError(err).Code != CodeValidation {
		t.Errorf("create with a wiki description against an HTML meta: %v", err)
	}
	if res, _ := call(t, a, "getCreateMeta", `{"project":"RBI","issueTypeId":"3"}`); res.(*CreateMetaDTO).DescriptionFormat != DescHTML {
		t.Error("the cached meta changed its format")
	}
}

// The {code}/{noformat}/{{…}} exemption needs a positively known wiki
// target: a sampled wiki project or a wiki issue; a guessed format screens
// the whole text.
func TestWikiCodeExemptionNeedsKnownFormat(t *testing.T) {
	create := func(a *App, desc string) error {
		b, _ := json.Marshal(desc)
		_, err := call(t, a, "createIssue", `{"project":"RBI","issueTypeId":"3","values":{"summary":"x","customfield_10300":{"id":"500"},"description":`+string(b)+`}}`)
		return err
	}
	// Known wiki: the project's sample is wiki.
	a, fj, _, _ := v5App(t)
	fj.routes[descSampleJQL] = `[{"key":"RBI-5","fields":{"description":"*wiki*"}}]`
	if err := create(a, "{code:java}Map<String, Integer> m;{code}"); err != nil {
		t.Errorf("known wiki, generic in code: %v", err)
	}
	if err := create(a, "{code}<script>alert(1)</script>{code} sample"); err != nil {
		t.Errorf("known wiki, script in code: %v", err)
	}
	if err := create(a, "Map<String, Integer> outside code"); ToError(err).Code != CodeValidation {
		t.Errorf("known wiki, generic outside code: %v", err)
	}
	if err := create(a, "{html}{code}<script>alert(1)</script>{code}{html}"); ToError(err).Code != CodeValidation {
		t.Errorf("{html} macro: %v", err)
	}
	// Guessed wiki: the project has no described issue (no sample).
	g, fj2, _, _ := v5App(t)
	fj2.routes[descSampleJQL] = `[]`
	if err := create(g, "{code}<script>alert(1)</script>{code}"); ToError(err).Code != CodeValidation {
		t.Errorf("guessed wiki, script in code: %v", err)
	}
	if err := create(g, "plain wiki with a <T> generic"); err != nil {
		t.Errorf("guessed wiki, plain text: %v", err)
	}
	// Edit of a wiki issue (its own description is wiki): known.
	e, fj3, v3, _ := v5App(t)
	fj3.routes[descSampleJQL] = `[]` // the project is a guess, the issue is not
	if _, err := call(t, e, "updateIssue", `{"key":"RBI-12","values":{"description":"{code}<x a=1>{code}"},"expectedUpdated":"`+v5Updated+`"}`); err != nil {
		t.Errorf("wiki issue, tag in code: %v", err)
	}
	// Empty description in a guessed project: the whole text is screened.
	v3.issue["RBI-12"] = rbi12Fields(v3.lastUpdated(), "")
	if _, err := call(t, e, "updateIssue", `{"key":"RBI-12","values":{"description":"{code}<x a=1>{code}"},"expectedUpdated":"`+v3.lastUpdated()+`"}`); ToError(err).Code != CodeValidation {
		t.Errorf("empty description, guessed project: %v", err)
	}
}

// CR-27: code masking is for the description only; a textarea field's
// renderer does not follow the description format.
func TestTextareaIsNeverMasked(t *testing.T) {
	ta := FieldMetaDTO{ID: "customfield_1", Kind: FKTextarea, Max: 32000}
	desc := FieldMetaDTO{ID: "description", Kind: FKDescription, Max: 32000}
	wc := writeCtx{html: htmlRules{jiraURL: "https://sd.local"}, wikiKnown: true}
	in := json.RawMessage(`"{code}<img src=x onerror=1>{code}"`)
	if _, _, ok := mapValue(ta, in, wc, &fieldErrs{lang: "en"}); ok {
		t.Error("textarea with a tag in {code} accepted")
	}
	if _, _, ok := mapValue(desc, in, wc, &fieldErrs{lang: "en"}); !ok {
		t.Error("known-wiki description with a tag in {code} rejected")
	}
}

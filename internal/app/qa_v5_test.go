package app

// QA tests for v0.5 create/edit: FieldValue read ⇄ write for every kind,
// clearing on update, the error caps of the contract, cache invalidation,
// the duplicate-create guard while a create runs, picker query safety, the
// FieldMeta JSON shape and i18n of the new messages.

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"jiraffe/internal/jira"
)

// --- FieldValue: the read form is accepted as the write form ------------------------------

// For every kind: Jira's stored value → readValue (what getEditMeta returns)
// → JSON (through the UI) → mapValue (what updateIssue sends) gives back the
// value Jira expects, so an unchanged field can always be saved again.
func TestV5QAFieldValueReadWriteRoundTrip(t *testing.T) {
	opts := []OptionDTO{{ID: "10601", Name: "8"}, {ID: "10603", Name: "5"}}
	tests := []struct {
		name  string
		field FieldMetaDTO
		jira  string // the value as Jira stores/returns it
		want  string // what is written back
	}{
		{"summary", FieldMetaDTO{Kind: FKSummary, Max: 255}, `"Fix the loader"`, `"Fix the loader"`},
		{"description html", FieldMetaDTO{Kind: FKDescription, Max: 32000}, `"<p dir=\"auto\">a &amp; b</p>"`, `"<p dir=\"auto\">a &amp; b</p>"`},
		{"textarea multi-line", FieldMetaDTO{Kind: FKTextarea, Max: 32000}, `"line 1\nline 2"`, `"line 1\nline 2"`},
		{"text", FieldMetaDTO{Kind: FKText, Max: 255}, `"https://x"`, `"https://x"`},
		{"epic name", FieldMetaDTO{Kind: FKEpicName, Max: 255}, `"Platform"`, `"Platform"`},
		{"number", FieldMetaDTO{Kind: FKNumber}, `2.5`, `2.5`},
		{"story points number", FieldMetaDTO{Kind: FKStoryPoints}, `3`, `3`},
		{"story points select", FieldMetaDTO{Kind: FKStoryPoints, Options: opts}, `{"self":"x","value":"8","id":"10601"}`, `{"id":"10601"}`},
		{"select", FieldMetaDTO{Kind: FKSelect, Options: opts}, `{"value":"5","id":"10603","disabled":false}`, `{"id":"10603"}`},
		{"priority", FieldMetaDTO{Kind: FKPriority, Options: opts}, `{"self":"x","name":"8","id":"10601"}`, `{"id":"10601"}`},
		{"multiselect", FieldMetaDTO{Kind: FKMultiselect, Options: opts}, `[{"value":"8","id":"10601"},{"value":"5","id":"10603"}]`, `[{"id":"10601"},{"id":"10603"}]`},
		{"components", FieldMetaDTO{Kind: FKComponents, Options: opts}, `[{"name":"API","id":"10601"}]`, `[{"id":"10601"}]`},
		{"versions", FieldMetaDTO{Kind: FKVersions, Options: opts}, `[{"name":"1.0","id":"10603","released":false}]`, `[{"id":"10603"}]`},
		{"labels", FieldMetaDTO{Kind: FKLabels}, `["a","b-c"]`, `["a","b-c"]`},
		{"user", FieldMetaDTO{Kind: FKUser}, `{"name":"bob","key":"JIRAUSER1","displayName":"Bob B","active":true}`, `{"name":"bob"}`},
		{"user without display name", FieldMetaDTO{Kind: FKUser}, `{"name":"bob"}`, `{"name":"bob"}`},
		{"users", FieldMetaDTO{Kind: FKUsers}, `[{"name":"bob","displayName":"Bob"},{"name":"carol"}]`, `[{"name":"bob"},{"name":"carol"}]`},
		{"epic", FieldMetaDTO{Kind: FKEpic}, `"RBI-1"`, `"RBI-1"`},
		{"parent", FieldMetaDTO{Kind: FKParent}, `{"id":"1","key":"RBI-2","fields":{"summary":"Parent"}}`, `{"key":"RBI-2"}`},
		{"sprint", FieldMetaDTO{Kind: FKSprint, Options: []OptionDTO{{ID: "12"}}},
			`[{"id":11,"state":"CLOSED","name":"S11"},{"id":12,"state":"ACTIVE","name":"S12"}]`, `12`},
		{"date", FieldMetaDTO{Kind: FKDate}, `"2026-12-01"`, `"2026-12-01"`},
		{"datetime", FieldMetaDTO{Kind: FKDateTime}, `"2026-12-01T10:30:00.000+0200"`, ``}, // same instant, checked below
		{"estimate", FieldMetaDTO{Kind: FKEstimate}, `{"originalEstimate":"1w 2d","remainingEstimate":"4h 30m","timeSpent":"1h"}`,
			`{"originalEstimate":"1w 2d","remainingEstimate":"4h 30m"}`},
	}
	for _, tc := range tests {
		tc.field.ID = "f"
		read := readValue(tc.field, json.RawMessage(tc.jira))
		if read == nil {
			t.Errorf("%s: nothing read from %s", tc.name, tc.jira)
			continue
		}
		ui, _ := json.Marshal(read)
		errs := &fieldErrs{lang: "en"}
		val, empty, ok := mapValue(tc.field, ui, writeCtx{hoursPerDay: 8, html: htmlRules{jiraURL: "https://sd.local"}, htmlDescription: true}, errs)
		if !ok || empty {
			t.Errorf("%s: read form %s not writable: %+v", tc.name, ui, errs.errs)
			continue
		}
		var buf strings.Builder
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(val)
		got := strings.TrimSpace(buf.String())
		if tc.field.Kind == FKDateTime {
			want, _ := jira.ParseTime(`2026-12-01T10:30:00.000+0200`)
			var s string
			_ = json.Unmarshal([]byte(got), &s)
			if g, err := jira.ParseTime(s); err != nil || !g.Equal(want) || !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.000[+-]\d{4}$`).MatchString(s) {
				t.Errorf("datetime: read %s, written %s (want the same instant in Jira's format)", ui, s)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("%s: read %s, written %s, want %s", tc.name, ui, got, tc.want)
		}
	}
}

// getEditMeta → updateIssue with every value unchanged: accepted, and Jira
// receives its own values back in write form.
func TestV5QAEditValuesSentBackUnchanged(t *testing.T) {
	a, _, v, _ := v5App(t)
	v.afterPut = func() { v.issue["RBI-12"] = rbi12Fields(v5Updated2, "h1. Wiki") }
	res, err := call(t, a, "getEditMeta", `{"key":"RBI-12"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(*EditMetaDTO)
	values, _ := json.Marshal(m.Values)
	b, _ := json.Marshal(m.Updated)
	out, err := call(t, a, "updateIssue", `{"key":"RBI-12","values":`+string(values)+`,"expectedUpdated":`+string(b)+`}`)
	if err != nil {
		t.Fatalf("unchanged values rejected: %+v", ToError(err))
	}
	if d := out.(*UpdatedIssueDTO); d.Updated != v5Updated2 || d.Warning != "" {
		t.Errorf("result %+v", d)
	}
	want := `{"fields":{"assignee":{"name":"alex"},"components":[{"id":"100"}],"customfield_10103":"RBI-1","customfield_10204":{"id":"10603"},
	 "customfield_10300":{"id":"501"},"customfield_10500":[{"name":"bob"}],"description":"h1. Wiki","duedate":"2026-12-01","labels":["x"],
	 "priority":{"id":"3"},"summary":"Old","timetracking":{"originalEstimate":"1d","remainingEstimate":"4h"}}}`
	if len(v.writes) != 2 || canon(t, v.writes[0].body) != canon(t, want) || v.writes[1].path != "/rest/agile/1.0/sprint/12/issue" {
		t.Errorf("writes %+v", v.writes)
	}
}

// On update, emptied optional values clear the field (null / []), an
// emptied estimate is not sent (clearing is not supported), and emptied
// required values are refused.
func TestV5QAUpdateClearsFields(t *testing.T) {
	tests := []struct {
		values string
		body   string // PUT body; "" = no PUT
		code   string
	}{
		{`{"components":null}`, `{"fields":{"components":[]}}`, ""},
		{`{"components":[]}`, `{"fields":{"components":[]}}`, ""},
		{`{"customfield_10103":null}`, `{"fields":{"customfield_10103":null}}`, ""},
		{`{"duedate":""}`, `{"fields":{"duedate":null}}`, ""},
		{`{"description":"  "}`, `{"fields":{"description":null}}`, ""},
		{`{"customfield_10204":null}`, `{"fields":{"customfield_10204":null}}`, ""},
		{`{"labels":["",""]}`, `{"fields":{"labels":[]}}`, ""},
		{`{"timetracking":{"originalEstimate":"","remainingEstimate":""}}`, "", ""},
		{`{"timetracking":null}`, "", ""},
		{`{"summary":""}`, "", CodeValidation},
		{`{"customfield_10500":[]}`, "", CodeValidation},
		{`{"customfield_10500":null}`, "", CodeValidation},
	}
	for _, tc := range tests {
		a, _, v, _ := v5App(t)
		_, err := call(t, a, "updateIssue", `{"key":"RBI-12","values":`+tc.values+`,"expectedUpdated":"`+v5Updated+`"}`)
		if got := ToErrorOrEmpty(err); got != tc.code {
			t.Errorf("%s: %v, want %q", tc.values, err, tc.code)
			continue
		}
		var puts []string
		for _, w := range v.writes {
			if w.method == "PUT" {
				puts = append(puts, canon(t, w.body))
			}
		}
		if tc.body == "" && len(puts) != 0 || tc.body != "" && (len(puts) != 1 || puts[0] != canon(t, tc.body)) {
			t.Errorf("%s: PUTs %v, want %s", tc.values, puts, tc.body)
		}
	}
}

// ToErrorOrEmpty is the error code, "" for nil.
func ToErrorOrEmpty(err error) string {
	if err == nil {
		return ""
	}
	return ToError(err).Code
}

// Length limits count characters (runes), not bytes.
func TestV5QALimitsCountCharacters(t *testing.T) {
	for _, tc := range []struct {
		field FieldMetaDTO
		in    string
		ok    bool
	}{
		{FieldMetaDTO{Kind: FKSummary, Max: 255}, strings.Repeat("я", 255), true},
		{FieldMetaDTO{Kind: FKSummary, Max: 255}, strings.Repeat("😀", 255), true},
		{FieldMetaDTO{Kind: FKSummary, Max: 255}, strings.Repeat("я", 256), false},
		{FieldMetaDTO{Kind: FKDescription, Max: 32000}, strings.Repeat("ж", 32000), true},
		{FieldMetaDTO{Kind: FKDescription, Max: 32000}, strings.Repeat("ж", 32001), false},
		{FieldMetaDTO{Kind: FKSummary, Max: 255}, "  " + strings.Repeat("a", 255) + "  ", true}, // trimmed first
		{FieldMetaDTO{Kind: FKSummary, Max: 255}, "a\rb", false},
		{FieldMetaDTO{Kind: FKLabels}, `["` + strings.Repeat("л", 255) + `"]`, true},
		{FieldMetaDTO{Kind: FKLabels}, `["` + strings.Repeat("л", 256) + `"]`, false},
	} {
		tc.field.ID = "f"
		raw := tc.in
		if tc.field.Kind != FKLabels {
			b, _ := json.Marshal(tc.in)
			raw = string(b)
		}
		errs := &fieldErrs{lang: "en"}
		_, _, ok := mapValue(tc.field, json.RawMessage(raw), writeCtx{hoursPerDay: 8, html: htmlRules{jiraURL: "https://sd.local"}}, errs)
		if ok != tc.ok {
			t.Errorf("%s of %d runes: ok=%v %+v", tc.field.Kind, utf8.RuneCountInString(tc.in), ok, errs.errs)
		}
	}
}

// --- errors -------------------------------------------------------------------------------

// A required field that can only be filled in Jira is reported once, also
// when the UI sends a value for it.
func TestV5QAUnsupportedRequiredReportedOnce(t *testing.T) {
	a, _, v, _ := v5App(t)
	_, err := call(t, a, "createIssue", `{"project":"RBI","issueTypeId":"10001","values":{"summary":"x","customfield_10300":{"id":"500"},"customfield_10301":"Europe"}}`)
	e := ToError(err)
	n := 0
	for _, f := range e.Fields {
		if f.Field == "customfield_10301" {
			n++
		}
	}
	if e.Code != CodeValidation || n != 1 || len(v.writes) != 0 {
		t.Errorf("customfield_10301 reported %d times: %+v", n, e.Fields)
	}
}

// SPEC v0.5 Errors: "each message ≤ 300 characters, the whole message
// ≤ 2000, field ids ≤ 64", also in bad_params messages.
func TestV5QAJiraErrorCapsPerSpec(t *testing.T) {
	a, _, _, _ := v5App(t)
	je := &jira.Error{Code: jira.CodeBadRequest, FieldErrors: map[string]string{}}
	for i := 0; i < 15; i++ {
		je.FieldErrors[fmt.Sprintf("customfield_%03d", i)] = strings.Repeat("ж", 1000)
	}
	je.FieldErrors["summary"] = strings.Repeat("s", 1000)
	for i := 0; i < 10; i++ {
		je.Messages = append(je.Messages, strings.Repeat("m", 1000))
	}
	e := a.jiraValidation(je, []FieldMetaDTO{{ID: "summary", Kind: FKSummary}})
	if len(e.Fields) != 1 {
		t.Fatalf("fields %+v", e.Fields)
	}
	if n := utf8.RuneCountInString(e.Fields[0].Message); n > 300 {
		t.Errorf("field message of %d characters, at most 300", n)
	}
	if n := utf8.RuneCountInString(e.Message); n > 2000 {
		t.Errorf("message of %d characters, at most 2000", n)
	}
	// Unknown field ids echoed in bad_params: cut to 64 characters.
	_, err := call(t, a, "createIssue", `{"project":"RBI","issueTypeId":"3","values":{"`+strings.Repeat("y", 200)+`":1}}`)
	if m := ToError(err).Message; strings.Contains(m, strings.Repeat("y", 64)+"…") || strings.Contains(m, strings.Repeat("y", 65)) {
		t.Errorf("echoed id longer than 64 characters: %q", m)
	}
	// Without the form (other edit calls), ids over 64 are not field ids.
	je2 := &jira.Error{Code: jira.CodeBadRequest, FieldErrors: map[string]string{strings.Repeat("q", 80): "bad", "ok_id": "fine"}}
	e2 := a.jiraValidation(je2, nil)
	for _, f := range e2.Fields {
		if utf8.RuneCountInString(f.Field) > 64 {
			t.Errorf("field id %q", f.Field)
		}
	}
}

// --- caches and events --------------------------------------------------------------------

func seedCaches(t *testing.T, a *App) []string {
	t.Helper()
	_, id, err := a.client()
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{cacheBoard + ":mine::0", cacheSprintGroups + ":RBI:x", cacheEpics + ":all:RBI", cacheStories + ":all:RBI", cacheProjects + ":x"}
	for _, k := range keys {
		_, gen, _ := a.cacheGet(k, id, 0)
		a.cachePut(k, id, gen, a.clock.Now(), "stale")
	}
	a.mu.Lock()
	a.epics = epicCache{identity: id, at: a.clock.Now(), m: map[string]epicInfo{"RBI-1": {}}}
	a.mu.Unlock()
	return keys
}

func cachedKeys(a *App, keys []string) []string {
	_, id, _ := a.client()
	var out []string
	for _, k := range keys {
		if _, _, ok := a.cacheGet(k, id, time.Hour); ok {
			out = append(out, k)
		}
	}
	return out
}

// A successful create/update drops board, sprint groups, epics, stories,
// project counts and epic names and emits "board"; a failed one keeps them
// and is silent.
func TestV5QAWritesInvalidateCaches(t *testing.T) {
	tests := []struct {
		name   string
		method string
		params string
		fail   bool
	}{
		{"create", "createIssue", `{"project":"RBI","issueTypeId":"3","values":{"summary":"x","customfield_10300":{"id":"500"}}}`, false},
		{"update", "updateIssue", `{"key":"RBI-12","values":{"summary":"x"},"expectedUpdated":"` + v5Updated + `"}`, false},
		{"sprint-only update", "updateIssue", `{"key":"RBI-12","values":{"customfield_10107":13},"expectedUpdated":"` + v5Updated + `"}`, false},
		{"create rejected by Jira", "createIssue", `{"project":"RBI","issueTypeId":"3","values":{"summary":"x","customfield_10300":{"id":"500"}}}`, true},
		{"update rejected by Jira", "updateIssue", `{"key":"RBI-12","values":{"summary":"x"},"expectedUpdated":"` + v5Updated + `"}`, true},
		{"update conflict", "updateIssue", `{"key":"RBI-12","values":{"summary":"x"},"expectedUpdated":"2020-01-01T00:00:00.000+0000"}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, _, v, rec := v5App(t)
			if tc.fail {
				bad := &jira.Response{StatusCode: 400, Body: []byte(`{"errorMessages":["no"],"errors":{}}`)}
				v.createResp, v.putResp = bad, bad
			}
			keys := seedCaches(t, a)
			_, err := call(t, a, tc.method, tc.params)
			if (err != nil) != tc.fail {
				t.Fatalf("err %v", err)
			}
			left := cachedKeys(a, keys)
			a.mu.Lock()
			epicNames := len(a.epics.m)
			a.mu.Unlock()
			if tc.fail {
				if len(left) != len(keys) || epicNames == 0 || len(rec.of("board")) != 0 {
					t.Errorf("failed write: caches left %v, epic names %d, events %+v", left, epicNames, rec.events)
				}
				return
			}
			if len(left) != 0 || epicNames != 0 || len(rec.of("board")) != 1 || rec.of("board")[0] != "{}" {
				t.Errorf("caches left %v, epic names %d, events %+v", left, epicNames, rec.events)
			}
		})
	}
}

// The same create is refused while it is running (a double click during a
// slow Jira), not only after it succeeded.
func TestV5QACreateBusyWhileInFlight(t *testing.T) {
	a, fj, v, _ := v5App(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	fj.fail = func(r *jira.Request) error {
		if r.Method == "POST" && strings.HasSuffix(r.URL, "/rest/api/2/issue") {
			once.Do(func() { close(entered); <-release })
		}
		return nil
	}
	params := `{"project":"RBI","issueTypeId":"3","values":{"summary":"Same","customfield_10300":{"id":"500"}}}`
	done := make(chan error, 1)
	go func() { _, err := call(t, a, "createIssue", params); done <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first create did not reach Jira")
	}
	// The same request, formatted differently and with the project in
	// lower case, is the same create.
	same := `{"project":"rbi","issueTypeId":"3","values":{"customfield_10300":{"id": "500"},"summary":"Same"}}`
	if _, err := call(t, a, "createIssue", same); ToError(err).Code != CodeBusy {
		t.Errorf("second create while the first runs: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	posts := 0
	for _, w := range v.writes {
		if w.method == "POST" && w.path == "/rest/api/2/issue" {
			posts++
		}
	}
	if posts != 1 {
		t.Errorf("%d creates sent", posts)
	}
}

// --- edit: images already in the issue ----------------------------------------------------

// An image already in the description is "known" wherever it is, also after
// characters whose lower case has another UTF-8 length (Turkish İ, the
// Kelvin sign) and when Jira's HTML has an unquoted src.
func TestV5QAKnownImagesFound(t *testing.T) {
	for _, desc := range []string{
		`<p>İstanbul</p><img src="https://cdn.example/a.png">`,
		`<p>5 K</p><img src="https://cdn.example/a.png">`, // U+212A KELVIN SIGN
		`<p>x</p><IMG SRC="https://cdn.example/a.png">`,
		`<p>x</p><img src=https://cdn.example/a.png>`,
		`<p>x</p><img alt="a" src='https://cdn.example/a.png' >`,
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%q: panic %v", desc, r)
				}
			}()
			if got := imgSources(desc); !got["https://cdn.example/a.png"] {
				t.Errorf("%q: known images %v", desc, got)
			}
		}()
	}
	// Never panics, whatever Jira returns.
	for _, s := range []string{"\xc70\x9b<img", "İİİİ<img", "<img", "<img src=", `<img src="`} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%q: panic %v", s, r)
				}
			}()
			_ = imgSources(s)
		}()
	}
}

// --- pickers ---------------------------------------------------------------------------------

// Whatever the user types, the JQL text term stays inside its quotes: only
// lower-case letters, digits, single spaces and "-_." between word
// characters reach it.
func TestV5QAPickerTextIsSafe(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	pool := []rune(`aZ09яЖ"'\*?~^!()[]{}:+-_.&|/ ` + "\t\n\x00ǅİ😀＊")
	for i := 0; i < 20000; i++ {
		rs := make([]rune, r.Intn(30))
		for j := range rs {
			rs[j] = pool[r.Intn(len(pool))]
		}
		in := string(rs)
		got := pickerText(in)
		if got != strings.TrimSpace(got) || strings.Contains(got, "  ") {
			t.Fatalf("%q → %q: spacing", in, got)
		}
		prev := ' '
		for k, c := range []rune(got) {
			switch {
			case unicode.IsLetter(c) || unicode.IsDigit(c):
				if unicode.IsUpper(c) || unicode.IsTitle(c) {
					t.Fatalf("%q → %q: upper case %q", in, got, c)
				}
			case c == ' ':
			case c == '-' || c == '_' || c == '.':
				next := []rune(got)[min(k+1, len([]rune(got))-1)]
				if !(unicode.IsLetter(prev) || unicode.IsDigit(prev)) || !(unicode.IsLetter(next) || unicode.IsDigit(next)) || k == len([]rune(got))-1 {
					t.Fatalf("%q → %q: %q not between word characters", in, got, c)
				}
			default:
				t.Fatalf("%q → %q: %q reaches JQL", in, got, c)
			}
			prev = c
		}
	}
}

// --- FieldMeta JSON shape -----------------------------------------------------------------

func TestV5QAFieldMetaJSONShape(t *testing.T) {
	a, _, _, _ := v5App(t)
	res, err := call(t, a, "getCreateMeta", `{"project":"RBI","issueTypeId":"3"}`)
	if err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(res)
	var m struct {
		Project           map[string]any   `json:"project"`
		IssueTypes        []map[string]any `json:"issueTypes"`
		IssueTypeID       string           `json:"issueTypeId"`
		Fields            []map[string]any `json:"fields"`
		DescriptionFormat string           `json:"descriptionFormat"`
	}
	if err := json.Unmarshal(js, &m); err != nil {
		t.Fatal(err)
	}
	if m.IssueTypeID != "3" || m.DescriptionFormat != DescHTML || m.Project["key"] != "RBI" || len(m.IssueTypes) != 4 {
		t.Errorf("meta %s", js)
	}
	for _, it := range m.IssueTypes {
		if keysOf(it) != "id,kind,name,subtask" {
			t.Errorf("issue type keys %s", keysOf(it))
		}
	}
	var order []string
	for _, f := range m.Fields {
		id, kind := f["id"].(string), f["kind"].(string)
		order = append(order, id)
		if id == "issuetype" || id == "project" {
			t.Errorf("%s in fields", id)
		}
		for _, k := range []string{"id", "name", "kind", "required", "main"} {
			if _, ok := f[k]; !ok {
				t.Errorf("%s lacks %s", id, k)
			}
		}
		_, hasOpts := f["options"]
		wantOpts := map[string]bool{FKSelect: true, FKMultiselect: true, FKPriority: true, FKComponents: true, FKVersions: true, FKSprint: true}[kind] ||
			kind == FKStoryPoints && id != "customfield_10130"
		if hasOpts != wantOpts {
			t.Errorf("%s (%s): options present %v", id, kind, hasOpts)
		}
		if max, ok := f["max"]; ok != map[string]bool{FKSummary: true, FKText: true, FKEpicName: true, FKDescription: true, FKTextarea: true}[kind] ||
			ok && max != float64(map[string]int{FKSummary: 255, FKText: 255, FKEpicName: 255, FKDescription: 32000, FKTextarea: 32000}[kind]) {
			t.Errorf("%s (%s): max %v", id, kind, max)
		}
		if kind == FKSprint {
			for _, o := range f["options"].([]any) {
				om := o.(map[string]any)
				if _, err := strconv.Atoi(om["id"].(string)); err != nil || (om["state"] != "active" && om["state"] != "future") {
					t.Errorf("sprint option %v", om)
				}
			}
		}
	}
	// SPEC order of the main fields present, then the required extras.
	want := "summary,description,assignee,priority,customfield_10103,customfield_10107,customfield_10130,customfield_10204,customfield_10205," +
		"labels,components,fixVersions,duedate,timetracking,customfield_10300"
	if got := strings.Join(order, ","); got != want {
		t.Errorf("order\n%s\nwant\n%s", got, want)
	}
}

func keysOf(m map[string]any) string {
	var k []string
	for key := range m {
		k = append(k, key)
	}
	sortStrings(k)
	return strings.Join(k, ",")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// --- i18n --------------------------------------------------------------------------------------

// Every message key literal of the app's sources (err_, fe_, warn_, field_,
// upd_ …, also those passed to helpers such as fieldErrs.add) exists.
func TestV5QAMessageKeyLiteralsExist(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	keyLike := regexp.MustCompile(`^(err|fe|warn|field|upd)_[a-z0-9_]+$`)
	fset := token.NewFileSet()
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || !keyLike.MatchString(s) {
				return true
			}
			found++
			if !hasText(s) {
				t.Errorf("%s: message key %q does not exist", fset.Position(lit.Pos()), s)
			}
			return true
		})
	}
	if found < 30 {
		t.Fatalf("only %d keys found: the scan is broken", found)
	}
	// The field messages listed in SPEC v0.5 exist in every language and are
	// distinct (a fallback to another message would hide a missing text).
	keys := []string{"fe_required", "fe_too_long", "fe_invalid", "fe_option", "fe_label_space", "fe_date", "fe_datetime", "fe_duration",
		"fe_number", "fe_key", "fe_single_line", "fe_unsupported", "fe_html", "err_validation_fields", "err_forbidden_edit",
		"err_forbidden_create", "err_busy_create", "err_edit_conflict", "err_unknown_field", "warn_sprint_not_changed", "field_sprint"}
	for _, lang := range []string{"en", "ru", "uk"} {
		seen := map[string]string{}
		for _, k := range keys {
			msg, ok := texts[lang][k]
			if !ok || strings.TrimSpace(msg) == "" {
				t.Errorf("%s: %s missing", lang, k)
				continue
			}
			if other, dup := seen[msg]; dup {
				t.Errorf("%s: %s and %s share %q", lang, other, k, msg)
			}
			seen[msg] = k
		}
	}
	if !strings.Contains(T("en", "fe_too_long", 255), "255") || !strings.Contains(T("uk", "fe_html", "the element <b>"), "the element <b>") {
		t.Error("format arguments are not used")
	}
}

// --- description format ------------------------------------------------------------------

// A failing sample search means "wiki" and is not cached (the next form
// asks again).
func TestV5QADescriptionFormatFailureNotCached(t *testing.T) {
	a, fj, _, _ := v5App(t)
	c, id, _ := a.client()
	fails := true
	fj.status = func(r *jira.Request) int {
		if fails && strings.Contains(r.URL, "description+is+not+EMPTY") {
			return 503
		}
		return 0
	}
	if f := a.projectDescriptionFormat(context.Background(), c, id, "RBI"); f != DescWiki {
		t.Errorf("failing search: %s", f)
	}
	fails = false
	if f := a.projectDescriptionFormat(context.Background(), c, id, "RBI"); f != DescHTML {
		t.Errorf("after recovery: %s (the failure was cached)", f)
	}
}

// Sprint options: open sprints of all the project's boards, active first,
// a sprint shared by two boards once, closed ones never; a board without
// sprints (kanban) does not break the list.
func TestV5QASprintOptionsAcrossBoards(t *testing.T) {
	a, fj, _, _ := v5App(t)
	fj.agileBoards["RBI"] = `[{"id":880,"name":"BI","type":"scrum"},{"id":881,"name":"Flow","type":"kanban"},{"id":882,"name":"Ops","type":"scrum"}]`
	fj.kanban = map[int]bool{881: true}
	fj.sprints[882] = []string{`{"id":13,"name":"S13","state":"future"}`, `{"id":20,"name":"Ops 20","state":"active"}`,
		`{"id":21,"name":"Ops 21","state":"closed"}`}
	res, err := call(t, a, "getCreateMeta", `{"project":"RBI","issueTypeId":"3"}`)
	if err != nil {
		t.Fatal(err)
	}
	f := findField(res.(*CreateMetaDTO).Fields, "customfield_10107")
	if f == nil {
		t.Fatal("no sprint field")
	}
	var got []string
	for _, o := range f.Options {
		got = append(got, o.ID+":"+o.State)
	}
	if strings.Join(got, " ") != "12:active 20:active 13:future" {
		t.Errorf("sprint options %v", got)
	}
	// Only offered sprints can be written.
	for v, ok := range map[string]bool{"12": true, "20": true, "13": true, "21": false, "11": false} {
		errs := &fieldErrs{lang: "en"}
		_, _, accepted := mapValue(*f, json.RawMessage(v), writeCtx{}, errs)
		if accepted != ok {
			t.Errorf("sprint %s accepted=%v", v, accepted)
		}
	}
}

// A project whose create screen offers only sub-task types (or no type at
// all) has no type to fall back to: forbidden_edit with the localized
// message, and nothing is written. (An explicitly chosen sub-task type is
// still a normal create: it needs its parent.)
func TestV5QACreateInProjectWithoutCreatableTypes(t *testing.T) {
	for name, types := range map[string][]string{"only sub-tasks": {fxTypes["sub"]}, "no types": nil} {
		t.Run(name, func(t *testing.T) {
			a, fj, v, rec := v5App(t)
			inner := fj.handle
			fj.handle = func(r *jira.Request, u *url.URL) (*jira.Response, bool) {
				if u.Path == "/rest/api/2/issue/createmeta/RBI/issuetypes" {
					return &jira.Response{StatusCode: 200, Body: []byte(page(types, u.Query(), 50))}, true
				}
				return inner(r, u)
			}
			for _, c := range []struct{ method, params string }{
				{"getCreateMeta", `{"project":"RBI"}`},
				// A type that is not offered falls back to the first non-sub-task type: none.
				{"createIssue", `{"project":"RBI","issueTypeId":"3","values":{"summary":"x"}}`},
			} {
				_, err := call(t, a, c.method, c.params)
				if e := ToError(err); e.Code != CodeForbiddenEdit || e.Message != T("ru", "err_forbidden_create") {
					t.Errorf("%s: %+v", c.method, e)
				}
			}
			if len(v.writes) != 0 || len(rec.of("board")) != 0 {
				t.Errorf("writes %+v events %+v", v.writes, rec.events)
			}
		})
	}
}

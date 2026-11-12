package jira

import (
	"encoding/json"
	"testing"
)

func TestDiscoverDevQAStoryPoints(t *testing.T) {
	tests := []struct {
		name    string
		defs    string
		dev, qa string
	}{
		{"real Jira: select fields next to the number fields Dev/QA Points", `[
		 {"id":"customfield_10200","name":"Dev Points","clauseNames":["cf[10200]","Dev Points"],"schema":{"type":"number"}},
		 {"id":"customfield_10201","name":"QA Points","clauseNames":["cf[10201]","QA Points"],"schema":{"type":"number"}},
		 {"id":"customfield_10204","name":"Dev Story Points","clauseNames":["cf[10204]","Dev Story Points"],"schema":{"type":"option","custom":"x:select"}},
		 {"id":"customfield_10205","name":"QA Story Points","clauseNames":["cf[10205]","QA Story Points"],"schema":{"type":"option","custom":"x:select"}}]`,
			"customfield_10204", "customfield_10205"},
		{"by clause name only, case and spaces ignored", `[
		 {"id":"customfield_1","name":"Разработка SP","clauseNames":["cf[1]"," dev story points "],"schema":{"type":"number"}},
		 {"id":"customfield_2","name":"Тест SP","clauseNames":["cf[2]","QA STORY POINTS"],"schema":{"type":"string"}}]`,
			"customfield_1", "customfield_2"},
		{"by name, without a schema", `[
		 {"id":"customfield_3","name":"  Dev Story Points","clauseNames":["cf[3]"]},
		 {"id":"customfield_4","name":"qa story points"}]`,
			"customfield_3", "customfield_4"},
		{"first match wins", `[
		 {"id":"customfield_5","name":"Dev Story Points","schema":{"type":"number"}},
		 {"id":"customfield_6","name":"Dev Story Points","schema":{"type":"option"}}]`,
			"customfield_5", ""},
		{"similar names do not match", `[
		 {"id":"customfield_7","name":"Story Points","clauseNames":["Story Points"],"schema":{"type":"number"}},
		 {"id":"customfield_8","name":"Dev Story Points (old)","schema":{"type":"number"}},
		 {"id":"customfield_9","name":"QA Story","clauseNames":["QA Story Point"],"schema":{"type":"number"}},
		 {"id":"","name":"Dev Story Points","schema":{"type":"number"}}]`,
			"", ""},
	}
	for _, tc := range tests {
		var defs []FieldDef
		if err := json.Unmarshal([]byte(tc.defs), &defs); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := DiscoverFields(defs)
		if got.DevStoryPoints != tc.dev || got.QAStoryPoints != tc.qa {
			t.Errorf("%s: dev %q qa %q, want %q %q", tc.name, got.DevStoryPoints, got.QAStoryPoints, tc.dev, tc.qa)
		}
	}
	// Story Points discovery is unaffected by the Dev/QA fields.
	var defs []FieldDef
	_ = json.Unmarshal([]byte(`[{"id":"customfield_10130","name":"Story Points","clauseNames":["Story Points"],"schema":{"type":"number"}},
	 {"id":"customfield_10204","name":"Dev Story Points","schema":{"type":"number"}}]`), &defs)
	if got := DiscoverFields(defs); got.StoryPoints != "customfield_10130" || got.DevStoryPoints != "customfield_10204" || len(got.List()) != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestCustomPoints(t *testing.T) {
	tests := []struct {
		raw  string // JSON value of customfield_1; "" = absent
		want any    // float64 or nil
	}{
		{`8`, 8.0},
		{`2.5`, 2.5},
		{`0`, 0.0},
		{`-1`, -1.0},
		{`"8"`, 8.0},
		{`" 13 "`, 13.0},
		{`"0.5"`, 0.5},
		{`".5"`, 0.5},
		{`{"self":"https://sd.local/rest/api/2/customFieldOption/10601","value":"8","id":"10601","disabled":false}`, 8.0},
		{`{"value":3}`, 3.0},
		{`{"value":" 21 "}`, 21.0},
		{``, nil},
		{`null`, nil},
		{`""`, nil},
		{`"XL"`, nil},
		{`"1,5"`, nil},
		{`"1.2.3"`, nil},
		{`"-"`, nil},
		{`"."`, nil},
		{`"NaN"`, nil},
		{`"Inf"`, nil},
		{`"1e3"`, nil},
		{`"0x10"`, nil},
		{`"5-"`, nil},
		{`true`, nil},
		{`[8]`, nil},
		{`[{"value":"8"}]`, nil},
		{`{}`, nil},
		{`{"value":null}`, nil},
		{`{"value":"XL"}`, nil},
		{`{"name":"8"}`, nil},
		{`{"value":{"value":"8"}}`, nil},
	}
	for _, tc := range tests {
		body := `{"key":"A-1","fields":{"summary":"S"}}`
		if tc.raw != "" {
			body = `{"key":"A-1","fields":{"summary":"S","customfield_1":` + tc.raw + `}}`
		}
		var is Issue
		if err := json.Unmarshal([]byte(body), &is); err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		got := is.Fields.CustomPoints("customfield_1")
		switch want := tc.want.(type) {
		case nil:
			if got != nil {
				t.Errorf("%s: got %v, want absent", tc.raw, *got)
			}
		case float64:
			if got == nil || *got != want {
				t.Errorf("%s: got %v, want %v", tc.raw, got, want)
			}
		}
	}
	var f Fields
	if f.CustomPoints("") != nil || f.CustomPoints("customfield_1") != nil {
		t.Error("no custom fields: must be absent")
	}
}

func TestTimeFields(t *testing.T) {
	var is Issue
	if err := json.Unmarshal([]byte(`{"key":"A-1","fields":{"timespent":60,"aggregatetimespent":3600,"aggregatetimeoriginalestimate":null}}`), &is); err != nil {
		t.Fatal(err)
	}
	if f := is.Fields; f.AggregateTimeSpent == nil || *f.AggregateTimeSpent != 3600 || f.AggregateTimeOriginalEstimate != nil {
		t.Errorf("fields %+v", is.Fields)
	}
}

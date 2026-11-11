package jira

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
)

// Legacy sprint strings as real Jira Server/DC versions write them, with the
// odd values that occur in practice.
func TestParseLegacySprintOddValues(t *testing.T) {
	const pfx = "com.atlassian.greenhopper.service.sprint.Sprint@6f00eb9e"
	tests := []struct {
		name string
		in   string
		want Sprint
	}{
		{"empty goal and null board",
			pfx + "[id=7,rapidViewId=<null>,state=FUTURE,name=Next,goal=,startDate=<null>,endDate=<null>,completeDate=<null>,sequence=7]",
			Sprint{ID: 7, Name: "Next", State: "future"}},
		{"brackets and equals signs in the name",
			pfx + "[id=8,rapidViewId=880,state=ACTIVE,name=Sprint [Q4] a=b,startDate=2026-11-03T10:00:00.000+02:00,sequence=8]",
			Sprint{ID: 8, Name: "Sprint [Q4] a=b", State: "active", StartDate: "2026-11-03T10:00:00.000+02:00", BoardID: 880}},
		{"id not first, mixed-case state, unknown attributes",
			pfx + "[rapidViewId=881,state=Closed,name=Old,id=9,autoStartStop=false,synced=false,incompleteIssuesDestinationId=<null>]",
			Sprint{ID: 9, Name: "Old", State: "closed", BoardID: 881}},
		{"comma followed by a non-identifier stays in the value",
			pfx + "[id=10,name=Sprint 10, 2nd half,goal=fix a, b and c,state=ACTIVE]",
			Sprint{ID: 10, Name: "Sprint 10, 2nd half", Goal: "fix a, b and c", State: "active"}},
		{"Cyrillic name",
			pfx + "[id=11,rapidViewId=880,state=ACTIVE,name=Спринт 11]",
			Sprint{ID: 11, Name: "Спринт 11", State: "active", BoardID: 880}},
		{"no class prefix",
			"[id=12,state=ACTIVE,name=Bare]",
			Sprint{ID: 12, Name: "Bare", State: "active"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseLegacySprint(tc.in)
			if !ok || got != tc.want {
				t.Errorf("got %+v (%v)\nwant %+v", got, ok, tc.want)
			}
		})
	}
	for _, bad := range []string{
		pfx + "[id=<null>,name=x]", pfx + "[id=0,name=x]", pfx + "[id=,name=x]", pfx + "[id= 5,name=x]",
		pfx + "]id=5[", pfx + "[ID=5]",
	} {
		if s, ok := ParseLegacySprint(bad); ok {
			t.Errorf("%q parsed as %+v", bad, s)
		}
	}
}

// The Sprint field may hold several sprints (an issue carried over), in
// either form; the order is kept and bad entries do not hide good ones.
func TestParseSprintsKeepsOrderOfCarriedOverSprints(t *testing.T) {
	raw := json.RawMessage(`[
	 "com.atlassian.greenhopper.service.sprint.Sprint@1[id=1,rapidViewId=880,state=CLOSED,name=S1]",
	 null,
	 "com.atlassian.greenhopper.service.sprint.Sprint@2[id=2,rapidViewId=880,state=ACTIVE,name=S2]",
	 {"id":3,"name":"S3","state":"FUTURE"}
	]`)
	got := ParseSprints(raw)
	if len(got) != 3 || got[0].ID != 1 || got[1].ID != 2 || got[2].ID != 3 || got[0].State != "closed" || got[2].State != "future" {
		t.Errorf("got %+v", got)
	}
}

// DiscoverFields: the first match wins, Story Points needs a number type, and
// a field without schema is ignored.
func TestDiscoverFieldsEdgeCases(t *testing.T) {
	var defs []FieldDef
	if err := json.Unmarshal([]byte(`[
	 {"id":"customfield_1","name":"Story Points","clauseNames":["Story Points"],"schema":{"type":"string"}},
	 {"id":"customfield_2","name":"No schema","clauseNames":["Story Points"]},
	 {"id":"customfield_3","name":"SP","clauseNames":["cf[3]"," story points "],"schema":{"type":"number"}},
	 {"id":"customfield_4","name":"SP2","clauseNames":["Story Points"],"schema":{"type":"number"}},
	 {"id":"customfield_5","schema":{"type":"any","custom":"com.pyxis.greenhopper.jira:gh-epic-link"}},
	 {"id":"customfield_6","schema":{"type":"any","custom":"com.pyxis.greenhopper.jira:gh-epic-link"}},
	 {"id":"","schema":{"type":"array","custom":"com.pyxis.greenhopper.jira:gh-sprint"}}
	]`), &defs); err != nil {
		t.Fatal(err)
	}
	got := DiscoverFields(defs)
	if got != (FieldIDs{EpicLink: "customfield_5", StoryPoints: "customfield_3"}) {
		t.Errorf("got %+v", got)
	}
	if l := got.List(); len(l) != 2 {
		t.Errorf("list %v", l)
	}
}

// AgileAvailable: 404 = no Jira Software (no error); other failures are
// errors, not "unavailable".
func TestAgileAvailableDistinguishesMissingFromFailing(t *testing.T) {
	for _, tc := range []struct {
		status    int
		available bool
		err       bool
	}{{200, true, false}, {404, false, false}, {500, false, true}, {401, false, true}} {
		ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
			u, _ := url.Parse(r.URL)
			if u.Path != "/rest/agile/1.0/board" {
				t.Errorf("path %s", u.Path)
			}
			return &Response{StatusCode: tc.status, Body: []byte(`{"startAt":0,"maxResults":1,"isLast":true,"values":[]}`)}, nil
		}}
		ok, err := New("https://sd.local", "t", ft).AgileAvailable(context.Background())
		if ok != tc.available || (err != nil) != tc.err {
			t.Errorf("HTTP %d: available=%v err=%v", tc.status, ok, err)
		}
	}
}

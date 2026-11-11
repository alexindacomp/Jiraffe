package jira

import (
	"context"
	"testing"
)

func TestBoardConfigurationAndStatuses(t *testing.T) {
	ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
		switch r.URL {
		case "https://sd.local/rest/agile/1.0/board/673/configuration":
			return &Response{StatusCode: 200, Body: []byte(`{"id":673,"name":"ETL","columnConfig":{"columns":[
			 {"name":"Backlog","statuses":[]},{"name":"Work","statuses":[{"id":"3","self":"x"},{"id":10200},{"id":null}]}]}}`)}, nil
		case "https://sd.local/rest/api/2/project/MB/statuses":
			return &Response{StatusCode: 200, Body: []byte(`[{"id":"3","name":"Задача","subtask":false,
			 "statuses":[{"id":"1","name":"ToDo","statusCategory":{"key":"new"}}]}]`)}, nil
		case "https://sd.local/rest/api/2/status/10200":
			return &Response{StatusCode: 200, Body: []byte(`{"id":"10200","name":"Reject","statusCategory":{"key":"done"}}`)}, nil
		}
		return &Response{StatusCode: 404, Body: []byte(`{}`)}, nil
	}}
	c := New("https://sd.local/", "tok", ft)
	ctx := context.Background()
	cfg, err := c.BoardConfiguration(ctx, 673)
	if err != nil {
		t.Fatal(err)
	}
	cols := cfg.ColumnConfig.Columns
	if len(cols) != 2 || len(cols[0].Statuses) != 0 || len(cols[1].Statuses) != 3 ||
		cols[1].Statuses[0].ID != "3" || cols[1].Statuses[1].ID != "10200" || cols[1].Statuses[2].ID != "" {
		t.Errorf("config %+v", cfg)
	}
	types, err := c.ProjectStatuses(ctx, "MB")
	if err != nil || len(types) != 1 || types[0].Statuses[0].Name != "ToDo" || types[0].Statuses[0].StatusCategory.Key != "new" {
		t.Errorf("statuses %+v %v", types, err)
	}
	if s, err := c.StatusByID(ctx, "10200"); err != nil || s.Name != "Reject" {
		t.Errorf("status %+v %v", s, err)
	}
	if _, err := c.BoardConfiguration(ctx, 1); !IsCode(err, CodeNotFound) {
		t.Errorf("missing board: %v", err)
	}
	// Parameters that could alter the request path never reach Jira.
	n := len(ft.reqs)
	if _, err := c.ProjectStatuses(ctx, "MB/../x"); !IsCode(err, CodeBadRequest) {
		t.Errorf("bad key: %v", err)
	}
	for _, id := range []string{"", "1/../2", "abc", "-1"} {
		if _, err := c.StatusByID(ctx, id); !IsCode(err, CodeBadRequest) {
			t.Errorf("bad status id %q: %v", id, err)
		}
	}
	if _, err := c.BoardConfiguration(ctx, 0); !IsCode(err, CodeBadRequest) {
		t.Errorf("board 0: %v", err)
	}
	if len(ft.reqs) != n {
		t.Errorf("%d requests sent for invalid parameters", len(ft.reqs)-n)
	}
}

package app

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// chromePlatform is a platform with a frameless window.
type chromePlatform struct {
	fakePlatform
	mu    sync.Mutex
	info  WindowInfo
	calls []string
}

func (p *chromePlatform) WindowInfo() WindowInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info
}

func (p *chromePlatform) WindowControl(action, edge string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, action+" "+edge)
}

func TestWindowControl(t *testing.T) {
	plat := &chromePlatform{info: WindowInfo{Glass: true, Maximized: false, Active: true}}
	a := newRegApp(t, plat)
	ctx := context.Background()
	res, err := a.Call(ctx, "getWindowInfo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); string(b) != `{"glass":true,"maximized":false,"active":true}` {
		t.Errorf("getWindowInfo %s", b)
	}
	tests := []struct {
		params string
		ok     bool
	}{
		{`{"action":"minimize"}`, true},
		{`{"action":"toggleMaximize"}`, true},
		{`{"action":"close"}`, true},
		{`{"action":"drag"}`, true},
		{`{"action":"resize","edge":"nw"}`, true},
		{`{"action":"resize","edge":"s"}`, true},
		{`{"action":"resize"}`, false},
		{`{"action":"resize","edge":"top"}`, false},
		{`{"action":"drag","edge":"n"}`, false},
		{`{"action":"maximize"}`, false},
		{`{"action":""}`, false},
		{`{}`, false},
		{`{"action":1}`, false},
		{`[]`, false},
	}
	var want []string
	for _, tc := range tests {
		res, err := a.Call(ctx, "windowControl", json.RawMessage(tc.params))
		if res != nil {
			t.Errorf("%s: result %v", tc.params, res)
		}
		if tc.ok {
			if err != nil {
				t.Errorf("%s: %v", tc.params, err)
			}
			var p struct{ Action, Edge string }
			_ = json.Unmarshal([]byte(tc.params), &p)
			want = append(want, p.Action+" "+p.Edge)
		} else if ToError(err).Code != CodeBadParams {
			t.Errorf("%s: %v, want bad_params", tc.params, err)
		}
	}
	if len(plat.calls) != len(want) {
		t.Fatalf("calls %v, want %v", plat.calls, want)
	}
	for i := range want {
		if plat.calls[i] != want[i] {
			t.Errorf("call %d: %q, want %q", i, plat.calls[i], want[i])
		}
	}
}

// Without a frameless window (headless, tests, non-Windows): no-ops.
func TestWindowControlWithoutChrome(t *testing.T) {
	a := newRegApp(t, &plainPlatform{})
	res, err := a.Call(context.Background(), "getWindowInfo", nil)
	if err != nil || res.(WindowInfo) != (WindowInfo{}) {
		t.Errorf("%v %v", res, err)
	}
	if _, err := a.Call(context.Background(), "windowControl", json.RawMessage(`{"action":"minimize"}`)); err != nil {
		t.Error(err)
	}
	if _, err := a.Call(context.Background(), "windowControl", json.RawMessage(`{"action":"x"}`)); ToError(err).Code != CodeBadParams {
		t.Errorf("validation still applies: %v", err)
	}
}

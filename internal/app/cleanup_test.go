package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jiraffe/internal/config"
	"jiraffe/internal/jira"
	"jiraffe/internal/scheduler"
	"jiraffe/internal/state"
)

// regPlatform simulates the HKCU Run value; writes can be made to fail.
type regPlatform struct {
	registered bool
	writeErr   error
	writes     int
}

func (p *regPlatform) Transport() jira.Transport             { return nil }
func (p *regPlatform) OpenURL(string) error                  { return nil }
func (p *regPlatform) Notify(string, string, string)         {}
func (p *regPlatform) SettingsChanged(config.Settings, bool) {}
func (p *regPlatform) SetAutostart(on bool) error {
	p.writes++
	if p.writeErr != nil {
		return p.writeErr
	}
	p.registered = on
	return nil
}
func (p *regPlatform) AutostartEnabled() (bool, error) { return p.registered, nil }

func newRegApp(t *testing.T, plat Platform) *App {
	t.Helper()
	dir := t.TempDir()
	cfg, _ := config.Open(filepath.Join(dir, "config.json"), plainProtector{})
	st, _ := state.Open(filepath.Join(dir, "state.json"))
	clock := scheduler.Clock(&fakeClock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, zone)})
	return New(cfg, st, plat, clock, nil)
}

func TestAutostartEffectiveReportsRegistry(t *testing.T) {
	plat := &regPlatform{writeErr: errors.New("access denied")}
	a := newRegApp(t, plat)
	a.Start() // stored default autostart=true; the write fails
	defer a.Stop(time.Second)
	ctx := context.Background()

	res, err := a.Call(ctx, "getSettings", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"autostart":true`) || !strings.Contains(string(b), `"autostartEffective":false`) {
		t.Fatalf("getSettings %s", b)
	}

	// Saving with an unchanged preference retries the write because the
	// registry disagrees; a failure is reported as a warning.
	writes := plat.writes
	res, err = a.Call(ctx, "saveSettings", json.RawMessage(`{"language":"en"}`))
	if err != nil {
		t.Fatal(err)
	}
	sr := res.(SaveResult)
	if plat.writes != writes+1 || sr.AutostartEffective || len(sr.Warnings) != 1 {
		t.Errorf("writes=%d→%d result=%+v", writes, plat.writes, sr)
	}

	plat.writeErr = nil
	res, _ = a.Call(ctx, "saveSettings", json.RawMessage(`{}`))
	if sr := res.(SaveResult); !sr.AutostartEffective || len(sr.Warnings) != 0 {
		t.Errorf("after a successful write: %+v", sr)
	}
}

func TestAutostartEffectiveWithoutReader(t *testing.T) {
	a := newRegApp(t, &plainPlatform{})
	if v := a.settingsView(a.cfg.Get()); v.AutostartEffective != v.Autostart {
		t.Errorf("platform without a reader must mirror the preference: %+v", v)
	}
}

type plainPlatform struct{}

func (plainPlatform) Transport() jira.Transport             { return nil }
func (plainPlatform) OpenURL(string) error                  { return nil }
func (plainPlatform) SetAutostart(bool) error               { return nil }
func (plainPlatform) Notify(string, string, string)         {}
func (plainPlatform) SettingsChanged(config.Settings, bool) {}

func TestBadNonceErrorCode(t *testing.T) {
	e := BadNonceError("uk")
	if e.Code != "bad_nonce" || e.Message != T("uk", "err_bad_nonce") {
		t.Errorf("%+v", e)
	}
}

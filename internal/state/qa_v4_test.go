package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jiraffe/internal/update"
)

// v0.4: skippedVersion and the last update check (with the latest release)
// survive a restart; a snapshot is a deep copy of the stored release.
func TestV4QAUpdateStatePersistsAndSnapshotIsACopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := open(t, path)
	rel := &update.Release{Version: "0.5.0", Notes: "n", Page: update.ReleasePage("0.5.0"),
		Exe: update.Asset{URL: "https://github.com/alexindacomp/Jiraffe/releases/download/v0.5.0/jiraffe.exe", Size: 10}}
	if err := s.Update(func(tx Tx) {
		tx.SkippedVersion = "0.5.0"
		tx.UpdateCheck = &UpdateCheck{At: "2026-11-11T10:00:00+02:00", OK: false, Code: "http_status", Detail: "HTTP 403", Latest: rel}
	}); err != nil {
		t.Fatal(err)
	}

	snap := s.Snapshot()
	snap.UpdateCheck.Latest.Version = "9.9.9"
	snap.UpdateCheck.Code = "x"
	if c := s.Snapshot().UpdateCheck; c.Latest.Version != "0.5.0" || c.Code != "http_status" {
		t.Errorf("snapshot shares memory with the store: %+v %+v", c, c.Latest)
	}

	again := open(t, path).Snapshot()
	c := again.UpdateCheck
	if again.SkippedVersion != "0.5.0" || c == nil || c.OK || c.Code != "http_status" || c.Detail != "HTTP 403" ||
		c.At != "2026-11-11T10:00:00+02:00" || c.Latest == nil || *c.Latest != *rel {
		t.Errorf("after reopen: skipped %q check %+v latest %+v", again.SkippedVersion, c, c.Latest)
	}

	// An older state.json (v0.3) has neither key.
	old := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(old, []byte(`{"lastWorklogDate":"2026-11-10"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := open(t, old).Snapshot()
	if d.SkippedVersion != "" || d.UpdateCheck != nil {
		t.Errorf("v0.3 state: %+v", d)
	}
	// Nothing is written for them until an update check happened.
	if err := open(t, old).Update(func(tx Tx) {}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(old); strings.Contains(string(b), "updateCheck") || strings.Contains(string(b), "skippedVersion") {
		t.Errorf("empty update state written: %s", b)
	}
}

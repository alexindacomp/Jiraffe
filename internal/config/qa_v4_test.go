package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// v0.4: autoCheckUpdates defaults to true, also for a config file written
// by an older version (no such key), and false survives a restart.
func TestV4QAAutoCheckUpdatesDefaultAndPersistence(t *testing.T) {
	if !Defaults().AutoCheckUpdates {
		t.Fatal("default must be true")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"jiraUrl":"https://sd.local","language":"uk"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, xorProtector{})
	if err != nil {
		t.Fatal(err)
	}
	if s := st.Get(); !s.AutoCheckUpdates || s.Language != "uk" {
		t.Fatalf("v0.3 config: autoCheckUpdates %v (want true), language %q", s.AutoCheckUpdates, s.Language)
	}

	s := st.Get()
	s.AutoCheckUpdates = false
	if _, _, err := st.Save(s, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var file map[string]any
	if err := json.Unmarshal(b, &file); err != nil || file["autoCheckUpdates"] != false {
		t.Errorf("file lacks the setting: %s", b)
	}
	again, err := Open(path, xorProtector{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Get().AutoCheckUpdates {
		t.Error("autoCheckUpdates=false lost on reopen")
	}
}

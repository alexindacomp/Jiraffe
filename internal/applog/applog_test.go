package applog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jiraffe/internal/filelock"
)

func TestRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "app.log")
	w, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 39) + "\n"
	for i := 0; i < 5; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	cur, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	if len(cur) > 100 || len(old) == 0 || len(old) > 100 || !strings.HasSuffix(string(cur), line) {
		t.Fatalf("cur=%d old=%d", len(cur), len(old))
	}
}

// A rotation that fails (on Windows: another process still has the file
// open) must not lose lines; it is retried after a pause.
func TestFailedRotationKeepsLogging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	w, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	now := time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	fails := 0
	w.rename = func(string, string) error { fails++; return errors.New("sharing violation") }
	line := strings.Repeat("x", 39) + "\n"
	for i := 0; i < 10; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	cur, _ := os.ReadFile(path)
	if got := strings.Count(string(cur), line); got != 10 {
		t.Fatalf("%d of 10 lines in the log", got)
	}
	if fails != 1 || !strings.Contains(string(cur), "applog: rotate failed") {
		t.Fatalf("rename attempts %d (want 1 within the retry pause); log %q", fails, cur)
	}
	now = now.Add(rotateRetry)
	w.rename = os.Rename
	if _, err := w.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if old, _ := os.ReadFile(path + ".1"); strings.Count(string(old), line) != 10 {
		t.Errorf("after the pause the rotation must succeed: .1 has %d lines", strings.Count(string(old), line))
	}
}

// Two processes writing one log: a rotation by one must not make the other
// delete or replace the fresh .1 file; every line survives.
func TestTwoWritersDoNotLoseLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	a, err := Open(path, 200)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(path, 200)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := 0; i < 12; i++ {
		w, who := a, "a"
		if i%2 == 1 {
			w, who = b, "b"
		}
		line := fmt.Sprintf("%s-%02d %s\n", who, i, strings.Repeat("y", 30))
		want = append(want, line)
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	a.Close()
	b.Close()
	cur, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	all := string(old) + string(cur)
	for _, l := range want {
		if !strings.Contains(all, l) {
			t.Errorf("lost line %q", l)
		}
	}
}

func TestOpenOversizedLogThatCannotRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("z", 300)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Open rotates at once; a second writer holding the lock makes it skip
	// the rotation instead of failing.
	unlock, err := filelock.Lock(path+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	w, err := Open(path, 100)
	unlock()
	if err != nil {
		t.Fatalf("open must not fail when the rotation cannot run: %v", err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("line\n")); err != nil {
		t.Fatal(err)
	}
	if cur, _ := os.ReadFile(path); !strings.HasSuffix(string(cur), "line\n") {
		t.Errorf("log %q", cur)
	}
}

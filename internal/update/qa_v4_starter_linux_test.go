package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// OSStarter starts the executable detached, with exactly the given
// arguments, in the executable's folder; a missing executable is an error.
// This is a Linux/WSL smoke test with /bin/sh only (hence the _linux file):
// the Windows specifics (argument quoting, GUI process without console
// handles) need the manual check on Windows.
func TestV4QAOSStarter(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this system")
	}
	if err := (OSStarter{}).Start(filepath.Join(t.TempDir(), "missing.exe"), nil); err == nil {
		t.Error("missing executable started")
	}
	out := filepath.Join(t.TempDir(), "out")
	// Line 1: the child's pid (to reap it), line 2: its working dir,
	// line 3: its arguments. Written to a temp file and renamed, so a
	// partially written file is never read.
	script := `{ echo $$; pwd; printf '%s|' "$@"; } > "$1.tmp"; mv "$1.tmp" "$1"`
	if err := (OSStarter{}).Start(sh, []string{"-c", script, "sh", out, "--wait-pid", "7"}); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if got, err = os.ReadFile(out); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the started process did not run")
		}
	}
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	if len(lines) != 3 {
		t.Fatalf("child output %q", got)
	}
	if pid, err := strconv.Atoi(lines[0]); err == nil {
		var ws syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &ws, 0, nil) // reap the detached child (no zombie)
	}
	dir, _ := filepath.EvalSymlinks(filepath.Dir(sh))
	if wd, _ := filepath.EvalSymlinks(lines[1]); wd != dir {
		t.Errorf("working dir %q, want %q", lines[1], dir)
	}
	if lines[2] != out+"|--wait-pid|7|" {
		t.Errorf("args %q", lines[2])
	}
}

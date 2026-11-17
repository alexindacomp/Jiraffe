//go:build linux

package linuxdesk

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func fakeKeyring() (*Keyring, *string) {
	stored := new(string)
	k := &Keyring{
		get: func() (string, error) {
			if *stored == "" {
				return "", keyring.ErrNotFound
			}
			return *stored, nil
		},
		set: func(v string) error { *stored = v; return nil },
	}
	return k, stored
}

func TestKeyringRoundTrip(t *testing.T) {
	k, stored := fakeKeyring()
	blob, err := k.Protect([]byte("secret-token"))
	if err != nil || *stored == "" {
		t.Fatalf("protect: %v (key stored %v)", err, *stored != "")
	}
	if bytes.Contains(blob, []byte("secret-token")) {
		t.Fatal("blob holds the plain token")
	}
	// A new process: the key comes from the keyring.
	k2 := &Keyring{get: k.get, set: func(string) error { t.Fatal("key replaced"); return nil }}
	plain, err := k2.Unprotect(blob)
	if err != nil || string(plain) != "secret-token" {
		t.Fatalf("unprotect: %q %v", plain, err)
	}
	blob[len(blob)-1] ^= 1
	if _, err := k2.Unprotect(blob); err == nil {
		t.Fatal("tampered blob accepted")
	}
	if _, err := k2.Unprotect([]byte("dpapi-blob")); err == nil {
		t.Fatal("foreign blob accepted")
	}
}

func TestKeyringUnavailable(t *testing.T) {
	k := &Keyring{get: func() (string, error) { return "", errors.New("no secret service") }, set: func(string) error { return nil }}
	if _, err := k.Protect([]byte("x")); err == nil {
		t.Fatal("protect without a keyring succeeded")
	}
	// Unprotect never creates a key.
	k3, stored := fakeKeyring()
	if _, err := k3.Unprotect([]byte(blobPrefix + strings.Repeat("0", 40))); err == nil || *stored != "" {
		t.Fatalf("unprotect created a key: %v", err)
	}
}

func TestAutostart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	exe := "/home/u/My Apps/jiraffe"
	if on, err := AutostartEnabled(exe); on || err != nil {
		t.Fatalf("initially %v %v", on, err)
	}
	if err := SetAutostart(exe, true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(autostartFile())
	if !strings.Contains(string(b), `Exec="/home/u/My Apps/jiraffe" --minimized`) {
		t.Fatalf("entry:\n%s", b)
	}
	if on, _ := AutostartEnabled(exe); !on {
		t.Fatal("not enabled after SetAutostart")
	}
	if on, _ := AutostartEnabled("/other/jiraffe"); on {
		t.Fatal("enabled for another exe")
	}
	if err := os.WriteFile(autostartFile(), append(b, []byte("Hidden=true\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if on, _ := AutostartEnabled(exe); on {
		t.Fatal("hidden entry counts as enabled")
	}
	if err := SetAutostart(exe, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(autostartFile()); !os.IsNotExist(err) {
		t.Fatal("entry not removed")
	}
	if err := SetAutostart(exe, false); err != nil {
		t.Fatalf("removing twice: %v", err)
	}
}

func TestExecQuote(t *testing.T) {
	if got := execQuote(`/a/b$c"d`); got != `"/a/b\\$c\\"d"` {
		t.Fatalf("got %s", got)
	}
}

func TestDirs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
	t.Setenv("XDG_CACHE_HOME", "relative") // ignored: not absolute
	t.Setenv("HOME", "/home/u")
	if ConfigDir() != "/x/cfg/Jiraffe" || CacheDir() != filepath.Join("/home/u", ".cache", "Jiraffe") {
		t.Fatalf("%s %s", ConfigDir(), CacheDir())
	}
}

func TestSingleInstance(t *testing.T) {
	in, first, err := AcquireSingleInstance()
	if err != nil || !first {
		t.Skipf("instance socket busy (a Jiraffe runs?): %v %v", first, err)
	}
	defer in.l.Close()
	shown := make(chan struct{}, 1)
	in.Serve(func() { shown <- struct{}{} })
	in2, first2, err := AcquireSingleInstance()
	if in2 != nil || first2 || err != nil {
		t.Fatalf("second: %v %v %v", in2, first2, err)
	}
	select {
	case <-shown:
	case <-time.After(3 * time.Second):
		t.Fatal("the first instance was not asked to show")
	}
}

func TestWaitProcessExit(t *testing.T) {
	if err := WaitProcessExit(os.Getpid(), 150*time.Millisecond); !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("own pid: %v", err)
	}
	if err := WaitProcessExit(1<<22-3, time.Second); err != nil {
		t.Fatalf("unused pid: %v", err)
	}
}

func TestOpenURLRejectsOtherSchemes(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "-x", "javascript:alert(1)"} {
		if err := OpenURL(u); err == nil {
			t.Errorf("%s opened", u)
		}
	}
}

func TestDesktopIntegration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	if err := EnsureDesktopIntegration("/home/u/.local/bin/jiraffe", []byte("png")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "applications", "jiraffe.desktop"))
	if err != nil || !strings.Contains(string(b), "Exec=\"/home/u/.local/bin/jiraffe\"\n") || !strings.Contains(string(b), "StartupWMClass=jiraffe") ||
		!strings.Contains(string(b), "Icon="+filepath.Join(dir, "icons", "hicolor", "256x256", "apps", "jiraffe.png")+"\n") {
		t.Fatalf("entry %q %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "icons", "hicolor", "256x256", "apps", "jiraffe.png")); string(b) != "png" {
		t.Fatalf("icon %q", b)
	}
	if err := EnsureDesktopIntegration("/opt/jiraffe", []byte("png")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "applications", "jiraffe.desktop")); !strings.Contains(string(b), `Exec="/opt/jiraffe"`) {
		t.Fatalf("moved exe not updated: %s", b)
	}
}

package update

// QA tests for v0.4 §5 (automatic updates): the swap's failure paths,
// which must never leave jiraffe.exe missing, download I/O failures,
// cleanup with locked files, the release-notes cap from the contract, and
// starting the new process.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// swapDir creates <tmp>/app/jiraffe.exe ("old exe") and a verified
// download of newExe next to it.
func swapDir(t *testing.T, newExe []byte) (exe, down string) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, "app", "jiraffe.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("old exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	down = filepath.Join(dir, "jiraffe-0.5.0.exe.part")
	if err := os.WriteFile(down, newExe, 0o644); err != nil {
		t.Fatal(err)
	}
	return exe, down
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// A stale exe.old that can neither be removed nor moved aside stops the
// install before the running exe is touched.
func TestV4QAInstallStaleOldInTheWay(t *testing.T) {
	newExe := exeBytes(3000)
	exe, down := swapDir(t, newExe)
	if err := os.WriteFile(exe+".old", []byte("stale old"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs := &faultFS{
		failRemove: map[string]bool{"jiraffe.exe.old": true},
		failRename: map[string]int{},
	}
	// Moving it aside fails too, whatever the generated suffix is.
	renameAside := &prefixRenameFailFS{faultFS: fs, fromBase: "jiraffe.exe.old", toPrefix: "jiraffe.exe.old."}
	u := New(Config{FS: renameAside, Exe: exe, Sleep: func(time.Duration) {}})

	swap, err := u.Install(down, int64(len(newExe)), sha256.Sum256(newExe))

	if swap != nil || CodeOf(err) != CodeInstall || !strings.Contains(err.Error(), "jiraffe.exe.old") {
		t.Fatalf("swap %v err %v, want install_failed naming jiraffe.exe.old", swap, err)
	}
	if got := readFile(t, exe); got != "old exe" {
		t.Errorf("running exe changed: %q", got)
	}
	if got := readFile(t, exe+".old"); got != "stale old" {
		t.Errorf("stale .old %q", got)
	}
	if _, err := os.Stat(exe + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staged exe.new left behind: %v", err)
	}
}

// prefixRenameFailFS fails every rename of fromBase to a name starting
// with toPrefix (the "move aside" of clearOld).
type prefixRenameFailFS struct {
	*faultFS
	fromBase, toPrefix string
}

func (f *prefixRenameFailFS) Rename(from, to string) error {
	if filepath.Base(from) == f.fromBase && strings.HasPrefix(filepath.Base(to), f.toPrefix) {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
	}
	return f.faultFS.Rename(from, to)
}

// Install: the new exe cannot be moved in AND the previous one cannot be
// moved back. The error is install_failed and names where the previous
// exe is (SPEC: "a failed rollback is reported as install_failed naming
// where the previous exe is"), and that file really holds it.
func TestV4QAInstallRestoreFailureNamesPrevious(t *testing.T) {
	newExe := exeBytes(3000)
	exe, down := swapDir(t, newExe)
	fs := &faultFS{failRename: map[string]int{
		"jiraffe.exe.new>jiraffe.exe": -1,
		"jiraffe.exe.old>jiraffe.exe": -1,
	}}
	u := New(Config{FS: fs, Exe: exe, Sleep: func(time.Duration) {}})

	swap, err := u.Install(down, int64(len(newExe)), sha256.Sum256(newExe))

	var ue *Error
	if swap != nil || !errors.As(err, &ue) || ue.Code != CodeInstall || ue.Previous != exe+".old" {
		t.Fatalf("swap %v err %#v, want install_failed with Previous=%s", swap, err, exe+".old")
	}
	if got := readFile(t, ue.Previous); got != "old exe" {
		t.Errorf("Previous %s holds %q, want the previous exe", ue.Previous, got)
	}
}

// Rollback after a finished swap when exe.old cannot be renamed back: the
// new exe is put back in place, so jiraffe.exe is never missing, and the
// error names the previous exe.
func TestV4QARollbackKeepsAnExeInPlace(t *testing.T) {
	newExe := exeBytes(3000)
	newExe[10] = 42
	tests := []struct {
		name        string
		fail        map[string]int // renames failing during the rollback
		wantExe     string         // content of jiraffe.exe afterwards
		wantNewAt   string         // where the new exe is left ("" = jiraffe.exe)
		wantDetails string
	}{
		{"previous exe cannot come back: the new one stays",
			map[string]int{"jiraffe.exe.old>jiraffe.exe": -1}, string(newExe), "", "the new exe stays"},
		{"neither can be moved: reported with both locations",
			map[string]int{"jiraffe.exe.old>jiraffe.exe": -1, "jiraffe.exe.new>jiraffe.exe": -1}, "", "jiraffe.exe.new", "no exe in place"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exe, down := swapDir(t, newExe)
			fs := &faultFS{failRename: map[string]int{}}
			u := New(Config{FS: fs, Exe: exe, Sleep: func(time.Duration) {}})
			swap, err := u.Install(down, int64(len(newExe)), sha256.Sum256(newExe))
			if err != nil {
				t.Fatal(err)
			}
			fs.mu.Lock()
			for k, v := range tc.fail {
				fs.failRename[k] = v
			}
			fs.mu.Unlock()

			err = swap.Rollback()

			var ue *Error
			if !errors.As(err, &ue) || ue.Code != CodeInstall || ue.Previous != exe+".old" || !strings.Contains(ue.Detail, tc.wantDetails) {
				t.Fatalf("err %#v, want install_failed (%q) with Previous", err, tc.wantDetails)
			}
			if got := readFile(t, exe+".old"); got != "old exe" {
				t.Errorf("previous exe %q", got)
			}
			if tc.wantNewAt == "" {
				if got := readFile(t, exe); got != tc.wantExe {
					t.Errorf("jiraffe.exe is %.20q, want the new exe back in place", got)
				}
			} else if got := readFile(t, filepath.Join(filepath.Dir(exe), tc.wantNewAt)); got != string(newExe) || !strings.Contains(ue.Detail, tc.wantNewAt) {
				t.Errorf("new exe at %s: %.20q (detail %q)", tc.wantNewAt, got, ue.Detail)
			}
		})
	}
}

// mkdirFailFS fails MkdirAll (e.g. %LOCALAPPDATA% not writable).
type mkdirFailFS struct{ OSFS }

func (mkdirFailFS) MkdirAll(string) error { return os.ErrPermission }

// closeFailFS returns a part file whose Close fails (disk full on flush).
type closeFailFS struct{ OSFS }

type failingCloser struct{ io.WriteCloser }

func (f failingCloser) Close() error {
	_ = f.WriteCloser.Close()
	return errors.New("disk full")
}

func (c closeFailFS) Create(path string) (io.WriteCloser, error) {
	w, err := c.OSFS.Create(path)
	if err != nil {
		return nil, err
	}
	return failingCloser{w}, nil
}

// Download: local I/O failures are file_error, an oversized exe is
// too_large, an unreachable (transport error) .sig stays a network error;
// a failed download never leaves the .part file behind.
func TestV4QADownloadFailures(t *testing.T) {
	priv := testKey(t)
	exe := exeBytes(5000)
	base := func() map[string]resp {
		return map[string]resp{
			dlBase + "jiraffe.exe.sha256": {status: 200, body: sumLine(exe)},
			dlBase + "jiraffe.exe.sig":    {status: 200, body: sigLine(priv, "0.5.0", exe)},
			dlBase + "jiraffe.exe":        {status: 200, body: exe},
		}
	}
	tests := []struct {
		name  string
		fs    FS
		route func(m map[string]resp)
		code  string
	}{
		{"download folder cannot be created", mkdirFailFS{}, nil, CodeFile},
		{"part file cannot be created", &faultFS{failCreate: map[string]error{"jiraffe-0.5.0.exe.part": os.ErrPermission}}, nil, CodeFile},
		{"part file cannot be flushed", closeFailFS{}, nil, CodeFile},
		{"exe over 20 MB", OSFS{}, func(m map[string]resp) {
			m[dlBase+"jiraffe.exe"] = resp{status: 200, body: append(append([]byte{}, exe...), make([]byte, MaxDownload)...)}
		}, CodeTooLarge},
		{"signature asset over the limit", OSFS{}, func(m map[string]resp) {
			m[dlBase+"jiraffe.exe.sig"] = resp{status: 200, body: bytes.Repeat([]byte("A"), maxSig+1)}
		}, CodeBadSignature},
		{"signature unreachable (transport)", OSFS{}, func(m map[string]resp) {
			m[dlBase+"jiraffe.exe.sig"] = resp{err: errors.New("WinHTTP 12029")}
		}, CodeNetwork},
		{"checksum asset over 4 KB", OSFS{}, func(m map[string]resp) {
			m[dlBase+"jiraffe.exe.sha256"] = resp{status: 200, body: bytes.Repeat([]byte(" "), maxSumFile+1)}
		}, CodeTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "update")
			routes := base()
			if tc.route != nil {
				tc.route(routes)
			}
			u := New(Config{Getter: &fakeGetter{routes: routes}, FS: tc.fs, Dir: dir})
			rel, err := ParseRelease(releaseJSON(nil))
			if err != nil {
				t.Fatal(err)
			}
			rel.Exe.Size = int64(len(exe))

			f, err := u.Download(context.Background(), rel, nil)

			if f != nil || CodeOf(err) != tc.code {
				t.Fatalf("fetched %+v err %v, want code %s", f, err, tc.code)
			}
			if _, err := os.Stat(u.PartPath("0.5.0")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf(".part left behind: %v", err)
			}
		})
	}
}

// fetch: a 304 is not a redirect, and an unparsable Location is refused.
func TestV4QAFetchOddResponses(t *testing.T) {
	exe := dlBase + "jiraffe.exe"
	for name, tc := range map[string]struct {
		r    resp
		code string
	}{
		"304 not modified":    {resp{status: 304}, CodeHTTP},
		"invalid Location":    {resp{status: 302, loc: "https://objects.githubusercontent.com/%zz"}, CodeRedirect},
		"Location with a NUL": {resp{status: 302, loc: "https://objects.githubusercontent.com/\x00"}, CodeRedirect},
		"1xx is not success":  {resp{status: 101}, CodeHTTP},
	} {
		g := &fakeGetter{routes: map[string]resp{exe: tc.r}}
		err := New(Config{Getter: g}).fetch(context.Background(), exe, assetHops, nil, io.Discard, 10)
		if CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %s", name, err, tc.code)
		}
		if len(g.calls) != 1 {
			t.Errorf("%s: %d requests %v", name, len(g.calls), g.calls)
		}
	}
}

// Cleanup tries every file and reports the first failure (a locked file is
// retried later by the app).
func TestV4QACleanupContinuesPastLockedFiles(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "jiraffe.exe")
	upd := filepath.Join(dir, "update")
	_ = os.MkdirAll(upd, 0o755)
	for _, f := range []string{exe, exe + ".old", exe + ".old.a1", exe + ".new",
		filepath.Join(upd, "jiraffe-0.5.0.exe.part"), filepath.Join(upd, "jiraffe-0.6.0.exe.part")} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fs := &faultFS{failRemove: map[string]bool{"jiraffe-0.5.0.exe.part": true, "jiraffe.exe.old": true}}
	u := New(Config{FS: fs, Exe: exe, Dir: upd})

	if err := u.CleanupDownloads(); err == nil {
		t.Error("CleanupDownloads: a locked file must be reported")
	}
	if err := u.CleanupOld(); err == nil {
		t.Error("CleanupOld: a locked file must be reported")
	}

	for f, want := range map[string]bool{
		exe: true, exe + ".old": true, exe + ".old.a1": false, exe + ".new": false,
		filepath.Join(upd, "jiraffe-0.5.0.exe.part"): true, filepath.Join(upd, "jiraffe-0.6.0.exe.part"): false,
	} {
		_, err := os.Stat(f)
		if exists := err == nil; exists != want {
			t.Errorf("%s exists=%v, want %v", filepath.Base(f), exists, want)
		}
	}
	// A folder that cannot be listed is an error, not "nothing to do".
	if err := New(Config{FS: listFailFS{}, Exe: exe, Dir: upd}).CleanupOld(); err == nil {
		t.Error("CleanupOld with an unreadable folder: no error")
	}
}

type listFailFS struct{ OSFS }

func (listFailFS) List(string) ([]string, error) { return nil, os.ErrPermission }

// Contract: UpdateRelease.notes is plain text of at most 20000 characters.
func TestV4QAReleaseNotesCap(t *testing.T) {
	for _, n := range []int{maxNotesRunes - 1, maxNotesRunes, maxNotesRunes + 1, 3 * maxNotesRunes} {
		body := strings.Repeat("ж", n)
		r, err := ParseRelease(releaseJSON(func(m map[string]any) { m["body"] = body }))
		if err != nil {
			t.Fatal(err)
		}
		got := utf8.RuneCountInString(r.Notes)
		if got > maxNotesRunes {
			t.Errorf("notes of %d runes kept as %d runes, contract allows at most %d", n, got, maxNotesRunes)
		}
		if n <= maxNotesRunes && r.Notes != body {
			t.Errorf("notes of %d runes changed although under the cap", n)
		}
		if n > maxNotesRunes && !strings.HasSuffix(r.Notes, "…") {
			t.Errorf("notes of %d runes cut without an ellipsis", n)
		}
	}
}

// The error text carries code, detail and cause (it is logged and is the
// English detail of the stored check error).
func TestV4QAErrorText(t *testing.T) {
	cause := errors.New("WinHTTP 12002")
	for _, tc := range []struct {
		err  *Error
		want string
	}{
		{&Error{Code: CodeNotExe}, "update: not_exe"},
		{&Error{Code: CodeHTTP, Detail: "HTTP 403"}, "update: http_status: HTTP 403"},
		{&Error{Code: CodeNetwork, Err: cause}, "update: network: WinHTTP 12002"},
		{&Error{Code: CodeNetwork, Detail: "timeout", Err: ErrTimeout}, "update: network: timeout: " + ErrTimeout.Error()},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
	if CodeOf(errors.New("x")) != "" || CodeOf(nil) != "" {
		t.Error("CodeOf of a foreign error")
	}
	wrapped := errors.Join(errors.New("ctx"), &Error{Code: CodeStart, Err: cause})
	if CodeOf(wrapped) != CodeStart || !errors.Is(wrapped, cause) {
		t.Errorf("wrapped: %q", CodeOf(wrapped))
	}
}

// An exe of exactly 20 MB is a valid release (the limit is inclusive).
func TestV4QAReleaseExeSizeLimitInclusive(t *testing.T) {
	r, err := ParseRelease(releaseJSON(func(m map[string]any) {
		for _, a := range m["assets"].([]any) {
			if a := a.(map[string]any); a["name"] == "jiraffe.exe" {
				a["size"] = MaxDownload
			}
		}
	}))
	if err != nil || r.Exe.Size != MaxDownload {
		t.Errorf("20 MB exe: %v", err)
	}
}

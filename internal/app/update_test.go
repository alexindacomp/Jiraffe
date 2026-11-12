package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jiraffe/internal/config"
	"jiraffe/internal/state"
	"jiraffe/internal/update"
)

// --- fakes ----------------------------------------------------------------------

type ghResp struct {
	status int
	loc    string
	body   []byte
}

// fakeGitHub serves the release API and assets (update.Getter).
type fakeGitHub struct {
	mu      sync.Mutex
	routes  map[string]ghResp
	calls   []string
	headers []map[string]string
}

func (g *fakeGitHub) Get(ctx context.Context, url string, headers map[string]string, w io.Writer, limit int64) (int, string, error) {
	g.mu.Lock()
	g.calls = append(g.calls, url)
	g.headers = append(g.headers, headers)
	r, ok := g.routes[url]
	g.mu.Unlock()
	switch {
	case !ok:
		return 404, "", nil
	case r.status >= 300 && r.status < 400:
		return r.status, r.loc, nil
	case r.status >= 200 && r.status < 300:
		if int64(len(r.body)) > limit {
			return 0, "", update.ErrTooLarge
		}
		// Streamed in 1 KB chunks like a real transport, so download
		// progress sees many small writes.
		for b := r.body; len(b) > 0; {
			n := min(len(b), 1024)
			if _, err := w.Write(b[:n]); err != nil {
				return 0, "", err
			}
			b = b[n:]
		}
		return r.status, "", nil
	}
	return r.status, "", nil
}

// waitFor polls cond until it holds and fails the test on timeout.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// goroutinesIn counts the goroutines whose stack contains fn (e.g.
// ".runInstall(") and how many of them are parked on a sync.Mutex. It lets
// a test wait for "the job is blocked on the lock" instead of sleeping.
func goroutinesIn(fn string) (total, onMutex int) {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	for _, g := range strings.Split(string(buf), "\n\n") {
		if !strings.Contains(g, fn) {
			continue
		}
		total++
		if header, _, _ := strings.Cut(g, "\n"); strings.Contains(header, "[sync.Mutex.Lock") {
			onMutex++
		}
	}
	return total, onMutex
}

// waitInstallBlockedOnLock waits until the install job announced the
// install phase and is parked on a mutex (runMu: a worklog run is in
// progress), so the swap provably has not happened yet.
func waitInstallBlockedOnLock(t *testing.T, e *updEnv) {
	t.Helper()
	waitFor(t, func() bool {
		if !strings.Contains(strings.Join(e.rec.of("updateProgress"), ""), `"install"`) {
			return false
		}
		_, blocked := goroutinesIn(".runInstall(")
		return blocked == 1
	}, "the install job to block on the worklog lock")
}

func (g *fakeGitHub) set(url string, r ghResp) {
	g.mu.Lock()
	g.routes[url] = r
	g.mu.Unlock()
}

func (g *fakeGitHub) count(url string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.calls {
		if c == url {
			n++
		}
	}
	return n
}

type fakeHost struct {
	open bool
	quit chan struct{}
	once sync.Once
}

func (h *fakeHost) WindowOpen() bool { return h.open }
func (h *fakeHost) Quit()            { h.once.Do(func() { close(h.quit) }) }

type fakeStarter struct {
	mu   sync.Mutex
	exe  string
	args []string
	err  error
}

func (s *fakeStarter) Start(exe string, args []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exe, s.args = exe, append([]string{}, args...)
	return s.err
}

// failFS makes the write probe in the exe folder fail (read-only folder).
type failFS struct{ update.OSFS }

func (failFS) Create(path string) (io.WriteCloser, error) {
	if filepath.Base(path) == ".jiraffe-update-probe" {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
	}
	return update.OSFS{}.Create(path)
}

type event struct {
	typ  string
	json string
}

type recorder struct {
	mu     sync.Mutex
	events []event
	onEmit func(typ string) // called before recording (from the emitting goroutine)
}

func (r *recorder) emit(typ string, payload any) {
	if r.onEmit != nil {
		r.onEmit(typ)
	}
	b, _ := json.Marshal(payload)
	r.mu.Lock()
	r.events = append(r.events, event{typ, string(b)})
	r.mu.Unlock()
}

func (r *recorder) of(typ string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		if e.typ == typ {
			out = append(out, e.json)
		}
	}
	return out
}

// wait polls until an event of typ was emitted.
func (r *recorder) wait(t *testing.T, typ string) []string {
	t.Helper()
	waitFor(t, func() bool { return len(r.of(typ)) > 0 }, "a "+typ+" event")
	return r.of(typ)
}

const relDL = "https://github.com/alexindacomp/Jiraffe/releases/download/"

func ghRelease(version string, exe []byte) []byte {
	b, _ := json.Marshal(map[string]any{
		"tag_name": "v" + version, "body": "## Changes\r\n- <b>bold</b> stays text", "published_at": "2026-11-10T12:00:00Z",
		"assets": []any{
			map[string]any{"name": "jiraffe.exe", "browser_download_url": relDL + "v" + version + "/jiraffe.exe", "size": len(exe)},
			map[string]any{"name": "jiraffe.exe.sha256", "browser_download_url": relDL + "v" + version + "/jiraffe.exe.sha256", "size": 80},
			map[string]any{"name": "jiraffe.exe.sig", "browser_download_url": relDL + "v" + version + "/jiraffe.exe.sig", "size": 89},
		},
	})
	return b
}

func fakeExe(n int, fill byte) []byte {
	b := bytes.Repeat([]byte{fill}, n)
	copy(b, "MZ")
	return b
}

// testReleaseKey signs the test releases; newUpdateApp makes the updater
// trust it instead of the real release key.
var testReleaseKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))

func signRelease(key ed25519.PrivateKey, version string, exe []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, update.SignedMessage(version, sha256.Sum256(exe)))) + "\n")
}

func serveRelease(g *fakeGitHub, version string, exe []byte) {
	sum := sha256.Sum256(exe)
	g.set(relDL+"v"+version+"/jiraffe.exe.sig", ghResp{status: 302, loc: "https://objects.githubusercontent.com/g" + version})
	g.set("https://objects.githubusercontent.com/g"+version, ghResp{status: 200, body: signRelease(testReleaseKey, version, exe)})
	g.set(update.LatestURL, ghResp{status: 200, body: ghRelease(version, exe)})
	g.set(relDL+"v"+version+"/jiraffe.exe.sha256", ghResp{status: 302, loc: "https://objects.githubusercontent.com/s" + version})
	g.set("https://objects.githubusercontent.com/s"+version, ghResp{status: 200, body: []byte(hex.EncodeToString(sum[:]) + "  jiraffe.exe\n")})
	g.set(relDL+"v"+version+"/jiraffe.exe", ghResp{status: 302, loc: "https://release-assets.githubusercontent.com/e" + version})
	g.set("https://release-assets.githubusercontent.com/e"+version, ghResp{status: 200, body: exe})
}

type updEnv struct {
	a       *App
	plat    *fakePlatform
	clock   *fakeClock
	gh      *fakeGitHub
	host    *fakeHost
	starter *fakeStarter
	rec     *recorder
	exe     string
}

// newUpdateApp is an app of version 0.4.0 with an updater on a temp dir.
// It swaps package globals (Version, update.PublicKey, cleanupRetry,
// keepOldFor; some tests also updateStartDelay/updateTick) and restores
// them in t.Cleanup: tests using it must not call t.Parallel.
func newUpdateApp(t *testing.T, fs update.FS) *updEnv {
	t.Helper()
	old, oldKey, oldRetry, oldKeep := Version, update.PublicKey, cleanupRetry, keepOldFor
	Version, update.PublicKey = "0.4.0", testReleaseKey.Public().(ed25519.PublicKey)
	cleanupRetry, keepOldFor = 20*time.Millisecond, 0
	t.Cleanup(func() { Version, update.PublicKey, cleanupRetry, keepOldFor = old, oldKey, oldRetry, oldKeep })
	dir := t.TempDir()
	cfg, err := config.Open(filepath.Join(dir, "config.json"), plainProtector{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := &updEnv{plat: &fakePlatform{t: &fakeJira{}}, clock: &fakeClock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, zone)},
		gh: &fakeGitHub{routes: map[string]ghResp{}}, host: &fakeHost{quit: make(chan struct{})}, starter: &fakeStarter{}, rec: &recorder{}}
	e.exe = filepath.Join(dir, "app", "jiraffe.exe")
	if err := os.MkdirAll(filepath.Dir(e.exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.exe, []byte("old exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if fs == nil {
		fs = update.OSFS{}
	}
	e.a = New(cfg, st, e.plat, e.clock, e.rec.emit)
	e.a.EnableUpdates(update.New(update.Config{Getter: e.gh, FS: fs, Starter: e.starter, Exe: e.exe,
		Dir: filepath.Join(dir, "local", "update"), Sleep: func(time.Duration) {}}), e.host)
	t.Cleanup(func() { e.a.Stop(5 * time.Second) })
	return e
}

func (e *updEnv) call(t *testing.T, method, params string) (any, error) {
	t.Helper()
	return e.a.Call(context.Background(), method, json.RawMessage(params))
}

func (e *updEnv) status(t *testing.T, method, params string) UpdateStatus {
	t.Helper()
	res, err := e.call(t, method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return res.(UpdateStatus)
}

// --- check and prompt ---------------------------------------------------------------

func TestUpdateCheckAndPrompt(t *testing.T) {
	e := newUpdateApp(t, nil)
	serveRelease(e.gh, "0.5.0", fakeExe(1000, 1))

	st := e.status(t, "getUpdateStatus", ``)
	if !st.Supported || st.CurrentVersion != "0.4.0" || st.Latest != nil || st.Available || st.Prompt || st.LastCheck != nil {
		t.Errorf("before any check %+v", st)
	}
	st = e.status(t, "checkForUpdates", ``)
	if !st.Available || !st.Prompt || st.Latest == nil || st.Latest.Version != "0.5.0" || st.Latest.Size != 1000 ||
		st.Latest.URL != "https://github.com/alexindacomp/Jiraffe/releases/tag/v0.5.0" || st.Latest.Notes != "## Changes\n- <b>bold</b> stays text" ||
		st.LastCheck == nil || !st.LastCheck.OK || st.LastCheck.At != "2026-11-11T10:00:00+02:00" {
		t.Errorf("after check %+v latest %+v check %+v", st, st.Latest, st.LastCheck)
	}
	var avail UpdateReleaseDTO
	if ev := e.rec.of("updateAvailable"); len(ev) != 1 || json.Unmarshal([]byte(ev[0]), &avail) != nil ||
		avail != (UpdateReleaseDTO{Version: "0.5.0", Notes: "## Changes\n- <b>bold</b> stays text", PublishedAt: "2026-11-10T12:00:00Z",
			Size: 1000, URL: "https://github.com/alexindacomp/Jiraffe/releases/tag/v0.5.0"}) ||
		!strings.Contains(ev[0], `"version":"0.5.0","notes":`) || !strings.Contains(ev[0], `"size":1000,"url":`) {
		t.Errorf("updateAvailable %v", ev)
	}
	// Window closed: one balloon per version and session.
	if len(e.plat.notices) != 1 || e.plat.notices[0].title != T("ru", "updAvailableTitle", "0.5.0") || e.plat.notices[0].url != "" {
		t.Errorf("balloons %+v", e.plat.notices)
	}
	e.status(t, "checkForUpdates", ``)
	if len(e.plat.notices) != 1 || len(e.rec.of("updateAvailable")) != 2 {
		t.Errorf("second check: %d balloons, %d events", len(e.plat.notices), len(e.rec.of("updateAvailable")))
	}
	if h := e.gh.headers[0]; len(h) != 1 || h["Accept"] != "application/vnd.github+json" {
		t.Errorf("API headers %v", h)
	}

	// Later: no more prompts for that version; the button still works.
	st = e.status(t, "skipUpdate", `{"version":"0.5.0"}`)
	if st.SkippedVersion != "0.5.0" || st.Prompt || !st.Available {
		t.Errorf("skipped %+v", st)
	}
	e.status(t, "checkForUpdates", ``)
	if len(e.rec.of("updateAvailable")) != 2 {
		t.Errorf("skipped version announced again")
	}
	// A newer one prompts again (window open: event, no balloon).
	e.host.open = true
	serveRelease(e.gh, "0.6.0", fakeExe(1200, 2))
	if st = e.status(t, "checkForUpdates", ``); !st.Prompt || st.Latest.Version != "0.6.0" || st.SkippedVersion != "0.5.0" {
		t.Errorf("newer than skipped %+v", st)
	}
	if len(e.rec.of("updateAvailable")) != 3 || len(e.plat.notices) != 1 {
		t.Errorf("window open: %d events, %d balloons", len(e.rec.of("updateAvailable")), len(e.plat.notices))
	}

	// A failed check keeps the known release and reports a localized error;
	// no balloon, no event.
	e.gh.set(update.LatestURL, ghResp{status: 403})
	e.clock.t = e.clock.t.Add(time.Hour)
	st = e.status(t, "checkForUpdates", ``)
	if st.LastCheck.OK || st.LastCheck.Error != T("ru", "upd_http_status")+" (HTTP 403 from https://api.github.com)" ||
		st.Latest == nil || st.Latest.Version != "0.6.0" || st.LastCheck.At != "2026-11-11T11:00:00+02:00" {
		t.Errorf("failed check %+v %+v", st.LastCheck, st.Latest)
	}
	if len(e.rec.of("updateAvailable")) != 3 || len(e.plat.notices) != 1 {
		t.Errorf("failed check announced something")
	}
	// The stored check survives a restart (persisted).
	if c := e.a.st.Snapshot().UpdateCheck; c == nil || c.Code != update.CodeHTTP || c.Latest.Version != "0.6.0" {
		t.Errorf("stored %+v", c)
	}
	for _, bad := range []string{`{}`, `{"version":"latest"}`, `{"version":1}`} {
		if _, err := e.call(t, "skipUpdate", bad); ToError(err).Code != CodeBadParams {
			t.Errorf("skipUpdate %s: %v", bad, err)
		}
	}
}

func TestUpdateBalloonRespectsPause(t *testing.T) {
	e := newUpdateApp(t, nil)
	e.a.SetPaused(true)
	serveRelease(e.gh, "0.5.0", fakeExe(10, 1))
	e.status(t, "checkForUpdates", ``)
	if len(e.plat.notices) != 0 || len(e.rec.of("updateAvailable")) != 1 {
		t.Errorf("paused: balloons %+v", e.plat.notices)
	}
}

func TestUpdateNoNewerVersion(t *testing.T) {
	e := newUpdateApp(t, nil)
	serveRelease(e.gh, "0.4.0", fakeExe(10, 1))
	if st := e.status(t, "checkForUpdates", ``); st.Available || st.Prompt || st.Latest.Version != "0.4.0" || !st.LastCheck.OK {
		t.Errorf("same version %+v", st)
	}
	if len(e.rec.of("updateAvailable")) != 0 || len(e.plat.notices) != 0 {
		t.Error("same version announced")
	}
	if _, err := e.call(t, "installUpdate", `{"version":"0.4.0"}`); ToError(err).Code != CodeUpdateStale {
		t.Errorf("install of the current version: %v", err)
	}
}

func TestUpdatesUnsupported(t *testing.T) {
	for _, v := range []string{"dev", "0.0.0"} {
		e := newUpdateApp(t, nil)
		Version = v
		serveRelease(e.gh, "0.5.0", fakeExe(10, 1))
		if st := e.status(t, "getUpdateStatus", ``); st.Supported || st.CurrentVersion != v {
			t.Errorf("%s: %+v", v, st)
		}
		for _, m := range []string{"checkForUpdates", "installUpdate"} {
			if _, err := e.call(t, m, `{"version":"0.5.0"}`); ToError(err).Code != CodeUpdateUnsupported {
				t.Errorf("%s %s: %v", v, m, err)
			}
		}
		if e.a.autoCheckDue(e.clock.t) || len(e.gh.calls) != 0 {
			t.Errorf("%s: auto check due or requests %v", v, e.gh.calls)
		}
	}
	// No updater at all (headless, tests).
	a, _, _ := newTestApp(t, &fakeJira{}, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	if _, err := a.Call(context.Background(), "checkForUpdates", nil); ToError(err).Code != CodeUpdateUnsupported {
		t.Errorf("no updater: %v", err)
	}
}

func TestUpdateAutoCheckSchedule(t *testing.T) {
	e := newUpdateApp(t, nil)
	serveRelease(e.gh, "0.4.0", fakeExe(10, 1))
	t0 := e.clock.t
	steps := []struct {
		at     time.Duration
		auto   bool
		checks int
	}{
		{0, true, 1},                 // first check of the session
		{time.Hour, true, 1},         // within a day: no
		{23 * time.Hour, true, 1},    // still within a day
		{24 * time.Hour, true, 2},    // a day later: again
		{30 * time.Hour, true, 2},    //
		{50 * time.Hour, false, 2},   // setting off: never
		{-time.Hour, true, 3},        // clock went back: check
		{-30 * time.Minute, true, 3}, // …but not again right away
	}
	for i, s := range steps {
		if !s.auto {
			cur := e.a.cfg.Get()
			cur.AutoCheckUpdates = false
			if _, _, err := e.a.cfg.Save(cur, nil); err != nil {
				t.Fatal(err)
			}
		} else if !e.a.cfg.Get().AutoCheckUpdates {
			cur := e.a.cfg.Get()
			cur.AutoCheckUpdates = true
			_, _, _ = e.a.cfg.Save(cur, nil)
		}
		e.clock.t = t0.Add(s.at)
		e.a.autoCheck(context.Background())
		if n := e.gh.count(update.LatestURL); n != s.checks {
			t.Errorf("step %d (%s): %d checks, want %d", i, s.at, n, s.checks)
		}
	}
	// A failed automatic check: no balloon, no event, result stored.
	e.gh.set(update.LatestURL, ghResp{status: 500})
	e.clock.t = t0.Add(100 * time.Hour)
	e.a.autoCheck(context.Background())
	if c := e.a.st.Snapshot().UpdateCheck; c == nil || c.OK || len(e.plat.notices) != 0 || len(e.rec.of("updateFailed")) != 0 {
		t.Errorf("failed auto check: %+v notices %+v", c, e.plat.notices)
	}
}

// --- install -------------------------------------------------------------------------

func TestInstallUpdate(t *testing.T) {
	for _, windowOpen := range []bool{false, true} {
		t.Run(fmt.Sprintf("window open %v", windowOpen), func(t *testing.T) {
			e := newUpdateApp(t, nil)
			e.host.open = windowOpen
			newExe := fakeExe(250_000, 7)
			serveRelease(e.gh, "0.5.0", newExe)
			e.status(t, "checkForUpdates", ``)
			if res, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); err != nil || res != nil {
				t.Fatalf("installUpdate %v %v", res, err)
			}
			select {
			case <-e.host.quit:
			case <-time.After(5 * time.Second):
				t.Fatalf("no quit; events %+v", e.rec.events)
			}
			if got, _ := os.ReadFile(e.exe); !bytes.Equal(got, newExe) {
				t.Errorf("exe not replaced (%d bytes)", len(got))
			}
			if got, _ := os.ReadFile(e.exe + ".old"); string(got) != "old exe" {
				t.Errorf("old exe %q", got)
			}
			want := fmt.Sprintf("--updated-from 0.4.0 --wait-pid %d", os.Getpid())
			if !windowOpen {
				want += " --minimized"
			}
			if e.starter.exe != e.exe || strings.Join(e.starter.args, " ") != want {
				t.Errorf("started %s %v, want %s", e.starter.exe, e.starter.args, want)
			}
			var phases []string
			for _, p := range e.rec.of("updateProgress") {
				var d UpdateProgressDTO
				_ = json.Unmarshal([]byte(p), &d)
				if len(phases) == 0 || phases[len(phases)-1] != d.Phase {
					phases = append(phases, d.Phase)
				}
			}
			if strings.Join(phases, ",") != "download,verify,install,restart" {
				t.Errorf("phases %v", phases)
			}
			prog := e.rec.of("updateProgress")
			if prog[0] != `{"phase":"download","received":0,"total":250000}` {
				t.Errorf("first progress %s", prog[0])
			}
			// The exe arrives in ~245 writes of 1 KB: progress is throttled to
			// at most one event per percent (plus the initial 0), received
			// grows strictly and ends at the total.
			var received []int64
			for _, p := range prog {
				var d UpdateProgressDTO
				if err := json.Unmarshal([]byte(p), &d); err != nil {
					t.Fatal(err)
				}
				if d.Phase != "download" {
					continue
				}
				if d.Received == nil || d.Total == nil || *d.Total != 250_000 {
					t.Fatalf("download progress %s", p)
				}
				if n := len(received); n > 0 && *d.Received <= received[n-1] {
					t.Errorf("received not increasing: %d after %d", *d.Received, received[n-1])
				}
				received = append(received, *d.Received)
			}
			if len(received) > 101 || len(received) < 50 || received[len(received)-1] != 250_000 {
				t.Errorf("download progress: %d events, last %d", len(received), received[len(received)-1])
			}
			if len(e.rec.of("updateFailed")) != 0 {
				t.Errorf("failed %v", e.rec.of("updateFailed"))
			}
			// The job stays "busy" until the process exits; no worklog run any more.
			if _, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); ToError(err).Code != CodeUpdateBusy {
				t.Errorf("second install %v", err)
			}
			if !e.status(t, "getUpdateStatus", ``).Installing {
				t.Error("status not installing")
			}
			if _, err := e.a.ManualWorklog(context.Background()); ToError(err).Code != CodeConflict ||
				ToError(err).Message != T("ru", "err_update_restarting") {
				t.Errorf("manual worklog during restart: %v", err)
			}
			if err := e.a.ScheduledWorklogAt(context.Background(), e.clock.t); err != nil || len(e.a.st.Snapshot().History) != 0 {
				t.Errorf("scheduled worklog during restart: %v, history %d", err, len(e.a.st.Snapshot().History))
			}
		})
	}
}

func TestInstallUpdateFailures(t *testing.T) {
	good := fakeExe(5000, 3)
	tests := []struct {
		name     string
		fs       update.FS
		serve    func(g *fakeGitHub)
		startErr error
		code     string
	}{
		{"hash mismatch", nil, func(g *fakeGitHub) {
			g.set("https://release-assets.githubusercontent.com/e0.5.0", ghResp{status: 200, body: fakeExe(5000, 4)})
		}, nil, update.CodeHashMismatch},
		{"truncated download", nil, func(g *fakeGitHub) {
			g.set("https://release-assets.githubusercontent.com/e0.5.0", ghResp{status: 200, body: good[:4000]})
		}, nil, update.CodeSizeMismatch},
		{"redirect off the allow-list", nil, func(g *fakeGitHub) {
			g.set(relDL+"v0.5.0/jiraffe.exe", ghResp{status: 302, loc: "https://evil.example/jiraffe.exe"})
		}, nil, update.CodeRedirect},
		{"asset gone", nil, func(g *fakeGitHub) {
			g.set("https://release-assets.githubusercontent.com/e0.5.0", ghResp{status: 404})
		}, nil, update.CodeHTTP},
		{"exe folder read-only", failFS{}, nil, nil, update.CodeNoWriteAccess},
		{"signed with another key", nil, func(g *fakeGitHub) {
			other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize))
			g.set("https://objects.githubusercontent.com/g0.5.0", ghResp{status: 200, body: signRelease(other, "0.5.0", good)})
		}, nil, update.CodeBadSignature},
		{"exe and checksum replaced, signature kept", nil, func(g *fakeGitHub) {
			evil := fakeExe(5000, 6)
			sum := sha256.Sum256(evil)
			g.set("https://release-assets.githubusercontent.com/e0.5.0", ghResp{status: 200, body: evil})
			g.set("https://objects.githubusercontent.com/s0.5.0", ghResp{status: 200, body: []byte(hex.EncodeToString(sum[:]) + "  jiraffe.exe\n")})
		}, nil, update.CodeBadSignature},
		{"signature asset unreachable", nil, func(g *fakeGitHub) {
			g.set("https://objects.githubusercontent.com/g0.5.0", ghResp{status: 404})
		}, nil, update.CodeBadSignature},
		{"signature asset removed from the release", nil, func(g *fakeGitHub) {
			var m map[string]any
			_ = json.Unmarshal(ghRelease("0.5.0", good), &m)
			m["assets"] = m["assets"].([]any)[:2]
			b, _ := json.Marshal(m)
			g.set(update.LatestURL, ghResp{status: 200, body: b})
		}, nil, update.CodeBadRelease},
		{"another version published meanwhile", nil, func(g *fakeGitHub) {
			serveRelease(g, "0.6.0", fakeExe(5000, 5))
		}, nil, update.CodeChanged},
		{"new exe does not start: rolled back", nil, nil, fmt.Errorf("CreateProcess: access denied"), update.CodeStart},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newUpdateApp(t, tc.fs)
			e.host.open = true
			serveRelease(e.gh, "0.5.0", good)
			e.status(t, "checkForUpdates", ``)
			if tc.serve != nil {
				tc.serve(e.gh)
			}
			e.starter.err = tc.startErr
			var jobAtFail atomic.Bool
			jobAtFail.Store(true)
			e.rec.onEmit = func(typ string) {
				if typ == "updateFailed" {
					jobAtFail.Store(e.a.updJob.Load())
				}
			}
			if _, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); err != nil {
				t.Fatal(err)
			}
			ev := e.rec.wait(t, "updateFailed")
			if jobAtFail.Load() {
				t.Error("updJob still set when updateFailed was emitted")
			}
			var f UpdateFailedDTO
			_ = json.Unmarshal([]byte(ev[0]), &f)
			if f.Code != tc.code || f.Message == "" || !strings.HasPrefix(f.Message, T("ru", "upd_"+tc.code)) {
				t.Errorf("updateFailed %+v, want code %s", f, tc.code)
			}
			// The current version stays in place and keeps running.
			if got, _ := os.ReadFile(e.exe); string(got) != "old exe" {
				t.Errorf("exe changed (%d bytes)", len(got))
			}
			select {
			case <-e.host.quit:
				t.Error("quit after a failure")
			default:
			}
			waitFor(t, func() bool { return !e.a.updJob.Load() }, "the job to finish")
			if e.a.updExiting.Load() {
				t.Error("job not finished")
			}
			parts, _ := filepath.Glob(filepath.Join(filepath.Dir(filepath.Dir(e.exe)), "local", "update", "*.part"))
			if tc.code != update.CodeStart && len(parts) != 0 {
				t.Errorf("partial download kept: %v", parts)
			}
			// Worklog runs again.
			if _, err := e.a.ManualWorklog(context.Background()); ToError(err).Code == CodeConflict {
				t.Errorf("worklog still blocked: %v", err)
			}
		})
	}
}

func TestInstallUpdateParams(t *testing.T) {
	e := newUpdateApp(t, nil)
	if _, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); ToError(err).Code != CodeUpdateStale {
		t.Errorf("before any check: %v", err)
	}
	serveRelease(e.gh, "0.5.0", fakeExe(10, 1))
	e.status(t, "checkForUpdates", ``)
	for params, code := range map[string]string{
		`{}`: CodeBadParams, `{"version":"v"}`: CodeBadParams, `{"version":5}`: CodeBadParams,
		`{"version":"0.6.0"}`: CodeUpdateStale, `{"version":"0.3.0"}`: CodeUpdateStale,
	} {
		if _, err := e.call(t, "installUpdate", params); ToError(err).Code != code {
			t.Errorf("%s: %v, want %s", params, err, code)
		}
	}
	e.a.updJob.Store(true)
	if _, err := e.call(t, "installUpdate", `{"version":"v0.5.0"}`); ToError(err).Code != CodeUpdateBusy {
		t.Errorf("busy: %v", err)
	}
	e.a.updJob.Store(false)
}

// The swap waits for a running worklog (runMu) and happens after it.
func TestInstallWaitsForWorklog(t *testing.T) {
	e := newUpdateApp(t, nil)
	newExe := fakeExe(3000, 9)
	serveRelease(e.gh, "0.5.0", newExe)
	e.status(t, "checkForUpdates", ``)
	e.a.runMu.Lock() // a worklog run in progress
	if _, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); err != nil {
		t.Fatal(err)
	}
	waitInstallBlockedOnLock(t, e)
	if got, _ := os.ReadFile(e.exe); string(got) != "old exe" || e.starter.exe != "" {
		t.Fatal("swapped during a worklog run")
	}
	e.a.runMu.Unlock()
	select {
	case <-e.host.quit:
	case <-time.After(5 * time.Second):
		t.Fatal("no quit after the worklog run finished")
	}
	if got, _ := os.ReadFile(e.exe); !bytes.Equal(got, newExe) {
		t.Error("not swapped")
	}
}

func TestUpdatedFromAndCleanup(t *testing.T) {
	e := newUpdateApp(t, nil)
	_ = e.a.st.Update(func(tx state.Tx) { tx.SkippedVersion = "0.4.0" })
	for _, f := range []string{e.exe + ".old", e.exe + ".new"} {
		_ = os.WriteFile(f, []byte("x"), 0o644)
	}
	e.a.UpdatedFrom("0.3.2")
	st := e.status(t, "getUpdateStatus", ``)
	if st.JustUpdated == nil || *st.JustUpdated != (UpdatedDTO{From: "0.3.2", To: "0.4.0"}) || st.SkippedVersion != "" {
		t.Errorf("status %+v", st)
	}
	if ev := e.rec.of("updated"); len(ev) != 1 || ev[0] != `{"from":"0.3.2","to":"0.4.0"}` {
		t.Errorf("updated event %v", ev)
	}
	if len(e.plat.notices) != 1 || e.plat.notices[0].text != T("ru", "updDoneText", "0.4.0") {
		t.Errorf("balloon %+v", e.plat.notices)
	}
	e.a.cleanupUpdates(context.Background())
	for _, f := range []string{e.exe + ".old", e.exe + ".new"} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s not removed", f)
		}
	}
	// Same version (restart without an update): nothing.
	e2 := newUpdateApp(t, nil)
	e2.a.UpdatedFrom("0.4.0")
	if e2.status(t, "getUpdateStatus", ``).JustUpdated != nil || len(e2.plat.notices) != 0 {
		t.Error("UpdatedFrom the same version")
	}
}

func TestOpenURLReleasePageOnly(t *testing.T) {
	a, plat, _ := newTestApp(t, &fakeJira{}, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	for _, ok := range []string{"https://github.com/alexindacomp/Jiraffe/releases/tag/v0.5.0", "https://github.com/alexindacomp/Jiraffe/releases/latest"} {
		if _, err := a.Call(context.Background(), "openUrl", json.RawMessage(`{"url":"`+ok+`"}`)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	if len(plat.opened) != 2 {
		t.Errorf("opened %v", plat.opened)
	}
	for _, bad := range []string{
		"https://github.com/alexindacomp/Jiraffe", "https://github.com/alexindacomp/Jiraffe/issues/1",
		"https://github.com/evil/repo/releases/tag/v1", "http://github.com/alexindacomp/Jiraffe/releases/tag/v0.5.0",
		"https://github.com/alexindacomp/Jiraffe/releases/../../evil", "https://api.github.com/repos/alexindacomp/Jiraffe/releases/latest",
		"https://github.com/alexindacomp/Jiraffe/releases/download/v0.5.0/jiraffe.exe", "https://github.com/alexindacomp/Jiraffe/releases/tag/v0.5.0/x",
	} {
		if _, err := a.Call(context.Background(), "openUrl", json.RawMessage(`{"url":"`+bad+`"}`)); ToError(err).Code != CodeForbiddenURL {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func TestAutoCheckUpdatesSetting(t *testing.T) {
	a, _, _ := newTestApp(t, &fakeJira{}, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	res, _ := a.Call(context.Background(), "getSettings", nil)
	if b, _ := json.Marshal(res); !strings.Contains(string(b), `"autoCheckUpdates":true`) {
		t.Errorf("default: %s", b)
	}
	if _, err := a.Call(context.Background(), "saveSettings", json.RawMessage(`{"autoCheckUpdates":false}`)); err != nil {
		t.Fatal(err)
	}
	if a.cfg.Get().AutoCheckUpdates {
		t.Error("not saved")
	}
	if _, err := a.Call(context.Background(), "saveSettings", json.RawMessage(`{"autoCheckUpdates":"yes"}`)); ToError(err).Code != CodeBadParams {
		t.Errorf("non-bool: %v", err)
	}
}

// S-1: the install reads the release again and never uses the stored one;
// stored releases are re-validated before display.
func TestInstallUsesFreshRelease(t *testing.T) {
	e := newUpdateApp(t, nil)
	newExe := fakeExe(4000, 2)
	serveRelease(e.gh, "0.5.0", newExe)
	e.status(t, "checkForUpdates", ``)
	// Tamper with the stored release: a valid shape but a wrong size and notes.
	_ = e.a.st.Update(func(tx state.Tx) {
		tx.UpdateCheck.Latest.Exe.Size = 999
		tx.UpdateCheck.Latest.Notes = "a\r\nb\x00"
		tx.UpdateCheck.Latest.Page = "https://evil.example/"
	})
	if st := e.status(t, "getUpdateStatus", ``); st.Latest == nil || st.Latest.URL != "https://github.com/alexindacomp/Jiraffe/releases/tag/v0.5.0" ||
		st.Latest.Notes != "a\nb" {
		t.Errorf("status of a tampered release %+v", st.Latest)
	}
	before := e.gh.count(update.LatestURL)
	if _, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.host.quit:
	case <-time.After(5 * time.Second):
		t.Fatalf("no quit; events %+v", e.rec.events)
	}
	if got, _ := os.ReadFile(e.exe); !bytes.Equal(got, newExe) || e.gh.count(update.LatestURL) != before+1 {
		t.Errorf("installed %d bytes after %d fresh checks", len(got), e.gh.count(update.LatestURL)-before)
	}
	// A stored release that does not validate is neither shown nor installable.
	e2 := newUpdateApp(t, nil)
	serveRelease(e2.gh, "0.5.0", newExe)
	e2.status(t, "checkForUpdates", ``)
	_ = e2.a.st.Update(func(tx state.Tx) { tx.UpdateCheck.Latest.Exe.URL = "https://evil.example/jiraffe.exe" })
	if st := e2.status(t, "getUpdateStatus", ``); st.Latest != nil || st.Available || st.Prompt {
		t.Errorf("invalid stored release shown: %+v", st)
	}
	if _, err := e2.call(t, "installUpdate", `{"version":"0.5.0"}`); ToError(err).Code != CodeUpdateStale {
		t.Errorf("install of an invalid stored release: %v", err)
	}
}

// cancelFS cancels the app context when exe.new is moved into place (the
// user quits between the swap and the restart).
type cancelFS struct {
	update.OSFS
	cancel func()
}

func (f cancelFS) Rename(from, to string) error {
	err := f.OSFS.Rename(from, to)
	if strings.HasSuffix(from, ".new") {
		f.cancel()
	}
	return err
}

// CR-6: quitting during the install never restarts; a done swap is undone.
func TestInstallCancelledByQuit(t *testing.T) {
	for _, when := range []string{"waiting for a worklog", "after the swap"} {
		t.Run(when, func(t *testing.T) {
			var e *updEnv
			if when == "after the swap" {
				var a *App
				e = newUpdateApp(t, cancelFS{cancel: func() { a.bgCancel() }})
				a = e.a
			} else {
				e = newUpdateApp(t, nil)
			}
			serveRelease(e.gh, "0.5.0", fakeExe(3000, 1))
			e.status(t, "checkForUpdates", ``)
			if when == "waiting for a worklog" {
				e.a.runMu.Lock()
			}
			if _, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); err != nil {
				t.Fatal(err)
			}
			if when == "waiting for a worklog" {
				waitInstallBlockedOnLock(t, e)
				e.a.bgCancel() // the user quits
				e.a.runMu.Unlock()
			}
			ev := e.rec.wait(t, "updateFailed")
			if !strings.Contains(ev[0], `"code":"cancelled"`) {
				t.Errorf("updateFailed %v", ev)
			}
			if got, _ := os.ReadFile(e.exe); string(got) != "old exe" || e.starter.exe != "" {
				t.Errorf("exe %q, started %q", got, e.starter.exe)
			}
			select {
			case <-e.host.quit:
				t.Error("quit by the updater")
			default:
			}
		})
	}
}

// CR-7: the updater's own timeout is reported as a network timeout.
func TestUpdateTimeoutMessage(t *testing.T) {
	e := newUpdateApp(t, nil)
	serveRelease(e.gh, "0.5.0", fakeExe(10, 1))
	e.status(t, "checkForUpdates", ``)
	e.a.EnableUpdates(update.New(update.Config{Getter: blockingGitHub{}, FS: update.OSFS{}, Starter: e.starter, Exe: e.exe,
		Dir: t.TempDir(), CheckTimeout: 20 * time.Millisecond, DownloadTimeout: 20 * time.Millisecond}), e.host)
	st := e.status(t, "checkForUpdates", ``)
	if st.LastCheck.OK || st.LastCheck.Error != T("ru", "upd_net_timeout") {
		t.Errorf("check %+v", st.LastCheck)
	}
	if _, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); err != nil {
		t.Fatal(err)
	}
	var f UpdateFailedDTO
	_ = json.Unmarshal([]byte(e.rec.wait(t, "updateFailed")[0]), &f)
	if f.Code != update.CodeNetwork || f.Message != T("ru", "upd_net_timeout") {
		t.Errorf("updateFailed %+v", f)
	}
}

type blockingGitHub struct{}

func (blockingGitHub) Get(ctx context.Context, _ string, _ map[string]string, _ io.Writer, _ int64) (int, string, error) {
	<-ctx.Done()
	return 0, "", ctx.Err()
}

// S-5 / CR-9: a cleanup pass is skipped while an install job owns the
// update files; it retries cleanupRetries times in all and stops early when
// the app exits.
func TestCleanupSkipsWhileInstalling(t *testing.T) {
	e := newUpdateApp(t, nil)
	cleanupRetry = time.Millisecond
	tests := []struct {
		name      string
		locked    bool
		failures  int // fn fails this many times, then succeeds (-1 = always)
		cancelled bool
		calls     int
	}{
		{"install owns the files: never touched", true, 0, false, 0},
		{"locked file retried until it succeeds", false, 2, false, 3},
		{"locked for good: gives up after cleanupRetries", false, -1, false, cleanupRetries},
		{"app exits: no retry", false, -1, true, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			fn := func() error {
				calls++
				if tc.failures < 0 || calls <= tc.failures {
					return errors.New("locked")
				}
				return nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			if tc.locked {
				e.a.updFilesMu.Lock()
			}
			e.a.retryCleanup(ctx, "test", fn) // returns: the attempts are bounded
			if tc.locked {
				e.a.updFilesMu.Unlock()
			}
			if calls != tc.calls {
				t.Errorf("fn called %d times, want %d", calls, tc.calls)
			}
		})
	}
}

// S-5: stale downloads and exe.new go at once; exe.old stays for
// keepOldFor (a manual rollback is still possible), also when the app exits
// earlier, and is removed after it.
func TestCleanupKeepsOldForAWhile(t *testing.T) {
	e := newUpdateApp(t, nil)
	part := filepath.Join(filepath.Dir(filepath.Dir(e.exe)), "local", "update", "jiraffe-0.5.0.exe.part")
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{e.exe + ".old", e.exe + ".new", part} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(f string) bool { _, err := os.Stat(f); return err == nil }

	keepOldFor = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.a.cleanupUpdates(ctx); close(done) }()
	waitFor(t, func() bool { return !exists(part) && !exists(e.exe+".new") }, "the downloads to be removed")
	cancel() // the app exits long before keepOldFor
	waitFor(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, "cleanup to stop at exit")
	if !exists(e.exe + ".old") {
		t.Error("exe.old removed before keepOldFor")
	}

	keepOldFor = 0
	e.a.cleanupUpdates(context.Background())
	if exists(e.exe + ".old") {
		t.Error("exe.old kept after keepOldFor")
	}
}

// CR-11: a new install that starts while the failed one is still finishing
// keeps its job flag.
func TestInstallRetryAfterFailureKeepsJobFlag(t *testing.T) {
	e := newUpdateApp(t, nil)
	good := fakeExe(3000, 1)
	serveRelease(e.gh, "0.5.0", good)
	e.status(t, "checkForUpdates", ``)
	e.gh.set("https://release-assets.githubusercontent.com/e0.5.0", ghResp{status: 200, body: fakeExe(3000, 2)}) // hash mismatch
	e.a.runMu.Lock()                                                                                             // keeps the second job before the swap
	var retryErr atomic.Value
	var once sync.Once
	e.rec.onEmit = func(typ string) {
		if typ == "updateFailed" {
			once.Do(func() {
				serveRelease(e.gh, "0.5.0", good)
				_, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`)
				retryErr.Store(fmt.Sprint(err))
			})
		}
	}
	if _, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return retryErr.Load() != nil }, "the retry from the updateFailed handler")
	if got := retryErr.Load(); got != "<nil>" {
		t.Fatalf("retry from the updateFailed handler: %v", got)
	}
	// The first job has fully returned (its deferred code included) once
	// the only install goroutine left is the second one, parked on runMu.
	waitFor(t, func() bool {
		total, blocked := goroutinesIn(".runInstall(")
		return total == 1 && blocked == 1
	}, "the first job to return and the second to wait for the worklog lock")
	if !e.a.updJob.Load() {
		t.Error("the first job cleared the second job's flag")
	}
	if _, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); ToError(err).Code != CodeUpdateBusy {
		t.Errorf("third install: %v", err)
	}
	e.a.runMu.Unlock()
	select {
	case <-e.host.quit:
	case <-time.After(5 * time.Second):
		t.Fatal("second job did not finish")
	}
}

// rollbackFailFS makes the rollback's first rename fail.
type rollbackFailFS struct{ update.OSFS }

func (f rollbackFailFS) Rename(from, to string) error {
	if filepath.Base(from) == "jiraffe.exe" && filepath.Base(to) == "jiraffe.exe.new" {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
	}
	return f.OSFS.Rename(from, to)
}

// CR-12: a failed rollback is reported, naming the previous exe.
func TestRollbackFailureReported(t *testing.T) {
	e := newUpdateApp(t, rollbackFailFS{})
	serveRelease(e.gh, "0.5.0", fakeExe(3000, 1))
	e.status(t, "checkForUpdates", ``)
	e.starter.err = fmt.Errorf("CreateProcess failed")
	if _, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); err != nil {
		t.Fatal(err)
	}
	var f UpdateFailedDTO
	_ = json.Unmarshal([]byte(e.rec.wait(t, "updateFailed")[0]), &f)
	if f.Code != update.CodeInstall || f.Message != T("ru", "upd_rollback_failed", e.exe+".old") {
		t.Errorf("updateFailed %+v", f)
	}
}

// blockingRenameFS blocks the rename of the running exe until released.
type blockingRenameFS struct {
	update.OSFS
	entered chan struct{}
	release chan struct{}
}

func (f *blockingRenameFS) Rename(from, to string) error {
	if strings.HasSuffix(to, ".old") && filepath.Base(from) == "jiraffe.exe" {
		close(f.entered)
		<-f.release
	}
	return f.OSFS.Rename(from, to)
}

// CR-13: Stop waits for a swap in progress beyond its timeout; the swap is
// completed and, since the app is quitting, rolled back — never left half done.
func TestStopWaitsForSwap(t *testing.T) {
	fs := &blockingRenameFS{entered: make(chan struct{}), release: make(chan struct{})}
	e := newUpdateApp(t, fs)
	serveRelease(e.gh, "0.5.0", fakeExe(3000, 1))
	e.status(t, "checkForUpdates", ``)
	if _, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fs.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("swap not reached")
	}
	stopped := make(chan struct{})
	go func() { e.a.Stop(10 * time.Millisecond); close(stopped) }()
	// Stop is parked on swapMu (the swap holds it): it cannot have returned.
	waitFor(t, func() bool { _, blocked := goroutinesIn(".(*App).Stop("); return blocked == 1 }, "Stop to wait for the swap")
	select {
	case <-stopped:
		t.Fatal("Stop returned during the swap")
	default:
	}
	close(fs.release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the swap")
	}
	if got, _ := os.ReadFile(e.exe); string(got) != "old exe" || e.starter.exe != "" {
		t.Errorf("exe %q, started %q", got, e.starter.exe)
	}
	if _, err := os.Stat(e.exe + ".new"); !os.IsNotExist(err) {
		t.Errorf("exe.new left: %v", err)
	}
}

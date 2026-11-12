package app

// QA tests for v0.4: i18n completeness of the new messages, the JSON
// contract of the update methods and events, localization of update
// failures, installing a skipped version, epic time read in key batches,
// and PlainText edge cases.

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"jiraffe/internal/jira"
	"jiraffe/internal/state"
	"jiraffe/internal/update"
)

// --- i18n -------------------------------------------------------------------------

// Every key passed as a literal to T(…) or newErr(…) in the app's sources
// exists in English (and so, by TestTextsCompleteInAllLanguages, in ru and
// uk). A typo would otherwise show the raw key to the user.
func TestV4QATextKeysUsedInCodeExist(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	used := map[string]string{} // key → position
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			name := ""
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				name = fn.Name
			case *ast.SelectorExpr:
				name = fn.Sel.Name
			}
			if name != "T" && name != "newErr" {
				return true
			}
			if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				key, _ := strconv.Unquote(lit.Value)
				used[key] = fset.Position(lit.Pos()).String()
			}
			return true
		})
	}
	if len(used) < 20 {
		t.Fatalf("only %d keys found: the scan is broken", len(used))
	}
	for _, k := range []string{"updAvailableTitle", "updDoneText", "err_update_busy", "err_update_restarting"} {
		if _, ok := used[k]; !ok {
			t.Errorf("scan missed the v0.4 key %q", k)
		}
	}
	for key, pos := range used {
		if !hasText(key) {
			t.Errorf("%s: text key %q does not exist", pos, key)
		}
	}
}

// Every updateFailed / check error code of SPEC v0.4 has its own message in
// every language (updateMessage falls back to "could not be installed"
// silently otherwise).
func TestV4QAUpdateCodesHaveMessages(t *testing.T) {
	codes := []string{update.CodeNetwork, update.CodeHTTP, update.CodeRedirect, update.CodeTooLarge, update.CodeBadRelease,
		update.CodeBadChecksum, update.CodeSizeMismatch, update.CodeHashMismatch, update.CodeNotExe, update.CodeBadSignature,
		update.CodeChanged, update.CodeNoWriteAccess, update.CodeInstall, update.CodeStart, update.CodeFile, update.CodeCancelled}
	// The codes are exactly the SPEC's list.
	spec := "network http_status bad_redirect too_large bad_release bad_checksum size_mismatch hash_mismatch not_exe " +
		"bad_signature release_changed no_write_access install_failed start_failed file_error cancelled"
	if got := strings.Join(codes, " "); got != spec {
		t.Errorf("codes %q\nspec  %q", got, spec)
	}
	for _, lang := range []string{"en", "ru", "uk"} {
		seen := map[string]string{}
		for _, c := range codes {
			msg, ok := texts[lang]["upd_"+c]
			if !ok || strings.TrimSpace(msg) == "" {
				t.Errorf("%s: no message for code %s", lang, c)
				continue
			}
			if got := updateMessage(lang, c, ""); got != msg {
				t.Errorf("%s/%s: updateMessage %q, want %q", lang, c, got, msg)
			}
			if other, dup := seen[msg]; dup && !(c == update.CodeNetwork || other == update.CodeNetwork) {
				t.Errorf("%s: codes %s and %s share the message %q", lang, other, c, msg)
			}
			seen[msg] = c
		}
	}
}

var fmtVerb = regexp.MustCompile(`%[-+# 0]*[0-9]*(?:\.[0-9]+)?[a-zA-Z]`)

// The translations take the same format arguments as English (a missing
// %s would print "%!(EXTRA …)"), and have no keys English lacks.
func TestV4QATextsFormatVerbsMatch(t *testing.T) {
	for key, en := range texts["en"] {
		want := fmtVerb.FindAllString(strings.ReplaceAll(en, "%%", ""), -1)
		for _, lang := range []string{"ru", "uk"} {
			tr, ok := texts[lang][key]
			if !ok {
				continue // reported by TestTextsCompleteInAllLanguages
			}
			got := fmtVerb.FindAllString(strings.ReplaceAll(tr, "%%", ""), -1)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s/%s: verbs %v, English has %v", lang, key, got, want)
			}
		}
	}
	for _, lang := range []string{"ru", "uk"} {
		for key := range texts[lang] {
			if _, ok := texts["en"][key]; !ok {
				t.Errorf("%s has %q, English does not", lang, key)
			}
		}
	}
}

// --- update failures: localized messages -------------------------------------------

// errGitHub fails every request with err (a transport failure).
type errGitHub struct{ err error }

func (g errGitHub) Get(context.Context, string, map[string]string, io.Writer, int64) (int, string, error) {
	return 0, "", g.err
}

// A failed check stores the code and English detail; the message is
// localized when the status is read, in the current language, and maps
// WinHTTP failure kinds to user-facing texts.
func TestV4QAUpdateCheckErrorMessages(t *testing.T) {
	tests := []struct {
		name string
		err  error
		key  string
	}{
		{"untrusted certificate", &jira.TransportError{Kind: jira.NetTLSUntrust, Message: "WinHTTP 12175"}, "upd_net_tls"},
		{"generic TLS", &jira.TransportError{Kind: jira.NetTLS, Message: "WinHTTP 12157"}, "upd_net_tls"},
		{"DNS", &jira.TransportError{Kind: jira.NetDNS, Message: "WinHTTP 12007"}, "upd_net_connect"},
		{"connect", &jira.TransportError{Kind: jira.NetConnect, Message: "WinHTTP 12029"}, "upd_net_connect"},
		{"proxy", &jira.TransportError{Kind: jira.NetProxy, Message: "WinHTTP 12180"}, "upd_net_connect"},
		{"transport timeout", &jira.TransportError{Kind: jira.NetTimeout, Message: "WinHTTP 12002"}, "upd_net_timeout"},
		{"reset", &jira.TransportError{Kind: jira.NetReset, Message: "WinHTTP 12030"}, "upd_net_other"},
		{"not a transport error", errors.New("boom"), "upd_network"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newUpdateApp(t, nil)
			e.a.EnableUpdates(update.New(update.Config{Getter: errGitHub{tc.err}, FS: update.OSFS{}, Starter: e.starter,
				Exe: e.exe, Dir: t.TempDir()}), e.host)

			st := e.status(t, "checkForUpdates", ``)

			if st.LastCheck == nil || st.LastCheck.OK || st.LastCheck.Error != T("ru", tc.key) {
				t.Errorf("ru: %+v, want %q", st.LastCheck, T("ru", tc.key))
			}
			if c := e.a.st.Snapshot().UpdateCheck; c == nil || c.Code != update.CodeNetwork {
				t.Errorf("stored %+v", c)
			}
			// Switching the language re-localizes the stored failure.
			cur := e.a.cfg.Get()
			cur.Language = "en"
			if _, _, err := e.a.cfg.Save(cur, nil); err != nil {
				t.Fatal(err)
			}
			if got := e.status(t, "getUpdateStatus", ``).LastCheck.Error; got != T("en", tc.key) {
				t.Errorf("en: %q, want %q", got, T("en", tc.key))
			}
			if len(e.plat.notices) != 0 || len(e.rec.of("updateFailed")) != 0 {
				t.Error("a failed check showed a balloon or an updateFailed event")
			}
		})
	}
}

// updateMessage: details are appended only for the codes whose detail
// helps (HTTP status, redirects, release/checksum problems, install
// errors); verification failures never show the English detail.
func TestV4QAUpdateMessageDetails(t *testing.T) {
	for _, tc := range []struct {
		code, detail string
		want         string
	}{
		{update.CodeHTTP, "HTTP 503 from https://api.github.com", T("en", "upd_http_status") + " (HTTP 503 from https://api.github.com)"},
		{update.CodeBadRelease, "release 0.5.0 lacks jiraffe.exe.sig", T("en", "upd_bad_release") + " (release 0.5.0 lacks jiraffe.exe.sig)"},
		{update.CodeHashMismatch, "the copy differs", T("en", "upd_hash_mismatch")},
		{update.CodeBadSignature, "the signature does not match", T("en", "upd_bad_signature")},
		{update.CodeSizeMismatch, "4000 bytes, expected 5000", T("en", "upd_size_mismatch")},
		{update.CodeInstall, "", T("en", "upd_install_failed")},
		{"some_future_code", "x", T("en", "upd_install_failed")},
		{update.CodeInstall, "rollback:C:\\Apps\\jiraffe.exe.old", T("en", "upd_rollback_failed", "C:\\Apps\\jiraffe.exe.old")},
	} {
		if got := updateMessage("en", tc.code, tc.detail); got != tc.want {
			t.Errorf("%s %q: %q, want %q", tc.code, tc.detail, got, tc.want)
		}
	}
	// Errors from outside the update package.
	if c := updateCode(context.Canceled); c != update.CodeCancelled {
		t.Errorf("context.Canceled: %s", c)
	}
	if c := updateCode(errors.New("x")); c != update.CodeInstall {
		t.Errorf("foreign error: %s", c)
	}
	if d := updateDetail(&jira.TransportError{Kind: jira.NetDNS, Message: "m"}); d != "net:dns" {
		t.Errorf("transport detail %q", d)
	}
}

// --- update contract -----------------------------------------------------------------

func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	var k []string
	for key := range m {
		k = append(k, key)
	}
	sort.Strings(k)
	return k
}

// UpdateStatus JSON: required keys always present, optional ones omitted
// when unknown; getUpdateStatus never touches the network.
func TestV4QAUpdateStatusJSONShape(t *testing.T) {
	e := newUpdateApp(t, nil)
	res, err := e.call(t, "getUpdateStatus", ``)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(jsonKeys(t, res), ","); got != "available,currentVersion,installing,prompt,supported" {
		t.Errorf("fresh status keys %s", got)
	}
	if len(e.gh.calls) != 0 {
		t.Errorf("getUpdateStatus made requests %v", e.gh.calls)
	}

	serveRelease(e.gh, "0.5.0", fakeExe(1000, 1))
	st := e.status(t, "checkForUpdates", ``)
	if got := strings.Join(jsonKeys(t, st), ","); got != "available,currentVersion,installing,lastCheck,latest,prompt,supported" {
		t.Errorf("after a check %s", got)
	}
	if got := strings.Join(jsonKeys(t, st.Latest), ","); got != "notes,publishedAt,size,url,version" {
		t.Errorf("latest keys %s", got)
	}
	if got := strings.Join(jsonKeys(t, st.LastCheck), ","); got != "at,ok" {
		t.Errorf("ok lastCheck keys %s", got)
	}
	if _, err := time.Parse(time.RFC3339, st.LastCheck.At); err != nil {
		t.Errorf("lastCheck.at %q: %v", st.LastCheck.At, err)
	}
	st = e.status(t, "skipUpdate", `{"version":"v0.5.0"}`)
	if st.SkippedVersion != "0.5.0" || !strings.Contains(strings.Join(jsonKeys(t, st), ","), "skippedVersion") {
		t.Errorf("skip with a leading v: %+v", st)
	}
	e.gh.set(update.LatestURL, ghResp{status: 502})
	st = e.status(t, "checkForUpdates", ``)
	if got := strings.Join(jsonKeys(t, st.LastCheck), ","); got != "at,error,ok" {
		t.Errorf("failed lastCheck keys %s", got)
	}
	e.a.UpdatedFrom("0.3.0")
	if got := jsonKeys(t, e.status(t, "getUpdateStatus", ``)); !strings.Contains(strings.Join(got, ","), "justUpdated") {
		t.Errorf("justUpdated missing: %v", got)
	}
}

// updateProgress carries received/total only in the download phase;
// updateFailed is exactly { code, message }.
func TestV4QAUpdateEventPayloads(t *testing.T) {
	e := newUpdateApp(t, nil)
	serveRelease(e.gh, "0.5.0", fakeExe(2000, 1))
	e.status(t, "checkForUpdates", ``)
	if _, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.host.quit:
	case <-time.After(5 * time.Second):
		t.Fatalf("no quit; events %+v", e.rec.events)
	}
	for _, p := range e.rec.of("updateProgress") {
		var m map[string]any
		_ = json.Unmarshal([]byte(p), &m)
		switch m["phase"] {
		case "download":
			if _, ok := m["received"]; !ok || m["total"] != float64(2000) || len(m) != 3 {
				t.Errorf("download progress %s", p)
			}
		case "verify", "install", "restart":
			if len(m) != 1 {
				t.Errorf("non-download progress with extra fields: %s", p)
			}
		default:
			t.Errorf("unknown phase %s", p)
		}
	}

	f := newUpdateApp(t, nil)
	serveRelease(f.gh, "0.5.0", fakeExe(2000, 1))
	f.status(t, "checkForUpdates", ``)
	f.gh.set("https://release-assets.githubusercontent.com/e0.5.0", ghResp{status: 200, body: []byte("PK not an exe")})
	if _, err := f.call(t, "installUpdate", `{"version":"0.5.0"}`); err != nil {
		t.Fatal(err)
	}
	ev := f.rec.wait(t, "updateFailed")
	var m map[string]string
	if err := json.Unmarshal([]byte(ev[0]), &m); err != nil || len(m) != 2 || m["code"] != update.CodeSizeMismatch || m["message"] != T("ru", "upd_size_mismatch") {
		t.Errorf("updateFailed %s", ev[0])
	}
}

// "Update to X.Y.Z" works also for the skipped version (SPEC §5 Settings).
func TestV4QAInstallSkippedVersion(t *testing.T) {
	e := newUpdateApp(t, nil)
	newExe := fakeExe(3000, 5)
	serveRelease(e.gh, "0.5.0", newExe)
	e.status(t, "checkForUpdates", ``)
	if st := e.status(t, "skipUpdate", `{"version":"0.5.0"}`); st.Prompt || !st.Available {
		t.Fatalf("skipped %+v", st)
	}

	if _, err := e.call(t, "installUpdate", `{"version":"0.5.0"}`); err != nil {
		t.Fatalf("install of the skipped version: %v", err)
	}

	select {
	case <-e.host.quit:
	case <-time.After(5 * time.Second):
		t.Fatalf("no quit; events %+v", e.rec.events)
	}
	if got, _ := os.ReadFile(e.exe); string(got) != string(newExe) {
		t.Errorf("not installed (%d bytes)", len(got))
	}
}

// An update newer than a skipped one is announced (event + one balloon),
// and the skipped version stays recorded.
func TestV4QASkipThenNewerAnnouncedWithBalloon(t *testing.T) {
	e := newUpdateApp(t, nil)
	serveRelease(e.gh, "0.5.0", fakeExe(100, 1))
	e.status(t, "skipUpdate", `{"version":"0.5.0"}`)
	e.status(t, "checkForUpdates", ``)
	if len(e.plat.notices) != 0 || len(e.rec.of("updateAvailable")) != 0 {
		t.Fatalf("skipped version announced: %+v", e.plat.notices)
	}
	serveRelease(e.gh, "0.5.1", fakeExe(100, 2))
	st := e.status(t, "checkForUpdates", ``)
	if !st.Prompt || st.SkippedVersion != "0.5.0" || len(e.plat.notices) != 1 ||
		e.plat.notices[0].title != T("ru", "updAvailableTitle", "0.5.1") || len(e.rec.of("updateAvailable")) != 1 {
		t.Errorf("newer than skipped: %+v, notices %+v", st, e.plat.notices)
	}
}

// --- epic time: embedded-only issues read in batches of ≤ 100 ----------------------

func TestV4QAEpicTimeKeyBatches(t *testing.T) {
	const tasks = 150
	var links []string
	var pool []fx
	for i := 0; i < tasks; i++ {
		k := "RBI-" + strconv.Itoa(1000+i)
		links = append(links, "out:relates:"+k+":task:new")
		pool = append(pool, fx{key: k, spent: 10, estimate: 3})
	}
	e1 := fx{key: "RBI-1", typ: "epic", spent: 100, estimate: 1}
	s10 := fx{key: "RBI-10", typ: "story", epic: "RBI-1", spent: 7, links: links}
	counted := []string{"RBI-1", "RBI-10"}
	for _, x := range pool {
		counted = append(counted, x.key)
	}
	for _, failSecond := range []bool{false, true} {
		t.Run("fail second batch "+strconv.FormatBool(failSecond), func(t *testing.T) {
			fj := &fakeJira{byKey: fxPool(pool...)}
			var batches []int
			var fields []string
			fj.status = func(r *jira.Request) int {
				u, _ := url.Parse(r.URL)
				m := specKeysJQL.FindStringSubmatch(u.Query().Get("jql"))
				if u.Path != "/rest/api/2/search" || m == nil {
					return 0
				}
				batches = append(batches, len(strings.Split(m[1], ",")))
				fields = append(fields, u.Query().Get("fields"))
				if failSecond && len(batches) == 2 {
					return 500
				}
				return 0
			}
			a := v2App(t, fj, []string{"RBI"})
			ctx := context.Background()
			c, id, err := a.client()
			if err != nil {
				t.Fatal(err)
			}
			v, err := a.newView(ctx, c, id)
			if err != nil {
				t.Fatal(err)
			}
			p := newTreePool(v)
			for _, x := range []fx{e1, s10} {
				var is jira.Issue
				if err := json.Unmarshal([]byte(x.json()), &is); err != nil {
					t.Fatal(err)
				}
				p.addEmbedded(p.add(is, true))
			}
			epics := []EpicNode{{counted: append([]string{}, counted...)}}

			partial := p.sumTimes(ctx, epics, maxTreeChildren)

			if len(batches) != 2 || batches[0] != 100 || batches[1] != 50 {
				t.Errorf("batches %v, want [100 50]", batches)
			}
			for _, f := range fields {
				if f != timeOnlyFields {
					t.Errorf("batch fields %q", f)
				}
			}
			wantSpent, wantEst := int64(100+7+tasks*10), int64(1+tasks*3)
			if failSecond {
				wantSpent, wantEst = wantSpent-50*10, wantEst-50*3
			}
			if got := epics[0].Time; got != (EpicTime{SpentSeconds: wantSpent, EstimateSeconds: wantEst, Issues: tasks + 2}) {
				t.Errorf("time %+v, want spent %d estimate %d", got, wantSpent, wantEst)
			}
			if partial != failSecond {
				t.Errorf("partial %v, want %v", partial, failSecond)
			}
		})
	}
}

// --- PlainText edge cases ------------------------------------------------------------

func TestV4QAPlainTextEdgeCases(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"entities are decoded once", "<p>a &amp;lt;b&amp;gt; c</p>", "a &lt;b&gt; c"},
		{"nbsp runs fold", "<p>x&nbsp;&nbsp;&nbsp;y</p><p>&nbsp;</p><p>z</p>", "x y z"},
		{"title content dropped", "<p>x</p><title>T</title><p>y</p>", "x y"},
		{"doctype and processing instruction", "<!DOCTYPE html><?xml version=\"1.0\"?><p>x</p>", "x"},
		{"single-quoted > in attribute", "<p data-x='a>b'>t</p>", "t"},
		{"iframe content dropped", "<p>a</p><iframe src=x>fallback</iframe><p>b</p>", "a b"},
		{"uppercase dropped tag", "<p>a</p><SCRIPT type='x'>evil()</SCRIPT><p>b</p>", "a b"},
		{"look-alike closing tag", "<p>a</p><script>x</scripts>y</script><p>b</p>", "a b"},
		// SVG and MathML are foreign elements: their self-closing form is
		// complete (like the UI's DOMParser sees it); the text after it stays.
		{"self-closing svg", `<p>hello</p><svg viewBox="0 0 1 1"/><p>world</p>`, "hello world"},
		{"self-closing math", `<p>a</p><math/><p>b</p>`, "a b"},
	}
	for _, tc := range tests {
		if got := PlainText(tc.in, 300); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	// The cap counts runes and never leaves a dangling space before "…".
	if got := PlainText("<p>"+strings.Repeat("ab ", 50)+"</p>", 10); got != "ab ab ab…" {
		t.Errorf("cap: %q", got)
	}
}

// --- automatic check loop (QR-1) --------------------------------------------------------

// The SPEC's schedule: first check 30 s after start, then a look every
// 15 min whether a day of uptime has passed; exe.old kept 10 minutes.
func TestV4QAUpdateTimingDefaults(t *testing.T) {
	if updateStartDelay != 30*time.Second || updateTick != 15*time.Minute || updateEvery != 24*time.Hour || keepOldFor != 10*time.Minute {
		t.Errorf("start delay %s, tick %s, every %s, keep old %s", updateStartDelay, updateTick, updateEvery, keepOldFor)
	}
}

// tickClock is a goroutine-safe clock that counts the calls made by the
// update loop's autoCheck (one per loop tick).
type tickClock struct {
	mu    sync.Mutex
	t     time.Time
	ticks int
}

func (c *tickClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if pc, _, _, ok := runtime.Caller(1); ok && strings.HasSuffix(runtime.FuncForPC(pc).Name(), ".(*App).autoCheck") {
		c.ticks++
	}
	return c.t
}

func (c *tickClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func (c *tickClock) tickCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ticks
}

// fastUpdateLoop makes the loop tick every millisecond. It must be called
// before newUpdateApp, so the globals are restored only after the app (and
// its loop) stopped.
func fastUpdateLoop(t *testing.T) {
	oldDelay, oldTick := updateStartDelay, updateTick
	updateStartDelay, updateTick = time.Millisecond, time.Millisecond
	t.Cleanup(func() { updateStartDelay, updateTick = oldDelay, oldTick })
}

// Start runs the stale-download cleanup and the automatic check: a newer
// release is announced without any call from the UI; within a day there is
// no second check, after a day of uptime there is.
func TestV4QAStartRunsAutomaticCheckAndCleanup(t *testing.T) {
	fastUpdateLoop(t)
	e := newUpdateApp(t, nil)
	clock := &tickClock{t: e.clock.t}
	e.a.clock = clock
	serveRelease(e.gh, "0.5.0", fakeExe(100, 1))
	part := filepath.Join(filepath.Dir(filepath.Dir(e.exe)), "local", "update", "jiraffe-0.4.9.exe.part")
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{part, e.exe + ".new"} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gone := func(f string) bool { _, err := os.Stat(f); return errors.Is(err, os.ErrNotExist) }

	e.a.Start()

	waitFor(t, func() bool { return len(e.rec.of("updateAvailable")) == 1 }, "updateAvailable from the automatic check")
	waitFor(t, func() bool { return gone(part) && gone(e.exe+".new") }, "the stale downloads to be removed at start")
	if st := e.status(t, "getUpdateStatus", ``); !st.Prompt || st.LastCheck == nil || !st.LastCheck.OK {
		t.Errorf("status after the automatic check %+v", st)
	}
	if len(e.plat.notices) != 1 {
		t.Errorf("window closed: balloons %+v", e.plat.notices)
	}
	// More ticks within the same day: no new request.
	after := clock.tickCount()
	waitFor(t, func() bool { return clock.tickCount() >= after+5 }, "five more loop ticks")
	if n := e.gh.count(update.LatestURL); n != 1 {
		t.Errorf("%d checks within a day, want 1", n)
	}
	// A day of uptime later: checked again.
	clock.add(updateEvery + time.Minute)
	waitFor(t, func() bool { return e.gh.count(update.LatestURL) == 2 }, "the daily check")
}

// autoCheckUpdates off: the loop keeps ticking but never asks GitHub; once
// the setting is turned on, the next tick checks.
func TestV4QAAutomaticCheckOff(t *testing.T) {
	fastUpdateLoop(t)
	e := newUpdateApp(t, nil)
	clock := &tickClock{t: e.clock.t}
	e.a.clock = clock
	serveRelease(e.gh, "0.5.0", fakeExe(100, 1))
	cur := e.a.cfg.Get()
	cur.AutoCheckUpdates = false
	if _, _, err := e.a.cfg.Save(cur, nil); err != nil {
		t.Fatal(err)
	}

	e.a.Start()

	waitFor(t, func() bool { return clock.tickCount() >= 5 }, "five loop ticks")
	if n := e.gh.count(update.LatestURL); n != 0 || len(e.rec.of("updateAvailable")) != 0 {
		t.Fatalf("setting off: %d checks", n)
	}
	cur.AutoCheckUpdates = true
	if _, _, err := e.a.cfg.Save(cur, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return e.gh.count(update.LatestURL) == 1 }, "a check after turning the setting on")
}

// QR-5: a stored newer release in a build without an updater is reported
// but never offered (supported, available and prompt are false).
func TestV4QAStatusWithoutUpdater(t *testing.T) {
	old := Version
	Version = "0.4.0"
	t.Cleanup(func() { Version = old })
	a, _, _ := newTestApp(t, &fakeJira{}, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	rel, err := update.ParseRelease(ghRelease("0.5.0", fakeExe(10, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.st.Update(func(tx state.Tx) {
		tx.UpdateCheck = &state.UpdateCheck{At: "2026-11-11T09:00:00+02:00", OK: true, Latest: rel}
	}); err != nil {
		t.Fatal(err)
	}
	res, err := a.Call(context.Background(), "getUpdateStatus", nil)
	if err != nil {
		t.Fatal(err)
	}
	st := res.(UpdateStatus)
	if st.Supported || st.Available || st.Prompt || st.Latest == nil || st.Latest.Version != "0.5.0" || st.CurrentVersion != "0.4.0" {
		t.Errorf("status %+v latest %+v", st, st.Latest)
	}
}

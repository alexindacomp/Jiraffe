package update

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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVersions(t *testing.T) {
	tests := []struct {
		latest, current string
		newer           bool
	}{
		{"0.5.0", "0.4.0", true},
		{"v0.4.1", "0.4.0", true},
		{"1.0.0", "0.99.99", true},
		{"0.10.0", "0.9.0", true}, // numeric, not lexical
		{"0.4.0", "0.4.0", false},
		{"0.3.9", "0.4.0", false},
		{"0.5.0", "dev", false},   // development builds never update
		{"0.5.0", "0.0.0", false}, // untagged CI builds
		{"0.5.0", "", false},
		{"0.5.0-rc1", "0.4.0", false},
		{"0.5", "0.4.0", false},
		{"x.y.z", "0.4.0", false},
		{"0.5.0", "v0.4.0", true},
		{"1.2.3.4", "0.4.0", false},
		{"+1.2.3", "0.4.0", false},
		{"1.2.-3", "0.4.0", false},
		{"1234567890.0.0", "0.4.0", false}, // absurd component
	}
	for _, tc := range tests {
		if got := Newer(tc.latest, tc.current); got != tc.newer {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.newer)
		}
	}
	for v, want := range map[string]bool{"0.4.0": true, "v1.0.0": true, "dev": false, "0.0.0": false, "": false, "1.0": false} {
		if Supported(v) != want {
			t.Errorf("Supported(%q) = %v", v, !want)
		}
	}
}

func TestAllowed(t *testing.T) {
	for raw, want := range map[string]bool{
		LatestURL: true,
		"https://github.com/alexindacomp/Jiraffe/releases/download/v1.0.0/jiraffe.exe":   true,
		"https://objects.githubusercontent.com/x?sig=1":                                  true,
		"https://release-assets.githubusercontent.com/github-production-release-asset/1": true,
		"http://github.com/alexindacomp/Jiraffe":                                         false, // not https
		"https://github.com:8443/x":                                                      false, // port
		"https://user:pw@github.com/x":                                                   false,
		"https://github.com.evil.example/x":                                              false,
		"https://evilgithub.com/x":                                                       false,
		"https://sd.local/rest/api/2/myself":                                             false,
		"https://GITHUB.COM/x":                                                           false, // exact host only
		"github.com/x":                                                                   false,
		"":                                                                               false,
		"https:github.com":                                                               false,
	} {
		if got := Allowed(raw); got != want {
			t.Errorf("Allowed(%q) = %v", raw, got)
		}
	}
}

func TestParseChecksum(t *testing.T) {
	h := strings.Repeat("ab", 32)
	var want [32]byte
	_, _ = hex.Decode(want[:], []byte(h))
	for _, ok := range []string{h + "  jiraffe.exe\n", h + " *jiraffe.exe", h, strings.ToUpper(h) + "  jiraffe.exe\r\n", "\n" + h + "  jiraffe.exe\n\n"} {
		if got, err := ParseChecksum([]byte(ok)); err != nil || got != want {
			t.Errorf("%q: %x %v", ok, got, err)
		}
	}
	for _, bad := range []string{"", h[:63] + "  jiraffe.exe", h + "0  jiraffe.exe", strings.Repeat("zz", 32) + "  jiraffe.exe",
		h + "  other.exe", h + "  jiraffe.exe extra", h + "  jiraffe.exe\n" + h + "  jiraffe.exe"} {
		if _, err := ParseChecksum([]byte(bad)); CodeOf(err) != CodeBadChecksum {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// --- fakes ------------------------------------------------------------------

type resp struct {
	status int
	loc    string
	body   []byte
	err    error
}

// fakeGetter serves canned responses by URL and records the requests.
type fakeGetter struct {
	routes  map[string]resp
	calls   []string
	headers []map[string]string
}

func (g *fakeGetter) Get(ctx context.Context, url string, headers map[string]string, w io.Writer, limit int64) (int, string, error) {
	g.calls = append(g.calls, url)
	g.headers = append(g.headers, headers)
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	r, ok := g.routes[url]
	if !ok {
		return 404, "", nil
	}
	if r.err != nil {
		return 0, "", r.err
	}
	if r.status >= 300 && r.status < 400 {
		return r.status, r.loc, nil
	}
	if r.status < 200 || r.status >= 300 {
		return r.status, "", nil // body discarded
	}
	if int64(len(r.body)) > limit {
		_, _ = w.Write(r.body[:limit])
		return 0, "", ErrTooLarge
	}
	if _, err := w.Write(r.body); err != nil {
		return 0, "", err
	}
	return r.status, "", nil
}

// --- download, verify, install ------------------------------------------------

// exeBytes is a fake executable of n bytes.
func exeBytes(n int) []byte {
	b := bytes.Repeat([]byte{0x90}, n)
	copy(b, "MZ")
	return b
}

func sumLine(b []byte) []byte {
	s := sha256.Sum256(b)
	return []byte(hex.EncodeToString(s[:]) + "  jiraffe.exe\n")
}

// files lists name → content of dir.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, e := range es {
		if e.IsDir() {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		m[e.Name()] = string(b)
	}
	return m
}

func keys(m map[string]string) []string {
	var k []string
	for n, v := range m {
		k = append(k, fmt.Sprintf("%s(%d)", n, len(v)))
	}
	sort.Strings(k)
	return k
}

type fakeStarter struct {
	exe  string
	args []string
	err  error
}

func (s *fakeStarter) Start(exe string, args []string) error {
	s.exe, s.args = exe, args
	return s.err
}

func TestStart(t *testing.T) {
	if got := strings.Join(RestartArgs("0.4.0", 4242, true), " "); got != "--updated-from 0.4.0 --wait-pid 4242 --minimized" {
		t.Errorf("args %s", got)
	}
	if got := strings.Join(RestartArgs("0.4.0", 1, false), " "); got != "--updated-from 0.4.0 --wait-pid 1" {
		t.Errorf("args %s", got)
	}
	st := &fakeStarter{}
	u := New(Config{Starter: st, Exe: `C:\Apps\jiraffe.exe`})
	if err := u.Start([]string{"--x"}); err != nil || st.exe != `C:\Apps\jiraffe.exe` || st.args[0] != "--x" {
		t.Errorf("%v %+v", err, st)
	}
	st.err = errors.New("CreateProcess failed")
	if err := u.Start(nil); CodeOf(err) != CodeStart {
		t.Errorf("%v", err)
	}
}

// --- signature-aware fixtures --------------------------------------------------------

const dlBase = "https://github.com/alexindacomp/Jiraffe/releases/download/v0.5.0/"

// testKey replaces the embedded release key for the test.
func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, ed25519.SeedSize))
	old := PublicKey
	PublicKey = priv.Public().(ed25519.PublicKey)
	t.Cleanup(func() { PublicKey = old })
	return priv
}

func sigLine(priv ed25519.PrivateKey, version string, exe []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, SignedMessage(version, sha256.Sum256(exe)))) + "\n")
}

func releaseJSON(mod func(m map[string]any)) []byte {
	m := map[string]any{
		"tag_name": "v0.5.0", "draft": false, "prerelease": false,
		"body": "Fixes\r\n- one\x00", "published_at": "2026-11-10T12:00:00Z",
		"html_url": "https://github.com/alexindacomp/Jiraffe/releases/tag/v0.5.0",
		"assets": []any{
			map[string]any{"name": "notes.txt", "browser_download_url": dlBase + "notes.txt", "size": 5},
			map[string]any{"name": "jiraffe.exe", "browser_download_url": dlBase + "jiraffe.exe", "size": 4321},
			map[string]any{"name": "jiraffe.exe.sha256", "browser_download_url": dlBase + "jiraffe.exe.sha256", "size": 78},
			map[string]any{"name": "jiraffe.exe.sig", "browser_download_url": dlBase + "jiraffe.exe.sig", "size": 89},
		},
	}
	if mod != nil {
		mod(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func TestReleasePageAllowed(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://github.com/alexindacomp/Jiraffe/releases/tag/v1.2.3":                  true,
		"https://github.com/alexindacomp/Jiraffe/releases/latest":                      true,
		"https://github.com/alexindacomp/Jiraffe/releases/tag/v1.2.3/":                 false,
		"https://github.com/alexindacomp/Jiraffe/releases/tag/1.2.3":                   false,
		"https://github.com/alexindacomp/Jiraffe/releases/tag/v1.02.3":                 false,
		"https://github.com/alexindacomp/Jiraffe/releases/tag/v1.2":                    false,
		"https://github.com/alexindacomp/Jiraffe/releases/download/v1.2.3/jiraffe.exe": false,
		"https://github.com/alexindacomp/Jiraffe/releases/latest/":                     false,
		"https://github.com/alexindacomp/Jiraffe/releases/latest?x=1":                  false,
		"https://github.com/alexindacomp/Jiraffe/releases/tag/v1.2.3#x":                false,
		"https://github.com/alexindacomp/Jiraffe/releases/":                            false,
		"https://github.com/alexindacomp/Jiraffe/releases/tag/v1.2.3/../../../x":       false,
		"https://github.com/alexindacomp/Jiraffe/releases/tag/%761.2.3":                false,
		"http://github.com/alexindacomp/Jiraffe/releases/tag/v1.2.3":                   false,
		"https://GITHUB.com/alexindacomp/Jiraffe/releases/tag/v1.2.3":                  false,
		"https://x@github.com/alexindacomp/Jiraffe/releases/tag/v1.2.3":                false,
		"https://github.com/other/Jiraffe/releases/tag/v1.2.3":                         false,
	} {
		if got := ReleasePageAllowed(raw); got != want {
			t.Errorf("ReleasePageAllowed(%q) = %v", raw, got)
		}
	}
	if p := ReleasePage("1.2.3"); !ReleasePageAllowed(p) || p != "https://github.com/alexindacomp/Jiraffe/releases/tag/v1.2.3" {
		t.Errorf("ReleasePage %s", p)
	}
}

func TestParseRelease(t *testing.T) {
	r, err := ParseRelease(releaseJSON(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Release{Version: "0.5.0", Notes: "Fixes\n- one", PublishedAt: "2026-11-10T12:00:00Z",
		Page: "https://github.com/alexindacomp/Jiraffe/releases/tag/v0.5.0",
		Exe:  Asset{URL: dlBase + "jiraffe.exe", Size: 4321}, Sum: Asset{URL: dlBase + "jiraffe.exe.sha256", Size: 78},
		Sig: Asset{URL: dlBase + "jiraffe.exe.sig", Size: 89}}
	if *r != want {
		t.Errorf("got %+v\nwant %+v", *r, want)
	}
	asset := func(name, url string, size int) map[string]any {
		return map[string]any{"name": name, "browser_download_url": url, "size": size}
	}
	exe := asset("jiraffe.exe", dlBase+"jiraffe.exe", 10)
	sum := asset("jiraffe.exe.sha256", dlBase+"jiraffe.exe.sha256", 78)
	sig := asset("jiraffe.exe.sig", dlBase+"jiraffe.exe.sig", 89)
	assets := func(as ...any) func(map[string]any) { return func(m map[string]any) { m["assets"] = as } }
	for name, mod := range map[string]func(map[string]any){
		"draft":                    func(m map[string]any) { m["draft"] = true },
		"prerelease":               func(m map[string]any) { m["prerelease"] = true },
		"tag without v":            func(m map[string]any) { m["tag_name"] = "0.5.0" },
		"tag with suffix":          func(m map[string]any) { m["tag_name"] = "v0.5.0-beta" },
		"tag not canonical":        func(m map[string]any) { m["tag_name"] = "v0.05.0" },
		"no signature asset":       assets(exe, sum),
		"no checksum asset":        assets(exe, sig),
		"no exe asset":             assets(sum, sig),
		"no assets":                assets(),
		"exe twice":                assets(exe, exe, sum, sig),
		"exe on another host":      assets(asset("jiraffe.exe", "https://evil.example/jiraffe.exe", 10), sum, sig),
		"exe of another repo":      assets(asset("jiraffe.exe", "https://github.com/other/Jiraffe/releases/download/v0.5.0/jiraffe.exe", 10), sum, sig),
		"exe of another tag":       assets(asset("jiraffe.exe", "https://github.com/alexindacomp/Jiraffe/releases/download/v0.4.0/jiraffe.exe", 10), sum, sig),
		"exe over http":            assets(asset("jiraffe.exe", "http://github.com/alexindacomp/Jiraffe/releases/download/v0.5.0/jiraffe.exe", 10), sum, sig),
		"signature from elsewhere": assets(exe, sum, asset("jiraffe.exe.sig", "https://objects.githubusercontent.com/x", 89)),
		"signature over 1 KB":      assets(exe, sum, asset("jiraffe.exe.sig", dlBase+"jiraffe.exe.sig", 1025)),
		"empty signature":          assets(exe, sum, asset("jiraffe.exe.sig", dlBase+"jiraffe.exe.sig", 0)),
		"empty exe":                assets(asset("jiraffe.exe", dlBase+"jiraffe.exe", 0), sum, sig),
		"exe too large":            assets(asset("jiraffe.exe", dlBase+"jiraffe.exe", MaxDownload+1), sum, sig),
		"checksum file over 4 KB":  assets(exe, asset("jiraffe.exe.sha256", dlBase+"jiraffe.exe.sha256", 5000), sig),
	} {
		if _, err := ParseRelease(releaseJSON(mod)); CodeOf(err) != CodeBadRelease {
			t.Errorf("%s: err %v", name, err)
		}
	}
	if _, err := ParseRelease([]byte(`<html>`)); CodeOf(err) != CodeBadRelease {
		t.Errorf("not JSON: %v", err)
	}
}

// A release loaded back from state.json is re-validated before any use.
func TestReleaseSanitized(t *testing.T) {
	r, _ := ParseRelease(releaseJSON(nil))
	c := *r
	c.Page, c.Notes = "https://evil.example/", "a\r\nb\x00"
	got, err := c.Sanitized()
	if err != nil || got.Page != r.Page || got.Notes != "a\nb" {
		t.Errorf("sanitized %+v %v", got, err)
	}
	for name, mod := range map[string]func(*Release){
		"exe URL":      func(r *Release) { r.Exe.URL = "https://evil.example/jiraffe.exe" },
		"sig missing":  func(r *Release) { r.Sig = Asset{} },
		"version":      func(r *Release) { r.Version = "0.5.0-x" },
		"version used": func(r *Release) { r.Version = "0.6.0" }, // asset URLs are of v0.5.0
		"size":         func(r *Release) { r.Exe.Size = -1 },
	} {
		c := *r
		mod(&c)
		if _, err := c.Sanitized(); CodeOf(err) != CodeBadRelease {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := (*Release)(nil).Sanitized(); err == nil {
		t.Error("nil release")
	}
}

func TestSignature(t *testing.T) {
	priv := testKey(t)
	exe := exeBytes(100)
	sum := sha256.Sum256(exe)
	if string(SignedMessage("1.2.3", sum)) != "jiraffe-release-v1\n1.2.3\n"+hex.EncodeToString(sum[:])+"\n" {
		t.Errorf("message %q", SignedMessage("1.2.3", sum))
	}
	sig, err := ParseSignature(sigLine(priv, "0.5.0", exe))
	if err != nil || !VerifySignature(PublicKey, "0.5.0", sum, sig) {
		t.Fatalf("round trip: %v", err)
	}
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))
	wrongKey, _ := ParseSignature(sigLine(other, "0.5.0", exe))
	tampered := append([]byte{}, exe...)
	tampered[50] ^= 1
	for name, ok := range map[string]bool{
		"wrong version": VerifySignature(PublicKey, "0.5.1", sum, sig),
		"tampered exe":  VerifySignature(PublicKey, "0.5.0", sha256.Sum256(tampered), sig),
		"wrong key":     VerifySignature(PublicKey, "0.5.0", sum, wrongKey),
		"truncated sig": VerifySignature(PublicKey, "0.5.0", sum, sig[:63]),
		"no key":        VerifySignature(nil, "0.5.0", sum, sig),
		"flipped bit":   VerifySignature(PublicKey, "0.5.0", sum, append(append([]byte{}, sig[:10]...), append([]byte{sig[10] ^ 1}, sig[11:]...)...)),
	} {
		if ok {
			t.Errorf("%s verifies", name)
		}
	}
	for _, bad := range []string{"", "not base64!", base64.StdEncoding.EncodeToString(sig[:40]), base64.StdEncoding.EncodeToString(append(sig, 0)),
		strings.Repeat("A", 2000)} {
		if _, err := ParseSignature([]byte(bad)); CodeOf(err) != CodeBadSignature {
			t.Errorf("%.20q: %v", bad, err)
		}
	}
	// The embedded key is a valid ed25519 public key.
	if len(mustPublicKey("H/tsEqhxPOKjmI14VJl9r7sUOG3mWQXHsQKY/Xp0ZEk=")) != ed25519.PublicKeySize {
		t.Error("embedded key")
	}
}

func TestFetchRedirectPolicies(t *testing.T) {
	const (
		objects = "https://objects.githubusercontent.com/b"
		assets  = "https://release-assets.githubusercontent.com/c"
	)
	exe := dlBase + "jiraffe.exe"
	chain := func(start string, n int) map[string]resp {
		m := map[string]resp{start: {status: 302, loc: "https://objects.githubusercontent.com/1"}}
		for i := 1; i < n; i++ {
			m[fmt.Sprintf("https://objects.githubusercontent.com/%d", i)] = resp{status: 302, loc: fmt.Sprintf("/%d", i+1)}
		}
		m[fmt.Sprintf("https://objects.githubusercontent.com/%d", n)] = resp{status: 200, body: []byte("end")}
		return m
	}
	tests := []struct {
		name   string
		policy hopPolicy
		start  string
		routes map[string]resp
		code   string // "" = success
		calls  int
	}{
		{"asset direct", assetHops, exe, map[string]resp{exe: {status: 200, body: []byte("x")}}, "", 1},
		{"asset via both storage hosts", assetHops, exe, map[string]resp{exe: {status: 302, loc: objects}, objects: {status: 307, loc: assets}, assets: {status: 200, body: []byte("ok")}}, "", 3},
		{"asset relative location", assetHops, exe, chain(exe, 1), "", 2},
		{"asset five redirects", assetHops, exe, chain(exe, 5), "", 6},
		{"asset six redirects", assetHops, exe, chain(exe, 6), CodeRedirect, 6},
		{"asset back to github.com", assetHops, exe, map[string]resp{exe: {status: 302, loc: "https://github.com/x"}}, CodeRedirect, 1},
		{"asset to the API", assetHops, exe, map[string]resp{exe: {status: 302, loc: LatestURL}}, CodeRedirect, 1},
		{"asset to another host", assetHops, exe, map[string]resp{exe: {status: 302, loc: "https://evil.example/x"}}, CodeRedirect, 1},
		{"asset downgrade to http", assetHops, exe, map[string]resp{exe: {status: 301, loc: "http://objects.githubusercontent.com/b"}}, CodeRedirect, 1},
		{"asset not at the download prefix", assetHops, "https://objects.githubusercontent.com/x", nil, CodeRedirect, 0},
		{"asset other repo", assetHops, "https://github.com/evil/Jiraffe/releases/download/v1.0.0/jiraffe.exe", nil, CodeRedirect, 0},
		{"api direct", apiHops, LatestURL, map[string]resp{LatestURL: {status: 200, body: []byte("{}")}}, "", 1},
		{"api within api.github.com", apiHops, LatestURL, map[string]resp{LatestURL: {status: 301, loc: "/repositories/1/releases/latest"},
			"https://api.github.com/repositories/1/releases/latest": {status: 200, body: []byte("{}")}}, "", 2},
		{"api to github.com", apiHops, LatestURL, map[string]resp{LatestURL: {status: 302, loc: "https://github.com/x"}}, CodeRedirect, 1},
		{"api to storage", apiHops, LatestURL, map[string]resp{LatestURL: {status: 302, loc: objects}}, CodeRedirect, 1},
		{"empty location", assetHops, exe, map[string]resp{exe: {status: 302}}, CodeRedirect, 1},
		{"not found", assetHops, exe, nil, CodeHTTP, 1},
		{"rate limited", apiHops, LatestURL, map[string]resp{LatestURL: {status: 403, body: []byte(`{"message":"API rate limit"}`)}}, CodeHTTP, 1},
		{"network error", assetHops, exe, map[string]resp{exe: {err: errors.New("WinHTTP 12007")}}, CodeNetwork, 1},
		{"too large", assetHops, exe, map[string]resp{exe: {status: 200, body: bytes.Repeat([]byte("x"), 11)}}, CodeTooLarge, 1},
	}
	for _, tc := range tests {
		g := &fakeGetter{routes: tc.routes}
		var buf bytes.Buffer
		err := New(Config{Getter: g}).fetch(context.Background(), tc.start, tc.policy, nil, &buf, 10)
		if CodeOf(err) != tc.code || tc.code == "" && err != nil {
			t.Errorf("%s: err %v, want code %q", tc.name, err, tc.code)
		}
		if len(g.calls) != tc.calls {
			t.Errorf("%s: %d requests %v, want %d", tc.name, len(g.calls), g.calls, tc.calls)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New(Config{Getter: &fakeGetter{}}).fetch(ctx, exe, assetHops, nil, io.Discard, 10); CodeOf(err) != CodeCancelled {
		t.Errorf("cancelled: %v", err)
	}
}

// blockingGetter blocks until the context ends (a stalled connection).
type blockingGetter struct{}

func (blockingGetter) Get(ctx context.Context, _ string, _ map[string]string, _ io.Writer, _ int64) (int, string, error) {
	<-ctx.Done()
	return 0, "", ctx.Err()
}

// The updater's own timeout is a network timeout, not a cancellation.
func TestTimeoutIsNetworkTimeout(t *testing.T) {
	u := New(Config{Getter: blockingGetter{}, FS: OSFS{}, Dir: t.TempDir(), CheckTimeout: 20 * time.Millisecond, DownloadTimeout: 20 * time.Millisecond})
	_, err := u.Latest(context.Background())
	if CodeOf(err) != CodeNetwork || !errors.Is(err, ErrTimeout) {
		t.Errorf("check: %v", err)
	}
	rel, _ := ParseRelease(releaseJSON(nil))
	if _, err := u.Download(context.Background(), rel, nil); CodeOf(err) != CodeNetwork || !errors.Is(err, ErrTimeout) {
		t.Errorf("download: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	u = New(Config{Getter: blockingGetter{}, CheckTimeout: time.Minute})
	if _, err := u.Latest(ctx); CodeOf(err) != CodeCancelled {
		t.Errorf("app exit: %v", err)
	}
}

func TestLatest(t *testing.T) {
	g := &fakeGetter{routes: map[string]resp{LatestURL: {status: 200, body: releaseJSON(nil)}}}
	r, err := New(Config{Getter: g}).Latest(context.Background())
	if err != nil || r.Version != "0.5.0" {
		t.Fatalf("%+v %v", r, err)
	}
	if h := g.headers[0]; h["Accept"] != "application/vnd.github+json" || len(h) != 1 {
		t.Errorf("headers %v (no credentials, nothing else)", h)
	}
}

func TestDownloadAndVerify(t *testing.T) {
	priv := testKey(t)
	good := exeBytes(5000)
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))
	notExe := append([]byte("PK"), good[2:]...)
	tampered := append([]byte{}, good...)
	tampered[4000] ^= 1
	tests := []struct {
		name          string
		exe, sum, sig []byte
		sigStatus     int
		size          int64
		code          string
	}{
		{"valid", good, sumLine(good), sigLine(priv, "0.5.0", good), 200, 5000, ""},
		{"size differs from the API", good, sumLine(good), sigLine(priv, "0.5.0", good), 200, 4999, CodeSizeMismatch},
		{"hash differs from .sha256", good, sumLine(exeBytes(4999)), sigLine(priv, "0.5.0", good), 200, 5000, CodeHashMismatch},
		{"not an executable", notExe, sumLine(notExe), sigLine(priv, "0.5.0", notExe), 200, 5000, CodeNotExe},
		{"bad checksum file", good, []byte("<html>"), sigLine(priv, "0.5.0", good), 200, 5000, CodeBadChecksum},
		// The attacker controls exe and .sha256 but not the key.
		{"tampered exe with matching .sha256", tampered, sumLine(tampered), sigLine(priv, "0.5.0", good), 200, 5000, CodeBadSignature},
		{"old exe re-tagged", good, sumLine(good), sigLine(priv, "0.4.0", good), 200, 5000, CodeBadSignature},
		{"signed with another key", good, sumLine(good), sigLine(other, "0.5.0", good), 200, 5000, CodeBadSignature},
		{"garbage signature", good, sumLine(good), []byte("garbage"), 200, 5000, CodeBadSignature},
		{"truncated signature", good, sumLine(good), sigLine(priv, "0.5.0", good)[:40], 200, 5000, CodeBadSignature},
		{"signature over 1 KB", good, sumLine(good), bytes.Repeat([]byte("A"), 1100), 200, 5000, CodeBadSignature},
		{"signature asset missing", good, sumLine(good), nil, 404, 5000, CodeBadSignature},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			g := &fakeGetter{routes: map[string]resp{
				dlBase + "jiraffe.exe.sha256":                      {status: 302, loc: "https://objects.githubusercontent.com/sum"},
				"https://objects.githubusercontent.com/sum":        {status: 200, body: tc.sum},
				dlBase + "jiraffe.exe.sig":                         {status: 302, loc: "https://objects.githubusercontent.com/sig"},
				"https://objects.githubusercontent.com/sig":        {status: tc.sigStatus, body: tc.sig},
				dlBase + "jiraffe.exe":                             {status: 302, loc: "https://release-assets.githubusercontent.com/exe"},
				"https://release-assets.githubusercontent.com/exe": {status: 200, body: tc.exe},
			}}
			u := New(Config{Getter: g, FS: OSFS{}, Dir: filepath.Join(dir, "update")})
			rel, _ := ParseRelease(releaseJSON(nil))
			rel.Exe.Size = tc.size
			var last [2]int64
			f, err := u.Download(context.Background(), rel, func(r, total int64) { last = [2]int64{r, total} })
			if err == nil {
				err = u.VerifyDownload(f, rel)
			}
			if CodeOf(err) != tc.code || tc.code == "" && err != nil {
				t.Fatalf("err %v, want %q", err, tc.code)
			}
			part := filepath.Join(dir, "update", "jiraffe-0.5.0.exe.part")
			if tc.code != "" {
				if _, err := os.Stat(part); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("partial file kept: %v", err)
				}
				return
			}
			if got, _ := os.ReadFile(f.Path); f.Path != part || !bytes.Equal(got, good) || f.Sum != sha256.Sum256(good) {
				t.Errorf("downloaded %s (%d bytes)", f.Path, len(got))
			}
			if last != [2]int64{5000, 5000} {
				t.Errorf("progress %v", last)
			}
			for _, h := range g.headers {
				if len(h) != 1 || h["Accept"] != "application/octet-stream" {
					t.Errorf("headers %v", h)
				}
			}
		})
	}
}

// faultFS wraps OSFS and fails chosen operations: failRename fails a rename
// ("from>to", base names) the given number of times (-1 = always).
type faultFS struct {
	OSFS
	mu         sync.Mutex
	failRename map[string]int
	failRemove map[string]bool  // base name
	failCreate map[string]error // base name → error
	renames    []string
}

func (f *faultFS) Rename(from, to string) error {
	f.mu.Lock()
	key := filepath.Base(from) + ">" + filepath.Base(to)
	f.renames = append(f.renames, key)
	n := f.failRename[key]
	if n != 0 {
		if n > 0 {
			f.failRename[key] = n - 1
		}
		f.mu.Unlock()
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrPermission}
	}
	f.mu.Unlock()
	return f.OSFS.Rename(from, to)
}

func (f *faultFS) Remove(path string) error {
	if f.failRemove[filepath.Base(path)] {
		return &os.PathError{Op: "remove", Path: path, Err: os.ErrPermission}
	}
	return f.OSFS.Remove(path)
}

func (f *faultFS) Create(path string) (io.WriteCloser, error) {
	if err := f.failCreate[filepath.Base(path)]; err != nil {
		return nil, err
	}
	return f.OSFS.Create(path)
}

// tamperFS replaces the staged exe.new right after it was written (between
// copy and rename), as a local attacker racing the swap would.
type tamperFS struct {
	OSFS
	when    string   // "copy" (the download changes before the copy) or "staged" (exe.new changes before the rename)
	renames []string // "from>to" (base names), in order
}

func (f *tamperFS) Open(path string) (io.ReadCloser, error) {
	if f.when == "copy" && strings.HasSuffix(path, ".part") {
		f.when = ""
		_ = os.WriteFile(path, exeBytes(3000), 0o644)
	}
	return f.OSFS.Open(path)
}

func (f *tamperFS) Rename(from, to string) error {
	f.renames = append(f.renames, filepath.Base(from)+">"+filepath.Base(to))
	if f.when == "staged" && strings.HasSuffix(to, ".old") {
		f.when = ""
		_ = os.WriteFile(strings.TrimSuffix(to, ".old")+".new", exeBytes(3000), 0o644)
	}
	return f.OSFS.Rename(from, to)
}

func TestInstallAndRollback(t *testing.T) {
	newExe := exeBytes(3000)
	newExe[100] = 1
	sum := sha256.Sum256(newExe)
	tests := []struct {
		name     string
		fs       func() FS
		staleOld bool
		code     string            // "" = installed
		want     map[string]string // files of the exe folder afterwards ("*" = any content)
		sleeps   int
	}{
		{"swap", func() FS { return OSFS{} }, false, "", map[string]string{"jiraffe.exe": string(newExe), "jiraffe.exe.old": "old exe"}, 0},
		{"stale .old replaced", func() FS { return OSFS{} }, true, "", map[string]string{"jiraffe.exe": string(newExe), "jiraffe.exe.old": "old exe"}, 0},
		{"locked stale .old moved aside", func() FS { return &faultFS{failRemove: map[string]bool{"jiraffe.exe.old": true}} }, true, "",
			map[string]string{"jiraffe.exe": string(newExe), "jiraffe.exe.old": "old exe", "jiraffe.exe.old.*": "stale old"}, 0},
		{"exe folder read-only", func() FS { return &faultFS{failCreate: map[string]error{"jiraffe.exe.new": os.ErrPermission}} }, false,
			CodeNoWriteAccess, map[string]string{"jiraffe.exe": "old exe"}, 0},
		{"rename retried with backoff", func() FS { return &faultFS{failRename: map[string]int{"jiraffe.exe>jiraffe.exe.old": 2}} }, false,
			"", map[string]string{"jiraffe.exe": string(newExe), "jiraffe.exe.old": "old exe"}, 2},
		{"running exe stays locked: install_failed", func() FS { return &faultFS{failRename: map[string]int{"jiraffe.exe>jiraffe.exe.old": -1}} }, false,
			CodeInstall, map[string]string{"jiraffe.exe": "old exe"}, 3},
		{"new exe cannot be moved in: rolled back", func() FS { return &faultFS{failRename: map[string]int{"jiraffe.exe.new>jiraffe.exe": -1}} }, false,
			CodeInstall, map[string]string{"jiraffe.exe": "old exe"}, 3},
		{"download changed after verification", func() FS { return &tamperFS{when: "copy"} }, false,
			CodeHashMismatch, map[string]string{"jiraffe.exe": "old exe"}, 0},
		{"exe.new changed before the rename: rolled back", func() FS { return &tamperFS{when: "staged"} }, false,
			CodeHashMismatch, map[string]string{"jiraffe.exe": "old exe"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "app", "jiraffe.exe")
			_ = os.MkdirAll(filepath.Dir(exe), 0o755)
			_ = os.WriteFile(exe, []byte("old exe"), 0o755)
			if tc.staleOld {
				_ = os.WriteFile(exe+".old", []byte("stale old"), 0o755)
			}
			down := filepath.Join(dir, "jiraffe-0.5.0.exe.part")
			_ = os.WriteFile(down, newExe, 0o644)
			sleeps := 0
			fs := tc.fs()
			u := New(Config{FS: fs, Exe: exe, Sleep: func(time.Duration) { sleeps++ }})
			swap, err := u.Install(down, int64(len(newExe)), sum)
			if CodeOf(err) != tc.code || tc.code == "" && err != nil {
				t.Fatalf("err %v, want %q", err, tc.code)
			}
			// A copy that differs from the verified download is caught before
			// the running exe is touched: not even renamed to exe.old.
			if tf, ok := fs.(*tamperFS); ok && tc.name == "download changed after verification" {
				for _, r := range tf.renames {
					if r == "jiraffe.exe>jiraffe.exe.old" {
						t.Errorf("running exe renamed although the copy did not match: %v", tf.renames)
					}
				}
			}
			got := files(t, filepath.Dir(exe))
			for n := range got { // name the moved-aside .old generically
				if strings.HasPrefix(n, "jiraffe.exe.old.") && n != "jiraffe.exe.old.*" {
					got["jiraffe.exe.old.*"] = got[n]
					delete(got, n)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("files %v\nwant %v", keys(got), keys(tc.want))
			}
			if sleeps != tc.sleeps {
				t.Errorf("%d backoff sleeps, want %d", sleeps, tc.sleeps)
			}
			if swap == nil {
				return
			}
			if err := swap.Rollback(); err != nil {
				t.Fatal(err)
			}
			if got := files(t, filepath.Dir(exe)); got["jiraffe.exe"] != "old exe" || got["jiraffe.exe.new"] != "" {
				t.Errorf("after rollback %v", keys(got))
			}
		})
	}
}

func TestCheckWritableAndCleanup(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "jiraffe.exe")
	upd := filepath.Join(dir, "update")
	_ = os.MkdirAll(upd, 0o755)
	for _, f := range []string{exe, exe + ".old", exe + ".old.k3x", exe + ".new", exe + ".older-notes.txt", filepath.Join(dir, "other.exe"),
		filepath.Join(upd, "jiraffe-0.5.0.exe.part"), filepath.Join(upd, "x")} {
		_ = os.WriteFile(f, []byte("x"), 0o644)
	}
	u := New(Config{FS: OSFS{}, Exe: exe, Dir: upd})
	if err := u.CheckWritable(); err != nil {
		t.Fatal(err)
	}
	if err := u.CleanupDownloads(); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(keys(files(t, dir))); got != "[jiraffe.exe(1) jiraffe.exe.old(1) jiraffe.exe.old.k3x(1) jiraffe.exe.older-notes.txt(1) other.exe(1)]" {
		t.Errorf("after download cleanup %s", got)
	}
	if got := files(t, upd); len(got) != 0 {
		t.Errorf("update dir %v", got)
	}
	if err := u.CleanupOld(); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(keys(files(t, dir))); got != "[jiraffe.exe(1) jiraffe.exe.older-notes.txt(1) other.exe(1)]" {
		t.Errorf("after .old cleanup %s", got)
	}
	// Missing folders are fine.
	if err := New(Config{FS: OSFS{}, Exe: exe, Dir: filepath.Join(dir, "none")}).CleanupDownloads(); err != nil {
		t.Error(err)
	}
	ro := New(Config{FS: &faultFS{failCreate: map[string]error{".jiraffe-update-probe": os.ErrPermission}}, Exe: exe})
	if err := ro.CheckWritable(); CodeOf(err) != CodeNoWriteAccess {
		t.Errorf("read-only: %v", err)
	}
}

// CR-12: Rollback retries renames and reports where the previous exe is.
func TestRollbackRetriesAndReports(t *testing.T) {
	newExe := exeBytes(3000)
	sum := sha256.Sum256(newExe)
	for _, tc := range []struct {
		name  string
		fails int // failures of the rollback's first rename (-1 = always)
		ok    bool
	}{{"locked briefly", 2, true}, {"locked for good", -1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "jiraffe.exe")
			_ = os.WriteFile(exe, []byte("old exe"), 0o755)
			down := filepath.Join(dir, "dl.part")
			_ = os.WriteFile(down, newExe, 0o644)
			fs := &faultFS{failRename: map[string]int{}}
			sleeps := 0
			u := New(Config{FS: fs, Exe: exe, Sleep: func(time.Duration) { sleeps++ }})
			swap, err := u.Install(down, int64(len(newExe)), sum)
			if err != nil {
				t.Fatal(err)
			}
			fs.mu.Lock()
			fs.failRename["jiraffe.exe>jiraffe.exe.new"] = tc.fails
			fs.mu.Unlock()
			err = swap.Rollback()
			var ue *Error
			switch {
			case tc.ok && (err != nil || sleeps != 2):
				t.Errorf("err %v, %d sleeps", err, sleeps)
			case !tc.ok && (!errors.As(err, &ue) || ue.Code != CodeInstall || ue.Previous != exe+".old"):
				t.Errorf("err %#v", err)
			}
			if tc.ok {
				if got, _ := os.ReadFile(exe); string(got) != "old exe" {
					t.Errorf("exe %q", got)
				}
			}
		})
	}
}

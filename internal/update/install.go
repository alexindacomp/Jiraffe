package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Getter performs one HTTP GET without following redirects. Only the body
// of a 2xx response is written to w (more than limit bytes fail with
// ErrTooLarge); other bodies are discarded. For a 3xx response it returns
// the Location header. The implementation must not add credentials of any
// kind.
type Getter interface {
	Get(ctx context.Context, url string, headers map[string]string, w io.Writer, limit int64) (status int, location string, err error)
}

// ErrTooLarge is returned by a Getter when the body exceeds the limit.
var ErrTooLarge = errors.New("response too large")

// FS are the file operations of the updater.
type FS interface {
	MkdirAll(dir string) error
	Create(path string) (io.WriteCloser, error) // create or truncate
	Open(path string) (io.ReadCloser, error)
	Rename(from, to string) error // replaces an existing target
	Remove(path string) error     // a missing file is not an error
	List(dir string) ([]string, error)
}

// Starter starts a detached process.
type Starter interface {
	Start(exe string, args []string) error
}

// Config wires an Updater. Zero durations and a nil Sleep mean the defaults.
type Config struct {
	Getter  Getter
	FS      FS
	Starter Starter
	Exe     string // path of the running executable
	Dir     string // download folder (%LOCALAPPDATA%\Jiraffe\update)

	CheckTimeout    time.Duration       // default 20 s
	DownloadTimeout time.Duration       // default 5 min
	Sleep           func(time.Duration) // rename retry backoff; default time.Sleep
}

// Updater checks, downloads and installs releases.
type Updater struct{ c Config }

// New creates an Updater.
func New(c Config) *Updater {
	if c.CheckTimeout <= 0 {
		c.CheckTimeout = CheckTimeout
	}
	if c.DownloadTimeout <= 0 {
		c.DownloadTimeout = DownloadTimeout
	}
	if c.Sleep == nil {
		c.Sleep = time.Sleep
	}
	return &Updater{c: c}
}

// Default timeouts of the check and the download (SPEC v0.4 §5).
const (
	CheckTimeout    = 20 * time.Second
	DownloadTimeout = 5 * time.Minute
	maxReleaseJSON  = 1 << 20
)

// hopPolicy decides whether hop number hop (0 = the first request) may go
// to url. Every hop must also be Allowed.
type hopPolicy func(hop int, url string) bool

func hostIs(raw string, hosts ...string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	for _, h := range hosts {
		if u.Host == h {
			return true
		}
	}
	return false
}

// apiHops: the release API stays on api.github.com.
func apiHops(_ int, raw string) bool { return hostIs(raw, "api.github.com") }

// assetHops: an asset download starts at the pinned release download URL
// and may only be redirected to GitHub's asset storage.
func assetHops(hop int, raw string) bool {
	if hop == 0 {
		return strings.HasPrefix(raw, downloadPrefix)
	}
	return hostIs(raw, "objects.githubusercontent.com", "release-assets.githubusercontent.com")
}

// fetch GETs raw into w, following at most MaxRedirects redirects, each
// Allowed and accepted by policy. Only a 2xx response is a success.
func (u *Updater) fetch(ctx context.Context, raw string, policy hopPolicy, headers map[string]string, w io.Writer, limit int64) error {
	cur := raw
	for hop := 0; ; hop++ {
		if !Allowed(cur) || !policy(hop, cur) {
			return &Error{Code: CodeRedirect, Detail: "refusing " + hostOf(cur)}
		}
		status, loc, err := u.c.Getter.Get(ctx, cur, headers, w, limit)
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return &Error{Code: CodeNetwork, Detail: "timeout", Err: ErrTimeout}
		case ctx.Err() != nil:
			return &Error{Code: CodeCancelled, Err: ctx.Err()}
		case errors.Is(err, ErrTooLarge):
			return &Error{Code: CodeTooLarge, Detail: fmt.Sprintf("over %d bytes", limit)}
		case err != nil:
			return &Error{Code: CodeNetwork, Err: err}
		case status >= 300 && status < 400 && status != 304:
			if hop >= MaxRedirects {
				return &Error{Code: CodeRedirect, Detail: "too many redirects"}
			}
			next, err := resolve(cur, loc)
			if err != nil {
				return &Error{Code: CodeRedirect, Detail: "invalid Location"}
			}
			cur = next
		case status >= 200 && status < 300:
			return nil
		default:
			return &Error{Code: CodeHTTP, Detail: "HTTP " + strconv.Itoa(status) + " from " + hostOf(cur)}
		}
	}
}

func resolve(base, loc string) (string, error) {
	if loc == "" {
		return "", errors.New("empty")
	}
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	l, err := url.Parse(loc)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(l).String(), nil
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return "an invalid URL"
}

// Latest reads the latest release (within the check timeout).
func (u *Updater) Latest(ctx context.Context) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, u.c.CheckTimeout)
	defer cancel()
	var buf bytes.Buffer
	if err := u.fetch(ctx, LatestURL, apiHops, map[string]string{"Accept": "application/vnd.github+json"}, &buf, maxReleaseJSON); err != nil {
		return nil, err
	}
	return ParseRelease(buf.Bytes())
}

// Progress reports the download: received of total bytes.
type Progress func(received, total int64)

// PartPath is the download file of a version.
func (u *Updater) PartPath(version string) string {
	return filepath.Join(u.c.Dir, "jiraffe-"+version+".exe.part")
}

// Fetched is a downloaded, not yet verified release.
type Fetched struct {
	Path string   // the .part file
	Sum  [32]byte // from the .sha256 asset
	Sig  []byte   // from the .sig asset (64 bytes)
}

// Download fetches the release's checksum, signature and executable
// (within the download timeout) into PartPath; VerifyDownload checks it
// next. On failure the partial file is deleted.
func (u *Updater) Download(ctx context.Context, rel *Release, progress Progress) (*Fetched, error) {
	ctx, cancel := context.WithTimeout(ctx, u.c.DownloadTimeout)
	defer cancel()
	octet := map[string]string{"Accept": "application/octet-stream"}
	var sb, gb bytes.Buffer
	if err := u.fetch(ctx, rel.Sum.URL, assetHops, octet, &sb, maxSumFile); err != nil {
		return nil, err
	}
	sum, err := ParseChecksum(sb.Bytes())
	if err != nil {
		return nil, err
	}
	if err := u.fetch(ctx, rel.Sig.URL, assetHops, octet, &gb, maxSig); err != nil {
		if CodeOf(err) == CodeTooLarge || CodeOf(err) == CodeHTTP {
			return nil, &Error{Code: CodeBadSignature, Detail: "signature asset unavailable", Err: err}
		}
		return nil, err
	}
	sig, err := ParseSignature(gb.Bytes())
	if err != nil {
		return nil, err
	}
	if err := u.c.FS.MkdirAll(u.c.Dir); err != nil {
		return nil, &Error{Code: CodeFile, Err: err}
	}
	part := u.PartPath(rel.Version)
	f, err := u.c.FS.Create(part)
	if err != nil {
		return nil, &Error{Code: CodeFile, Err: err}
	}
	cw := &countingWriter{w: f, total: rel.Exe.Size, progress: progress}
	err = u.fetch(ctx, rel.Exe.URL, assetHops, octet, cw, MaxDownload)
	if cerr := f.Close(); err == nil && cerr != nil {
		err = &Error{Code: CodeFile, Err: cerr}
	}
	if err != nil {
		_ = u.c.FS.Remove(part)
		return nil, err
	}
	return &Fetched{Path: part, Sum: sum, Sig: sig}, nil
}

// VerifyDownload checks a download against the release: size from the
// API, the "MZ" header, SHA-256 from the .sha256 asset, and the signature
// over SignedMessage(release version, SHA-256 of the downloaded bytes). It
// deletes the file on any mismatch.
func (u *Updater) VerifyDownload(f *Fetched, rel *Release) error {
	got, err := verifyFile(u.c.FS, f.Path, rel.Exe.Size, f.Sum)
	if err == nil && !VerifySignature(PublicKey, rel.Version, got, f.Sig) {
		err = &Error{Code: CodeBadSignature, Detail: "the signature does not match this file and version"}
	}
	if err != nil {
		_ = u.c.FS.Remove(f.Path)
	}
	return err
}

// countingWriter reports progress while writing.
type countingWriter struct {
	w        io.Writer
	n, total int64
	progress Progress
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if c.progress != nil && n > 0 {
		c.progress(c.n, c.total)
	}
	return n, err
}

// hashFile returns the SHA-256, the size (up to limit+1 bytes read) and the
// first two bytes of path.
func hashFile(fs FS, path string, limit int64) (sum [32]byte, size int64, head [2]byte, err error) {
	f, err := fs.Open(path)
	if err != nil {
		return sum, 0, head, &Error{Code: CodeFile, Err: err}
	}
	defer f.Close()
	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(h, &headWriter{head: &head}), io.LimitReader(f, limit+1))
	if err != nil {
		return sum, 0, head, &Error{Code: CodeFile, Err: err}
	}
	copy(sum[:], h.Sum(nil))
	return sum, size, head, nil
}

// headWriter keeps the first two bytes written.
type headWriter struct {
	head *[2]byte
	n    int
}

func (w *headWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if w.n >= 2 {
			break
		}
		w.head[w.n] = b
		w.n++
	}
	return len(p), nil
}

// Verify checks that path has exactly size bytes, starts with "MZ" and has
// the SHA-256 sum.
func Verify(fs FS, path string, size int64, sum [32]byte) error {
	_, err := verifyFile(fs, path, size, sum)
	return err
}

// verifyFile is Verify returning the SHA-256 computed from the file.
func verifyFile(fs FS, path string, size int64, sum [32]byte) ([32]byte, error) {
	got, n, head, err := hashFile(fs, path, size)
	switch {
	case err != nil:
		return got, err
	case n != size:
		return got, &Error{Code: CodeSizeMismatch, Detail: fmt.Sprintf("%d bytes, expected %d", n, size)}
	case head != [2]byte{'M', 'Z'}:
		return got, &Error{Code: CodeNotExe}
	case got != sum:
		return got, &Error{Code: CodeHashMismatch}
	}
	return got, nil
}

// CheckWritable fails with CodeNoWriteAccess when the folder of the
// executable cannot be written (probe file created and removed).
func (u *Updater) CheckWritable() error {
	probe := filepath.Join(filepath.Dir(u.c.Exe), ".jiraffe-update-probe")
	f, err := u.c.FS.Create(probe)
	if err != nil {
		return &Error{Code: CodeNoWriteAccess, Err: err}
	}
	_ = f.Close()
	_ = u.c.FS.Remove(probe)
	return nil
}

// Swap is an installed update that can still be rolled back.
type Swap struct{ u *Updater }

// renameDelays is the backoff of rename retries (antivirus scanners and
// indexers briefly lock new executables).
var renameDelays = []time.Duration{100 * time.Millisecond, 300 * time.Millisecond, time.Second}

// rename renames with a few retries.
func (u *Updater) rename(from, to string) error {
	err := u.c.FS.Rename(from, to)
	for _, d := range renameDelays {
		if err == nil {
			return nil
		}
		u.c.Sleep(d)
		err = u.c.FS.Rename(from, to)
	}
	return err
}

// Install replaces the running executable with the verified download
// (size and sum as verified):
//
//  1. the download is copied to exe.new while hashing exactly the bytes
//     written; they must match size and sum;
//  2. a stale exe.old of an earlier update is removed, or moved aside to
//     exe.old.<n> if it cannot be removed;
//  3. the running exe is renamed to exe.old (allowed on Windows);
//  4. exe.new is hashed again immediately before
//  5. it is renamed to exe.
//
// Any failure restores the previous state. Residual risk (documented in
// SPEC): a process of the same user that can write the exe folder could
// still replace exe.new between step 4 and 5 (microseconds) — but such a
// process could replace jiraffe.exe itself at any time.
func (u *Updater) Install(downloaded string, size int64, sum [32]byte) (*Swap, error) {
	fs, exe := u.c.FS, u.c.Exe
	staged, old := exe+".new", exe+".old"
	got, n, err := copyHashing(fs, downloaded, staged)
	if err != nil {
		_ = fs.Remove(staged)
		if errors.Is(err, os.ErrPermission) {
			return nil, &Error{Code: CodeNoWriteAccess, Err: err}
		}
		return nil, &Error{Code: CodeInstall, Err: err}
	}
	if n != size || got != sum {
		_ = fs.Remove(staged)
		return nil, &Error{Code: CodeHashMismatch, Detail: "the copy differs from the verified download"}
	}
	if err := u.clearOld(); err != nil {
		_ = fs.Remove(staged)
		return nil, &Error{Code: CodeInstall, Detail: "a previous " + filepath.Base(old) + " is in the way", Err: err}
	}
	if err := u.rename(exe, old); err != nil {
		_ = fs.Remove(staged)
		return nil, &Error{Code: CodeInstall, Err: err}
	}
	restore := func(cause error) error {
		if rerr := u.rename(old, exe); rerr != nil {
			return &Error{Code: CodeInstall, Detail: "rollback failed: " + rerr.Error(), Err: cause, Previous: old}
		}
		_ = fs.Remove(staged)
		return cause
	}
	if err := Verify(fs, staged, size, sum); err != nil {
		return nil, restore(err)
	}
	if err := u.rename(staged, exe); err != nil {
		return nil, restore(&Error{Code: CodeInstall, Err: err})
	}
	return &Swap{u: u}, nil
}

// clearOld makes room for exe.old: removes it, or moves it aside when it
// cannot be removed (e.g. still locked); CleanupOld deletes those later.
func (u *Updater) clearOld() error {
	old := u.c.Exe + ".old"
	if err := u.c.FS.Remove(old); err == nil {
		return nil
	}
	return u.rename(old, old+"."+strconv.FormatInt(time.Now().UnixNano(), 36))
}

// copyHashing copies from → to and returns the SHA-256 and size of the
// bytes written.
func copyHashing(fs FS, from, to string) ([32]byte, int64, error) {
	var sum [32]byte
	in, err := fs.Open(from)
	if err != nil {
		return sum, 0, err
	}
	defer in.Close()
	out, err := fs.Create(to)
	if err != nil {
		return sum, 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, MaxDownload+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	copy(sum[:], h.Sum(nil))
	return sum, n, err
}

// Rollback puts the previous executable back (the new one is moved to
// exe.new and removed), with the same rename retries as Install. A failure
// is a CodeInstall error whose Previous names where the previous exe is.
func (s *Swap) Rollback() error {
	u := s.u
	exe := u.c.Exe
	staged, old := exe+".new", exe+".old"
	if err := u.rename(exe, staged); err != nil {
		return &Error{Code: CodeInstall, Detail: "rollback failed: move the new exe away", Err: err, Previous: old}
	}
	if err := u.rename(old, exe); err != nil {
		if rerr := u.rename(staged, exe); rerr != nil {
			return &Error{Code: CodeInstall, Detail: "rollback failed: no exe in place, the new one is at " + staged,
				Err: errors.Join(err, rerr), Previous: old}
		}
		return &Error{Code: CodeInstall, Detail: "rollback failed: the new exe stays", Err: err, Previous: old}
	}
	_ = u.c.FS.Remove(staged)
	return nil
}

// RestartArgs are the arguments of the new process after an update.
func RestartArgs(fromVersion string, pid int, minimized bool) []string {
	args := []string{"--updated-from", fromVersion, "--wait-pid", strconv.Itoa(pid)}
	if minimized {
		args = append(args, "--minimized")
	}
	return args
}

// Start starts the (new) executable with args.
func (u *Updater) Start(args []string) error {
	if err := u.c.Starter.Start(u.c.Exe, args); err != nil {
		return &Error{Code: CodeStart, Err: err}
	}
	return nil
}

// CleanupDownloads deletes exe.new and every file of the download folder.
// It returns the first failure (e.g. a locked file) after trying all.
func (u *Updater) CleanupDownloads() error {
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	keep(u.c.FS.Remove(u.c.Exe + ".new"))
	names, err := u.c.FS.List(u.c.Dir)
	keep(err)
	for _, n := range names {
		keep(u.c.FS.Remove(filepath.Join(u.c.Dir, n)))
	}
	return first
}

// CleanupOld deletes exe.old and exe.old.<n> (previous versions). Call it
// only once the running version has proven itself (see the app).
func (u *Updater) CleanupOld() error {
	dir, base := filepath.Dir(u.c.Exe), filepath.Base(u.c.Exe)+".old"
	names, err := u.c.FS.List(dir)
	if err != nil {
		return err
	}
	var first error
	for _, n := range names {
		if n == base || strings.HasPrefix(n, base+".") {
			if err := u.c.FS.Remove(filepath.Join(dir, n)); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

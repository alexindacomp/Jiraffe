// Package update checks the GitHub releases of Jiraffe for a newer version,
// downloads and verifies it and swaps the executable (SPEC v0.4 §5).
//
// It is OS independent: HTTP, the file system, process start and the clock
// come in through interfaces (WinHTTP, os, … on Windows; fakes in tests).
// It never uses net/http.
package update

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The release source and the assets every release must carry.
const (
	LatestURL = "https://api.github.com/repos/alexindacomp/Jiraffe/releases/latest"
	// ReleasesURL prefixes the release pages; openUrl may open URLs below it.
	ReleasesURL = "https://github.com/alexindacomp/Jiraffe/releases/"
	// downloadPrefix is where the release assets must be downloaded from.
	downloadPrefix = ReleasesURL + "download/"

	AssetExe = "jiraffe.exe"
	AssetSum = "jiraffe.exe.sha256"
	AssetSig = "jiraffe.exe.sig"

	// MaxDownload bounds the downloaded executable.
	MaxDownload = 20 << 20
	// MaxRedirects bounds the redirects followed for one request.
	MaxRedirects = 5
	// maxNotesRunes bounds the release notes kept (plain text).
	maxNotesRunes = 20000
	// maxSig bounds the signature asset.
	maxSig = 1 << 10
	// maxSumFile bounds the checksum asset.
	maxSumFile = 4 << 10
)

// allowedHosts are the only hosts updater requests may reach (https only).
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

// Allowed reports whether raw is an https URL on an allow-listed host
// (default port, no user info).
func Allowed(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" &&
		allowedHosts[u.Host] && u.Opaque == ""
}

// ReleasePageAllowed reports whether raw is exactly a release page,
// https://github.com/alexindacomp/Jiraffe/releases/tag/vX.Y.Z (canonical
// version) or …/releases/latest: the only URLs outside Jira that openUrl
// may open.
func ReleasePageAllowed(raw string) bool {
	if raw == ReleasesURL+"latest" {
		return true
	}
	ver, ok := strings.CutPrefix(raw, ReleasesURL+"tag/v")
	if !ok {
		return false
	}
	v, ok := ParseVersion(ver)
	return ok && v.String() == ver
}

// ReleasePage is the page of release tag vX.Y.Z.
func ReleasePage(version string) string { return ReleasesURL + "tag/v" + version }

// --- versions ---------------------------------------------------------------

// Version is a parsed X.Y.Z version.
type Version struct{ Major, Minor, Patch int }

// ParseVersion parses "X.Y.Z" or "vX.Y.Z" (decimal numbers only, no
// pre-release or build suffix).
func ParseVersion(s string) (Version, bool) {
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var n [3]int
	for i, p := range parts {
		if p == "" || len(p) > 9 {
			return Version{}, false
		}
		for j := 0; j < len(p); j++ {
			if p[j] < '0' || p[j] > '9' {
				return Version{}, false
			}
		}
		n[i], _ = strconv.Atoi(p)
	}
	return Version{n[0], n[1], n[2]}, true
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Less reports whether v < w.
func (v Version) Less(w Version) bool {
	if v.Major != w.Major {
		return v.Major < w.Major
	}
	if v.Minor != w.Minor {
		return v.Minor < w.Minor
	}
	return v.Patch < w.Patch
}

// Supported reports whether a build of version current takes part in
// updates: development builds ("dev", "0.0.0", anything unparsable) never do.
func Supported(current string) bool {
	v, ok := ParseVersion(current)
	return ok && v != Version{}
}

// Newer reports whether latest is semver-greater than current; always false
// for builds that are not Supported.
func Newer(latest, current string) bool {
	l, ok1 := ParseVersion(latest)
	c, ok2 := ParseVersion(current)
	return ok1 && ok2 && Supported(current) && c.Less(l)
}

// --- releases ---------------------------------------------------------------

// Asset is a release file.
type Asset struct {
	URL  string `json:"url"` // browser_download_url
	Size int64  `json:"size"`
}

// Release is the latest release as far as the updater needs it.
type Release struct {
	Version     string `json:"version"` // X.Y.Z (tag without "v")
	Notes       string `json:"notes"`   // release body, plain text
	PublishedAt string `json:"publishedAt"`
	Page        string `json:"page"` // release page (ReleasePage)
	Exe         Asset  `json:"exe"`
	Sum         Asset  `json:"sum"`
	Sig         Asset  `json:"sig"`
}

// ParseRelease reads a GitHub "release" JSON object. The release must be
// published (not a draft or prerelease), tagged vX.Y.Z and carry
// jiraffe.exe, jiraffe.exe.sha256 and jiraffe.exe.sig (see Release.check).
func ParseRelease(b []byte) (*Release, error) {
	var gh struct {
		TagName     string `json:"tag_name"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(b, &gh); err != nil {
		return nil, badRelease("invalid JSON: %v", err)
	}
	if gh.Draft || gh.Prerelease {
		return nil, badRelease("the latest release is a draft or prerelease")
	}
	v, ok := ParseVersion(gh.TagName)
	if !ok || gh.TagName != "v"+v.String() {
		return nil, badRelease("unexpected tag %q", gh.TagName)
	}
	r := &Release{Version: v.String(), Notes: plainNotes(gh.Body), PublishedAt: gh.PublishedAt, Page: ReleasePage(v.String())}
	for _, a := range gh.Assets {
		var dst *Asset
		switch a.Name {
		case AssetExe:
			dst = &r.Exe
		case AssetSum:
			dst = &r.Sum
		case AssetSig:
			dst = &r.Sig
		default:
			continue
		}
		if dst.URL != "" {
			return nil, badRelease("asset %s listed twice", a.Name)
		}
		*dst = Asset{URL: a.URL, Size: a.Size}
	}
	if err := r.check(); err != nil {
		return nil, err
	}
	return r, nil
}

// assetURL is the only accepted download URL of an asset of version.
func assetURL(version, name string) string { return downloadPrefix + "v" + version + "/" + name }

// check validates a release: canonical version, the three assets at their
// exact download URLs with plausible sizes, page and notes as derived.
func (r *Release) check() error {
	v, ok := ParseVersion(r.Version)
	if !ok || v.String() != r.Version {
		return badRelease("unexpected version %q", r.Version)
	}
	for _, a := range []struct {
		name  string
		asset Asset
		max   int64
	}{{AssetExe, r.Exe, MaxDownload}, {AssetSum, r.Sum, maxSumFile}, {AssetSig, r.Sig, maxSig}} {
		switch {
		case a.asset.URL == "":
			return badRelease("release %s lacks %s", r.Version, a.name)
		case a.asset.URL != assetURL(r.Version, a.name) || !Allowed(a.asset.URL):
			return badRelease("asset %s has an unexpected URL", a.name)
		case a.asset.Size <= 0 || a.asset.Size > a.max:
			return badRelease("%s has an unexpected size %d", a.name, a.asset.Size)
		}
	}
	if r.Page != ReleasePage(r.Version) {
		return badRelease("unexpected release page")
	}
	return nil
}

// Sanitized re-validates a release loaded from storage (state.json): a
// copy with the page and notes derived again, or an error if it is not a
// release ParseRelease would accept.
func (r *Release) Sanitized() (*Release, error) {
	if r == nil {
		return nil, badRelease("no release")
	}
	c := *r
	c.Page = ReleasePage(c.Version)
	c.Notes = plainNotes(c.Notes)
	if err := c.check(); err != nil {
		return nil, err
	}
	return &c, nil
}

// plainNotes keeps the release body as text: valid UTF-8, no NULs, CRLF
// folded, capped.
func plainNotes(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\x00", "")
	if utf8.RuneCountInString(s) > maxNotesRunes {
		s = string([]rune(s)[:maxNotesRunes-1]) + "…" // ≤ maxNotesRunes with the ellipsis
	}
	return strings.TrimSpace(s)
}

// ParseChecksum reads a sha256sum line "<64 hex>  jiraffe.exe" (or
// " *jiraffe.exe", or the hash alone).
func ParseChecksum(b []byte) ([32]byte, error) {
	var sum [32]byte
	line := strings.TrimSpace(string(b))
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		if strings.TrimSpace(line[i:]) != "" {
			return sum, &Error{Code: CodeBadChecksum, Detail: "more than one line"}
		}
		line = line[:i]
	}
	fields := strings.Fields(line)
	if len(fields) == 0 || len(fields) > 2 || len(fields[0]) != 64 {
		return sum, &Error{Code: CodeBadChecksum, Detail: "unexpected format"}
	}
	if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") != AssetExe {
		return sum, &Error{Code: CodeBadChecksum, Detail: "checksum of another file " + strconv.Quote(fields[1])}
	}
	if _, err := hex.Decode(sum[:], []byte(fields[0])); err != nil {
		return sum, &Error{Code: CodeBadChecksum, Detail: "not hexadecimal"}
	}
	return sum, nil
}

// --- errors -----------------------------------------------------------------

// Error codes (updateFailed.code and the check error).
const (
	CodeNetwork       = "network"         // transport failure (Err holds it)
	CodeHTTP          = "http_status"     // unexpected HTTP status (Detail "HTTP 403")
	CodeRedirect      = "bad_redirect"    // redirect outside the allow-list or too many
	CodeTooLarge      = "too_large"       // download over the limit
	CodeBadRelease    = "bad_release"     // release JSON unusable
	CodeBadChecksum   = "bad_checksum"    // .sha256 asset unusable
	CodeSizeMismatch  = "size_mismatch"   // downloaded size ≠ asset size
	CodeHashMismatch  = "hash_mismatch"   // SHA-256 ≠ .sha256
	CodeNotExe        = "not_exe"         // does not start with "MZ"
	CodeBadSignature  = "bad_signature"   // .sig missing, malformed or not valid for this exe and version
	CodeChanged       = "release_changed" // the latest release changed since the check
	CodeNoWriteAccess = "no_write_access" // the exe folder is not writable
	CodeInstall       = "install_failed"  // swapping the exe failed (rolled back)
	CodeStart         = "start_failed"    // the new exe could not be started (rolled back)
	CodeFile          = "file_error"      // local file I/O failed
	CodeCancelled     = "cancelled"       // the app is exiting
)

// ErrTimeout is the cause of a CodeNetwork error when the updater's own
// timeout (check or download) expired.
var ErrTimeout = errors.New("the request to GitHub timed out")

// Error is an updater failure with a stable code.
type Error struct {
	Code   string
	Detail string // English, for logs and as the message suffix
	Err    error  // cause, if any
	// Previous is set when restoring the previous version failed: the path
	// where the previous executable now is.
	Previous string
}

func (e *Error) Error() string {
	msg := "update: " + e.Code
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

func badRelease(format string, args ...any) error {
	return &Error{Code: CodeBadRelease, Detail: fmt.Sprintf(format, args...)}
}

// CodeOf returns the code of an updater error ("" for other errors).
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

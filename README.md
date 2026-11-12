# Jiraffe

A small Windows tray companion for Jira Server/Data Center: automatic daily
worklogs (with a load factor), a board of my or all open issues with a project
filter and each project's own columns (from its agile board or workflow), quick
status changes from a dropdown, issue details (description, comments, history,
estimates, links, epic and stories), sprints as a backlog-style list grouped by
sprint, epics & stories, tracked projects and alerts about new comments on my
issues, a per-project epic time dashboard and automatic updates. One
self-contained `jiraffe.exe` (Go, no CGO, WebView2 UI, about 4.6 MB). The full specification is in [SPEC.md](SPEC.md).

## Download

**[⬇ jiraffe.exe](https://github.com/alexindacomp/Jiraffe/releases/latest/download/jiraffe.exe)** — latest release, Windows 10/11 x64, about 4.6 MB.
All versions and changes: [Releases](https://github.com/alexindacomp/Jiraffe/releases).

Put it somewhere permanent, e.g. `%LOCALAPPDATA%\Programs\Jiraffe\jiraffe.exe`
(autostart points to the exe's current location), run it and enter your Jira URL
and personal access token. Requires the Microsoft Edge WebView2 Runtime
(preinstalled on Windows 10/11).

Binaries are built by GitHub Actions ([.github/workflows/build.yml](.github/workflows/build.yml)),
not committed to the repo. Every push to `master` and every pull request runs
`go vet`, `go test` and the build, and keeps `jiraffe.exe` as a workflow artifact.
To publish a version, push a tag — the version is taken from it (no files to bump):

```sh
git tag -a v0.4.0 -m "Jiraffe v0.4.0" -m "- what changed"   # tag message = release notes
git push origin v0.4.0
```

The workflow builds `jiraffe.exe` with version 0.4.0, signs it and publishes the
GitHub release `v0.4.0` (assets `jiraffe.exe`, `jiraffe.exe.sha256`,
`jiraffe.exe.sig`) as the latest one, so the download link above serves it and
installed copies offer the update.

**Release signing.** The release job runs in the GitHub environment `release`
(only `v*` tags may use it), which holds the secret `RELEASE_SIGNING_KEY`
(base64 ed25519 seed). `tools/sign` signs
`"jiraffe-release-v1\n" + version + "\n" + sha256(exe) + "\n"`; the app embeds
the public key (`internal/update/sign.go`) and refuses an exe without a valid
signature. Keep an offline backup of the seed; the rotation procedure is in
SPEC.md §5.

## Build (WSL / Linux)

```sh
./build.sh            # → build/jiraffe.exe, prints the size (budget 5 MB)
./build.sh --check    # go vet (linux + windows) and go test first
VERSION=1.2.0 ./build.sh   # default version: the latest vX.Y.Z git tag
```

`build.sh` generates the icon (`tools/icongen` → `assets/icon.ico`), embeds icon,
manifest (PerMonitorV2 DPI awareness, common controls v6) and version info as
`rsrc_windows_amd64.syso` via `go run github.com/tc-hib/go-winres@v0.3.3`
(the tool is not added to go.mod and nothing of it ends up in the binary), then
cross-compiles with `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 -ldflags "-s -w -H windowsgui"`.

Tests run on Linux: `go vet ./... && go test ./...`.

Requires the Microsoft Edge WebView2 Runtime (preinstalled on Windows 10/11).

## Running

| command | effect |
|---|---|
| `jiraffe.exe` | starts in the tray and opens the window (or shows the running instance) |
| `jiraffe.exe --minimized` | tray only (used by autostart) |
| `jiraffe.exe --updated-from X.Y.Z --wait-pid N` | used by the updater for the restart: waits up to 15 s for the old process, then starts normally |
| `jiraffe.exe --worklog-preview out.json` | headless: computes today's worklog preview, writes `WorklogRun` JSON, exits 0/1; never writes to Jira. `JIRAFFE_URL` / `JIRAFFE_TOKEN` override the saved settings (from WSL pass them with `WSLENV=JIRAFFE_URL:JIRAFFE_TOKEN`) |

Files:

- `%APPDATA%\Jiraffe\config.json` — settings; the token is stored as `tokenEnc`
  (DPAPI, current user, base64), never in plain text
- `%APPDATA%\Jiraffe\state.json` — last scheduled run date, worklog history (60),
  notifications (200), seen comment ids (5000), skipped update version, last update check
- `%LOCALAPPDATA%\Jiraffe\update` — update downloads (temporary)
- `%APPDATA%\Jiraffe\jiraffe.log` — log, rotated to `.log.1` at 1 MB; the token is never logged
- `%LOCALAPPDATA%\Jiraffe\WebView2` — WebView2 user data
- `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\Jiraffe` — autostart

## Layout

```
main.go                 embeds ui/, calls run()
main_windows.go         wiring, single instance, tray + webview main-thread loop, JS binding
main_other.go           stub for non-Windows builds
internal/worklog        pure: in-progress intervals, clipping, allocation, durations, calendar
internal/jira           REST v2 client over a Transport interface
internal/config         settings, defaults, validation (ru/uk/en messages), DPAPI via Protector
internal/state          history, notifications, seen comments (atomic JSON writes)
internal/scheduler      worklog-at-time with catch-up/retry + comment polling, injected clock
internal/app            JS ↔ Go contract (methods, events, errors), worklog/comment/update services
internal/update         self-update: GitHub release check, download, verify (sha256 + ed25519), swap
internal/webui          inlines ui/*.css|*.js into one HTML document, drops mock.js, adds CSP
internal/winapi         *_windows.go: WinHTTP transport, DPAPI, tray/balloons, autostart, shell
internal/applog         rotating log writer
tools/icongen           build-time icon generator
tools/sign              CI release signer (ed25519, key from env)
assets/winres.json      go-winres resource definition
```

## Design notes

- **No `net/http`.** HTTP goes through WinHTTP (`winhttp.dll` syscalls): it uses
  the Windows certificate store (corporate CAs), the system/automatic proxy and
  keeps the binary small. Certificate checks are never disabled.
- **Threading.** go-webview2 runs `Bind` callbacks on the UI thread, so there is a
  single binding `window.__jiraffe(id, method, paramsJSON, nonce)` that returns
  at once and runs the method in a goroutine; the result comes back through
  `window.__jiraffeResolve(id, ok, payload)` (payload is a JSON value; on error
  `{"code","message","fields"?}` with a message in the UI language). `nonce` is
  a random 128-bit value generated at startup and inlined only into the app
  document (`<script>window.__jiraffeNonce="…"</script>`, first in `<head>`);
  calls without it are rejected with `{"code":"forbidden"}`. Goroutines never touch the webview directly:
  closures are queued and executed on the main thread via a message posted to
  the hidden tray window (`WM_APP+2`), which works both inside `webview.Run()` and
  in the app's own loop while the window is closed. Closing the window destroys
  the webview; the process keeps running in the tray.
- **Worklog** is a port of the proven Python script: per-issue in-progress
  intervals from the status changelog, clipped to working hours minus lunch,
  parallel time split equally, already-logged time subtracted, daily cap with
  proportional scaling, whole minutes. Scheduled run at `worklogTime` on working
  days with same-day catch-up; failures are retried every 15 minutes (max 4 per day).
- **Security.** The page gets a strict CSP (no network) and the binding nonce;
  files/links dropped from outside are cancelled so they cannot replace the app
  page. `openUrl` only opens http(s) links under the configured Jira URL. The
  stored token is only ever sent to the saved Jira origin (scheme + host +
  port): changing it requires entering the token again (`saveSettings` field
  error `token`; `testConnection` asks for a token; in headless mode a
  `JIRAFFE_URL` of another origin without `JIRAFFE_TOKEN` is "not configured").
  WinHTTP never follows redirects (they would resend the token); a 3xx is
  reported with the target host. Saving an `http://` URL returns a warning.
- **Updates.** 30 s after start and then daily the app asks
  `api.github.com/repos/alexindacomp/Jiraffe/releases/latest` (separate WinHTTP
  session: no Jira token or headers; redirects followed manually only to the
  GitHub release-asset hosts). A newer signed release is offered in a dialog
  (or a tray balloon when the window is closed); "Later" skips that version,
  Settings → Updates can install it any time or turn the startup check off.
  Install: download to `%LOCALAPPDATA%\Jiraffe\update`, verify size, SHA-256,
  `MZ` and the ed25519 signature, rename the running exe to `jiraffe.exe.old`,
  move the new one in (rollback on any failure), start it with `--updated-from`
  and exit. `.old` is removed after the new version has run 10 minutes.
- **Errors** from Jira/WinHTTP become readable messages; every goroutine and
  binding call recovers from panics and logs them.

# Jiraffe

A small Windows tray companion for Jira Server/Data Center: automatic daily
worklogs, a board of my open issues, quick status changes and alerts about new
comments on my issues. One self-contained `jiraffe.exe` (Go, no CGO, WebView2
UI, about 3.6 MB). The full specification is in [SPEC.md](SPEC.md).

## Download

**[⬇ jiraffe.exe](https://github.com/alexindacomp/Jiraffe/raw/master/dist/jiraffe.exe)** — Windows 10/11 x64, about 3.6 MB.

Put it somewhere permanent, e.g. `%LOCALAPPDATA%\Programs\Jiraffe\jiraffe.exe`
(autostart points to the exe's current location), run it and enter your Jira URL
and personal access token. Requires the Microsoft Edge WebView2 Runtime
(preinstalled on Windows 10/11).

To publish a new build, run `./build.sh` and copy `build/jiraffe.exe` to `dist/`.

## Build (WSL / Linux)

```sh
./build.sh            # → build/jiraffe.exe, prints the size (budget 5 MB)
./build.sh --check    # go vet (linux + windows) and go test first
VERSION=1.2.0 ./build.sh
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
| `jiraffe.exe --worklog-preview out.json` | headless: computes today's worklog preview, writes `WorklogRun` JSON, exits 0/1; never writes to Jira. `JIRAFFE_URL` / `JIRAFFE_TOKEN` override the saved settings (from WSL pass them with `WSLENV=JIRAFFE_URL:JIRAFFE_TOKEN`) |

Files:

- `%APPDATA%\Jiraffe\config.json` — settings; the token is stored as `tokenEnc`
  (DPAPI, current user, base64), never in plain text
- `%APPDATA%\Jiraffe\state.json` — last scheduled run date, worklog history (60),
  notifications (200), seen comment ids (5000)
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
internal/app            JS ↔ Go contract (methods, events, errors), worklog/comment services
internal/webui          inlines ui/*.css|*.js into one HTML document, drops mock.js, adds CSP
internal/winapi         *_windows.go: WinHTTP transport, DPAPI, tray/balloons, autostart, shell
internal/applog         rotating log writer
tools/icongen           build-time icon generator
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
- **Errors** from Jira/WinHTTP become readable messages; every goroutine and
  binding call recovers from panics and logs them.

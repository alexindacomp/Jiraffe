# Jiraffe — specification

A small Windows desktop companion for Jira Server/Data Center:
automatic daily worklogs, a board of my open issues, quick status changes
and alerts about new comments on my issues.

## Hard constraints

- Language: **Go** (toolchain in WSL: `export PATH=$PATH:/usr/local/go/bin`, Go 1.24).
- One self-contained Windows binary `jiraffe.exe`, built from WSL by cross-compiling
  (`GOOS=windows GOARCH=amd64 CGO_ENABLED=0`). **No CGO.**
- Binary size **≤ 5 MB** without UPX. Measured: go-webview2 + encoding/json + embed ≈ 2.6 MB;
  adding `net/http` alone makes it 6.4 MB. Therefore:
  - **Do not import `net/http`, `crypto/tls`, `net/url`-heavy libs, or anything that pulls them in.**
    HTTP goes through **WinHTTP** (`winhttp.dll` via `syscall`/`golang.org/x/sys/windows`).
    WinHTTP also uses the Windows certificate store (the corporate Jira `https://sd.local`
    uses a corporate CA that only the Windows store trusts) and the system proxy.
  - Check the size after every build: `ls -l build/jiraffe.exe`.
- UI: **WebView2** via `github.com/jchv/go-webview2` (pure Go, already in go.mod) with an
  embedded single-page UI (`ui/` → `//go:embed`). No CDN, no npm, no build step: plain
  HTML + CSS + vanilla JS (ES2020, modules allowed if embedded and served via `SetHtml`/inline).
- Modern UI: clean, Fluent/Linear-like, light + dark (follows `prefers-color-scheme`),
  system font stack (`"Segoe UI Variable", "Segoe UI", system-ui`), smooth transitions,
  keyboard friendly.
- Never disable TLS certificate verification.

## Project layout (target)

```
jiraffe/
  main.go                     // windows: wiring, single instance, main-thread loop
  internal/worklog/           // PURE Go, no OS deps: allocation + duration format + day rules (unit-tested on Linux)
  internal/jira/              // Jira REST client over a Transport interface (unit-tested with a fake)
  internal/config/            // config struct, defaults, validation, load/save JSON (token encrypted via a Protector interface)
  internal/scheduler/         // worklog-at-time + comment poller; clock injected (unit-tested)
  internal/winapi/            // *_windows.go: winhttp transport, DPAPI, tray+balloons, autostart (HKCU Run), shell open, single instance
  internal/app/               // binding layer: methods exposed to JS (see contract)
  ui/index.html, ui/app.css, ui/app.js, ui/i18n.js, ui/mock.js
  assets/                     // icon (generated), manifest; winres config
  build.sh                    // cross-compile from WSL, embeds icon/manifest (.syso via go-winres run with `go run`), prints size
  README.md
```

Pure packages must build and test on Linux: `go test ./internal/...` (Windows-only files
use `_windows.go` suffix; provide `_other.go` stubs where needed so `go vet ./...` passes on Linux).

## Runtime behaviour

- Starts to tray. CLI flag `--minimized` (used by autostart) → no window, tray only.
  Otherwise opens the main window.
- **Single instance**: named mutex `Local\Jiraffe`; a second launch tells the running
  instance to show its window (e.g. `FindWindow` on the tray's hidden window class +
  custom message) and exits.
- **Tray icon** (Shell_NotifyIconW on a hidden message-only window owned by the main
  thread): left click → open/focus window; right click → menu: Open, Fill worklog now,
  Pause/Resume notifications, Quit.
- **Window close = hide to tray**: the webview window is destroyed, the process keeps running;
  opening again creates a new webview. Quit only from tray menu.
- **Threading** (important, verified in go-webview2 source):
  - go-webview2 `Bind` callbacks run synchronously on the UI thread → long calls freeze UI.
    Expose ONE binding `__jiraffe(id, method, paramsJSON)` that returns immediately and
    runs the method in a goroutine; deliver the result with
    `w.Dispatch(func(){ w.Eval("window.__jiraffeResolve(id, ok, payloadJSON)") })`.
  - `w.Run()` pumps the thread's message queue (so it also serves the tray window); when the
    webview window is closed it calls PostQuitMessage and `Run` returns. The main thread
    then runs its own GetMessage loop until a "show window" request arrives, then
    creates a new webview. Use a custom thread message ≠ `WM_APP (0x8000)` (go-webview2 uses WM_APP).
  - go-webview2 `New*` returns nil if the WebView2 runtime is missing → MessageBox with a hint and exit.
  - WebView2 user data folder: `%LOCALAPPDATA%\Jiraffe\WebView2`.
- **Notifications**: tray balloon (`NIM_MODIFY` + `NIF_INFO`; Windows 10/11 shows them as toasts).
  Clicking the balloon (`NIN_BALLOONUSERCLICK`) opens the related issue in the default browser.
  No PowerShell, no console windows ever flash (`-H windowsgui`).
- **Config** at `%APPDATA%\Jiraffe\config.json`; token stored encrypted with DPAPI
  (`CryptProtectData`, current user) as base64. State (last worklog run date, seen comment
  ids, history, notifications) in `%APPDATA%\Jiraffe\state.json`. Log file `%APPDATA%\Jiraffe\jiraffe.log` (rotated at ~1 MB).
- **Autostart**: `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\Jiraffe = "<exe path>" --minimized`.
- **Headless check mode** (for testing from WSL without UI): `jiraffe.exe --worklog-preview <out.json>`
  reads `JIRAFFE_URL` / `JIRAFFE_TOKEN` env vars if set (else config), computes the worklog
  preview for today (never writes to Jira), writes the JSON result to the file, exits.
  Exit code 0 on success, 1 on error (error text in the JSON).

## Jira (Server/DC, REST API v2, `Authorization: Bearer <PAT>`)

- `GET /rest/api/2/myself` — connection test, current user (`name`, `displayName`, `key`).
- `GET /rest/api/2/search?jql=…&fields=…&expand=changelog&maxResults=100&startAt=…` — paginate.
- `GET /rest/api/2/issue/{key}/worklog`, `POST /rest/api/2/issue/{key}/worklog`
  body `{"timeSpentSeconds": N, "started": "2026-11-11T09:00:00.000+0200"}`
  (format the *display* duration separately; send seconds).
- `GET /rest/api/2/issue/{key}/transitions`, `POST …/transitions` body `{"transition":{"id":"…"}}`.
- `GET /rest/api/2/issue/{key}/comment` (or `fields=comment` in search).
- Status names seen in this Jira: Backlog, ToDo, In Progress (displayed "В работе"), ForReview,
  InReview, ForTesting, Предоставлено решение, Закрыт, Закрыт автоматически, Готово, Reject, Отложен.
  Status categories: `new` (К выполнению), `indeterminate` (В работе), `done` (Выполнено).
  Changelog status items carry `fromString`/`toString` and `from`/`to` status ids.
  Jira time tracking here: **1d = 9h**, 1w = 5d (only used for display formatting).
- Timestamps like `2026-11-09T14:24:46.807+0200` (note: offset without colon) — parse robustly.

## Worklog rules (port of the proven Python script /home/alex/jira-worklog/worklog.py)

Run once per **working day** at `worklogTime` (default 18:00, Europe/Kyiv = local Windows time).
Working day = weekday in `workDays` and not in any vacation range and not a holiday.
If the app was not running at that time, run once when it starts later the same day
(catch-up), never for past days. Re-runs are idempotent.

1. Issues: JQL
   `(assignee = currentUser() OR status changed BY currentUser() DURING (startOfDay(), now())) AND (status changed DURING (startOfDay(), now()) OR statusCategory = "In Progress")`
   with `expand=changelog`.
2. For each issue rebuild intervals spent in an *in-progress* status (names in
   `inProgressStatuses`, case-insensitive; default `In Progress`, `В работе`, `В роботі`) from
   the status changelog (initial status = first change's `fromString`, or the current status if no changes;
   initial interval starts at issue `created`). Open interval ends at `now`.
3. Clip to today's working hours `[workdayStart, min(now, workdayEnd)]` and cut out
   `[lunchStart, lunchEnd]`. Defaults: 09:00–18:00, lunch 13:00–14:00 ⇒ 8h.
4. Split time between parallel issues: for every elementary time slice, divide it equally
   among the issues in progress during that slice.
5. Subtract what *I* already logged today on that issue (worklog author = me, `started` date = today).
6. Daily cap `dailyCapHours` (default 8) covers **all my worklogs started today**, on any issue
   (found with `worklogAuthor = currentUser() AND worklogDate …`, then filtered to my worklogs
   whose `started` local date is today): if `sum(todo) > cap − allMyLoggedToday`,
   scale all todo proportionally. Round each down to whole minutes. Skip < 1 minute.
   If my time logged today cannot be determined, the run fails (nothing is logged).
7. Worklog `started` = start of the issue's first clipped interval today.
8. Duration display format: Jira style `1w 2d 3h 15m` with `hoursPerDay` (9) and 5 days/week.

Reference scenarios (must be unit tests), day 09:00–18:00, lunch 13–14, cap 8h:
| scenario | expected |
|---|---|
| A in progress since yesterday, still open at 18:00 | A 8h, start 09:00 |
| A and B both in progress all day | 4h + 4h |
| A all day, B 15:00–17:00 | A 7h, B 1h |
| A 10:00–15:30 then ForReview | A 4h 30m (lunch excluded), start 10:00 |
| A taken 13:30, ForReview 16:00 | A 2h, start 14:00 |
| A,B all day, C from 12:00, A already logged 2h today | A 1h 10m, B 3h 10m, C 1h 40m (total new 6h + logged 2h = 8h) |
| A,B all day, B already logged 6h, A logged 0 | shares 4h/4h → todo A 4h, B 0; budget 8h−6h=2h ⇒ A 2h |
| run twice | second run logs nothing |
| weekend / vacation day / holiday | nothing logged, reason reported |

## Comment alerts

Every `commentPollMinutes` (default 5) while enabled: JQL
`assignee = currentUser() AND updated >= -<N+1>m` with `fields=summary,comment`; any comment
whose id is not in the seen set and whose author ≠ me → notification (stored, max 200,
balloon shown, event sent to the UI). On first run ever, mark existing comments as seen
(no flood). Seen-id set capped (e.g. keep last 5000).

## Settings (JSON, all with defaults)

| key | default | notes |
|---|---|---|
| `jiraUrl` | `""` | e.g. `https://sd.local` |
| `token` | — | write-only from UI; stored DPAPI-encrypted; UI gets `hasToken` |
| `language` | `"ru"` | `ru` / `uk` / `en` |
| `autostart` | `true` | writes/removes the HKCU Run value |
| `worklogEnabled` | `true` | |
| `worklogTime` | `"18:00"` | HH:MM local |
| `workdayStart` / `workdayEnd` | `"09:00"` / `"18:00"` | |
| `lunchStart` / `lunchEnd` | `"13:00"` / `"14:00"` | empty = no lunch |
| `dailyCapHours` | `8` | |
| `hoursPerDay` | `9` | display format only |
| `workDays` | `[1,2,3,4,5]` | ISO weekday, Mon=1 … Sun=7 |
| `vacations` | `[]` | `[{"from":"2026-12-24","to":"2027-01-02","note":"…"}]` inclusive |
| `holidays` | `[]` | `["2026-12-25", …]` |
| `inProgressStatuses` | `["In Progress","В работе","В роботі"]` | |
| `notifyComments` | `true` | |
| `commentPollMinutes` | `5` | 1–60 |
| `boardJql` | `assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC` | advanced |

Validation errors are returned per field: `{"field":"worklogTime","message":"…"}`.

## JS ↔ Go contract

JS calls `await api(method, params)` (defined in `ui/app.js` on top of the binding
`window.__jiraffe(id, method, paramsJSON)`; results arrive via
`window.__jiraffeResolve(id, ok, payload)`; on error payload is `{"code":"…","message":"…","fields":[…]?}`).
Go pushes events with `window.dispatchEvent(new CustomEvent("jiraffe", {detail:{type, payload}}))`.

Types (JSON, camelCase):
```
Issue      { key, summary, url, project, status: Status, type: {name}, priority: {name}, updated (RFC3339), assignee: {displayName} }
Status     { id, name, category: "new"|"indeterminate"|"done" }
Transition { id, name, to: Status }
WorklogItem{ key, summary, status, shareSeconds, loggedSeconds, todoSeconds, duration (e.g. "2h 56m"), started (RFC3339), result?: "logged"|"skipped"|"error", error? }
WorklogRun { at (RFC3339), date ("2026-11-11"), trigger: "schedule"|"manual"|"preview", items: [WorklogItem], totalSeconds, total (formatted), skippedReason?: "weekend"|"vacation"|"holiday"|"disabled"|"not-configured", error? }
Notification { id, issueKey, issueSummary, author, body (plain text, ≤ 300 chars), created, url, read }
Settings   { …table above…, hasToken }
```

Methods:
| method | params | result |
|---|---|---|
| `getState` | – | `{ configured, user: {name, displayName}?, version, language, unread, nextWorklogAt? (RFC3339), lastWorklog?: WorklogRun }` |
| `getSettings` | – | `Settings` |
| `testConnection` | `{ jiraUrl, token? }` (token omitted ⇒ stored one) | `{ ok, displayName?, error? }` |
| `saveSettings` | `Settings` (+ optional `token`) | `Settings` (or error with `fields`) |
| `getBoard` | `{ force?: bool }` | `{ columns: [{ status: Status, issues: [Issue] }], fetchedAt }` — columns ordered by category new → indeterminate → done, then by first appearance |
| `getTransitions` | `{ key }` | `[Transition]` |
| `transition` | `{ key, transitionId }` | `Issue` (fresh) |
| `openUrl` | `{ url }` | `null` — only http(s) URLs under `jiraUrl` |
| `worklogPreview` | – | `WorklogRun` (trigger "preview", nothing written) |
| `worklogRun` | – | `WorklogRun` (trigger "manual") |
| `getWorklogHistory` | – | `[WorklogRun]` newest first, max 60 |
| `getNotifications` | – | `[Notification]` newest first |
| `markNotificationsRead` | `{ ids?: [string] }` (omitted ⇒ all) | `{ unread }` |

Events: `notification` (Notification), `worklog` (WorklogRun), `board` (`{}` – board may be stale, refetch),
`state` (same shape as getState).

## UI

Single window, min 960×600, default 1200×780. Left nav rail (icons + labels): **Board**,
**Worklog**, **Notifications** (badge), **Settings**. Top bar: search box (filters cards by key/summary),
project filter chips, refresh button, connection/user avatar.

- **Board**: kanban columns per status (horizontal scroll), cards with key, summary (2 lines),
  type + priority badges, relative "updated". Drag a card to another column → look up a
  transition whose `to.id` (or name) matches that column; one match → run it (optimistic move,
  revert + toast on error); several → small picker; none → toast "no transition". Card click →
  side drawer: details, all available transitions as buttons, "Open in Jira". Empty/loading/error states.
  Keyboard: `/` focuses search, `r` refresh.
- **Worklog**: today card (next run time, working-day status/reason), preview table
  (issue, status, share, already logged, will log, start) with total vs cap bar, buttons
  "Preview" and "Log now" (confirm dialog), history list of past runs.
- **Notifications**: list (author, issue, snippet, time), unread highlight, "mark all read", click → open in Jira.
- **Settings**: sections Connection (URL, token field with show/hide, Test button), Worklog
  (enable, time, workday, lunch, cap, statuses), Calendar (work days toggles Mon–Sun, vacations list
  add/remove with date range, holidays list), Notifications (enable, interval), Application
  (language ru/uk/en applied instantly, autostart). Save with inline field errors.
- First start (not configured) → onboarding card asking for Jira URL + token.
- i18n: all strings via `i18n.js` dictionaries for ru, uk, en.
- `ui/mock.js`: when `window.__jiraffe` is absent (UI opened in a normal browser),
  install a fake backend with realistic data (statuses above, ~12 issues) so the UI can be
  developed and reviewed in a browser without Windows.

## Acceptance criteria

1. `./build.sh` in WSL produces `build/jiraffe.exe` ≤ 5 MB, no CGO, with app icon and manifest (DPI aware, comctl v6).
2. `go vet ./...` and `go test ./...` pass on Linux; worklog reference scenarios are covered.
3. All Settings from the table are editable in the UI, persisted, validated; token is DPAPI-encrypted on disk.
4. Worklog runs automatically at the configured time on working days only, catch-up on late start, idempotent; manual preview/run from UI; history kept.
5. Board shows my not-done issues grouped by status; status change via drag & drop and via drawer buttons.
6. New comments by others on my issues produce a tray toast + in-app notification.
7. Autostart toggle works (HKCU Run), window close hides to tray, single instance.
8. UI works in ru/uk/en, light/dark, without network resources.
9. `jiraffe.exe --worklog-preview out.json` works headless.

---

# v0.2 — all issues, projects, issue details, sprints, epics & stories, load factor, redesign

Everything above stays valid unless changed here. Facts from the real Jira (sd.local, Server/DC):

- Issue type names are localized in responses ("Epic", "История", "Задача", sub-task types),
  but JQL accepts canonical names: `issuetype = Epic`, `issuetype = Story` work.
  Classify issues by **issuetype id**: learn the Epic/Story type ids from the results of the
  `issuetype = Epic` / `issuetype = Story` queries (and from `/rest/api/2/issuetype` where the
  name matches `Epic`/`Story` case-insensitively); `issuetype.subtask == true` ⇒ sub-task; else task.
- Custom field ids differ between Jira instances — **discover them** via `GET /rest/api/2/field`
  (cache per connection) by `schema.custom`:
  `com.pyxis.greenhopper.jira:gh-epic-link` (Epic Link, here customfield_10103, value = epic key),
  `…:gh-epic-label` (Epic Name, 10105), `…:gh-epic-color` (Epic Color, 10106, values like `ghx-label-4`),
  `…:gh-sprint` (Sprint, 10107 — array of strings `com.atlassian.greenhopper.service.sprint.Sprint@…[id=6338,…,name=…,state=CLOSED,…]`
  or objects; parse both), Story Points = a number field whose `clauseNames` contain `Story Points`
  (here customfield_10130). Missing field ⇒ feature degrades gracefully (no crash, empty value).
- Story → task relation in this Jira is via **issue links** (any link type, e.g. "relates to",
  "blocks") and sub-tasks; tasks and stories belong to an epic via Epic Link.
- Agile REST: `GET /rest/agile/1.0/board?projectKeyOrId=KEY&maxResults=50`,
  `GET /rest/agile/1.0/board/{id}/sprint?state=active,future` and `state=closed` (paginated, `isLast`;
  ordered oldest first). Sprint issues via JQL `sprint = {id}`.

## Settings additions

| key | default | notes |
|---|---|---|
| `loadFactor` | `1.0` | 0.0–1.0 (step 0.05). Effective daily cap = `dailyCapHours × loadFactor` |
| `trackedProjects` | `null` | list of project keys. `null` = not initialised: on first need, initialise to the distinct projects of my not-done issues and persist |

`boardJql` stays (advanced) and defines the "mine" board query.

**Tracked projects** filter the board (both scopes), sprints (board list), epics & stories and
comment alerts (post-filter by project key). The **worklog ignores this filter** — time is logged
for everything I actually worked on, otherwise the daily cap would be wrong.

## Worklog change — load factor

Step 6 cap becomes `capSeconds = round(dailyCapHours × loadFactor × 3600)`. Everything else is the
same. `WorklogRun` gets `capSeconds`. Tests (day 09–18, lunch 13–14, cap 8h):
| scenario | expected |
|---|---|
| loadFactor 0.6, A all day | A 4h 48m |
| loadFactor 0.5, A and B all day | 2h + 2h |
| loadFactor 0.5, A all day, already logged 3h today elsewhere | A 1h |
| loadFactor 0 | nothing logged, items show todo 0 |
| loadFactor 1.0 | identical to v0.1 results |
Validation: number, 0 ≤ x ≤ 1, field error otherwise.

## Types (additions)

```
Kind       "epic" | "story" | "task" | "subtask"
IssueType  { name, kind, iconColor? }
User       { name, displayName }
Issue      + { id, type: IssueType, assignee?: User, mine: bool, epic?: { key, name, color }, storyPoints?: number }
IssueRef   { key, summary, url, status: Status, type: IssueType, assignee?: User, mine: bool }
Progress   { done, inProgress, todo, total }          // counted by status category
Project    { key, name, category?, lead?: string, tracked: bool, myOpen: number, available: bool }   // available=false: tracked key Jira no longer lists (shown only to untrack)
Sprint     { id, name, state: "active"|"future"|"closed", startDate?, endDate?, completeDate?, goal?, boardId }
AgileBoard { id, name, type: "scrum"|"kanban", projectKey }
Comment    { id, author: User, created, updated, body }          // body = raw Jira wiki markup
ChangeItem { field, from, to }                                    // display strings (fromString/toString, fallback from/to)
Change     { id, author: User, created, items: [ChangeItem] }
Link       { relation, issue: IssueRef }   // relation as seen from this issue: outward ⇒ type.outward, inward ⇒ type.inward
IssueDetail {
  issue: Issue, description (raw wiki markup, "" if none), reporter?: User, creator?: User,
  created, updated, dueDate?, resolution?, labels: [], components: [], fixVersions: [],
  priority?, timeTracking: { originalSeconds?, remainingSeconds?, spentSeconds?,
                             original?, remaining?, spent? },   // formatted with hoursPerDay
  storyPoints?, sprints: [Sprint],
  epic?: IssueRef + { name, color },  // Epic Link target
  parent?: IssueRef,                  // for sub-tasks
  stories: [IssueRef],                // stories linked to this issue (for a task)
  children: [IssueRef],               // epic ⇒ issues with this Epic Link; story ⇒ sub-tasks + linked non-epic/non-story issues; task ⇒ sub-tasks
  links: [Link], comments: [Comment] (oldest first, max 200), changelog: [Change] (newest first),
  watchers?: number
}
StoryNode  { story: IssueRef, epic?: IssueRef + { name, color } (getStories only), tasks: [IssueRef], progress: Progress, storyPoints? }
EpicNode   { epic: IssueRef + { name, color }, stories: [StoryNode], tasks: [IssueRef], progress: Progress }
```
Epic tree rules: an epic contains its stories (Epic Link) and its tasks (Epic Link, not a story,
not linked to one of the epic's stories). A story contains its sub-tasks and linked issues that are
neither epics nor stories (dedupe by key; a task linked to several stories appears under each).
Progress of an epic counts every distinct descendant issue. Stories without Epic Link are
"orphan stories". Sort: not-done first, then by status category (indeterminate, new, done), then key desc.
`scope: "mine"` keeps an epic/story if it, or any descendant, is assigned to me.
Limits: max 300 epics/stories and 1000 children per call ⇒ `truncated: true`.

## Methods (additions / changes)

| method | params | result |
|---|---|---|
| `getBoard` | `{ force?, scope?: "mine"\|"all" }` (default "mine") | `{ columns, fetchedAt, scope, truncated, hidden?: { count, projects: [key] } }` (`hidden` = my issues filtered out because their project is not tracked; always present for scope mine — `{count:0, projects:[]}` when none — absent for all). "mine" = `boardJql`; "all" = `project in (tracked) AND statusCategory != Done ORDER BY updated DESC`, max 500 issues. Both post-filtered by tracked projects. Cache per scope |
| `getIssue` | `{ key }` | `IssueDetail` (`expand=changelog`, all comments) |
| `getProjects` | `{ force? }` | `[Project]` all projects I can browse (`/rest/api/2/project`), tracked first, then by name. `myOpen` = count of my not-done issues per project (one JQL) |
| `setProjectTracked` | `{ key, tracked }` | `[Project]`; persists `trackedProjects`; invalidates board/epics/sprint caches and emits `board` |
| `getAgileBoards` | `{ force? }` | `[AgileBoard]` of tracked projects (deduped by id) |
| `getSprints` | `{ boardId }` | `[Sprint]`: active, then future, then the last 5 closed (newest first) |
| `getSprint` | `{ sprintId, scope? }` | `{ sprint, columns (as board, including done), progress: Progress, storyPoints: { done, total }, truncated }` |
| `getEpics` | `{ includeDone?, scope?, force? }` | `{ epics: [EpicNode], orphanStories: [StoryNode], fetchedAt, truncated }` — epics of tracked projects, not-done unless `includeDone` |
| `getStories` | `{ includeDone?, scope?, force? }` | `{ stories: [StoryNode], fetchedAt, truncated }` |
| `getUiPrefs` | – | object (`{}` if none) — UI preferences stored in state.json (`uiPrefs`) |
| `setUiPrefs` | `{ prefs: object }` | `null`; whole object replaced, ≤ 32 KB compact JSON |

Errors as before (`{code,message}` localized). Agile API unavailable (404) ⇒ `getAgileBoards` returns `[]`, UI says sprints are unavailable.

## UI v0.2

Nav rail: **Board, Sprints, Epics & stories, Worklog, Notifications, Projects, Settings**.

- **Board**: a toggle in the top bar **"Only mine"** (checkbox/switch, remembered in localStorage;
  default on) switching `scope`. In "all" mode cards show the assignee avatar (initials, colour
  hashed from name); my cards are subtly highlighted. Truncated ⇒ hint. Cards also show epic chip
  (epic colour) and story points.
- **Status change via dropdown**: the status pill on a card and the status control in the drawer
  open a dropdown listing the available transitions (fetched on open, loading state, target status
  colour dot, keyboard: arrows/Enter/Esc). Drag & drop stays.
- **Issue drawer** (wider, ~520px, resizable not required): header (type icon, key, summary,
  status dropdown, open in Jira), tabs **Details / Comments (n) / History**.
  Details: description (render a safe subset of Jira wiki markup: `h1.`–`h6.`, `*bold*`, `_italic_`,
  `{{mono}}`, `{code}`/`{noformat}` blocks, `* `/`# ` lists, `[text|url]` links via `openUrl`,
  `[~user]` mentions; everything else as text — **never innerHTML of Jira content**),
  people (assignee, reporter), estimates (original / remaining / spent with a bar, story points),
  sprint(s), epic, stories, children, linked issues grouped by relation, labels/components/versions,
  dates. Every related issue is clickable and opens in the drawer (back button / history stack).
  Comments: author, relative time, rendered body. History: changelog entries, `field: from → to`.
- **Sprints**: board picker (from `getAgileBoards`, remembered), sprint picker (active selected by
  default; state badges), sprint header (name, goal, dates, days left, progress bar by category,
  story points done/total), issues as kanban columns (same cards, dropdown, drawer) with the
  "Only mine" toggle. Kanban boards / no sprints ⇒ friendly empty state.
- **Epics & stories**: segmented control **Epics | Stories**, search, "Only mine", "Show done".
  Epics: list of expandable epic rows (epic colour stripe, name, key, status, progress bar with
  counts); expanded ⇒ stories (each expandable to its tasks) and then direct tasks; a separate
  section **"Stories without epic"** at the end. Stories: list of expandable story rows (epic chip
  if any) with their tasks. Best practices: progressive disclosure, remembered expansion state,
  keyboard (arrows / Enter / Space to expand), counts in collapsed rows, skeleton loading,
  empty states, sticky section headers.
- **Projects**: searchable list/grid of all accessible projects with a toggle "Tracked", my open
  issue count, category; tracked first; bulk "Track only projects with my issues".
- **Settings → Worklog**: `loadFactor` slider 0–1 (step 0.05) + numeric value, live hint
  "max per day: 4h 48m". Worklog page shows the effective cap.
- **Visual redesign — "task board" style, yellow × graphite**: graphite surfaces
  (#1E1F22 / #2A2C30 / #35383D dark; light theme: warm paper #F7F5EF / white cards, graphite text
  #2A2C30) with a signature yellow accent (#FFC400, hover #FFD54A, pressed #E6AC00, text on
  yellow = graphite). More colour: status categories (new = slate blue-grey, indeterminate =
  yellow/amber, done = green), priority colours (highest red → lowest blue), issue-type colours
  (epic purple, story green, task blue, bug red, subtask teal), epic colours from Jira `ghx-label-N`
  (map 1–14 to a palette), assignee avatar colours. Column headers with coloured top bar and
  counts, cards with left colour stripe by type, subtle shadows, rounded 10px.
  Motion: card enter (fade/translate, staggered), drag lift (scale+shadow), column drop
  highlight, drawer slide-in, dropdown scale-in, expand/collapse height animation, progress bar
  fill, skeleton shimmer, toast slide. 150–250 ms, ease-out; **all disabled under
  `prefers-reduced-motion`**. Contrast WCAG AA in both themes; focus rings visible (yellow).
- `ui/mock.js`: extend with ~4 projects, other people's issues, epics (with colours), stories
  (in and outside epics, linked tasks, sub-tasks), 2 agile boards (scrum + kanban), sprints
  (active/future/closed), comments with wiki markup, changelogs — enough to review every screen.

## Acceptance criteria v0.2

1. Board "Only mine" toggle switches between my issues and all not-done issues of tracked projects.
2. Projects page lists accessible projects; toggling tracking persists and affects board, sprints, epics, alerts; worklog unaffected.
3. Drawer shows description, comments, changelog, estimates/story points, people, links, epic, stories, children; related issues navigable.
4. Status change via dropdown on card and in drawer.
5. `loadFactor` setting validated, persisted, applied to the cap; scenario tests pass.
6. Sprints view: boards → sprints → sprint issues with progress.
7. Epics & stories view with the hierarchy rules above, orphan stories separately.
8. Yellow-graphite redesign with animations, reduced-motion respected, ru/uk/en, light/dark, no network resources.
9. `go vet`, `go test` pass; `build/jiraffe.exe` ≤ 5 MB.

Note (v0.2): `saveSettings` invalidates the board caches and emits `board` when `jiraUrl`, the token or `boardJql` changed.

---

# v0.3 — project filter everywhere, project columns on the Board, Sprints as a grouped list

Everything above stays valid unless changed here.

Facts from the real Jira (sd.local): `sprint in openSprints()`, `sprint in futureSprints()` and
`ORDER BY Rank ASC` work in JQL. A project usually has **several agile boards** and boards are often
**shared** between projects (board 880 "BI DEV Agile board" belongs to both MB and CMA; MB also has
673 "ETL board"; CMA has 937 "CMA dev board" kanban and 951 "CMA general" scrum). Statuses differ per
project (e.g. MB has "Reject", CMA has "ForTesting", both have "Закрыт").

## Project filter (Board, Sprints, Epics & stories)

One **shared** project selection for the three views (`uiPrefs.project`: a project key or `""` =
all tracked projects). Shown as a single-select chip row in the top bar: **"All projects"** + one
chip per **tracked** project (from `getProjects`, `tracked && available`, sorted by key). Hidden when
fewer than 2 tracked projects; when the chips do not fit, the row collapses into a compact "Project: KEY ▾" menu; then the only tracked project (if exactly one) is the **effective
project**. A selected key that is no longer tracked falls back to `""`. The previous client-side
multi-select chips on the Board are replaced. Chips may show counts of the current view's issues
(optional; never hide a tracked project because it has 0 issues).

## Jira (additions)

- `GET /rest/agile/1.0/board?projectKeyOrId=KEY` — board objects may include
  `location: { projectKey, … }` (optional, older versions lack it).
- `GET /rest/agile/1.0/board/{id}/configuration` →
  `columnConfig.columns: [{ name, statuses: [{ id, self }] }]` (statuses carry only ids).
- `GET /rest/api/2/project/{key}/statuses` → `[{ id (issue type id), name, subtask, statuses: [{ id, name, statusCategory: { key } }] }]` —
  the project's statuses per issue type; used to name/categorise column statuses and as the fallback layout.

## Board — project columns

`getBoard { force?, scope?, project?, boardId? }`.

- `project` empty ⇒ unchanged v0.2 behaviour (tracked projects, not-done issues, columns = statuses
  that have issues, ordered by category), `layout` absent.
- `project` = KEY (must be an accessible project; tracking not required ⇒ else error `project_unavailable` (localized message; a distinct code so the UI can tell it from Jira 400s, which stay `bad_request`)):
  - JQL "mine": `project = KEY AND ((<boardJql without ORDER BY>) OR (assignee = currentUser() AND statusCategory = Done AND updated >= -14d)) ORDER BY Rank ASC`
    (if `boardJql` has no where-part, use `assignee = currentUser() AND (statusCategory != Done OR updated >= -14d)`).
  - JQL "all": `project = KEY AND (statusCategory != Done OR updated >= -14d) ORDER BY Rank ASC`.
    If ordering by Rank fails (400 — no Jira Software), retry with `ORDER BY updated DESC`.
  - Limits as before (mine 500 total cap applies, all 500) ⇒ `truncated`.
  - **Layout** (cached per project+board for 1 h, single-flight like other metadata):
    1. Agile boards of the project (`/board?projectKeyOrId=KEY`, all pages, max 50). Chosen board =
       `boardId` param if it is in that list; else the first board whose `location.projectKey == KEY`;
       else the first board (lowest id). Columns = the board configuration columns in order; a column's
       statuses = its status ids resolved to `Status` via the project statuses (unknown id ⇒ fetch
       `/rest/api/2/status/{id}` once, or name = id, category "indeterminate"); columns with no statuses are dropped.
       **Shared boards:** when the project statuses are known, a column keeps only the statuses that belong
       to this project's workflow (statuses of other projects on a shared board are removed, and no
       `/status/{id}` lookup is made for them); columns left without statuses are dropped. If that
       leaves no columns at all, use the workflow fallback. Issues of this project in a status not on
       the board still go to the "other" column.
    2. No Agile API / no boards / configuration fails ⇒ fallback `source: "workflow"`: one column per
       distinct status of `/project/{key}/statuses` (union over issue types, sub-tasks included),
       ordered by category new → indeterminate → done, then by first appearance.
  - `source: "issues"`: neither a usable agile board nor the project statuses could be read ⇒ columns
    are built from the issues (statuses that have issues, as without a project); UI note "Only statuses that have issues are shown".
- Every column is returned **even if empty**. Issues whose status is in no column go to a trailing
    column `{ id: "other", name: "" (UI shows "Other statuses"), statuses: [the distinct statuses of those issues] }`, present only when non-empty. Its category = the shared category of its statuses, else "indeterminate".
  - `hidden` is not computed in project mode (absent).
- Column (both modes) becomes:
  `Column { id: string, name: string, category: "new"|"indeterminate"|"done", statuses: [Status], issues: [Issue], status: Status }`
  - status-mode (no project / workflow fallback): `id = status.id`, `name = status.name`, `statuses = [status]`, `status` = that status (kept for compatibility).
  - agile mode: `id = "col-<index>"`, `name` = column name, `category` = category of the column's
    statuses if all equal, else `"done"` if it is the last column of the board and **any** of its statuses is done, else `"indeterminate"`;
    `status` = first status of the column.
- Result adds `layout?: { source: "agile"|"workflow"|"issues", project, boardId?, boardName?, boards: [AgileBoard] }`
  (`boards` = all agile boards of the project, for a picker — also when the chosen board fell back to the workflow layout because it has none of the project's statuses; `[]` only when the project has no boards or the Agile API is unavailable; `boardId`/`boardName` = the chosen board whenever there is one).
- Cache key includes scope, project and boardId. `setProjectTracked`/`saveSettings` invalidation unchanged.
- Drag & drop: target statuses = the column's `statuses`; transitions whose `to.id` is one of them
  (name fallback); one ⇒ run it, several ⇒ picker, none ⇒ toast (unchanged UX).

## Sprints — grouped list (replaces the v0.2 boards → sprint → kanban page)

New method `getSprintGroups { project?, scope?, includeClosed?, force? }` (scope default "all")
→ `{ groups: [SprintGroup], backlog: SprintGroup|null, fetchedAt, truncated, available: bool, closedUnavailable?: bool }`
(`closedUnavailable: true` only when `includeClosed` was requested and the closed-sprint part failed; the UI then shows a short note "Closed sprints could not be loaded" in place of the closed sections).

```
SprintGroup { sprint?: Sprint (absent for backlog), issues: [Issue], progress: Progress,
              storyPoints: { done, total }, truncated: bool }
```

- Projects = `project` if given (accessible), else tracked projects (∩ accessible, as elsewhere).
- Open sprints: JQL `project in (P) AND (sprint in openSprints() OR sprint in futureSprints())`
  (+ `AND assignee = currentUser()` for scope mine) `ORDER BY Rank ASC`, fields as the board plus the
  Sprint field, max 1000 ⇒ `truncated`. Group by the issue's sprints whose state is active or future
  (an issue carried over keeps closed sprints in the field — ignore them; an issue in two open sprints
  appears in both). Sprint metadata (name, state, dates, goal, board) comes from the Sprint field values.
  Group order: active (by startDate, then id), then future (by id). Issues inside a group keep JQL (Rank) order.
- Backlog: `project in (P) AND statusCategory != Done AND (sprint is EMPTY OR sprint not in (openSprints(), futureSprints()))`
  (+ mine) `ORDER BY Rank ASC`, max 300 ⇒ group `truncated`. Returned as `backlog` (null if 0 issues).
- `includeClosed`: additionally the **last 3 closed sprints** of the selected projects: collect
  closed sprints from the projects' agile boards (`getSprints`-style, existing agile cache), keep those
  whose sprint issues include the projects (JQL below filters), order by completeDate desc, take 3, JQL
  `project in (P) AND sprint in (ids)` (+ mine) `ORDER BY Rank ASC`, grouped by those sprint ids, appended after future sprints (newest first).
- Progress/storyPoints per group as in `getSprint` (by status category; story points done = sum of SP of done issues).
- Agile API / Jira Software missing (JQL `openSprints()` returns 400) ⇒ `{ available: false, groups: [], backlog: null }`.
- Cache per (project, scope, includeClosed) 2 min; `force` bypasses. Invalidated where board caches are.
- `getSprint` and the UI board/sprint pickers are removed. `getAgileBoards` / `getSprints` stay
  (used by `includeClosed` and the board layout).

## Epics & stories — project filter

`getEpics` / `getStories` get `project?` (KEY): restrict the epic/story queries to `project = KEY`
instead of the tracked list. Children (stories/tasks/linked issues) are not filtered by project.
Cache key includes project.

## UI v0.3

- **Top bar**: shared project chip row on Board, Sprints, Epics & stories (see above); selection
  persisted in uiPrefs and applied immediately (reload of the current view).
- **Board** with a project: all its columns, empty ones included (empty column shows a subtle
  "No issues" placeholder and still accepts drops); a column header shows the column name, count and,
  for multi-status columns, the statuses as small muted text/tooltip; done columns visually calmer;
  hint "Done: last 14 days" on done columns. Agile layout with several boards ⇒ a compact board
  picker ("Board: CMA dev board ▾") in a board bar row under the top bar (together with the workflow note), remembered per project
  (`uiPrefs.boardByProject[KEY]`). Workflow fallback ⇒ small note "Columns from the project workflow".
  Without a project ("All projects") ⇒ v0.2 behaviour plus a dismissible hint
  "Select a project to see all its columns".
- **Sprints** — Jira-backlog-like list:
  - Toolbar: project chips, search, "Only mine", "Show closed sprints" (off by default, remembered).
  - One collapsible **section per sprint**: header = chevron, sprint name, state badge (active /
    future / closed), dates + "N days left" (active) or "starts …" (future), issue count, story points
    done/total, progress bar by category; goal shown under the header (2 lines, full on expand).
    Active sprints expanded by default, future/closed collapsed; expansion remembered per sprint id.
  - Rows (dense list, not cards): type icon, key, summary (1 line, ellipsis), epic chip, status
    dropdown pill (same component as the board), priority icon, story points, assignee avatar
    (always in this view). Click/Enter ⇒ issue drawer. Done issues muted.
  - Last section **Backlog** (collapsed by default) with the backlog issues.
  - "All projects" mode: sprint headers also show the project keys present in the sprint.
  - Search filters rows (key/summary/epic/assignee); sections with no matching rows are hidden; a
    banner shows when the filter hides everything. Keyboard: arrows move between rows/headers,
    Enter/Space toggles a header; ARIA: section headers are buttons with `aria-expanded`.
  - States: skeleton loading, empty (no open sprints ⇒ friendly text + backlog still shown),
    unavailable (`available: false`), error with retry.
- **Epics & stories**: the shared project chips; changing the project reloads the tree.
- `ui/mock.js`: projects with **different status sets** (e.g. one with "Reject"/"ForTesting"),
  agile boards with column configurations (multi-status columns, a project with 2 boards, a shared
  board, a project without boards ⇒ workflow fallback), sprints across projects (active, future,
  closed), backlog issues, issues carried over from a closed sprint.
- i18n ru/uk/en for every new string; reduced motion, contrast, light/dark as before.

## Acceptance criteria v0.3

1. Board with a selected project shows all its columns (agile board configuration, or project workflow
   statuses as fallback), including empty ones; multi-status columns work for drag & drop; board picker when a project has several boards.
2. Board "All projects" keeps v0.2 behaviour.
3. Sprints page is a list of issues grouped by sprint (active, future, optional last 3 closed) plus backlog, with the project filter, "Only mine" and search.
4. Epics & stories have the same project filter; selection is shared between the three views and persisted.
5. Real-Jira facts honoured: shared boards, several boards per project, Rank ordering with fallback.
6. `go vet`, `go test` pass; `build/jiraffe.exe` ≤ 5 MB; ru/uk/en; light/dark; no network resources.

# v0.4 — HTML descriptions, dark title bar, epic dashboard, Dev/QA story points

Real-Jira facts (checked on the user's Jira):
- Descriptions and comments of many issues are **HTML** (JEditor plugin), not wiki markup: `<div class="userTemplate">`,
  panels `<div class="jePanel_blue|jePanel_yell" style=…>` with `<div class="jePanelHeader|jePanelHeader_red">BRIEF:</div>`
  and `<div class="jePanelContent">`, `<p dir="auto">`, `<br>`, `<b>/<i>/<code>`, `<ul>/<ol>/<li>`, `<hr>`, `&nbsp;`,
  inline `style` everywhere (light backgrounds), `<span class="image-wrap"><img src="https://<jira>/plugins/servlet/jeditor_file_provider?…">`.
  Other issues still use wiki markup / plain text.
- `Dev Story Points` (`customfield_10204`) and `QA Story Points` (`customfield_10205`) are **select** fields:
  value `{"value": "8"}` (a string). Do not confuse them with the number fields "Dev Points" / "QA Points".
- The window colours follow the Windows app theme (`prefers-color-scheme`), but the native title bar stays light.

## 1. HTML descriptions and comments (frontend; backend only for plain-text snippets)

- `wiki.render(text)` (description, comment bodies) detects HTML: the text contains an element tag of the known
  set (`p div br span b strong i em u s strike del ins code pre ul ol li a img table thead tbody tfoot tr td th
  h1–h6 hr blockquote sub sup font`) — e.g. `/<\/?(p|div|br|…)\b[^>]*>/i`. HTML ⇒ the new safe HTML renderer;
  otherwise the existing wiki renderer (unchanged).
- Safe HTML renderer (still **never innerHTML of Jira content into the live document**): parse with
  `new DOMParser().parseFromString(text, 'text/html')` (inert document: no scripts, no loads), walk the nodes and
  build fresh elements with `createElement`/`textContent`:
  - allowed tags rebuilt as the same tag: `p br b strong i em u s del ins sub sup code pre ul ol li table thead
    tbody tfoot tr td th h1–h6 hr blockquote`; `strike` → `s`; `h1–h6` are demoted to the drawer's heading scale
    (no huge headings).
  - **no attributes are copied** (no `style`, `class`, `id`, `on*`), except: `td/th` `colspan`/`rowspan` (integers 1–50),
    `ol` `start` (integer).
  - `a href` → the existing wiki link button (issue key ⇒ opens the drawer, http(s) ⇒ `openUrl`); other schemes ⇒ text.
  - `img` → a small inline button "🖼 Image" (i18n, with the alt text if any) that calls `openUrl(src)` for http(s)
    sources (the page CSP forbids loading images; `openUrl` itself only allows the Jira origin). No `<img>` is created.
  - JEditor panels: `div` whose class starts with `jePanel` and is not a header/content ⇒ `div.desc-panel`
    (variant from the class: `_blue` ⇒ info, `_yell`/`_red` ⇒ warning, else neutral; colours from theme tokens,
    readable in light and dark); `jePanelHeader*` ⇒ `div.desc-panel-head` (bold, small caps-like label);
    `jePanelContent` ⇒ `div.desc-panel-body`. The class is only *read* for this mapping.
  - other `div`/`span`/`font`/unknown elements ⇒ unwrapped (children kept; `div` keeps a block boundary).
  - dropped with their content: `script style iframe frame object embed form input button select textarea svg
    math template noscript link meta head title canvas video audio`.
  - comments/processing instructions dropped; text nodes copied as text; runs of `&nbsp;`-only/empty paragraphs
    collapsed (no tall blank gaps); a hard cap of 5000 output nodes (rest dropped + "…").
  - tables scroll horizontally inside the drawer; `pre`/`code` use the existing mono style; long words wrap.
- Notification / balloon snippets (`PlainText` in `internal/app/comments.go`): HTML-looking bodies get tags stripped
  (block tags and `<br>` ⇒ space), entities decoded (`html.UnescapeString`), then the existing whitespace folding and
  length cap.
- `ui/mock.js`: at least one issue with a JEditor-style HTML description (panels, list, code, image, table, a link,
  a `<script>` and an `onerror` attribute that must not run) and one HTML comment.

## 2. Dark title bar (Windows)

- New method `setWindowTheme { dark: bool, caption?: "#rrggbb" }` → `null`. Windows: on the app window
  `DwmSetWindowAttribute(hwnd, DWMWA_USE_IMMERSIVE_DARK_MODE=20, BOOL)` (if that fails, attribute 19 for older
  Windows 10), and when `caption` is given `DWMWA_CAPTION_COLOR=35` (COLORREF 0x00BBGGRR; Windows 11 only, errors
  ignored). Bad params ⇒ `bad_params`. No window ⇒ no-op. Never fails the UI.
- In `openWindow`, with a dark system app theme (`HKCU\…\Personalize\AppsUseLightTheme` = 0) the backend
  switches the title bar to dark from the first message dispatched on the UI thread after go-webview2 shows the
  window (found by class via `EnumThreadWindows`), and again when the call returns; the WebView background is set
  via `WEBVIEW2_DEFAULT_BACKGROUND_COLOR`. No window hooks (`SetWindowsHookEx`) are used: they did not work for DWM
  (the window is not ready at HCBT_CREATEWND) and look suspicious to antivirus heuristics. The page refines it.
- The page calls `setWindowTheme` at startup and on `matchMedia('(prefers-color-scheme: dark)')` change, with
  `caption` = the computed top-bar background colour, so the title bar matches the app (both themes).
- Non-Windows/test builds: the platform hook is a no-op; the method is covered by an App test with a fake hook.

## 3. Epic dashboard (Epics & stories page, Epics mode)

- `getEpics` result: every `EpicNode` gets `time: { spentSeconds, estimateSeconds, issues }`:
  - the counted set of an epic = the epic itself + every distinct issue of its tree (the same descendants as its
    progress: stories, their tasks/linked issues, direct tasks, sub-tasks) — each issue once per epic;
  - `spentSeconds` = Σ `aggregatetimespent`, `estimateSeconds` = Σ `aggregatetimeoriginalestimate` (both include
    the issue's sub-tasks, also those outside the counted set) over the counted set, **skipping every counted issue
    whose `parent` is also in the counted set** (its time is already in the parent's aggregate); null/absent = 0;
    `issues` = size of the counted set (as progress; skipped sub-tasks still count here);
  - time fields are read for every counted issue: add `aggregatetimespent`, `aggregatetimeoriginalestimate`,
    `parent` to the tree search fields; issues known only as embedded copies (links/sub-tasks lack the fields) are
    fetched by `key in (…)` in batches of ≤100 (fields `aggregatetimespent,aggregatetimeoriginalestimate,parent`),
    within the call's timeout and the existing caps; issues that could not be read ⇒ `EpicsResult.timePartial: true`
    (not an error).
  - cached with the tree (same key; `force` refreshes).
- UI: in Epics mode, above the epic list, a collapsible **dashboard** (collapsed state remembered in uiPrefs):
  - one card **per project** of the listed epics (a selected project ⇒ one card; "All projects" ⇒ one per project
    present, ordered by project key), title = project key + name if known;
  - a **donut chart** (inline SVG, no libraries) of each epic's share of the project total: slice = epic,
    colour = the epic colour (fallback: the existing hashed palette), slices ordered by size; epics beyond the top 8
    and slices < 2 % merged into "Other"; centre label = project total (hours) and the metric name;
  - a legend/table next to it (stacks under it when narrow): colour dot, epic name/key, hours (`12.5 h`, i18n unit,
    one decimal), share %, issue count; zero-time epics listed last, muted, without a slice;
  - metric toggle **Logged / Estimate** (`spentSeconds` / `estimateSeconds`), default Logged, remembered in uiPrefs;
  - hover/focus on a slice or legend row highlights both and shows a tooltip (name, hours, %); click ⇒ opens the
    epic in the drawer; keyboard reachable via the legend rows; the SVG has `role="img"` + an `aria-label` summary;
  - follows "Show done" and the project filter (same data as the list); `timePartial` ⇒ a small note
    "Some issues could not be read; totals may be incomplete"; project total 0 ⇒ "No logged time (estimate) in
    these epics" instead of the chart;
  - Stories mode: no dashboard.
- `ui/mock.js`: epics with logged and estimated time in two projects, one epic with no time, more than 8 epics in one
  project (to show "Other").

## 4. Dev / QA story points in issue details

- Field discovery: `Dev Story Points` / `QA Story Points` by field name or clause name (case-insensitive, trimmed),
  any schema type. Values: number, numeric string, or option object `{ "value": "8" }` ⇒ number; anything else ⇒ absent.
- `IssueDetail` gets `devStoryPoints?: number`, `qaStoryPoints?: number` (read only in `getIssue`).
- Drawer, estimates block: "Story points", "Dev story points", "QA story points" shown together (each only when
  present). i18n ru/uk/en. Mock: issues with both, one, none.

## Contract additions (v0.4)

Types:
```
EpicTime    { spentSeconds: number, estimateSeconds: number, issues: number }   // integers; aggregate time incl. sub-tasks, each issue once; 0 when nothing logged/estimated
EpicNode    + { time: EpicTime }                    // always present
EpicsResult + { timePartial: bool }                 // always present (false unless some counted issue could not be read)
IssueDetail + { devStoryPoints?: number, qaStoryPoints?: number }               // omitted when the field is missing or not numeric
```
Methods:
| method | params | result |
|---|---|---|
| `setWindowTheme` | `{ dark: bool, caption?: "#rrggbb" }` — `dark` required; `caption` hex, either case; `null` = absent | `null`; `bad_params` otherwise. Works without a Jira configuration. `caption` absent ⇒ the caption colour is reset to the system default |

## 5. Automatic updates (from GitHub releases)

Source of truth: the GitHub releases of `alexindacomp/Jiraffe` (public). CI publishes every release with
`jiraffe.exe`, `jiraffe.exe.sha256` and `jiraffe.exe.sig` (`<64 hex>  jiraffe.exe`); the workflow is updated accordingly.

**Release signature (ed25519)** — the sha256 file only protects against corruption; authenticity comes from a signature:
- CI (release job only, environment `release` that only `v*` tags may use; secret `RELEASE_SIGNING_KEY` = base64 of the 32-byte ed25519 seed) runs
  `go run ./tools/sign -version X.Y.Z -file dist/jiraffe.exe > dist/jiraffe.exe.sig` and publishes
  `jiraffe.exe.sig` as a third asset. The tool reads the key from env `RELEASE_SIGNING_KEY`, never from a file
  in the repo, and prints one line: base64 of the 64-byte signature.
- Signed message (exact bytes): `"jiraffe-release-v1\n" + version + "\n" + lowercase hex SHA-256 of the exe + "\n"`
  (binds the version, so an old signed exe can't be served under a newer tag).
- The app embeds the public key `H/tsEqhxPOKjmI14VJl9r7sUOG3mWQXHsQKY/Xp0ZEk=` (base64, 32 bytes) in
  `internal/update`, and verification requires: the `.sig` asset present (≤ 1 KB), a valid signature over the message
  built from the release version and the hash computed from the **downloaded bytes**, plus the existing
  size/sha256/MZ checks. A `.sig` that cannot be used (HTTP error status, over 1 KB, malformed) or does not verify ⇒
  `updateFailed { code: "bad_signature" }`, nothing installed; a transport failure while fetching it is `network`
  (retryable).
  A release without `.sig` is not offered as an update.
- Private key backup: `~/.config/jiraffe/release-signing.key` on the maintainer's WSL (0600), never committed.
- Key rotation (if the private key ever leaks or is replaced): publish a bridge release signed with the old key whose
  `internal/update` accepts both the old and the new public key, then release with the new key only and drop the old
  key one release later. Keep an offline copy of the seed.

**Check**
- At startup (30 s after start, so it never slows the launch; also when the app has been running > 24 h, once a day)
  and on demand from Settings: `GET https://api.github.com/repos/alexindacomp/Jiraffe/releases/latest`
  (`Accept: application/vnd.github+json`, `User-Agent: Jiraffe/<version>`), timeout 20 s.
- **Network isolation**: updater requests use a separate transport **without** the Jira token or any Jira
  header; they may follow redirects only to `https://` hosts `api.github.com`, `github.com`,
  `objects.githubusercontent.com`, `release-assets.githubusercontent.com` (max 5 hops). The Jira transport keeps
  its no-redirect rule. Certificate checks are never disabled; the system proxy is used.
- Newer = the tag `vX.Y.Z` is semver-greater than `app.Version`. Builds with version `dev` or `0.0.0` never offer
  updates. Drafts/prereleases are ignored (the `latest` endpoint already does that). The release must have all three assets
  (`jiraffe.exe`, `jiraffe.exe.sha256`, `jiraffe.exe.sig`; see "Contract additions (v0.4 updates)").
- Failures are logged and shown only in Settings ("Could not check: …"); no balloons for failed background checks.

**Prompt**
- Settings `autoCheckUpdates` (default `true`). State keeps `skippedVersion`.
- A newer version that is not `skippedVersion` ⇒ event `updateAvailable { version, notes, publishedAt, size }`:
  - window open ⇒ an in-app dialog "Jiraffe X.Y.Z is available" with the release notes (plain text, scrollable,
    rendered safely), buttons **Update now** (primary) and **Later**;
  - window closed (e.g. autostart minimized) ⇒ one tray balloon "Update X.Y.Z available — click to install";
    clicking it opens the window with the dialog.
  - **Later** ⇒ `skippedVersion = X.Y.Z`: no more prompts for that version; a newer one prompts again.
- Settings, new **Updates** section: current version, latest known version + date, "Check for updates" button,
  "Update to X.Y.Z" button when newer (works also for the skipped version), toggle "Check for updates at startup",
  last check time/result, link "What's new" (opens the release page via the shell, allowed for this exact URL only).

**Install (within the session, then automatic restart)**
- Method `installUpdate { version }` → starts the job; progress via events
  `updateProgress { phase: "download"|"verify"|"install"|"restart", received?, total? }`, failure via
  `updateFailed { code, message }` (localized). Only one job at a time; `version` must equal the latest checked one.
- Download the asset to `%LOCALAPPDATA%\Jiraffe\update\jiraffe-X.Y.Z.exe.part` (streamed, size limit 20 MB,
  timeout 5 min, cancellable on app exit), then verify: size equals the API asset size, SHA-256 equals the
  `.sha256` asset, the file starts with `MZ`, the `.sig` signature is valid. Any mismatch ⇒ delete, `updateFailed`.
- Swap: rename the running exe `jiraffe.exe` → `jiraffe.exe.old` (allowed for a running image), move the new file to
  the original path; on any failure roll back (rename `.old` back) and report (a failed rollback is reported as
  `install_failed` naming where the previous exe is). The swap is never cut short by quitting. No write access to the exe folder
  ⇒ `updateFailed { code: "no_write_access" }` with a hint to download manually (button opens the release page).
- Restart: start the new exe with `--updated-from <oldVersion> --wait-pid <pid>` (plus `--minimized` if the window
  is closed), then the current process exits cleanly (tray icon removed, state saved, scheduler stopped). The new
  process waits up to 15 s for the old PID to exit before taking the single-instance mutex; if the PID is still
  alive (or cannot be waited for) it logs that and the single-instance mutex decides (never two instances; a
  reused PID never blocks the start).
- The new instance deletes stale `update\*` files and `jiraffe.exe.new` at start, and `jiraffe.exe.old*` only after it
  has run for 10 minutes (retry later if locked),
  shows a balloon/toast "Updated to X.Y.Z", and clears `skippedVersion`. Autostart keeps working (same path).
- If the new exe fails to start (CreateProcess error) ⇒ roll back the swap, keep running, report.
- Worklog safety: an update never starts while a worklog run is in progress (it waits for it to finish).

**Mock / tests**
- `ui/mock.js`: flags to simulate "update available", download progress, failure, and no-update.
- Go tests (pure logic behind interfaces): semver compare (incl. `dev`), release JSON parsing and asset selection,
  redirect allow-list, sha256 file parsing, verification (size/hash/MZ), swap + rollback on a temp dir,
  skippedVersion rules, prompt/event logic with a fake clock and fake HTTP.

## Contract additions (v0.4 updates)

Settings: `+ autoCheckUpdates: bool` (default `true`; a non-boolean ⇒ `bad_params`, like the other booleans).

Types:
```
UpdateRelease  { version: "X.Y.Z", notes: string (plain text, CRLF→LF, ≤ 20000 chars), publishedAt: string (RFC 3339 from GitHub, may be ""),
                 size: number (bytes of jiraffe.exe), url: string (release page https://github.com/alexindacomp/Jiraffe/releases/tag/vX.Y.Z) }
UpdateStatus   {
  currentVersion: string,           // app version ("dev" for development builds)
  supported: bool,                  // false: development build (dev / 0.0.0) or no updater (then hide/disable the Updates actions)
  latest?: UpdateRelease,           // latest release seen by the last successful check (persisted; kept when a later check fails)
  available: bool,                  // supported && latest is newer than currentVersion ("Update to X.Y.Z" button; also when skipped)
  prompt: bool,                     // available && latest.version != skippedVersion ⇒ show the dialog (on load and on updateAvailable)
  skippedVersion?: string,          // set by skipUpdate ("Later")
  lastCheck?: { at: string (RFC 3339), ok: bool, error?: string (localized, only when !ok) },
  installing: bool,                 // an install job runs (stays true while the app restarts)
  justUpdated?: { from: string, to: string }   // this session started right after an update
}
UpdateProgress { phase: "download"|"verify"|"install"|"restart", received?: number, total?: number }   // received/total only for "download"
UpdateFailed   { code: string, message: string (localized) }
```
`UpdateFailed.code` (and the stored check error): `network` (incl. the updater's own 20 s / 5 min timeouts),
`http_status`, `bad_redirect`, `too_large`, `bad_release`, `bad_checksum`, `size_mismatch`, `hash_mismatch`, `not_exe`,
`bad_signature` (the `.sig` download returns an HTTP error status, is over 1 KB or malformed, or the signature is not valid
for this exe and version; a transport failure while fetching it is `network`, retryable; a release that does not list
`jiraffe.exe.sig` at all is not offered — `bad_release`),
`release_changed` (`installUpdate` re-reads the latest release; another version was published since the check),
`no_write_access` (show "Download manually" → `openUrl(latest.url)`), `install_failed`, `start_failed`, `file_error`,
`cancelled` (the app is quitting; a finished swap is rolled back, no restart). For every code the running version is unchanged.

Release requirements (else the release is not offered: `bad_release` on check): tag `vX.Y.Z` (canonical), not draft/prerelease,
assets exactly `https://github.com/alexindacomp/Jiraffe/releases/download/vX.Y.Z/<name>` for `jiraffe.exe` (1 B…20 MB),
`jiraffe.exe.sha256` (≤ 4 KB) and `jiraffe.exe.sig` (≤ 1 KB). Redirects: the API request stays on `api.github.com`;
asset requests start at that download URL and may then only go to `objects.githubusercontent.com` /
`release-assets.githubusercontent.com` (max 5). A release stored in state.json is re-validated before it is shown and only
selects the version: `installUpdate` always installs the freshly read latest release.

Install details: the download is copied to `jiraffe.exe.new` while hashing the written bytes (must equal the verified hash) and
hashed again right before it is renamed into place. Residual risk: a process of the same user with write access to the exe folder
could replace `jiraffe.exe.new` in the microseconds between that last hash and the rename — such a process could replace
`jiraffe.exe` itself anyway. Renames are retried (≈ 1.4 s backoff) before `install_failed`. A stale `jiraffe.exe.old` is removed,
or moved aside to `jiraffe.exe.old.<n>` when locked. The new version keeps `jiraffe.exe.old` (manual rollback) and deletes
`jiraffe.exe.old*` only after running 10 minutes; stale downloads/`jiraffe.exe.new` are deleted at start (never while an
install runs). `--updated-from` is used only if it is a version; `--wait-pid` values ≤ 0 or absurd are ignored, and if waiting is
impossible the single-instance check decides.

Methods:
| method | params | result |
|---|---|---|
| `getUpdateStatus` | – | `UpdateStatus` (no network) |
| `checkForUpdates` | – | `UpdateStatus` after a check; a failed check is **not** an error (see `lastCheck.error`). Error `update_unsupported` when `!supported` |
| `installUpdate` | `{ version: "X.Y.Z" }` (a leading `v` is accepted) | `null` once the job started; then `updateProgress`… (the job first re-reads the latest release: `release_changed` if it is no longer `version`) and either the app restarts or `updateFailed`. Errors: `bad_params` (no/invalid version), `update_unsupported`, `update_stale` (not the `latest.version` of the last check, or not newer), `update_busy` (a job runs) |
| `skipUpdate` | `{ version: "X.Y.Z" }` | `UpdateStatus` (`skippedVersion` set); `bad_params` for an invalid version |
| `openUrl` | `{ url }` | additionally allows exactly `https://github.com/alexindacomp/Jiraffe/releases/tag/vX.Y.Z` (canonical version) and `https://github.com/alexindacomp/Jiraffe/releases/latest` — nothing else outside Jira |

Events:
| event | payload | when |
|---|---|---|
| `updateAvailable` | `UpdateRelease` | a check (automatic or manual) found a newer version that is not `skippedVersion`. Window closed ⇒ additionally one tray balloon per version and session (not while notifications are paused); clicking it opens the window, whose `getUpdateStatus().prompt` is then `true` |
| `updateProgress` | `UpdateProgress` | during `installUpdate`: `download` (from `received: 0`, at most ~100 events), `verify`, `install` (waits for a running worklog), `restart` |
| `updateFailed` | `UpdateFailed` | the install job failed (the job is already over when the event arrives: `installUpdate` may be called again at once) |
| `updated` | `{ from, to }` | at startup after an update (usually before the page listens: use `UpdateStatus.justUpdated`); also a tray balloon "Updated to X.Y.Z" |

Startup: the first automatic check runs 30 s after start, then at most once per 24 h of uptime (checked every 15 min),
only while `autoCheckUpdates` is on. While the new version is being started, worklog runs are refused
(`worklogRun` ⇒ `conflict`, "Jiraffe is restarting to finish an update"; scheduled runs are left to the new version).

## Acceptance criteria v0.4

1. A JEditor HTML description renders as formatted text (panels, paragraphs, lists, bold/code, tables, links) in
   both themes; no raw tags; no Jira HTML attribute/script reaches the live DOM; wiki-markup descriptions unchanged.
2. The window title bar is dark when the app is dark (and light when light), switching live with the Windows theme.
3. Epics page shows per-project donut charts of epic shares of logged time (toggle: estimate) with legend, %, hours.
4. Issue details show Dev and QA story points.
5. At startup a newer GitHub release is detected; the user can update now (download, verify, swap, automatic
   restart into the new version) or postpone (Later ⇒ not asked again for that version; update from Settings).
6. `go vet`, `go test` pass (incl. GOOS=windows vet); `build/jiraffe.exe` ≤ 5 MB; ru/uk/en; light/dark; no network resources in the page.

# v0.5 — create and edit issues

Decisions (user): **rich-text editor** for HTML descriptions; editing via an **edit form** (same form as create,
pre-filled); fields = **main + everything Jira marks required** for the project/type.

## Jira (additions)

- Create meta (Jira ≥ 8.4): `GET /rest/api/2/issue/createmeta/{projectKey}/issuetypes` (paged: `startAt`,
  `maxResults`, `isLast`/`total`) and `GET /rest/api/2/issue/createmeta/{projectKey}/issuetypes/{typeId}` (paged
  fields: `fieldId`, `name`, `required`, `schema`, `allowedValues`, `hasDefaultValue`, `defaultValue`, `autoCompleteUrl`).
  On 404 fall back to the legacy `GET /rest/api/2/issue/createmeta?projectKeys=KEY&expand=projects.issuetypes.fields`
  (same field objects keyed by id).
- Edit meta: `GET /rest/api/2/issue/{key}/editmeta` → `{fields: {id: FieldMeta}}`. Empty ⇒ no edit permission.
- Create: `POST /rest/api/2/issue {fields}` → `{id, key, self}`. Edit: `PUT /rest/api/2/issue/{key} {fields}` → 204.
  Errors: 400 `{errorMessages: [], errors: {fieldId: message}}` ⇒ per-field errors.
- Assignable users: `GET /rest/api/2/user/assignable/search?project=KEY&username=Q&maxResults=20` (create) or
  `?issueKey=KEY&username=Q` (edit). Users are identified by `name` (Server/DC): value `{"name": "…"}`.
- Issue picker: JQL search (existing `Search`) — epics: `project = KEY AND issuetype = Epic AND statusCategory != Done
  [AND (summary ~ "Q*" OR key = Q)]`; parents for sub-tasks: same without the type filter but `issuetype not in subTaskIssueTypes()`.
- Values on write (backend maps; the UI never builds Jira JSON):
  text/textarea/description ⇒ string; number ⇒ number; date ⇒ `"YYYY-MM-DD"`; datetime ⇒ Jira datetime;
  select/radio (option) ⇒ `{"id": "…"}`; multiselect/checkboxes ⇒ `[{"id"}]`; priority ⇒ `{"id"}`;
  components / fixVersions / versions ⇒ `[{"id"}]`; labels ⇒ `["a","b"]` (no spaces); assignee/user ⇒ `{"name"}`
  (`null` = unassigned); multi-user ⇒ `[{"name"}]`; Epic Link ⇒ `"KEY"` (or null); Sprint ⇒ sprint id number (or null);
  parent ⇒ `{"key"}`; timetracking ⇒ `{"originalEstimate": "1d 2h", "remainingEstimate": …}`; issuetype/project
  only on create (`{"id"}` / `{"key"}`); Epic Name ⇒ string.
- If Sprint is not in edit meta but the board allows it, change the sprint via
  `POST /rest/agile/1.0/sprint/{id}/issue {"issues":[KEY]}` (and `POST /rest/agile/1.0/backlog/issue` to remove).

## Description format

- `descriptionFormat: "html" | "wiki"` per issue / project:
  - edit: `"html"` if the current raw description looks like HTML (same detection as the v0.4 renderer), `"wiki"`
    if it is non-empty and not HTML; empty ⇒ project default;
  - project default (create, and empty descriptions): look at up to 10 latest issues of the project with a non-empty
    description; `"html"` if most look like HTML, else `"wiki"`; cached per project (30 min).
- `"html"` ⇒ **rich-text editor** (below), saved as clean HTML. `"wiki"` ⇒ a monospace textarea with the raw wiki
  markup (lossless) and a **Preview** toggle using the existing wiki renderer. Never convert between formats.

## Rich-text editor (frontend, no libraries)

- `contenteditable` region with a toolbar: Heading (H3/H4/paragraph), **Bold**, *Italic*, Underline, Strikethrough,
  inline Code, bulleted / numbered list, indent/outdent in lists, Link (dialog: text + http(s) URL), Code block,
  Quote, Horizontal rule, Table (insert 2×2; add/remove row/column via the row's context buttons), Clear formatting,
  Undo/Redo. Keyboard: Ctrl+B/I/U, Ctrl+K (link), Ctrl+Z/Y, Tab/Shift+Tab in lists. Accessible toolbar
  (`role=toolbar`, roving tabindex, `aria-pressed` for toggles), the region has a label.
- **Document model = the v0.4 safe subset**: the editor only ever contains elements of the sanitizer's allow-list
  (p br b strong i em u s code pre ul ol li a table thead tbody tr td th h3 h4 hr blockquote, plus panel blocks and
  image chips). After every input/paste/command it is normalized: unknown elements unwrapped, attributes stripped
  (except the editor's own data-* markers), `<b>`→`<strong>`, `<i>`→`<em>`, empty inline elements removed.
- **Loading existing HTML**: parse with the v0.4 sanitizer into the editor; JEditor panels become editable panel
  blocks (`div.desc-panel[data-variant=info|warning|neutral]` with a header line and a body); images become
  non-editable image chips that **keep the original `src`/`alt`/`width`/`height`** in data attributes. If the
  sanitizer dropped anything meaningful (an unknown element with text, inline colours/fonts, iframes, etc.), the form
  shows a notice: "Some formatting from Jira will be simplified when you save" + "Edit in Jira" link; the description
  is only sent if the user changed it (unchanged description is never re-written).
- **Saving**: a serializer walks the editor DOM and emits HTML strings with full escaping of text and attribute
  values (never `innerHTML` read-back of untrusted content as the output): allowed tags only; links as
  `<a href="…">` (http/https only); panels back to JEditor markup
  (`<div class="jePanel_blue|jePanel_yell" style="…fixed template…"><div class="jePanelHeader|jePanelHeader_red" style="…">HEADER</div><div class="jePanelContent" style="…">…</div></div>`,
  styles copied from the real Jira templates in the v0.4 facts); image chips back to
  `<span class="image-wrap"><img src alt width height></span>` with the original attribute values (src must be
  http(s), else the image is dropped); paragraphs `<p dir="auto">`.
- **Paste**: intercept `paste`; take `text/html` → v0.4 sanitizer → normalized subset; else `text/plain` with line
  breaks → paragraphs. Never insert pasted HTML unsanitized; images from the clipboard are not supported (notice).
- **Insert panel** toolbar button (BRIEF/TASK style): Info panel / Warning panel, with an editable header.

## HTML description contract (serializer ⇄ server-side validator)

The frontend serializer emits, and the backend validator (`description`/`textarea` values that look like HTML) accepts,
**only** this subset; anything else ⇒ `validation` error on that field (reject, never rewrite):
- tags: `p br strong em u s code pre ul ol li a table thead tbody tr td th h3 h4 hr blockquote div span img`;
- attributes: `p[dir="auto"]`; `a[href]` http(s) only; `td/th[colspan|rowspan]` integers 1–50; `ol[start]` integer;
  `img[src|alt|width|height]` — src http(s) and (create) on the Jira origin, (edit) on the Jira origin or already present
  in the issue's current description; width/height integers;
- JEditor panels, exact values only (class + style must match one row; no other class/style anywhere):

| element | class | style |
|---|---|---|
| panel div | `jePanel_blue` | `background-color:#e0efff; border:1px dashed #cccccc; margin-bottom:1em; margin-left:1em; margin-right:1em; margin-top:1em` |
| panel div | `jePanel_yell` | `background-color:#ffffce; border:1px dashed #cccccc; margin-bottom:1em; margin-left:1em; margin-right:1em; margin-top:1em` |
| header div | `jePanelHeader` | `border-bottom-color:#cccccc; border-bottom-style:dashed; border-bottom-width:1px; font-weight:bold; padding:.3em; text-align:center` |
| header div | `jePanelHeader_red` | `background-color:#f7d6c1; border-bottom-color:#cccccc; border-bottom-style:dashed; border-bottom-width:1px; font-weight:bold; padding:.3em; text-align:center` |
| content div | `jePanelContent` | `padding:.5em 2em .5em 2em` |
| image wrapper span | `image-wrap` | (none) |

  Info variant ⇒ `jePanel_blue` + `jePanelHeader`; warning variant ⇒ `jePanel_yell` + `jePanelHeader_red`;
  neutral panels are serialized as info. No wrapper `div.userTemplate` is emitted.
- no comments, CDATA, doctype, processing instructions, `on*` attributes, `javascript:`/`data:` URLs, entities other than
  the standard named/numeric ones; nesting depth ≤ 64.

## Methods (additions)

| method | params | result |
|---|---|---|
| `getCreateMeta` | `{ project, issueTypeId? }` | `{ project: {key, name}, issueTypes: [{id, name, kind, subtask}], issueTypeId, fields: [FieldMeta], descriptionFormat }` — without `issueTypeId` the backend picks the last used type for the project (uiPrefs passes it) or the first non-subtask type; `fields` are for the chosen type |
| `getEditMeta` | `{ key }` | `{ key, issueTypeId, project: {key,name}, updated, fields: [FieldMeta], descriptionFormat, values: { fieldId: FieldValue } }`; `fields: []` ⇒ no edit permission |
| `searchAssignable` | `{ project?, key?, query }` | `[User]` (≤ 20; empty query ⇒ first 20) |
| `searchIssuesFor` | `{ purpose: "epic"|"parent", project, query }` | `[IssueRef]` (≤ 20) |
| `createIssue` | `{ project, issueTypeId, values: { fieldId: FieldValue } }` | `{ key, url }`; emits the existing data-changed events so open views refresh |
| `updateIssue` | `{ key, values: { fieldId: FieldValue }, expectedUpdated }` | `{ key, updated }` — only changed fields are sent; if the issue's `updated` differs from `expectedUpdated` ⇒ error `edit_conflict` (nothing written) |

Errors: `bad_params`; `validation { fields: [{field, message}] }` (localized; from our checks or Jira's 400 `errors`,
Jira field ids mapped back to the form's field ids; `errorMessages` ⇒ form-level message); `forbidden_edit`
(no permission); `edit_conflict`; existing Jira/transport errors. Caches (board, sprints, epics, issue detail) are
invalidated for the affected project/issue.

```
FieldMeta {
  id,                 // Jira field id ("summary", "customfield_10100", …)
  name,               // localized label from Jira
  kind,               // "summary" | "description" | "text" | "textarea" | "number" | "date" | "datetime" |
                      // "select" | "multiselect" | "priority" | "user" | "users" | "labels" | "components" |
                      // "versions" | "epic" | "epicName" | "sprint" | "parent" | "estimate" | "storyPoints" | "unsupported"
  required: bool,
  main: bool,         // one of the main fields (shown always) vs required-only extras
  options?: [{ id, name }],      // select/multiselect/priority/components/versions (+ sprint: open/future sprints of the project's boards)
  default?: FieldValue,
  max?: number        // e.g. summary 255
}
FieldValue = string | number | null | string[] | { id } | { name, displayName } | { key, summary } |
             { originalEstimate?, remainingEstimate? }
```

- **Main fields** (when present in the meta, in this order): Summary, Issue type (create only, a separate control),
  Description, Assignee, Priority, Epic Link (not for epics and sub-tasks), Epic Name (epics), Parent (sub-tasks),
  Sprint, Story Points, Dev Story Points, QA Story Points, Labels, Components, Fix Version/s, Due date, Original /
  Remaining estimate. Plus **every other field with `required: true`** (`main: false`), rendered generically by
  kind. Required fields of an unsupported schema ⇒ `kind: "unsupported"`: the form shows them with "This field
  can only be filled in Jira" and disables Create (with an "Open in Jira" button to the Jira create screen).
- Story points select fields (Dev/QA) use their `options`; number Story Points uses a number input.

## UI v0.5

- **Create button** in the top bar on Board, Sprints and Epics & stories: primary button "+ Create" (icon-only with a
  tooltip/aria-label below ~900 px window width); keyboard shortcut `C` (not while typing). The top bar must not
  overflow at 760 px (fix the pre-existing overflow too: avatar, project picker and buttons never overlap; items
  shrink/collapse in a defined order).
- **Create dialog** (large modal, ~720 px, full height scroll, sticky footer):
  - Project (defaults to the current project filter, else the last used project; tracked projects first) and
    Issue type (icons; defaults to the last used type per project, uiPrefs). Changing them reloads the meta
    keeping compatible values (summary, description, labels, assignee).
  - Fields per the meta in the order above; required fields marked; per-field inline errors; the description editor
    per `descriptionFormat`.
  - Context defaults: on the Epics page with an epic expanded/selected ⇒ Epic Link = that epic (if the user opened
    Create from that epic's "+ issue" action); on Sprints from a sprint header "+" ⇒ Sprint = that sprint. (Add a
    small "+" action on sprint headers and on epic rows that opens Create pre-filled.)
  - Footer: "Create another" checkbox (keeps project/type/epic/sprint, clears the rest), Cancel, **Create** (busy
    state). Success ⇒ toast "PROJ-123 created" with "Open" (opens the drawer); the current view refreshes.
  - Unsaved changes ⇒ closing (Esc/Cancel/scrim) asks to discard. Draft is kept in memory if the dialog is closed
    by an error, not persisted.
- **Edit**: an "Edit" button (pencil) in the drawer header ⇒ the same dialog in edit mode, title "Edit PROJ-123",
  pre-filled from `getEditMeta`; project and type are read-only; Save sends only changed fields (`updateIssue`);
  disabled with a tooltip when `fields` is empty (no permission). `edit_conflict` ⇒ message "The issue was changed in
  Jira after you opened it" with **Reload** (re-fetch, keeps the user's edits in fields the other change didn't
  touch, marks the rest) and Cancel. After save the drawer re-renders.
- Pickers: Assignee (search-as-you-type via `searchAssignable`, "Unassigned", "Assign to me"), Epic and Parent
  (search via `searchIssuesFor`, shows key + summary + status), Labels (chips, Enter/comma to add, no spaces),
  multiselects (chips + dropdown with filter), Sprint (open/future sprints, "Backlog" = none), Estimate (Jira
  duration text `1d 2h 30m` validated on blur with the configured hours-per-day; shown as hint).
- All new strings ru/uk/en; both themes; keyboard and screen reader accessible (labels, `aria-invalid`,
  `aria-describedby` for errors, focus to the first invalid field on submit).
- `ui/mock.js`: create meta for 2 projects (one HTML/JEditor with Dev/QA SP selects and a required custom select,
  one wiki with an unsupported required field), edit meta, user/issue search, validation errors from "Jira",
  `edit_conflict`, no-permission issue.

## Security

- The editor never puts Jira or pasted HTML into the live DOM without the v0.4 sanitizer; the serializer escapes
  everything; links/images keep only http(s) URLs.
- `createIssue`/`updateIssue` only write fields present in the current create/edit meta (unknown field ids ⇒
  `bad_params`); values are type-checked against `kind` in Go; strings length-limited (description ≤ 32 000 chars,
  summary ≤ 255); requests go only to the configured Jira origin (existing transport).

## Contract additions (v0.5)

Exact shapes as implemented (the frontend builds against this block; deviations from the sections above are marked **≠**).

### FieldMeta
```json
{ "id": "customfield_10204", "name": "Dev Story Points", "kind": "storyPoints", "required": false, "main": true,
  "options": [{ "id": "10601", "name": "8" }, { "id": "10603", "name": "5" }] }
{ "id": "summary", "name": "Summary", "kind": "summary", "required": true, "main": true, "max": 255 }
{ "id": "priority", "name": "Priority", "kind": "priority", "required": false, "main": true,
  "options": [{ "id": "2", "name": "High" }, { "id": "3", "name": "Medium" }], "default": { "id": "3" } }
{ "id": "customfield_10107", "name": "Sprint", "kind": "sprint", "required": false, "main": true,
  "options": [{ "id": "12", "name": "BI Sprint 7", "state": "active" }, { "id": "13", "name": "BI Sprint 8", "state": "future" }] }
{ "id": "customfield_10301", "name": "Region", "kind": "unsupported", "required": true, "main": false }
```
- `max`: summary 255, text / epicName 255, description / textarea 32000 (absent for other kinds).
- `options` (omitted when none): select, multiselect, priority, components, versions, storyPoints (select-type Dev/QA SP),
  sprint. Disabled options and archived versions are left out. **≠** sprint options carry `state`
  (`"active"|"future"`, active first) and their `id` is the sprint id **as a string**; the sprint FieldValue is a number.
- `default` (omitted when none): Jira's default in FieldValue form (below).
- Order: the main fields present, in the SPEC order (Summary, Description, Assignee, Priority, Epic Link / Epic Name /
  Parent by type, Sprint, Story Points, Dev SP, QA SP, Labels, Components, Fix Version/s, Due date, Time tracking), then
  every other `required` field in Jira's order with `main: false`. Optional fields outside the main list are not
  returned (and cannot be written). Issue type and project are never in `fields`.
- **≠** Sprint: when Jira's create/edit meta has no Sprint field but the project's agile boards have open/future
  sprints, a Sprint field is still returned (name localized, `required: false`); the backend sets it through the agile
  API (`POST /rest/agile/1.0/sprint/{id}/issue`, `null` ⇒ `POST /rest/agile/1.0/backlog/issue`) after the create/update.
  Not for sub-task types (they follow their parent's sprint). A failed sprint move after the issue was created / its
  fields were saved is not an error: the result carries a localized `warning` ("Saved, but the sprint was not changed: …").
- A required `reporter` without a Jira default gets the current user as `default` (`{name, displayName}`); on create it
  may be left out (Jira Server fills it in) and is then not sent.
- `kind` mapping: system fields by id (summary, description, environment ⇒ textarea, assignee/reporter ⇒ user,
  priority, labels, components, fixVersions/versions ⇒ versions, duedate ⇒ date, timetracking ⇒ estimate, parent);
  Epic Link / Epic Name / Sprint / Story Points / Dev SP / QA SP by the discovered field ids (Story Points and Dev/QA SP
  ⇒ `storyPoints` for number or select schemas); custom fields by schema (string textfield/url ⇒ text, textarea,
  number, date, datetime, option/radio/single version/security level ⇒ select, array of option ⇒ multiselect, user,
  array of user ⇒ users, labels, array of version ⇒ versions, array of component ⇒ components). Anything else
  (cascading select, group, project picker, read-only, select without allowed values, …) ⇒ `unsupported`.

### FieldValue per kind (read in `values`/`default`; accepted by createIssue/updateIssue)
| kind | read | write (also accepted) | sent to Jira |
|---|---|---|---|
| summary, text, epicName | `"text"` | string (trimmed; no line breaks) | string |
| description, textarea | `"text"` | string, as is | string |
| number | `2.5` | number | number |
| storyPoints (no options) | `3` | number | number |
| storyPoints (options), select, priority | `{"id":"10601"}` | `{"id":"…"}` (id string or number) | `{"id":"…"}` |
| multiselect, components, versions | `["100","101"]` | `["100"]` or `[{"id":"100"}]` | `[{"id":"100"}]` |
| labels | `["a","b"]` | string[] (trimmed, deduplicated, no whitespace) | `["a","b"]` |
| user | `{"name":"bob","displayName":"Bob"}` | `{"name":"bob"}` or `"bob"`; `null` = unassigned | `{"name":"bob"}` / `null` |
| users **≠** | `[{"name":"bob","displayName":"Bob"}]` | `[{"name":"bob"}]` or `["bob"]` | `[{"name":"bob"}]` |
| epic | `{"key":"RBI-1","summary":"Platform"}` | `"RBI-1"` or `{"key":"RBI-1"}`; `null` = none | `"RBI-1"` / `null` |
| parent | `{"key":"RBI-2","summary":"…"}` | `"RBI-2"` or `{"key":"RBI-2"}` (not null) | `{"key":"RBI-2"}` |
| sprint | `12` (the issue's active/future sprint) | sprint id number from `options`; `null` = backlog | number / agile move |
| date | `"2026-12-01"` | `"YYYY-MM-DD"`; `""`/`null` = none | `"2026-12-01"` |
| datetime | RFC 3339 (`"2026-12-01T10:30:00+02:00"`) | RFC 3339, or local `"YYYY-MM-DDTHH:MM[:SS]"` | `"2026-12-01T10:30:00.000+0200"` |
| estimate | `{"originalEstimate":"1d","remainingEstimate":"4h"}` | same; each a Jira duration (`1w 2d 3h 30m`, `2h30m`, `1.5h`, w/d/h/m each once) checked with `hoursPerDay`; `""`/missing part = not sent | `{"originalEstimate":"1d 2h"}` (normalized: lower case, one space) |
| unsupported | – | never writable | – |

Empty values (`null`, `""`, `[]`) of a `required` field ⇒ field error "required". On create, empty optional values are
simply not sent; on update they clear the field (`null` / `[]`). Clearing an estimate is not supported.

### Methods
```jsonc
// getCreateMeta {"project":"RBI","issueTypeId":"10001"}   (issueTypeId optional; unknown/absent ⇒ first non-subtask type)
{ "project": { "key": "RBI", "name": "BI platform" },
  "issueTypes": [ { "id": "10000", "name": "Epic", "kind": "epic", "subtask": false },
                  { "id": "5", "name": "Подзадача", "kind": "subtask", "subtask": true } ],
  "issueTypeId": "10001", "fields": [ /* FieldMeta */ ], "descriptionFormat": "html" }

// getEditMeta {"key":"RBI-12"}
{ "key": "RBI-12", "issueTypeId": "3", "project": { "key": "RBI", "name": "BI platform" },
  "updated": "2026-11-12T10:00:00.000+0200",   // opaque Jira timestamp: pass it back unchanged as expectedUpdated
  "fields": [ /* FieldMeta; [] ⇒ no edit permission */ ], "descriptionFormat": "wiki",
  "values": { "summary": "Old", "assignee": { "name": "alex", "displayName": "Alex" }, "customfield_10107": 12 } }
  // values: only fields of `fields` that have a value

// searchAssignable {"project":"RBI","query":"bo"} or {"key":"RBI-12","query":""}   (query ≤ 100 chars)
[ { "name": "bob", "displayName": "Bob" } ]          // ≤ 20

// searchIssuesFor {"purpose":"epic"|"parent","project":"RBI","query":"load"}   (query ≤ 100 chars)
[ IssueRef ]                                          // ≤ 20, not done, newest updated first

// createIssue {"project":"RBI","issueTypeId":"3","values":{"summary":"New","customfield_10107":12}}
{ "key": "RBI-77", "url": "https://sd.local/browse/RBI-77" }
{ "key": "RBI-77", "url": "https://sd.local/browse/RBI-77",
  "warning": "Saved, but the sprint was not changed: Jira rejected the request (HTTP 400): Sprint is closed" }

// updateIssue {"key":"RBI-12","values":{"summary":"New"},"expectedUpdated":"2026-11-12T10:00:00.000+0200"}
{ "key": "RBI-12", "updated": "2026-11-12T10:05:00.000+0200" }   // the new opaque timestamp
{ "key": "RBI-12", "updated": "",                                 // saved, but `updated` could not be re-read: refetch
  "warning": "Saved, but the sprint was not changed: …" }         // fields saved, the agile sprint move failed
```
- `warning` (optional, localized): the save succeeded but a part did not apply; show it, do not treat it as an error.
  If only the sprint was to change and that fails, updateIssue returns the error (nothing was saved).
- `updated: ""`: the write succeeded but the new timestamp is unknown ⇒ call getEditMeta again before another save.
- `searchIssuesFor` JQL: `project = "KEY" AND issuetype = Epic AND statusCategory != Done` (epic) or
  `… AND issuetype not in subTaskIssueTypes() AND statusCategory != Done` (parent), plus
  ` AND (summary ~ "q*" [OR key = "Q"])` for a non-empty query and ` ORDER BY updated DESC` **≠**: `q` keeps only
  letters and digits, plus `-` `_` `.` between them, everything else becomes a space, lower-cased (so no Lucene
  operators or AND/OR/NOT); the key clause only when the query is an issue key. The search is sent with
  `validateQuery=false`, so a key that does not exist yet simply finds nothing.
- `descriptionFormat` of a project: the 10 latest issues with `description is not EMPTY`; `"html"` only if more than half
  look like HTML; no sample or a failing search ⇒ `"wiki"`. Cached per project for 30 min.
- **HTML descriptions** are checked against "HTML description contract" above (reject, never rewrite;
  `validation` on that field with "This formatting cannot be saved: <reason>"): the `description` value always when the
  project (create, format of the create meta) or the issue (edit: its own `descriptionFormat`, the project's when the
  description is empty) uses HTML descriptions. For wiki targets (and `textarea` values) the text is checked as HTML —
  and so rejected unless it is contract HTML — when it looks like HTML or has a tag (`<` + letter, read like a browser)
  with any attribute, a deny-listed tag (script, style, iframe, svg, img, form, details, …), a tag that never ends, or a
  comment/CDATA/PI. Regions Jira's wiki renderer shows literally — `{code…}…{code}`, `{noformat…}…{noformat}` (an
  unclosed one runs to the end) and one-line `{{…}}` — are exempt only when the wiki format is **known**: an edit of an
  issue whose own description is wiki, or a project format from a real sample (≥ 1 described issue read). A guessed
  format (no sample, failed sample search) screens the whole text, and so does any text containing `{html`, `{iframe`,
  `{script` or `{javascript`. So in a known wiki target `Map<String, Integer>` is fine inside `{code}` or `{{…}}` but
  rejected in plain text. Images: create — the Jira origin only; edit — the Jira origin or a `src`
  already in the issue's current description; on the Jira origin only attachment/thumbnail/avatar/JEditor-file paths.
  Attribute values may contain `&lt;` but not a raw `<`; URLs never contain `<`.
- `createIssue` refuses the same request (project, type and values) while it is running or for 10 s after it
  succeeded: error `busy` ("This issue is already being created").
- `updateIssue` re-reads the issue's `updated` and edit meta first; a different `updated` (other than the same instant
  written differently) ⇒ `edit_conflict`, nothing written. Only the given fields are sent (one PUT), then the agile
  sprint move if any. `createIssue` uses the create meta cached for 10 min (as returned by getCreateMeta).
- After a successful create/update: board, sprint groups, epics, stories, project counts and epic names are dropped and
  the existing event `board` (`{}`) is emitted. There is no issue-detail cache (getIssue always reads Jira).

### Errors
```jsonc
{ "code": "bad_params", "message": "Field customfield_10302 cannot be written here" }   // a field id not in the meta
{ "code": "validation", "message": "Some fields are invalid",                              // our checks, localized
  "fields": [ { "field": "summary", "message": "This field is required" },
              { "field": "customfield_10301", "message": "This field can only be filled in Jira" } ] }
{ "code": "validation", "message": "Some fields are invalid: foo: Bar; Team is wrong",     // Jira's 400
  "fields": [ { "field": "customfield_10300", "message": "Bad team" },                     // Jira's text, as sent
              { "field": "timetracking", "message": "Bad estimate" } ] }                   // timeoriginalestimate etc. ⇒ timetracking
{ "code": "forbidden_edit", "message": "You have no permission to edit this issue in Jira" }  // empty edit meta, HTTP 403
{ "code": "busy", "message": "This issue is already being created" }                     // duplicate createIssue
{ "code": "edit_conflict", "message": "The issue was changed in Jira after you opened it" }
```
- Jira 400: `errors` of form fields ⇒ `fields`; `errors` of other ids (as `id: message`) and `errorMessages` are appended
  to `message`. At most 20 entries are used, each message ≤ 300 characters, the whole `message` ≤ 2000, field ids
  ≤ 64, control characters dropped. Unknown field ids in `bad_params` messages are cut to 64 characters. A create in a project where no type can be created ⇒ `forbidden_edit` ("You cannot create issues in
  this project").
- Field messages (localized): required, too long (`At most N characters`), invalid value, choose one of the offered
  values, labels without spaces, date `YYYY-MM-DD`, date and time, Jira duration, number, issue key, line breaks not
  allowed, can only be filled in Jira, formatting cannot be saved.
- `getCreateMeta`: the type list is cached per project and the fields per resolved type (10 min); an unknown
  `issueTypeId` still falls back to the first non-subtask type.

## Acceptance criteria v0.5

1. "+ Create" in the top bar of Board, Sprints, Epics (and `C`) opens a create form for the current project with
   the main fields + required fields from Jira; creating an issue works and the view refreshes.
2. An existing issue can be edited from the drawer; only changed fields are sent; conflicts are detected.
3. HTML (JEditor) descriptions are edited in a rich-text editor and saved as clean HTML with panels and images
   preserved; wiki descriptions are edited as raw markup with preview.
4. Validation errors (ours and Jira's) are shown per field; unsupported required fields block creation with a way
   to finish in Jira.
5. `go vet`, `go test` pass; exe ≤ 5 MB; ru/uk/en; light/dark; no network resources in the page; top bar fits at 760 px.

# v0.6 — issue panel: more fields, inline editing, links, comments

Decisions (user): fields are edited **in place in the issue panel** (click → editor → save that one field; the
v0.5 edit form stays); MRs/branches come from **fields of the issue** (filled in by hand); review = code + design only.

Real-Jira facts (checked):
- `Merge_requests` (`customfield_13619`) and `Branches` (`customfield_13702`): **textarea**, free text, typically one
  URL / one `repo: branch` per line, CRLF; the literal default text **"Не заполнено"** means empty.
  Examples: `https://gitlab.rozetka.company/marketplace/front/-/merge_requests/6224`,
  `front: MF-8026/moderation-stages-chips-filters`, `warranty-service-api:WS-131`.
- `Developer` (`customfield_10400`), `Reviewer` (`customfield_10402`), `QA Engineer` (`customfield_10401`):
  **multi-user pickers**; empty is represented by a placeholder user whose display name is **"неназначено"**.
- Issue links: standard (`issuelinks`, types like Blocks: inward "is blocked by" / outward "blocks", Relates).
- The dev-status panel is empty / access-denied in this Jira — not used.

## Discovery (by field name or clause name, case-insensitive, trimmed; absent ⇒ feature hidden)
`mergeRequests`: "Merge_requests" | "Merge requests"; `branches`: "Branches"; `developer`: "Developer";
`reviewer`: "Reviewer"; `qa`: "QA Engineer" (fallback "QA"). Dev/QA story points as in v0.4.

## IssueDetail (additions)
```
updated stays as is (display); for edits the UI always uses getEditMeta.updated
developer?: [User], reviewer?: [User], qa?: [User]      // present (possibly []) iff the field exists;
                                                        // placeholder users ("неназначено", "unassigned",
                                                        // "не назначено", case-insensitive) are dropped
mergeRequests?: string, branches?: string               // raw text, "\r\n"→"\n", trimmed; "Не заполнено" ⇒ "";
                                                        // present iff the field exists
links: [LinkDTO + { id }]                               // link id added (for removal)
commentFormat: "html" | "wiki"                          // see Comments
```
Placeholder handling is display-only: writes send exactly what the user chose (`[]` clears; Jira may re-apply its default).

## Inline editing (drawer)
- Editable in place: **description**, **assignee**, **reporter**, **developer**, **reviewer**, **qa**, **story points**,
  **dev story points**, **qa story points**, **merge requests**, **branches**, and **links** (add/remove).
- On the first inline edit of an issue the UI calls `getEditMeta {key}` (once per drawer open, reused; never
  prefetched on open). A field not present in the edit meta ⇒ read-only (tooltip "can't be edited in Jira for this
  issue"); `fields: []` ⇒ no edit permission (v0.5 behaviour).
- Backend: the edit meta's form fields now also include, when present in Jira's editmeta: `reporter` (kind `user`),
  developer/reviewer/qa (kind `users`), mergeRequests/branches (kind `textarea`), so `updateIssue` accepts them
  (they also appear in the v0.5 edit form after the main fields). No other contract change.
- Save = `updateIssue {key, values: {fieldId: value}, expectedUpdated}` with **one field**; on success the drawer
  re-renders from `getIssue` and the cached edit meta's `updated` is refreshed (refetch edit meta if `updated` is "");
  `edit_conflict` ⇒ inline message "Changed in Jira meanwhile" + Reload (re-fetch, keep the user's draft in the
  editor). `validation` ⇒ inline field error. `warning` ⇒ toast.
- Editors: people → the v0.5 pickers (assignee: `searchAssignable`; reporter/developer/reviewer/qa: new
  `searchUsers`), multi-user as chips; numbers / select story points as in the form; MR/branches → plain multi-line
  textarea; description → the v0.5 rich editor (html) or wiki textarea + preview, same lossy notice rules.
- UX: pencil appears on hover/focus of the field row (rows are keyboard-focusable: Enter/F2 opens the editor);
  Save / Cancel buttons; Enter saves single-line editors, Ctrl+Enter multi-line/rich; Esc cancels (asks only if the
  rich description changed); busy state; one inline editor open at a time (opening another asks to save/discard if
  dirty); focus returns to the field row.

## Merge requests & branches (display)
- A "Development" section in Details: **Merge requests** — one row per non-empty line; an `http(s)://` URL becomes a
  link (label: `<project path> !<iid>` for GitLab MR URLs `…/<group>/<project>/-/merge_requests/<n>`, else the URL);
  other text shown as text. **Branches** — one row per line, monospace; `repo: branch` / `repo:branch` shown as repo
  chip + branch. Empty ⇒ "—". Copy-to-clipboard button for branch names.
- Opening links: new method `openFieldLink {key, field: "mergeRequests"|"branches", url}` — the backend re-reads that
  field of that issue and opens `url` via the shell **only if** it appears verbatim as a whitespace-separated token in
  the field's current value and is an absolute `http(s)` URL without userinfo or control characters. Otherwise
  `bad_params`. (`openUrl` keeps its Jira-origin rule.)

## Links ("Relates to", "Blocks", …)
- Display as today, grouped by relation phrase, plus a remove (×) button per link (confirm) and an "+ Link issue"
  action: relation select (every link type's outward and inward phrase, from `getLinkTypes`), issue picker
  (`searchIssuesFor` with new purpose `"link"`: any issue type in any project the user can see; query by key or text),
  Save.
- Methods:
  - `getLinkTypes` → `[{ id, name, inward, outward }]` (cached 1 h).
  - `addIssueLink { key, typeId, direction: "outward"|"inward", target }` → null. Meaning: "`key` <phrase> `target`"
    where phrase = the type's outward (direction outward) or inward text. Map to
    `POST /rest/api/2/issueLink {type:{id}, inwardIssue:{key}, outwardIssue:{key}}` so that reading `key`'s
    `issuelinks` afterwards shows exactly that phrase towards `target` (verify the direction mapping against the
    Jira Server docs and encode it in a test with a fake Jira that mirrors Jira's GET representation).
  - `deleteIssueLink { key, linkId }` → null; the backend first reads `key`'s links and only deletes a link id that
    belongs to `key` (else `bad_params`); `DELETE /rest/api/2/issueLink/{id}`.
  - Errors: 403 ⇒ `forbidden_edit`; 400/404 ⇒ `validation`/`not_found`. Caches invalidated + `board` event.

## Comments
- `IssueDetail.commentFormat`: `"html"` if most of the issue's last ≤ 10 comments look like HTML; `"wiki"` if they
  look like wiki/plain; no comments ⇒ the project's description format (v0.5, known/guessed).
- Method `addComment { key, body, format }` → `CommentDTO` (the created comment). `format` must equal the issue's
  current `commentFormat` (else `bad_params`). `html` ⇒ strict `checkHTML` (images: Jira attachment paths only);
  `wiki` ⇒ the full unmasked screen (like textarea). Body 1–32 000 chars, trimmed; empty ⇒ validation.
  `POST /rest/api/2/issue/{key}/comment {body}`; 403 ⇒ `forbidden_comment` (localized). The comment-alert
  poller must not alert on the user's own new comment (already filtered by author — keep it so).
- UI: at the bottom of the Comments tab a composer: the rich editor (html) or wiki textarea + preview, "Comment"
  button (Ctrl+Enter), Cancel clears (asks if non-empty). On success the new comment appears at the end, the
  composer clears, the tab count updates, focus goes to the new comment. Draft kept per issue in memory while the
  app runs. Errors inline.

## Methods (additions)
`searchUsers { query }` → `[User]` (≤ 20; `GET /rest/api/2/user/search?username=Q&maxResults=20`; empty query ⇒ []),
`openFieldLink`, `getLinkTypes`, `addIssueLink`, `deleteIssueLink`, `addComment`; `searchIssuesFor` purpose `"link"`.

## Contract additions (v0.6)

Exact shapes as implemented (deviations / precisions marked **≠**).

### IssueDetail (getIssue)
```jsonc
{
  // … v0.5 fields unchanged …
  "links": [ { "id": "501", "relation": "blocks", "issue": { "key": "RBI-61", "summary": "…", /* IssueRef */ } } ],
  "developer": [ { "name": "bob", "displayName": "Bob" } ],   // present iff the field exists in this Jira
  "reviewer": [],                                             // [] — empty, or only placeholder users
  "qa": [],                                                   // a null field value is [] too
  "mergeRequests": "https://gitlab.example/front/-/merge_requests/6224\nfront: MF-8026/chips",  // present iff the field exists
  "branches": "",                                             // "" — empty or "Не заполнено"
  "commentFormat": "html"                                     // always present: "html" | "wiki"
}
```
- People: placeholder users are dropped when the display name or user name is (trimmed, case-insensitive)
  "неназначено", "не назначено" or "unassigned". MR/branch text: `\r\n` and `\r` → `\n`, trimmed, the default
  text "Не заполнено" (case-insensitive) ⇒ `""`.
- `commentFormat`: the latest 10 comments, counting the non-empty ones; `"html"` when more than half of those look like
  HTML, else `"wiki"`; none counted ⇒ the project's description format (v0.5; `"wiki"` when unknown).
- Field discovery (field name or clause name, case-insensitive, trimmed; first match wins): mergeRequests
  "Merge_requests" | "Merge requests", branches "Branches", developer "Developer", reviewer "Reviewer", qa
  "QA Engineer", else "QA".

### Edit meta (getEditMeta / updateIssue)
- When Jira's edit meta has them, `fields` also contains, **after** the main fields and before the required extras,
  with `main: true`: `reporter` (kind `user`), developer / reviewer / qa (kind `users`), mergeRequests / branches (kind
  `textarea`, `max` 32000) — the kinds come from the Jira schema as in v0.5 (an unsupported schema leaves the field
  out unless required). Create meta is unchanged (a required `reporter` keeps its v0.5 default).
- `role` (new, optional) names a field's purpose independently of its instance-specific id and localized name:
  `"reporter"`, `"assignee"`, `"description"` (system fields), `"developer"`, `"reviewer"`, `"qa"`, `"mergeRequests"`,
  `"branches"`, `"storyPoints"`, `"devStoryPoints"`, `"qaStoryPoints"` (discovered fields); omitted for every other
  field (create meta too). Match fields by `role`, never by `name`:
```jsonc
{ "id": "customfield_10400", "name": "Developer", "kind": "users", "required": false, "main": true, "role": "developer" }
{ "id": "customfield_10204", "name": "Dev Story Points", "kind": "storyPoints", "required": false, "main": true,
  "options": [ { "id": "10601", "name": "8" } ], "role": "devStoryPoints" }
{ "id": "summary", "name": "Summary", "kind": "summary", "required": true, "main": true, "max": 255 }   // no role
```
- `values` of these fields: people as in v0.5 (`users` ⇒ `[{name, displayName}]`) **≠** with placeholder users
  dropped; MR/branch text normalized like IssueDetail (`"Не заполнено"` ⇒ `""`).
- Writes as v0.5: `users` accepts `[{"name"}]` or `["name"]` (`[]` clears); a textarea `""` is sent as `null`
  (clears); textarea values get the full wiki screen (no `{code}` exemption). Example single-field saves:
```jsonc
{ "key": "RBI-50", "values": { "customfield_10400": [ { "name": "bob" }, "carol" ] }, "expectedUpdated": "…" }
// → PUT {"fields":{"customfield_10400":[{"name":"bob"},{"name":"carol"}]}}
{ "key": "RBI-50", "values": { "reporter": { "name": "carol" } }, "expectedUpdated": "…" }
{ "key": "RBI-50", "values": { "customfield_13619": "https://gitlab.example/x/-/merge_requests/1\nhttps://gitlab.example/y" }, "expectedUpdated": "…" }
```

### Methods
```jsonc
// searchUsers {"query":"bo"}           (trimmed, ≤ 100 chars; "" ⇒ [] without a request)
[ { "name": "bob", "displayName": "Bob" } ]   // ≤ 20, placeholder users dropped

// openFieldLink {"key":"RBI-40","field":"mergeRequests"|"branches","url":"https://gitlab.example/front/-/merge_requests/6224"}
null   // opened via the shell; bad_params unless the url equals a whitespace-separated token of the field's current
       // value (re-read now) — verbatim, or with the token's trailing ".,;:)" characters removed ("…/57)," ⇒ "…/57") —
       // and is an absolute http(s) URL with a host, without user info, spaces, control or invisible format (Cf)
       // characters, at most 2 KB; bad_params also when this Jira has no such field

// getLinkTypes                          (cached 1 h)
[ { "id": "10000", "name": "Blocks", "inward": "is blocked by", "outward": "blocks" } ]

// addIssueLink {"key":"RBI-60","typeId":"10000","direction":"outward","target":"RBI-61"}
null   // afterwards RBI-60 shows "blocks RBI-61" and RBI-61 shows "is blocked by RBI-60";
       // direction "inward" ⇒ RBI-60 "is blocked by" the target. target is upper-cased.

// deleteIssueLink {"key":"RBI-60","linkId":"501"}
null   // only a link listed on `key` (source or destination); else bad_params

// addComment {"key":"RBI-80","body":"Fixed in *1.2*","format":"wiki"}
{ "id": "10234", "author": { "name": "alex", "displayName": "Alex" },
  "created": "2026-11-12T11:00:00+02:00", "updated": "2026-11-12T11:00:00+02:00", "body": "Fixed in *1.2*" }

// searchIssuesFor {"purpose":"link","query":"cma-12"}    (project optional and ignored for "link")
[ IssueRef ]   // JQL: (summary ~ "cma-12*" OR key = "CMA-12") ORDER BY updated DESC — any project, any type,
               // any status; an empty query ⇒ "ORDER BY updated DESC"
```
- Link direction (Jira Server): `POST /rest/api/2/issueLink` creates the link with `inwardIssue` as its **source**
  (the issue that shows the outward phrase) and `outwardIssue` as its destination. So
  "key <outward> target" ⇒ `{type:{id}, inwardIssue:{key}, outwardIssue:{target}}`;
  "key <inward> target" ⇒ `{type:{id}, inwardIssue:{target}, outwardIssue:{key}}`.
- `addIssueLink` errors: `bad_params` (invalid keys, target = key, unknown `typeId` (not in getLinkTypes), direction
  other than outward/inward); Jira 403 ⇒ `forbidden_edit`; 400 ⇒ `validation` (Jira's text in `message`); 404 ⇒
  `not_found`. `deleteIssueLink`: same mapping. Both drop the issue caches and emit `board` (`{}`).
- `addComment`: the body is trimmed; `format` must equal the issue's current commentFormat (the latest ≤ 10 comments,
  else the project's format) — else `bad_params` (`"format must be \"wiki\" for this issue"`). Checks (`validation`
  on field `"body"`): empty ⇒ required; > 32 000 characters ⇒ too long; `html` ⇒ the HTML description contract (images
  on the Jira origin's attachment/avatar paths only); `wiki` ⇒ the full wiki screen (no `{code}` exemption). Jira 403 ⇒
  `forbidden_comment` ("You have no permission to comment on this issue in Jira"); Jira 400 ⇒ `validation` (Jira's
  `comment` error on `"body"`). No event (no cached view shows comments); the comment poller never alerts on my own
  comments.

## Acceptance criteria v0.6
1. The issue panel shows Merge requests, Branches (with working links), Developer, Reviewer, QA, and links with ids.
2. Description, assignee, reporter, developer, reviewer, QA, story points (incl. Dev/QA), MRs and branches can be
   edited in place; links can be added and removed; conflicts and errors are shown inline.
3. Comments can be written (HTML or wiki per the issue) and appear immediately.
4. `go vet`, `go test`, `./build.sh --check` pass; exe ≤ 5 MB; ru/uk/en; light/dark; keyboard accessible.

# v0.7 — glass redesign (reference: oats-os-04122735.figma.site)

User request: restyle the app after the reference board: **rounded window corners; a translucent, blurred
("frosted glass") window; the sidebar visually separate from the main panel with a gap (two parts of one window);
no classic title bar, but window buttons on the right; the reference's fonts and icon style; neutral (not greenish)
glass**. Pipeline: code review + design review.

Reference tokens (measured in Edge from the live site; the site itself only *imitates* blur with translucent
gradients over a darkened photo — Jiraffe uses the real OS backdrop):
- Font **Manrope** (SIL OFL), variable 400–800. Embedded subsets in `assets/fonts/`: `manrope-latin.woff2`
  (U+0000-00FF, U+0131, U+0152-0153, U+02BB-02BC, U+02C6, U+02DA, U+02DC, U+0304, U+0308, U+0329, U+2000-206F, U+20AC,
  U+2122, U+2191, U+2193, U+2212, U+2215, U+FEFF, U+FFFD) and `manrope-cyrillic.woff2` (U+0301, U+0400-045F,
  U+0490-0491, U+04B0-04B1, U+2116). Fallback `"Segoe UI Variable", "Segoe UI", system-ui, sans-serif`.
- Accent `#fff200` (hover `#fff652`, gradient end `#fff78a`), text on accent `#12180c`; lavender `#adb5ff`,
  violet `#7d8cff`, pink `#f0a8ff`.
- Glass panel: `linear-gradient(135deg, a .78, b .65)` + 1px `rgba(255,255,255,.11)` + `0 24px 70px rgba(0,0,0,.35),
  inset 0 1px rgba(255,255,255,.07)`; card: `linear-gradient(145deg, …57, …62)` + `rgba(255,255,255,.114)` border,
  `inset 0 1px rgba(255,255,255,.055), 0 12px 26px rgba(0,0,0,.12)`, hover border `.20` + `translateY(-2px)`;
  stat tile `linear-gradient(145deg, rgba(255,255,255,.075), rgba(255,255,255,.024))`; wells (search, segmented)
  `rgba(…, .25)`; columns `rgba(…, .18)` + `.07` border.
  **Jiraffe: same structure and alphas, neutral greys instead of the green-tinted ones** (e.g. panel
  `rgba(40,41,44,.62)→rgba(22,23,26,.52)`, card `rgba(78,80,84,.45)→rgba(34,35,38,.55)` — final values tuned on the
  real acrylic so contrast holds).
- Radii: main panel 28–34, sidebar 24, column 21, stat 19, card 16, nav/icon button 14, controls 13, segmented 12/9,
  pill 7, tag 6. Sidebar 64px wide, nav items 42×42 (active = accent square with dark icon), gap sidebar→panel 16–20px,
  column gap 12, card gap 9, controls 42px tall.
- Tags: lime `#fff78a`/`rgba(255,242,0,.11)`, blue `#a8d9ff`/`rgba(98,177,255,.12)`, violet `#d0c5ff`/
  `rgba(154,129,255,.13)`, orange `#ffc39e`/`rgba(255,141,78,.13)`. Avatars pastel `#d5ff78 #c6c8ff #ffc3a7` with
  text `#273024`. Progress 4px, track `rgba(255,255,255,.10)`, fill `linear-gradient(90deg,#fff200,#fff78a)`.
- Icons: Lucide-style inline SVG, 24 viewBox, `stroke-width 1.8`, round caps/joins; sizes 19 (nav), 18, 17, 15.
- Motion: `.2s` transitions, entrance fade-up `.55s`; Jiraffe keeps `prefers-reduced-motion` and its focus rings.
- **Do not copy** the reference's tiny type (8–12px) or its muted text alphas (.40–.45 fail AA): keep Jiraffe's
  scale (≥ 11px, body 13–14px) and text ≥ AA (muted ≥ .60 on cards). Keep the light theme (light frosted glass,
  same structure).

## Window (Go, Windows)
- **Frameless**: keep `WS_OVERLAPPEDWINDOW` (snap, min/max animations, Alt+Space) but remove the non-client area:
  subclass the go-webview2 window (`SetWindowLongPtrW(GWLP_WNDPROC)`, forward everything else to the original proc)
  and answer `WM_NCCALCSIZE(wParam=TRUE)` with 0; when maximized, inset the client rect by the frame thickness
  (`GetSystemMetricsForDpi(SM_CXFRAME)+SM_CXPADDEDBORDER`) so content doesn't spill off-screen. Trigger a frame
  change after subclassing (`SetWindowPos(SWP_FRAMECHANGED)`).
- **Rounded corners**: `DWMWA_WINDOW_CORNER_PREFERENCE (33) = DWMWCP_ROUND (2)` (Windows 11; ignored on 10).
- **Glass**: when the OS supports it (build ≥ 22621) and the user's Windows transparency setting
  (`HKCU\…\Personalize\EnableTransparency`) is on, the window gets a see-through blur backdrop and the WebView2
  default background is **fully transparent** (`WEBVIEW2_DEFAULT_BACKGROUND_COLOR=0x00000000` before creation).
  Default backdrop (v0.7b): plain blur, `SetWindowCompositionAttribute(WCA_ACCENT_POLICY,
  ACCENT_ENABLE_BLURBEHIND)` (undocumented API) with `DWMWA_SYSTEMBACKDROP_TYPE = DWMSBT_NONE (1)` and the 1 px frame
  extension for the shadow. It is **untinted**: the page supplies the tint (measured gap colour with the v0.7 CSS:
  over black `#060606`, over `#808080` `#535353`, over white `#A0A0A0`), and it **stays while the window is
  inactive**. If the accent call fails, fallback: the documented system acrylic, `DwmExtendFrameIntoClientArea(-1)` +
  `DWMWA_SYSTEMBACKDROP_TYPE (38) = DWMSBT_TRANSIENTWINDOW (3)` (strongly tinted, opaque while inactive; known
  residual: faint DWM caption-button glyphs at the top right). With glass the subclass paints the window's own
  surface black (`WM_ERASEBKGND`/`WM_PAINT`; black is transparent for DWM) — unpainted it showed as a pale layer.
  Test switch `JIRAFFE_BACKDROP` (not for users): `A` system acrylic, `B[:<ABGR hex>]` accent acrylic
  (`ACCENT_ENABLE_ACRYLICBLURBEHIND`, default tint `0x40141518`), `C` plain blur (the default). Otherwise (Windows
  10, older 11, transparency off): no backdrop, opaque WebView background (current `--bg` colours), the page uses
  opaque surfaces.
- **Window state for the page**: method `getWindowInfo` → `{ glass: bool, maximized: bool, active: bool, resizing: bool }`; event
  `windowState` with the same shape on maximize/restore, activate/deactivate (WM_ACTIVATE) and when the transparency
  setting changes (WM_SETTINGCHANGE "ImmersiveColorSet").
- **Window controls**: method `windowControl { action }` with `action` ∈ `minimize | toggleMaximize | close | drag |
  resize`, plus `edge` for resize (`n s e w ne nw se sw`). `drag`/`resize` run on the UI thread:
  `ReleaseCapture(); PostMessage(hwnd, WM_NCLBUTTONDOWN, HTCAPTION | HTLEFT…, 0)` (the Wails approach); only valid
  while the left button is down (`GetAsyncKeyState`); `close` = the existing close-to-tray behaviour. Bad action/edge ⇒
  `bad_params`. Headless/non-Windows: no-op.
- Minimum window size stays; DPI awareness unchanged.

## UI
- **Shell**: `html, body` transparent when `glass`, else opaque `--bg`. Layout = window padding (12px; 0 when
  maximized) + **sidebar panel** (64px, radius 24, glass) + **gap 14–16px** + **main panel** (radius 28; 0 outer radius
  when maximized) — the gap and the padding show the bare window glass, so the two panels read as separate parts.
- **Sidebar** (the current nav rail): Jiraffe mark on an accent square at the top (40×40, radius 14), nav items
  42×42 radius 14 (hover `rgba(255,255,255,.08)`, active = accent square, dark icon), bottom group (notifications,
  settings, avatar). Labels as tooltips (`title` + `aria-label`); keyboard and current-page semantics as today.
- **Top bar** (inside the main panel, replaces the window title bar): eyebrow (uppercase, letter-spacing .16em) +
  page title (Manrope 600–650, ~22px, -0.02em) on the left; on the right the existing controls (project filter,
  search, refresh, "+ Create" as the accent button) **then the window buttons** (minimize, maximize/restore, close —
  40×32 icon buttons, close hover red `#e81123` with white icon; tooltips; keyboard reachable; maximize icon swaps
  with state). The empty areas of the top bar and of the sidebar are **drag regions** (`mousedown` primary button
  on a `[data-drag]` element and not on an interactive child ⇒ `windowControl drag`; `dblclick` ⇒ toggleMaximize).
  Invisible 6px **resize handles** on the window edges and 12px corners (hidden when maximized) ⇒ `windowControl
  resize`. In the mock (browser) the buttons are present but log only.
- **Surfaces**: board columns, cards, sprint rows, epics tree, dashboard cards, drawer, dialogs, menus, toasts,
  settings — all restyled with the tokens above (glass panel / card / well / stat tile), Manrope, Lucide-style icons
  (stroke 1.8; redraw the sprite in that style), accent yellow for primary actions, focus rings, selected states,
  progress bars; status/category colours keep their meaning (todo/in-progress/done), mapped onto the reference
  palette (dots `#fff200`/`#7d8cff`/`#f0a8ff`-like, done calmer). Tag-like chips (epic, labels) use the four tag
  tints. Avatars pastel.
- **Drawer and modals** float as glass panels over the main panel (radius 24/28), scrim darker+blur.
- **Light theme**: the same structure with light frosted glass (white tints, dark text, accent darkened for contrast
  where it is text — e.g. `#8a8000`-ish on light; the accent square keeps `#fff200` with dark icon).
- **Performance**: use CSS `backdrop-filter` only for the drawer/modal scrim and floating menus (where there is page
  content behind), not on every card; the window blur comes from the OS.
- Fonts embedded as base64 `@font-face` in `app.css` (CSP already allows `font-src data:`), `font-display: block`,
  both subsets with their unicode ranges; `assets/fonts/OFL.txt` with the licence + a README "Third-party" note.
- Size budget: exe ≤ 5 MB (fonts ≈ 39 KB).

## Contract additions (v0.7)

```jsonc
// getWindowInfo            (no params)
{ "glass": true, "maximized": false, "active": true }

// event "windowState" — same shape; sent when one of the values changes:
// maximize/restore (WM_SIZE), activate/deactivate (WM_ACTIVATE), transparency
// setting (WM_SETTINGCHANGE "ImmersiveColorSet")
{ "type": "windowState", "payload": { "glass": false, "maximized": true, "active": true } }

// windowControl {"action":"minimize"|"toggleMaximize"|"close"|"drag"}
// windowControl {"action":"resize","edge":"n"|"s"|"e"|"w"|"ne"|"nw"|"se"|"sw"}
null
```
- `glass`: the glass backdrop is on (Windows 11 build ≥ 22621, transparency effects on at window creation, and the
  backdrop accepted) **and** transparency is still on. When it is false the page must paint opaque
  surfaces: the WebView2 background is transparent whenever the window was created for glass, so turning
  transparency off at runtime only flips `glass` (the backdrop stays until the window is reopened); turning it on at
  runtime takes effect the next time the window opens.
- `windowControl` errors: `bad_params` for another action, a missing/unknown `edge` on resize, or an `edge` on any
  other action. `drag`/`resize` only start while the primary mouse button is down (call them from `mousedown`);
  `close` = the window's close (to the tray). Without a frameless window (headless, tests, non-Windows): `getWindowInfo`
  ⇒ `{glass:false, maximized:false, active:false}`, `windowControl` ⇒ `null`, nothing happens.
- The window has no title bar or frame; resizing is only through `windowControl resize` (the page's edge handles).
  Without glass the DWM frame is extended 1 px into the top of the client area (`DwmExtendFrameIntoClientArea
  {0,0,1,0}`) so Windows draws the standard window shadow and border; the page covers that row. Maximized, the client
  rect leaves 1 px free on each monitor edge with an auto-hide taskbar (`ABM_GETAUTOHIDEBAREX`) so the taskbar can
  still slide out.
  `setWindowTheme` keeps working (its `dark` flag tints the fallback system acrylic, not the default blur; `caption`
  no longer shows).

## Acceptance criteria v0.7
1. On Windows 11 with transparency on: frameless window with rounded corners, acrylic (blurred desktop) visible
   through the translucent panels and in the gap between sidebar and main panel; window buttons top-right work
   (min / max-restore / close-to-tray), drag by empty header/sidebar areas, double-click maximizes, edges resize,
   Aero Snap works.
2. Without glass support: same layout, opaque surfaces, everything works.
3. The UI uses Manrope, Lucide-style icons, accent yellow, glass surfaces; both themes; AA contrast; reduced motion.
4. `./build.sh --check` passes; exe ≤ 5 MB; no network resources.

## v0.7.1 — smooth interactive resize
- With a real mouse, resizing a window with the accent blur made the frame lag far behind the cursor (DWM recomputes
  the blur per size step), and the page could not re-render fast enough either. Now, from the first `WM_SIZING` to
  `WM_EXITSIZEMOVE`: the accent blur is swapped for a cheap tinted surface (`ACCENT_ENABLE_TRANSPARENTGRADIENT`,
  dark graphite `#3A3C41` / light `#EFF1F3` at 80 % by theme — user's choice: dark but not black) and restored afterwards; the WebView keeps its size
  (`WM_SIZE` held back) and is resized once when the resize ends — WebView2 could not follow a real mouse even
  throttled to 50/s; `windowState.resizing` is true, the page sets `.is-resizing` (no box-shadow / backdrop-filter /
  filter / transitions / animations) and its resize work (project filter fit, table scroll state, dashboard tooltip)
  is coalesced per frame and skipped until the resize ends. Moving the window keeps the blur.

## v0.7.1 — missed worklog days, safe state saves

Incident (2026-11-12/13): the 12 Nov worklog *was* posted at 18:00 and recorded in `state.json`, but the entry was
later lost — another Jiraffe process holding an older in-memory state saved over the file (last writer wins). The
log lines of that evening were lost the same way. Jira was fine. User decisions: restore the entry (done by hand) and
add **catch-up for missed days** (asking first).

### 1. State saves never lose other writers' data
- Every save takes an exclusive lock (`LockFileEx` on `%APPDATA%\Jiraffe\state.lock`; a no-op/flock in tests), re-reads
  `state.json`, **merges** and writes atomically:
  - `history`: union by `(date, at)`, newest first, cap 60; `notifications`: union by id, cap 200;
    `seenComments`: union, cap 5000; `skippedWorklogDays` (new): union; `lastWorklogDate`: the max;
  - other fields: this process's values win (as today).
- Log a line when the merge picked up entries this process did not have (`state: merged N foreign entries`).
- The log writer: before rotating, re-check the file under the same lock; never delete a `.log.1` it did not create in
  this rotation step (best effort; keep it simple).

### 2. Worklog for a past day
- `computeWorklog` gains a target **date D** (default today). For D < today:
  - Issues JQL with absolute bounds: `status WAS IN (<inProgressStatuses>) DURING ("D 00:00", "D 23:59") AND
    (assignee WAS currentUser() DURING ("D 00:00", "D 23:59") OR status CHANGED BY currentUser() DURING ("D 00:00", "D 23:59"))`
    (status names from settings, quoted/escaped; keep the existing validateQuery/limits). Verify against the real Jira
    the MCP is connected to (read-only search) that this JQL is accepted and returns RBI-1028 for 2026-11-12.
  - In-progress intervals from the changelog as today, clipped to D's workday (`workdayStart..workdayEnd` minus lunch,
    local time) — "now" for an interval still open at the end of D is D's `workdayEnd`.
  - Already logged = my worklogs with `started` on D; cap = `dailyCapHours × loadFactor`; worklog `started` on D.
  - Idempotent like today's run (re-running for D adds only the missing part).
  - Calendar: a manual run for a non-working D is allowed but the preview says so; automatic catch-up only checks
    working days.
- Methods: `worklogPreview { date? }`, `worklogRun { date? }` — `date` = `YYYY-MM-DD`, not in the future, at most 30
  days back (else `bad_params`). History entries keep `date` = D and get `trigger` `"manual"` / `"catchup"`.

### 3. Missed days (catch-up, asks first)
- On start (after the first successful Jira call) and once a day, look at **working days D in the last 7 days before
  today** (not before the first day Jiraffe ever ran on this machine — `firstRunDate`, new state field set once;
  migrate: earliest history date, else today) for which there is **no history entry** (any trigger except "preview")
  and D is not in `skippedWorklogDays`.
- For each such D (oldest first, max 5): compute a preview. Nothing to add (already logged by hand, by another run, or
  no in-progress time) ⇒ store a history entry for D with `trigger: "catchup"` and the "nothing to add" result, silently.
  Something to add ⇒ the day is **missed**.
- Missed days ⇒ event `worklogMissed { days: [WorklogRun preview per day] }`, a single tray balloon
  ("Worklog not recorded for 12 Nov — open Jiraffe"), and `getMissedWorklogDays` returns them.
- New method `skipWorklogDay { date }` ⇒ adds D to `skippedWorklogDays` (never asked again).
- UI:
  - an app-level notice (dismissible) on Board/Worklog: "Worklog was not recorded for Wed 12 Nov (5h 36m)" with
    **Review** (opens the Worklog screen on that day's preview), **Log now** (runs `worklogRun {date}` after a
    confirm), **Skip** (`skipWorklogDay`);
  - Worklog screen: a **day selector** (today by default, up to 30 days back, past days marked) — Preview / Log run
    for the chosen day, with a clear "writes to Jira" confirm for past days; history list shows the trigger
    ("by schedule", "manually", "catch-up").
- i18n ru/uk/en; both themes; accessible.

### Acceptance v0.7.1
1. Two Jiraffe processes saving alternately never lose each other's history/notifications (test with two App
   instances on one data dir).
2. A missed working day within the last 7 days is detected after start; the user can review, log or skip it; logging
   writes only the missing time for that day.
3. Manual preview/run for a past day works; JQL verified against the real Jira (read-only).
4. The resize changes of 0.7.1 (paused blur, frozen WebView size, dark graphite tint) stay.
5. `./build.sh --check` passes; exe ≤ 5 MB.

## Contract additions (v0.7.1)

Types (additions in **bold**; everything else as before):
```
WorklogRun {
  at, date ("YYYY-MM-DD" — the computed day D), trigger: "schedule"|"manual"|"preview"|**"catchup"**,
  items: [WorklogItem], totalSeconds, otherLoggedSeconds, capSeconds, total, skippedReason?, error?,
  **nonWorkingDay?: "weekend"|"holiday"|"vacation"**   // only for a past day computed on request
}
MissedDays { days: [WorklogRun] }   // previews (trigger "preview") of the missed days, oldest first; never null
```
- `trigger: "catchup"` appears only in the history: a past day the missed-day check found with nothing to add
  (`totalSeconds: 0`), recorded silently. UI label "catch-up".
- `nonWorkingDay`: a past weekend/holiday/vacation day previewed or run on request is computed normally (no
  `skippedReason`); show "not a working day" next to it. Today keeps the old behaviour (`skippedReason`).

Methods (changed / new):
```
worklogPreview { date?: "YYYY-MM-DD" } → WorklogRun            // trigger "preview"; nothing written
worklogRun     { date?: "YYYY-MM-DD" } → WorklogRun            // trigger "manual"; writes to Jira; recorded in history
getMissedWorklogDays {}                → MissedDays
skipWorklogDay { date: "YYYY-MM-DD" }  → MissedDays           // the remaining missed days
```
- `date` omitted, `""` or today ⇒ today exactly as before. Otherwise a real date, not in the future and at most 30 days
  back, else `bad_params` (also for a non-string). A past run logs only the missing part (my worklogs started on D
  are subtracted; cap = dailyCapHours × loadFactor covers all my worklogs of D; `started` on D) and never changes
  `nextWorklogAt`/the scheduled day.
- `skipWorklogDay`: same date rule; the day is stored in `skippedWorklogDays` (state) and never reported again.
- `getMissedWorklogDays` re-filters the last check's days: a day logged or skipped meanwhile (also by another
  process) is not returned.

Events (new):
```
worklogMissed MissedDays
```
- Sent after each missed-day check whose result differs from the previous one (the first check of a session always
  sends, also `{ "days": [] }`), and whenever a day leaves the list (`worklogRun {date}` or `skipWorklogDay`).
- The tray balloon is shown once per check that finds missed days (not while notifications are paused):
  en "Worklog not recorded for 12 Nov — open Jiraffe" (several days: "10 Nov, 12 Nov"); ru "Время не списано за
  12 нояб. — откройте Jiraffe"; uk "Час не списано за 12 лист. — відкрийте Jiraffe". Clicking it opens the window.

Missed-day check (Go):
- Runs after the first successful Jira call of the session and then once a day (scheduler tick), only while
  configured and `worklogEnabled`; after a failure (Jira unreachable) it is retried after 15 min.
- Days: working days among the 7 days before today, not before `firstRunDate`, oldest first, at most 5, without a
  *completed* history entry (trigger ≠ "preview" and no `error`; a failed scheduled run does not count as done) and
  not in `skippedWorklogDays`.
- Past-day issue query (verified against the real Jira 2026-11-13, read-only headless preview: accepted, returns
  RBI-1028 for 2026-11-12, preview "nothing to add" — 5h 36m logged = cap 8h × 0.7):
  `status WAS IN ("In Progress", "В работе", "В роботі") DURING ("2026-11-12 00:00", "2026-11-12 23:59") AND (assignee
  WAS currentUser() DURING ("2026-11-12 00:00", "2026-11-12 23:59") OR status CHANGED BY currentUser() DURING
  ("2026-11-12 00:00", "2026-11-12 23:59"))` — statuses from settings (trimmed, de-duplicated, `"`/`\` escaped).
- Headless: `jiraffe.exe --worklog-preview out.json --worklog-date YYYY-MM-DD` previews a past day (read-only).

State (`state.json`, new fields): `firstRunDate` ("YYYY-MM-DD", set once: earliest history date of an existing
install, else the first start's day), `skippedWorklogDays` (["YYYY-MM-DD"], ascending, max 100). Saves take
`state.lock` (LockFileEx) and merge as in §1; the log writer rotates under `jiraffe.log.lock`, re-checks the file
first, never deletes `jiraffe.log.1` (the rename replaces it) and keeps appending when the rotation fails.

- v0.7.1: `scope` of `getBoard` and `getSprintGroups` also accepts **`"reported"`** = issues where I am the reporter
  (`reporter = currentUser()` wherever "mine" uses its condition: tracked board `reporter = currentUser() AND
  statusCategory != Done ORDER BY updated DESC`, project board incl. done-in-14-days, sprints/backlog); `hidden` is
  reported for it like for "mine"; cached per scope. `getEpics`/`getStories` reject it (`bad_params`).

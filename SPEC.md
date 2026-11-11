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

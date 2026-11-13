//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"jiraffe/internal/app"
	"jiraffe/internal/applog"
	"jiraffe/internal/config"
	"jiraffe/internal/jira"
	"jiraffe/internal/scheduler"
	"jiraffe/internal/state"
	"jiraffe/internal/update"
	"jiraffe/internal/webui"
	"jiraffe/internal/winapi"
)

const (
	appName   = "Jiraffe"
	trayClass = "JiraffeTray"
	mutexName = `Local\Jiraffe`

	// Messages posted to the tray window (go-webview2 uses WM_APP itself and
	// the tray callback uses WM_APP+1).
	msgDispatch = winapi.WMApp + 2 // run queued closures on the UI thread
	msgShow     = winapi.WMApp + 3 // another instance asks us to show the window

	// maxPid bounds --wait-pid (Windows process ids are DWORDs, in practice
	// far below this).
	maxPid = 1 << 30

	// webviewClass is the window class go-webview2 registers.
	webviewClass = "webview"

	// Background of the webview before the page paints, as the
	// WEBVIEW2_DEFAULT_BACKGROUND_COLOR value (0xAARRGGBB): the --bg token of
	// ui/app.css (light #f7f5ef, dark #1e1f22), so opening never flashes white.
	webviewBgLight = "0xFFF7F5EF"
	webviewBgDark  = "0xFF1E1F22"
	// webviewBgClear lets the acrylic backdrop show through the page (glass).
	webviewBgClear = "0x00000000"
)

func init() {
	// The UI thread must stay on the main OS thread (go-webview2 also locks
	// it in its init).
	runtime.LockOSThread()
}

func run(args []string) int {
	fs := flag.NewFlagSet("jiraffe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	minimized := fs.Bool("minimized", false, "start in the tray without a window")
	previewOut := fs.String("worklog-preview", "", "write today's worklog preview JSON to this file and exit")
	updatedFrom := fs.String("updated-from", "", "started by the updater of this version")
	waitPid := fs.Int("wait-pid", 0, "wait up to 15 s for this process (the updated instance) to exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dataDir := filepath.Join(os.Getenv("APPDATA"), appName)
	if lw, err := applog.Open(filepath.Join(dataDir, "jiraffe.log"), 1<<20); err == nil {
		log.SetOutput(lw)
		defer lw.Close()
	} else {
		log.SetOutput(io.Discard)
	}
	log.SetFlags(log.LstdFlags)
	log.Printf("Jiraffe %s starting (minimized=%v, preview=%v, updatedFrom=%q)", app.Version, *minimized, *previewOut != "", *updatedFrom)

	if *previewOut != "" {
		return headlessPreview(dataDir, *previewOut)
	}

	// --updated-from is only trusted as a version; anything else is ignored.
	if *updatedFrom != "" {
		if v, ok := update.ParseVersion(*updatedFrom); ok {
			*updatedFrom = v.String()
		} else {
			log.Printf("ignoring --updated-from %q: not a version", *updatedFrom)
			*updatedFrom = ""
		}
	}
	// After an update the previous instance is still exiting: wait for it.
	// The single-instance mutex below decides in any case, so a PID that
	// was reused by another process never blocks the start.
	if pid := *waitPid; pid > 0 && pid <= maxPid && pid != os.Getpid() {
		switch err := winapi.WaitProcessExit(uint32(pid), 15*time.Second); {
		case errors.Is(err, winapi.ErrWaitTimeout):
			log.Printf("the previous instance (pid %d) is still running after 15 s; relying on the single-instance check", pid)
		case err != nil:
			log.Printf("cannot wait for the previous instance: %v; relying on the single-instance check", err)
		}
	} else if pid != 0 {
		log.Printf("ignoring --wait-pid %d", pid)
	}

	first, err := winapi.AcquireSingleInstance(mutexName)
	if err != nil {
		log.Printf("single instance: %v", err)
	}
	if err == nil && !first {
		log.Printf("another instance is running; asking it to show its window")
		winapi.SignalOtherInstance(trayClass, msgShow)
		return 0
	}

	cfg, err := config.Open(filepath.Join(dataDir, "config.json"), winapi.DPAPI{})
	if err != nil {
		log.Printf("config: %v", err)
	}
	st, err := state.Open(filepath.Join(dataDir, "state.json"))
	if err != nil {
		log.Printf("state: %v", err)
	}
	transport, err := winapi.NewTransport(appName + "/" + app.Version)
	if err != nil {
		log.Printf("winhttp: %v", err)
		winapi.MessageBox(appName, "WinHTTP is not available: "+err.Error(), winapi.MBIconError)
		return 1
	}
	defer transport.Close()

	// Only the app document knows the nonce; the binding rejects calls
	// without it (a dropped file or foreign page also gets __jiraffe).
	nonce, err := webui.NewNonce()
	if err != nil {
		log.Printf("nonce: %v", err)
		winapi.MessageBox(appName, err.Error(), winapi.MBIconError)
		return 1
	}
	uiFS, err := loadUI()
	if err != nil {
		log.Printf("ui: %v", err)
		winapi.MessageBox(appName, "The embedded UI is broken: "+err.Error(), winapi.MBIconError)
		return 1
	}
	scriptNonce, err := webui.NewNonce() // CSP nonce, separate from the binding nonce
	if err != nil {
		log.Printf("nonce: %v", err)
		winapi.MessageBox(appName, err.Error(), winapi.MBIconError)
		return 1
	}
	html, err := webui.Inline(uiFS, ".", nonce, scriptNonce)
	if err != nil {
		log.Printf("ui: %v", err)
		winapi.MessageBox(appName, "The embedded UI is broken: "+err.Error(), winapi.MBIconError)
		return 1
	}

	u := &ui{html: html, nonce: nonce, dataDir: dataDir, mainThread: windows.GetCurrentThreadId()}
	plat := &platform{u: u, t: transport}
	a := app.New(cfg, st, plat, scheduler.SystemClock{}, u.emit)
	u.app = a
	if updTransport := enableUpdates(a, plat); updTransport != nil {
		defer updTransport.Close()
	}

	tray, err := winapi.NewTray(trayClass, appName, winapi.TrayHandlers{
		OnOpen:         u.show,
		Menu:           u.menu,
		OnBalloonClick: u.balloonClicked,
		OnMessage:      u.onMessage,
	})
	if tray == nil {
		log.Printf("tray: %v", err)
		winapi.MessageBox(appName, "Cannot create the tray icon: "+err.Error(), winapi.MBIconError)
		return 1
	}
	if err != nil {
		log.Printf("tray icon: %v (will retry when the taskbar is created)", err)
	}
	u.tray = tray

	a.Start()
	if *updatedFrom != "" {
		a.UpdatedFrom(*updatedFrom)
	}
	u.wantShow = !*minimized
	u.loop()

	log.Printf("shutting down")
	tray.Remove()
	a.Stop(5 * time.Second)
	return 0
}

// enableUpdates gives the app its updater: a WinHTTP session of its own
// (no Jira token or headers; redirects are followed by the update package
// only to allow-listed GitHub hosts), the real file system and process
// start. It returns the transport to close at exit (nil = not enabled).
func enableUpdates(a *app.App, plat *platform) *winapi.Transport {
	exe, err := os.Executable()
	local := os.Getenv("LOCALAPPDATA")
	if err != nil || local == "" {
		log.Printf("updates disabled: exe %q (%v), LOCALAPPDATA %q", exe, err, local)
		return nil
	}
	t, err := winapi.NewTransport(appName + "/" + app.Version)
	if err != nil {
		log.Printf("updates disabled: %v", err)
		return nil
	}
	a.EnableUpdates(update.New(update.Config{
		Getter:  updateGetter{t},
		FS:      update.OSFS{},
		Starter: update.OSStarter{},
		Exe:     exe,
		Dir:     filepath.Join(local, appName, "update"),
	}), plat)
	return t
}

// updateGetter adapts the WinHTTP transport to update.Getter.
type updateGetter struct{ t *winapi.Transport }

func (g updateGetter) Get(ctx context.Context, url string, headers map[string]string, w io.Writer, limit int64) (int, string, error) {
	status, loc, err := g.t.Stream(ctx, url, headers, w, limit)
	if errors.Is(err, winapi.ErrBodyTooLarge) {
		err = update.ErrTooLarge
	}
	return status, loc, err
}

// ui owns the main-thread state: the tray, the (optional) webview window and
// the queue of closures posted from goroutines.
type ui struct {
	html       string
	nonce      string
	dataDir    string
	mainThread uint32
	app        *app.App
	tray       *winapi.Tray

	// shown mirrors "a window is open" for other goroutines (WindowOpen).
	shown atomic.Bool

	// Frameless chrome (v0.7): the window state the page sees; written on
	// the UI thread, read by getWindowInfo from any goroutine.
	stateMu  sync.Mutex
	state    app.WindowInfo
	acrylic  bool    // a glass backdrop was set on the current window
	chromeOn uintptr // the window the chrome was applied to

	// main thread only
	w          webview2.WebView
	hwnd       uintptr
	wantShow   bool
	quit       bool
	balloonURL string

	mu    sync.Mutex
	queue []func()
}

// post runs f on the UI thread; safe from any goroutine. It reports whether
// the request could be posted.
func (u *ui) post(f func()) bool {
	if u.tray == nil {
		return false
	}
	u.mu.Lock()
	u.queue = append(u.queue, f)
	u.mu.Unlock()
	return u.tray.Post(msgDispatch, 0, 0)
}

// onUIThread reports whether the caller runs on the main (UI) thread.
func (u *ui) onUIThread() bool { return windows.GetCurrentThreadId() == u.mainThread }

func (u *ui) onMessage(msg uint32, _, _ uintptr) bool {
	switch msg {
	case msgDispatch:
		u.mu.Lock()
		q := u.queue
		u.queue = nil
		u.mu.Unlock()
		for _, f := range q {
			u.safe(f)
		}
		return true
	case msgShow:
		u.show()
		return true
	}
	return false
}

func (u *ui) safe(f func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ui callback panicked: %v\n%s", r, debug.Stack())
		}
	}()
	f()
}

// loop is the main-thread loop: it alternates between the webview's own
// message loop (while a window is open) and a plain loop serving the tray.
func (u *ui) loop() {
	for !u.quit {
		if u.wantShow {
			u.wantShow = false
			if !u.openWindow() {
				return
			}
			u.w.Run() // returns when the window is closed (PostQuitMessage)
			u.w, u.hwnd = nil, 0
			u.setState(func(s *app.WindowInfo) { *s = app.WindowInfo{} }, false)
			u.acrylic, u.chromeOn = false, 0
			u.shown.Store(false)
			continue
		}
		winapi.Pump(func() bool { return u.quit || u.wantShow })
	}
}

func (u *ui) windowOpen() bool { return u.w != nil && winapi.IsWindow(u.hwnd) }

// applyChrome makes the window frameless with rounded corners, the dark
// tint and (wanted and supported) the acrylic backdrop. UI thread; runs
// again harmlessly for the same window.
func (u *ui) applyChrome(hwnd uintptr, dark, glass bool) {
	if err := winapi.MakeFrameless(hwnd, u.onWindowMessage); err != nil {
		log.Printf("frameless window: %v", err)
	}
	if u.chromeOn == hwnd {
		return
	}
	u.chromeOn = hwnd
	_ = winapi.SetRoundCorners(hwnd) // Windows 11 only
	if dark {
		if err := winapi.SetDarkTitleBar(hwnd, true); err != nil {
			log.Printf("dark title bar: %v", err)
		}
	}
	u.acrylic = false
	var backdrop winapi.Backdrop
	if glass {
		// JIRAFFE_BACKDROP is a test switch for comparing the backdrops.
		want, tint, ok := winapi.ParseBackdrop(os.Getenv("JIRAFFE_BACKDROP"))
		if !ok {
			log.Printf("JIRAFFE_BACKDROP %q ignored", os.Getenv("JIRAFFE_BACKDROP"))
		}
		got, fallback, err := winapi.EnableBackdrop(hwnd, want, tint)
		if err != nil {
			log.Printf("glass backdrop: %v (opaque surfaces)", err)
		} else {
			u.acrylic, backdrop = true, got
			if fallback != nil {
				log.Printf("glass backdrop: %v; using %s", fallback, got)
			}
		}
	}
	if !u.acrylic { // no glass: a 1 px frame extension brings back the DWM shadow
		if err := winapi.ExtendFrameForShadow(hwnd); err != nil {
			log.Printf("window shadow: %v", err)
		}
	}
	if u.acrylic {
		log.Printf("window chrome: build %d, glass %v (%s)", winapi.BuildNumber(), u.acrylic, backdrop)
	} else {
		log.Printf("window chrome: build %d, glass %v", winapi.BuildNumber(), u.acrylic)
	}
	u.setState(func(s *app.WindowInfo) {
		s.Glass = u.acrylic && winapi.TransparencyEnabled()
		s.Maximized = winapi.IsMaximized(hwnd)
		s.Active = true
	}, true)
}

// onWindowMessage follows maximize/restore, activation and the
// transparency setting for the page (UI thread).
func (u *ui) onWindowMessage(msg uint32, wParam, lParam uintptr) {
	switch msg {
	case winapi.WMSize:
		switch wParam {
		case winapi.SizeMaximized:
			u.setState(func(s *app.WindowInfo) { s.Maximized = true }, true)
		case winapi.SizeRestored:
			u.setState(func(s *app.WindowInfo) { s.Maximized = false }, true)
		}
	case winapi.WMActivate:
		active := wParam&0xFFFF != 0 // WA_ACTIVE / WA_CLICKACTIVE
		u.setState(func(s *app.WindowInfo) { s.Active = active }, true)
	case winapi.WMSettingChange:
		if winapi.SettingChangeArea(lParam) == "ImmersiveColorSet" {
			// The backdrop stays; with transparency off the page paints
			// opaque surfaces over it.
			glass := u.acrylic && winapi.TransparencyEnabled()
			u.setState(func(s *app.WindowInfo) { s.Glass = glass }, true)
		}
	}
}

// setState changes the window state and, if it changed and emit, sends
// the "windowState" event.
func (u *ui) setState(change func(*app.WindowInfo), emit bool) {
	u.stateMu.Lock()
	before := u.state
	change(&u.state)
	after := u.state
	u.stateMu.Unlock()
	if emit && after != before {
		u.emit("windowState", after)
	}
}

func (u *ui) windowInfo() app.WindowInfo {
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	return u.state
}

func (u *ui) show() {
	if u.windowOpen() {
		winapi.BringToFront(u.hwnd)
		return
	}
	u.wantShow = true
}

func (u *ui) requestQuit() {
	u.quit = true
	if u.windowOpen() {
		u.w.Destroy() // WM_CLOSE → DestroyWindow → PostQuitMessage → Run returns
	}
}

func (u *ui) openWindow() bool {
	dpi := winapi.SystemDPI()
	scale := func(v int) int { return v * dpi / 96 }
	width, height := scale(1200), scale(780)
	if waW, waH := winapi.WorkArea(); waW > 0 && waH > 0 {
		width, height = min(width, waW*95/100), min(height, waH*95/100)
	}
	// Native widgets of the page follow the app language. The browser
	// arguments are part of the WebView2 environment, and an environment with
	// other arguments cannot share a user data folder with a browser process
	// that may still be shutting down, so each language gets its own folder.
	lang := u.app.Language()
	if lang != "ru" && lang != "uk" && lang != "en" {
		lang = "en"
	}
	_ = os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "--lang="+lang)
	// The webview's background before the page paints; WebView2 reads it
	// when the controller is created.
	// Glass (Windows 11 22H2+, transparency effects on): the WebView2
	// background must be transparent from its creation, so the decision is
	// made before it, from the build number (the product name says
	// "Windows 10" on 11) and the user's setting.
	dark := winapi.AppsUseDarkTheme()
	glass := winapi.GlassCandidate(winapi.BuildNumber(), winapi.TransparencyEnabled())
	bg := webviewBgLight
	if dark {
		bg = webviewBgDark
	}
	if glass {
		bg = webviewBgClear
	}
	_ = os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", bg)
	var w webview2.WebView
	create := func() {
		w = webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:  filepath.Join(os.Getenv("LOCALAPPDATA"), appName, "WebView2", lang),
			AutoFocus: true,
			WindowOptions: webview2.WindowOptions{
				Title:  appName,
				Width:  uint(width),
				Height: uint(height),
				IconId: winapi.AppIconID,
				Center: true,
			},
		})
	}
	// go-webview2 creates and shows the window in one call and then pumps
	// messages while WebView2 starts. The frameless chrome (no title bar,
	// rounded corners, dark tint, acrylic) is applied from the first message
	// dispatched on this thread (a few milliseconds after the window
	// appears), and again once the call returns (idempotent).
	thread := uint32(windows.GetCurrentThreadId())
	chrome := func() {
		if hwnd := winapi.ThreadWindow(thread, webviewClass); hwnd != 0 {
			u.applyChrome(hwnd, dark, glass)
		}
	}
	u.post(chrome)
	create()
	if w != nil {
		chrome()
	}
	if w == nil {
		log.Printf("WebView2 runtime is not available")
		winapi.MessageBox(appName, app.T(u.app.Language(), "noWebView2"), winapi.MBIconError)
		u.quit = true
		return false
	}
	w.SetSize(min(scale(960), width), min(scale(600), height), webview2.HintMin)
	if err := w.Bind("__jiraffe", u.binding); err != nil {
		log.Printf("bind: %v", err)
	}
	// Dropping a file or link onto the window would navigate away from the
	// app (go-webview2 exposes no NavigationStarting/AllowExternalDrop hook),
	// so drops carrying files or links are refused in every document (board
	// card drags carry only text/plain and are unaffected).
	w.Init(dropGuardJS)
	w.SetHtml(u.html)
	u.w, u.hwnd = w, uintptr(w.Window())
	u.shown.Store(true)
	winapi.BringToFront(u.hwnd)
	return true
}

// validID accepts a JSON number or string as the call id.
func validID(id json.RawMessage) bool {
	if len(id) == 0 || !json.Valid(id) {
		return false
	}
	c := id[0]
	return c == '"' || c == '-' || (c >= '0' && c <= '9')
}

// dropGuardJS cancels drops that come from outside the page (files, links),
// which would otherwise navigate the webview away from the app. It is
// stateless: any dragenter/dragover/drop carrying "Files" or "text/uri-list"
// is refused. Board card drags carry only text/plain and are unaffected.
const dropGuardJS = `(function(){function ext(e){var t=e.dataTransfer&&e.dataTransfer.types;if(!t)return false;` +
	`for(var i=0;i<t.length;i++){if(t[i]==="Files"||t[i]==="text/uri-list")return true}return false}` +
	`function refuse(e){if(ext(e)){e.preventDefault();e.stopPropagation();e.dataTransfer.dropEffect="none"}}` +
	`addEventListener("dragenter",refuse,true);addEventListener("dragover",refuse,true);` +
	`addEventListener("drop",function(e){if(ext(e)){e.preventDefault();e.stopPropagation()}},true)})()`

// resolve delivers a call result to the page.
func (u *ui) resolve(id json.RawMessage, ok bool, payload []byte) {
	js := "window.__jiraffeResolve&&window.__jiraffeResolve(" + string(id) + "," + strconv.FormatBool(ok) + "," + string(payload) + ")"
	u.post(func() { u.eval(js) })
}

// binding is window.__jiraffe(id, method, paramsJSON, nonce). It runs on the
// UI thread, so it only starts a goroutine; the result is delivered through
// window.__jiraffeResolve(id, ok, payload). Calls without the document's
// nonce are rejected with {code:"forbidden"}.
func (u *ui) binding(id json.RawMessage, method string, rest ...json.RawMessage) {
	var params, nonce json.RawMessage
	if len(rest) > 0 {
		params = rest[0]
	}
	if len(rest) > 1 {
		nonce = rest[1]
	}
	if !validID(id) {
		id = json.RawMessage("null")
	}
	if !app.CheckNonce(u.nonce, nonce) {
		log.Printf("rejected call %q: missing or invalid nonce", method)
		b, _ := json.Marshal(app.BadNonceError(u.app.Language()))
		u.resolve(id, false, b)
		return
	}
	go func() {
		ok, payload := false, []byte(`{"code":"internal","message":"internal error"}`)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("call %s panicked: %v\n%s", method, r, debug.Stack())
				ok, payload = false, []byte(`{"code":"internal","message":"internal error"}`)
			}
			u.resolve(id, ok, payload)
		}()
		ctx, cancel := context.WithTimeout(u.app.Context(), 5*time.Minute)
		defer cancel()
		res, err := u.app.Call(ctx, method, params)
		var v any = res
		if err != nil {
			e := u.app.ErrorFor(err)
			if e.Code == app.CodeInternal || e.Code == jira.CodeServer {
				log.Printf("call %s: %s", method, e.Message)
			}
			v = e
		}
		b, merr := json.Marshal(v)
		if merr != nil {
			log.Printf("call %s: encode result: %v", method, merr)
			return
		}
		ok, payload = err == nil, b
	}()
}

func (u *ui) eval(js string) {
	if u.windowOpen() {
		u.w.Eval(js)
	}
}

// emit dispatches a "jiraffe" CustomEvent in the page (dropped when closed).
func (u *ui) emit(typ string, payload any) {
	b, err := json.Marshal(struct {
		Type    string `json:"type"`
		Payload any    `json:"payload"`
	}{typ, payload})
	if err != nil {
		log.Printf("emit %s: %v", typ, err)
		return
	}
	js := `window.dispatchEvent(new CustomEvent("jiraffe",{detail:` + string(b) + `}))`
	u.post(func() { u.eval(js) })
}

func (u *ui) menu() []winapi.MenuItem {
	lang := u.app.Language()
	paused := u.app.Paused()
	pause := app.T(lang, "menuPause")
	if paused {
		pause = app.T(lang, "menuResume")
	}
	return []winapi.MenuItem{
		{Text: app.T(lang, "menuOpen"), Default: true, Action: u.show},
		{Text: app.T(lang, "menuFill"), Action: u.app.ManualWorklogFromTray},
		{Text: pause, Action: func() { u.app.SetPaused(!paused) }},
		{Separator: true},
		{Text: app.T(lang, "menuQuit"), Action: u.requestQuit},
	}
}

func (u *ui) balloonClicked() {
	if u.balloonURL != "" {
		u.app.OpenNotificationURL(u.balloonURL)
		return
	}
	u.show()
}

// platform implements app.Platform on Windows.
type platform struct {
	u *ui
	t *winapi.Transport
}

func (p *platform) Transport() jira.Transport { return p.t }

// OpenURL runs ShellExecute on the UI thread (COM STA); calls from other
// goroutines are marshalled there and waited for.
func (p *platform) OpenURL(u string) error {
	if p.u.onUIThread() {
		return winapi.OpenURL(u)
	}
	res := make(chan error, 1)
	// Set when the caller gave up: the queued closure must then not open the
	// browser late, after the user already saw an error.
	var cancelled atomic.Bool
	if !p.u.post(func() {
		if cancelled.Load() {
			return
		}
		res <- winapi.OpenURL(u)
	}) {
		cancelled.Store(true)
		return errors.New("UI thread is not available")
	}
	select {
	case err := <-res:
		return err
	case <-time.After(15 * time.Second):
		cancelled.Store(true)
		return errors.New("opening the browser timed out")
	}
}

func (p *platform) SetAutostart(enabled bool) error { return winapi.SetAutostart(appName, enabled) }

// WindowInfo implements app.WindowChrome.
func (p *platform) WindowInfo() app.WindowInfo { return p.u.windowInfo() }

// WindowControl implements app.WindowChrome: the action runs on the UI
// thread; drag/resize only while the primary mouse button is still down.
func (p *platform) WindowControl(action, edge string) {
	p.u.post(func() {
		if !p.u.windowOpen() {
			return
		}
		h := p.u.hwnd
		switch action {
		case "minimize":
			winapi.Minimize(h)
		case "toggleMaximize":
			winapi.ToggleMaximize(h)
		case "close": // as the close button: the window goes, the tray stays
			winapi.PostClose(h)
		case "drag":
			winapi.StartNonClientDrag(h, winapi.HTCaption)
		case "resize":
			if ht, ok := winapi.EdgeHitTest(edge); ok {
				winapi.StartNonClientDrag(h, ht)
			}
		}
	})
}

// WindowOpen implements app.UpdateHost.
func (p *platform) WindowOpen() bool { return p.u.shown.Load() }

// Quit implements app.UpdateHost: the main loop ends, then run() removes
// the tray icon and stops the app (scheduler, background work); the state
// is saved on every change.
func (p *platform) Quit() {
	if !p.u.post(p.u.requestQuit) {
		log.Printf("quit: UI thread is not available")
	}
}

// AutostartEnabled implements app.AutostartReader.
func (p *platform) AutostartEnabled() (bool, error) { return winapi.AutostartEnabled(appName) }

func (p *platform) Notify(title, text, url string) {
	p.u.post(func() {
		// Set on every balloon (also to ""): Shell_NotifyIcon does not say
		// which balloon was clicked, so a click always refers to the latest;
		// without a URL it opens the main window.
		p.u.balloonURL = url
		p.u.tray.ShowBalloon(title, text)
	})
}

// SetWindowTheme recolours the title bar of the open window on the UI
// thread (the window handle is owned there); errors are logged, never
// returned. Without a window nothing happens.
func (p *platform) SetWindowTheme(t app.WindowTheme) {
	p.u.post(func() {
		if !p.u.windowOpen() {
			return
		}
		hwnd := p.u.hwnd
		if err := winapi.SetDarkTitleBar(hwnd, t.Dark); err != nil {
			log.Printf("dark title bar: %v", err)
		}
		caption := winapi.CaptionColorDefault
		if c := t.Caption; c != nil {
			caption = winapi.ColorRef(c.R, c.G, c.B)
		}
		_ = winapi.SetCaptionColor(hwnd, caption) // Windows 11 only
		winapi.RedrawFrame(hwnd)
	})
}

func (p *platform) SettingsChanged(s config.Settings, paused bool) {
	tip := app.T(s.Language, "tooltip")
	if paused {
		tip = app.T(s.Language, "tooltipPaused")
	}
	p.u.post(func() { p.u.tray.SetTooltip(tip) })
}

// headlessPlatform is used by --worklog-preview: no UI, nothing written.
type headlessPlatform struct{ t *winapi.Transport }

func (h headlessPlatform) Transport() jira.Transport           { return h.t }
func (headlessPlatform) OpenURL(string) error                  { return nil }
func (headlessPlatform) SetAutostart(bool) error               { return nil }
func (headlessPlatform) Notify(string, string, string)         {}
func (headlessPlatform) SettingsChanged(config.Settings, bool) {}
func (headlessPlatform) SetWindowTheme(app.WindowTheme)        {}

// headlessPreview computes today's worklog preview and writes it as JSON to
// out. JIRAFFE_URL / JIRAFFE_TOKEN override the saved configuration.
func headlessPreview(dataDir, out string) (code int) {
	code = 1
	var result any
	defer func() {
		if r := recover(); r != nil {
			log.Printf("preview panicked: %v\n%s", r, debug.Stack())
			result, code = map[string]string{"error": "internal error"}, 1
			_ = writeJSON(out, result)
		}
	}()

	cfg, err := config.Open(filepath.Join(dataDir, "config.json"), winapi.DPAPI{})
	if err != nil {
		log.Printf("config: %v", err)
	}
	if envURL := os.Getenv("JIRAFFE_URL"); envURL != "" && !config.ValidateURL(config.NormalizeURL(envURL)) {
		result = map[string]string{"code": app.CodeBadParams, "error": "JIRAFFE_URL is not a valid http(s) URL"}
		if err := writeJSON(out, result); err != nil {
			log.Printf("preview: %v", err)
		}
		return 1
	}
	cfg.Override(os.Getenv("JIRAFFE_URL"), os.Getenv("JIRAFFE_TOKEN"))
	// The preview only reads state; nothing is written.
	st, err := state.Open(filepath.Join(dataDir, "state.json"))
	if err != nil {
		log.Printf("state: %v", err)
	}

	transport, err := winapi.NewTransport(appName + "/" + app.Version)
	if err != nil {
		result = map[string]string{"code": app.CodeInternal, "error": err.Error()}
		_ = writeJSON(out, result)
		return 1
	}
	defer transport.Close()

	a := app.New(cfg, st, headlessPlatform{t: transport}, scheduler.SystemClock{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	run, err := a.ComputeWorklog(ctx)
	result = run
	if err == nil && run.SkippedReason != "not-configured" && run.Error == "" {
		code = 0
	}
	if err != nil {
		log.Printf("preview: %v", err)
	}
	if werr := writeJSON(out, result); werr != nil {
		log.Printf("preview: %v", werr)
		return 1
	}
	return code
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

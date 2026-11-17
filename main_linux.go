//go:build linux && cgo

package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/systray"

	"jiraffe/internal/app"
	"jiraffe/internal/applog"
	"jiraffe/internal/config"
	"jiraffe/internal/gtkwin"
	"jiraffe/internal/httpx"
	"jiraffe/internal/jira"
	"jiraffe/internal/linuxdesk"
	"jiraffe/internal/scheduler"
	"jiraffe/internal/state"
	"jiraffe/internal/update"
	"jiraffe/internal/webui"
)

const (
	appName = "Jiraffe"
	maxPid  = 1 << 22 // pid_max is at most 2^22 on Linux

	// Background before the page paints: the --bg token of ui/app.css.
	bgLight = "#f7f5ef"
	bgDark  = "#1e1f22"
)

//go:embed assets/icon.png
var iconPNG []byte

// bindingJS gives the page window.__jiraffe(...) like the Windows binding:
// the arguments travel as one JSON array to the "jiraffe" message handler.
const bindingJS = `(function(){var h=window.webkit&&window.webkit.messageHandlers&&window.webkit.messageHandlers.jiraffe;` +
	`if(!h)return;window.__jiraffe=function(){h.postMessage(JSON.stringify(Array.prototype.slice.call(arguments)))}})();`

func init() {
	// GTK must stay on the main OS thread.
	runtime.LockOSThread()
}

func run(args []string) int {
	fs := flag.NewFlagSet("jiraffe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	minimized := fs.Bool("minimized", false, "start in the tray without a window")
	previewOut := fs.String("worklog-preview", "", "write today's worklog preview JSON to this file and exit")
	previewDate := fs.String("worklog-date", "", "with --worklog-preview: preview this past day (YYYY-MM-DD) instead of today")
	updatedFrom := fs.String("updated-from", "", "started by the updater of this version")
	waitPid := fs.Int("wait-pid", 0, "wait up to 15 s for this process (the updated instance) to exit")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "usage: jiraffe [--minimized] [--worklog-preview FILE [--worklog-date YYYY-MM-DD]]")
		return 2
	}

	update.UseLinuxAssets()
	dataDir := linuxdesk.ConfigDir()
	_ = os.MkdirAll(dataDir, 0o700)
	if lw, err := applog.Open(filepath.Join(dataDir, "jiraffe.log"), 1<<20); err == nil {
		log.SetOutput(lw)
		defer lw.Close()
	} else {
		log.SetOutput(io.Discard)
	}
	log.SetFlags(log.LstdFlags)
	log.Printf("Jiraffe %s (linux) starting (minimized=%v, preview=%v, updatedFrom=%q)", app.Version, *minimized, *previewOut != "", *updatedFrom)

	keyring := linuxdesk.NewKeyring()
	if *previewOut != "" {
		return headlessPreview(dataDir, *previewOut, *previewDate, keyring, func() (jira.Transport, func(), error) {
			t := httpx.New(appName + "/" + app.Version)
			return t, t.Close, nil
		})
	}

	if *updatedFrom != "" {
		if v, ok := update.ParseVersion(*updatedFrom); ok {
			*updatedFrom = v.String()
		} else {
			log.Printf("ignoring --updated-from %q: not a version", *updatedFrom)
			*updatedFrom = ""
		}
	}
	if pid := *waitPid; pid > 0 && pid <= maxPid && pid != os.Getpid() {
		switch err := linuxdesk.WaitProcessExit(pid, 15*time.Second); {
		case errors.Is(err, linuxdesk.ErrWaitTimeout):
			log.Printf("the previous instance (pid %d) is still running after 15 s; relying on the single-instance check", pid)
		case err != nil:
			log.Printf("cannot wait for the previous instance: %v; relying on the single-instance check", err)
		}
	} else if pid != 0 {
		log.Printf("ignoring --wait-pid %d", pid)
	}

	inst, first, err := linuxdesk.AcquireSingleInstance()
	if err != nil {
		log.Printf("single instance: %v", err)
	}
	if err == nil && !first {
		log.Printf("another instance is running; asked it to show its window")
		return 0
	}

	if !gtkwin.Init() {
		log.Printf("cannot open the display")
		fmt.Fprintln(os.Stderr, "jiraffe: cannot open the display (is a graphical session running?)")
		return 1
	}
	gtkwin.SetDefaultIcon(iconPNG)
	if exe, err := exePath(); err == nil {
		if err := linuxdesk.EnsureDesktopIntegration(exe, iconPNG); err != nil {
			log.Printf("menu entry: %v", err)
		}
	}
	fatal := func(msg string) int {
		log.Print(msg)
		gtkwin.MessageBox(appName, msg)
		return 1
	}

	cfg, err := config.Open(filepath.Join(dataDir, "config.json"), keyring)
	if err != nil {
		log.Printf("config: %v", err)
	}
	st, err := state.Open(filepath.Join(dataDir, "state.json"))
	if err != nil {
		log.Printf("state: %v", err)
	}
	transport := httpx.New(appName + "/" + app.Version)
	defer transport.Close()

	nonce, err := webui.NewNonce()
	if err != nil {
		return fatal(err.Error())
	}
	uiFS, err := loadUI()
	if err != nil {
		return fatal("The embedded UI is broken: " + err.Error())
	}
	scriptNonce, err := webui.NewNonce()
	if err != nil {
		return fatal(err.Error())
	}
	html, err := webui.Inline(uiFS, ".", nonce, scriptNonce)
	if err != nil {
		return fatal("The embedded UI is broken: " + err.Error())
	}

	u := &ui{html: html}
	debugUI = u
	u.bridge = bridge{nonce: nonce, post: u.post, eval: u.eval}
	plat := &platform{u: u, t: transport}
	a := app.New(cfg, st, plat, scheduler.SystemClock{}, u.emit)
	u.app = a
	enableUpdates(a, plat)

	// The page follows the desktop's dark style through GTK.
	u.dark.Store(linuxdesk.WatchDarkStyle(func(dark bool) {
		u.dark.Store(dark)
		u.post(func() { gtkwin.SetPreferDark(dark) })
	}))
	gtkwin.SetPreferDark(u.dark.Load())

	if n, err := linuxdesk.NewNotifier(func(url string) {
		u.post(func() {
			if url != "" {
				a.OpenNotificationURL(url)
				return
			}
			u.show()
		})
	}); err != nil {
		log.Printf("notifications: %v", err)
	} else {
		plat.notifier = n
	}

	startTray, endTray := systray.RunWithExternalLoop(func() { u.trayReady(a) }, nil)
	startTray()
	inst.Serve(func() { u.post(u.show) })

	a.Start()
	if *updatedFrom != "" {
		a.UpdatedFrom(*updatedFrom)
	}
	if !*minimized {
		u.show()
	}
	gtkwin.Main()

	log.Printf("shutting down")
	endTray()
	a.Stop(5 * time.Second)
	return 0
}

// enableUpdates gives the app its updater: an HTTP transport of its own (no
// Jira token or headers), the real file system and process start. The
// binary updates itself in place, so it must live in a folder the user can
// write (~/.local/bin, see scripts/install.sh).
func enableUpdates(a *app.App, plat *platform) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		log.Printf("updates disabled: exe %q (%v)", exe, err)
		return
	}
	t := httpx.New(appName + "/" + app.Version)
	a.EnableUpdates(update.New(update.Config{
		Getter:  updateGetter{t},
		FS:      update.OSFS{},
		Starter: update.OSStarter{},
		Exe:     exe,
		Dir:     filepath.Join(linuxdesk.CacheDir(), "update"),
	}), plat)
}

// updateGetter adapts the HTTP transport to update.Getter.
type updateGetter struct{ t *httpx.Transport }

func (g updateGetter) Get(ctx context.Context, url string, headers map[string]string, w io.Writer, limit int64) (int, string, error) {
	status, loc, err := g.t.Stream(ctx, url, headers, w, limit)
	if errors.Is(err, httpx.ErrBodyTooLarge) {
		err = update.ErrTooLarge
	}
	return status, loc, err
}

// debugUI is the UI for the snapshot hook of debug builds (main_linux_debug.go).
var debugUI *ui

// ui owns the GTK-thread state: the (optional) window and the tray menu.
type ui struct {
	bridge // nonce, app, binding, resolve, emit

	html string

	shown atomic.Bool // a window is open (read from any goroutine)
	dark  atomic.Bool // the desktop prefers dark

	stateMu sync.Mutex
	state   app.WindowInfo

	w    *gtkwin.Window // GTK thread only
	quit bool

	menuMu                      sync.Mutex
	mOpen, mFill, mPause, mQuit *systray.MenuItem
}

// post runs f on the GTK thread; safe from any goroutine.
func (u *ui) post(f func()) bool {
	gtkwin.Post(f)
	return true
}

func (u *ui) eval(js string) {
	if u.w != nil {
		u.w.Eval(js)
	}
}

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

// show raises the window, opening it when closed (GTK thread).
func (u *ui) show() {
	if u.quit {
		return
	}
	if u.w != nil {
		u.w.Present()
		return
	}
	u.openWindow()
}

func (u *ui) openWindow() {
	width, height := 1200, 780
	if waW, waH := gtkwin.WorkArea(); waW > 0 && waH > 0 {
		width, height = min(width, waW*95/100), min(height, waH*95/100)
	}
	bg := bgLight
	if u.dark.Load() {
		bg = bgDark
	}
	u.w = gtkwin.Open(gtkwin.Options{
		Title:      appName,
		Width:      width,
		Height:     height,
		MinWidth:   min(960, width),
		MinHeight:  min(600, height),
		DataDir:    filepath.Join(linuxdesk.DataDir(), "webkit"),
		CacheDir:   filepath.Join(linuxdesk.CacheDir(), "webkit"),
		DevTools:   os.Getenv("JIRAFFE_DEVTOOLS") == "1",
		InitJS:     bindingJS + dropGuardJS,
		Background: bg,
		OnMessage:  u.onMessage,
		OnState: func(maximized, focused bool) {
			u.setState(func(s *app.WindowInfo) { s.Maximized, s.Active = maximized, focused }, true)
		},
		OnClosed: func() {
			u.w = nil
			u.shown.Store(false)
			u.setState(func(s *app.WindowInfo) { *s = app.WindowInfo{} }, false)
		},
	})
	u.setState(func(s *app.WindowInfo) { *s = app.WindowInfo{Active: true} }, false)
	u.w.LoadHTML(u.html)
	u.shown.Store(true)
	u.w.Present()
}

// onMessage receives window.__jiraffe(id, method, paramsJSON, nonce) as a
// JSON array.
func (u *ui) onMessage(msg string) {
	var args []json.RawMessage
	if err := json.Unmarshal([]byte(msg), &args); err != nil || len(args) < 2 {
		log.Printf("ignored a malformed call from the page")
		return
	}
	var method string
	if err := json.Unmarshal(args[1], &method); err != nil {
		log.Printf("ignored a call without a method name")
		return
	}
	u.binding(args[0], method, args[2:]...)
}

func (u *ui) requestQuit() {
	u.quit = true
	if u.w != nil {
		u.w.Close()
	}
	gtkwin.Quit()
}

// trayReady builds the tray icon and menu (systray goroutine).
func (u *ui) trayReady(a *app.App) {
	systray.SetIcon(iconPNG)
	systray.SetTitle(appName)
	systray.SetOnTapped(func() { u.post(u.show) })
	u.menuMu.Lock()
	u.mOpen = systray.AddMenuItem("", "")
	u.mFill = systray.AddMenuItem("", "")
	u.mPause = systray.AddMenuItem("", "")
	systray.AddSeparator()
	u.mQuit = systray.AddMenuItem("", "")
	u.menuMu.Unlock()
	u.updateTray(a.Language(), a.Paused())
	on := func(item *systray.MenuItem, f func()) {
		go func() {
			for range item.ClickedCh {
				u.post(f)
			}
		}()
	}
	on(u.mOpen, u.show)
	on(u.mFill, func() { go a.ManualWorklogFromTray() })
	on(u.mPause, func() { a.SetPaused(!a.Paused()) })
	on(u.mQuit, u.requestQuit)
}

// updateTray localises the menu and the tooltip.
func (u *ui) updateTray(lang string, paused bool) {
	u.menuMu.Lock()
	defer u.menuMu.Unlock()
	if u.mOpen == nil {
		return
	}
	pause, tip := app.T(lang, "menuPause"), app.T(lang, "tooltip")
	if paused {
		pause, tip = app.T(lang, "menuResume"), app.T(lang, "tooltipPaused")
	}
	u.mOpen.SetTitle(app.T(lang, "menuOpen"))
	u.mFill.SetTitle(app.T(lang, "menuFill"))
	u.mPause.SetTitle(pause)
	u.mQuit.SetTitle(app.T(lang, "menuQuit"))
	systray.SetTooltip(tip)
}

// platform implements app.Platform on Linux.
type platform struct {
	u        *ui
	t        *httpx.Transport
	notifier *linuxdesk.Notifier
}

func (p *platform) Transport() jira.Transport { return p.t }

func (p *platform) OpenURL(u string) error { return linuxdesk.OpenURL(u) }

func exePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func (p *platform) SetAutostart(enabled bool) error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	return linuxdesk.SetAutostart(exe, enabled)
}

// AutostartEnabled implements app.AutostartReader.
func (p *platform) AutostartEnabled() (bool, error) {
	exe, err := exePath()
	if err != nil {
		return false, err
	}
	return linuxdesk.AutostartEnabled(exe)
}

// WindowInfo implements app.WindowChrome (no glass on Linux).
func (p *platform) WindowInfo() app.WindowInfo { return p.u.windowInfo() }

// WindowControl implements app.WindowChrome on the GTK thread.
func (p *platform) WindowControl(action, edge string) {
	p.u.post(func() {
		w := p.u.w
		if w == nil {
			return
		}
		switch action {
		case "minimize":
			w.Iconify()
		case "toggleMaximize":
			w.ToggleMaximize()
		case "close": // the window goes, the tray stays
			w.Close()
		case "drag":
			w.BeginMove()
		case "resize":
			w.BeginResize(edge)
		}
	})
}

// WindowOpen implements app.UpdateHost.
func (p *platform) WindowOpen() bool { return p.u.shown.Load() }

// Quit implements app.UpdateHost.
func (p *platform) Quit() { p.u.post(p.u.requestQuit) }

func (p *platform) Notify(title, text, url string) {
	if p.notifier == nil {
		log.Printf("notification not shown (no notification service): %s", title)
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("notification panicked: %v\n%s", r, debug.Stack())
			}
		}()
		p.notifier.Show(title, text, url)
	}()
}

// SetWindowTheme: the page draws its own title bar on Linux, nothing to
// recolour.
func (p *platform) SetWindowTheme(app.WindowTheme) {}

func (p *platform) SettingsChanged(s config.Settings, paused bool) {
	p.u.updateTray(s.Language, paused)
}

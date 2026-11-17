//go:build linux && cgo

// Package gtkwin is the app window of the Linux build: a frameless GTK 3
// window holding a WebKitGTK (4.1) web view. The page draws the window
// chrome (title bar, buttons, resize edges, SPEC v0.7) and asks for moves
// and resizes, as on Windows. Everything here runs on the GTK (main) thread;
// Post queues work onto it from any goroutine.
package gtkwin

/*
#cgo pkg-config: gtk+-3.0 webkit2gtk-4.1
#include <stdlib.h>
#include <stdint.h>
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>
#include <signal.h>

extern void goIdle(uintptr_t h);
extern void goMessage(char *json);
extern void goState(int maximized, int focused);
extern void goDestroyed(void);

static gboolean idle_cb(gpointer data) { goIdle((uintptr_t)data); return G_SOURCE_REMOVE; }
static void post_idle(uintptr_t h) { g_idle_add_full(G_PRIORITY_DEFAULT, idle_cb, (gpointer)h, NULL); }

static int init_gtk(void) {
	g_set_prgname("jiraffe"); // the Wayland app id / X11 class, matches jiraffe.desktop
	g_set_application_name("Jiraffe");
	return gtk_init_check(NULL, NULL);
}

typedef struct {
	GtkWidget *win;
	WebKitWebView *view;
	// the last primary button press in the page: moves and resizes start from it
	guint button;
	gdouble x, y;
	guint32 time;
} jwin;

static WebKitWebContext *context; // one for the app's lifetime

// WebKit (JavaScriptCore) installs signal handlers without SA_ONSTACK; the Go
// runtime aborts when such a handler runs on a Go thread. Add the flag.
static void fix_signals(void) {
	int sigs[] = { SIGCHLD, SIGHUP, SIGINT, SIGQUIT, SIGABRT, SIGFPE, SIGTERM, SIGBUS, SIGSEGV, SIGXCPU, SIGXFSZ, SIGUSR1, SIGUSR2, SIGPIPE };
	for (size_t i = 0; i < sizeof sigs / sizeof sigs[0]; i++) {
		struct sigaction sa;
		if (sigaction(sigs[i], NULL, &sa) == 0 && sa.sa_handler != SIG_DFL && sa.sa_handler != SIG_IGN && !(sa.sa_flags & SA_ONSTACK)) {
			sa.sa_flags |= SA_ONSTACK;
			sigaction(sigs[i], &sa, NULL);
		}
	}
}

static void on_message(WebKitUserContentManager *m, WebKitJavascriptResult *r, gpointer d) {
	JSCValue *v = webkit_javascript_result_get_js_value(r);
	if (!jsc_value_is_string(v)) return;
	char *s = jsc_value_to_string(v);
	goMessage(s);
	g_free(s);
}

static gboolean on_state(GtkWidget *w, GdkEventWindowState *e, gpointer d) {
	GdkWindowState s = e->new_window_state;
	goState((s & (GDK_WINDOW_STATE_MAXIMIZED | GDK_WINDOW_STATE_FULLSCREEN)) != 0, (s & GDK_WINDOW_STATE_FOCUSED) != 0);
	return FALSE;
}

static void on_destroy(GtkWidget *w, gpointer d) {
	g_free(d);
	goDestroyed();
}

static gboolean on_press(GtkWidget *w, GdkEventButton *e, gpointer d) {
	jwin *j = d;
	if (e->type == GDK_BUTTON_PRESS && e->button == GDK_BUTTON_PRIMARY) {
		j->button = e->button;
		j->x = e->x_root;
		j->y = e->y_root;
		j->time = e->time;
	}
	return FALSE; // the page gets the press as usual
}

// Only the app document (loaded as HTML, about:blank) may be shown; links
// are opened by the app in the browser, so any other navigation is refused.
static gboolean on_policy(WebKitWebView *v, WebKitPolicyDecision *d, WebKitPolicyDecisionType t, gpointer u) {
	if (t == WEBKIT_POLICY_DECISION_TYPE_NAVIGATION_ACTION) {
		WebKitNavigationAction *a = webkit_navigation_policy_decision_get_navigation_action(WEBKIT_NAVIGATION_POLICY_DECISION(d));
		const char *uri = webkit_uri_request_get_uri(webkit_navigation_action_get_request(a));
		if (uri && (g_str_has_prefix(uri, "about:") || g_str_has_prefix(uri, "data:"))) {
			webkit_policy_decision_use(d);
		} else {
			webkit_policy_decision_ignore(d);
		}
		return TRUE;
	}
	if (t == WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION) {
		webkit_policy_decision_ignore(d);
		return TRUE;
	}
	return FALSE;
}

// The browser context menu (Back, Reload…) only where it helps: editable
// fields and selected text (copy / paste).
static gboolean on_context_menu(WebKitWebView *v, WebKitContextMenu *m, GdkEvent *e, WebKitHitTestResult *h, gpointer dev) {
	if (dev) return FALSE;
	if (webkit_hit_test_result_context_is_editable(h) || webkit_hit_test_result_context_is_selection(h)) return FALSE;
	return TRUE;
}

static jwin *jw_new(const char *title, int w, int h, int minw, int minh,
		const char *data_dir, const char *cache_dir, int dev, const char *init_js, const char *bg) {
	if (!context) {
		WebKitWebsiteDataManager *dm = webkit_website_data_manager_new(
			"base-data-directory", data_dir, "base-cache-directory", cache_dir, NULL);
		context = webkit_web_context_new_with_website_data_manager(dm);
		g_object_unref(dm);
	}
	WebKitUserContentManager *ucm = webkit_user_content_manager_new();
	WebKitWebView *view = WEBKIT_WEB_VIEW(g_object_new(WEBKIT_TYPE_WEB_VIEW,
		"web-context", context, "user-content-manager", ucm, NULL));
	g_object_unref(ucm);

	WebKitSettings *s = webkit_web_view_get_settings(view);
	webkit_settings_set_enable_developer_extras(s, dev);
	webkit_settings_set_javascript_can_access_clipboard(s, TRUE);
	webkit_settings_set_enable_back_forward_navigation_gestures(s, FALSE);

	webkit_user_content_manager_register_script_message_handler(ucm, "jiraffe");
	g_signal_connect(ucm, "script-message-received::jiraffe", G_CALLBACK(on_message), NULL);
	WebKitUserScript *us = webkit_user_script_new(init_js, WEBKIT_USER_CONTENT_INJECT_TOP_FRAME,
		WEBKIT_USER_SCRIPT_INJECT_AT_DOCUMENT_START, NULL, NULL);
	webkit_user_content_manager_add_script(ucm, us);
	webkit_user_script_unref(us);

	GdkRGBA c;
	if (gdk_rgba_parse(&c, bg)) webkit_web_view_set_background_color(view, &c);

	jwin *j = g_new0(jwin, 1);
	j->view = view;
	j->win = gtk_window_new(GTK_WINDOW_TOPLEVEL);
	GtkWindow *win = GTK_WINDOW(j->win);
	gtk_window_set_title(win, title);
	gtk_window_set_decorated(win, FALSE);
	gtk_window_set_default_size(win, w, h);
	GdkGeometry geo = { .min_width = minw, .min_height = minh };
	gtk_window_set_geometry_hints(win, NULL, &geo, GDK_HINT_MIN_SIZE);
	gtk_window_set_position(win, GTK_WIN_POS_CENTER);
	gtk_container_add(GTK_CONTAINER(win), GTK_WIDGET(view));

	g_signal_connect(win, "window-state-event", G_CALLBACK(on_state), NULL);
	g_signal_connect(win, "destroy", G_CALLBACK(on_destroy), j);
	g_signal_connect(view, "button-press-event", G_CALLBACK(on_press), j);
	g_signal_connect(view, "decide-policy", G_CALLBACK(on_policy), NULL);
	g_signal_connect(view, "context-menu", G_CALLBACK(on_context_menu), GINT_TO_POINTER(dev));

	gtk_widget_show_all(j->win);
	gtk_widget_grab_focus(GTK_WIDGET(view));
	fix_signals();
	return j;
}

static void jw_load_html(jwin *j, const char *html) { webkit_web_view_load_html(j->view, html, NULL); }
static void jw_eval(jwin *j, const char *js) {
	webkit_web_view_evaluate_javascript(j->view, js, -1, NULL, NULL, NULL, NULL, NULL);
}
static void jw_present(jwin *j) { gtk_window_present(GTK_WINDOW(j->win)); }
static void jw_iconify(jwin *j) { gtk_window_iconify(GTK_WINDOW(j->win)); }
static void jw_close(jwin *j) { gtk_widget_destroy(j->win); }
static int jw_maximized(jwin *j) { return gtk_window_is_maximized(GTK_WINDOW(j->win)); }
static void jw_toggle_max(jwin *j) {
	if (gtk_window_is_maximized(GTK_WINDOW(j->win))) gtk_window_unmaximize(GTK_WINDOW(j->win));
	else gtk_window_maximize(GTK_WINDOW(j->win));
}
static void jw_move(jwin *j) {
	if (!j->button) return;
	gtk_window_begin_move_drag(GTK_WINDOW(j->win), j->button, (gint)j->x, (gint)j->y, j->time);
}
static void jw_resize(jwin *j, int edge) {
	if (!j->button) return;
	gtk_window_begin_resize_drag(GTK_WINDOW(j->win), (GdkWindowEdge)edge, j->button, (gint)j->x, (gint)j->y, j->time);
}

static void work_area(int *w, int *h) {
	*w = *h = 0;
	GdkDisplay *d = gdk_display_get_default();
	if (!d) return;
	GdkMonitor *m = gdk_display_get_primary_monitor(d);
	if (!m && gdk_display_get_n_monitors(d) > 0) m = gdk_display_get_monitor(d, 0);
	if (!m) return;
	GdkRectangle r;
	gdk_monitor_get_workarea(m, &r);
	*w = r.width;
	*h = r.height;
}

static void set_default_icon(const guchar *data, gsize len) {
	GdkPixbufLoader *l = gdk_pixbuf_loader_new();
	gboolean ok = gdk_pixbuf_loader_write(l, data, len, NULL);
	ok = gdk_pixbuf_loader_close(l, NULL) && ok;
	if (ok) {
		GdkPixbuf *p = gdk_pixbuf_loader_get_pixbuf(l);
		if (p) gtk_window_set_default_icon(p);
	}
	g_object_unref(l);
}

static void set_prefer_dark(int dark) {
	GtkSettings *s = gtk_settings_get_default();
	if (s) g_object_set(s, "gtk-application-prefer-dark-theme", dark ? TRUE : FALSE, NULL);
}

static void message_box(const char *title, const char *text) {
	GtkWidget *d = gtk_message_dialog_new(NULL, 0, GTK_MESSAGE_ERROR, GTK_BUTTONS_OK, "%s", text);
	gtk_window_set_title(GTK_WINDOW(d), title);
	gtk_dialog_run(GTK_DIALOG(d));
	gtk_widget_destroy(d);
}
*/
import "C"

import (
	"log"
	"runtime/debug"
	"sync"
	"unsafe"
)

// Init initialises GTK; false when there is no display.
func Init() bool { return C.init_gtk() != 0 }

// Main runs the GTK main loop until Quit.
func Main() { C.gtk_main() }

// Quit ends Main (GTK thread).
func Quit() { C.gtk_main_quit() }

var (
	postMu   sync.Mutex
	postNext uintptr
	postFns  = map[uintptr]func(){}
)

// Post runs f on the GTK thread; safe from any goroutine.
func Post(f func()) {
	postMu.Lock()
	postNext++
	h := postNext
	postFns[h] = f
	postMu.Unlock()
	C.post_idle(C.uintptr_t(h))
}

//export goIdle
func goIdle(h C.uintptr_t) {
	postMu.Lock()
	f := postFns[uintptr(h)]
	delete(postFns, uintptr(h))
	postMu.Unlock()
	if f != nil {
		safe(f)
	}
}

func safe(f func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("gtk callback panicked: %v\n%s", r, debug.Stack())
		}
	}()
	f()
}

// Options describe the window.
type Options struct {
	Title               string
	Width, Height       int // logical pixels
	MinWidth, MinHeight int
	DataDir, CacheDir   string // web storage (first window only)
	DevTools            bool
	InitJS              string // runs at the start of every document
	Background          string // "#rrggbb" before the page paints
	OnMessage           func(msg string)
	OnState             func(maximized, focused bool)
	OnClosed            func()
}

// Window is the open app window. Only one exists at a time.
type Window struct {
	j    *C.jwin
	opts Options
}

var current *Window

// Open creates and shows the window. Pages post messages with
// window.webkit.messageHandlers.jiraffe.postMessage(string).
func Open(o Options) *Window {
	cs := func(s string) *C.char { return C.CString(s) }
	title, data, cache, js, bg := cs(o.Title), cs(o.DataDir), cs(o.CacheDir), cs(o.InitJS), cs(o.Background)
	defer func() {
		for _, p := range []*C.char{title, data, cache, js, bg} {
			C.free(unsafe.Pointer(p))
		}
	}()
	dev := C.int(0)
	if o.DevTools {
		dev = 1
	}
	w := &Window{opts: o}
	current = w
	w.j = C.jw_new(title, C.int(o.Width), C.int(o.Height), C.int(o.MinWidth), C.int(o.MinHeight), data, cache, dev, js, bg)
	return w
}

//export goMessage
func goMessage(s *C.char) {
	if w := current; w != nil && w.opts.OnMessage != nil {
		msg := C.GoString(s)
		safe(func() { w.opts.OnMessage(msg) })
	}
}

//export goState
func goState(maximized, focused C.int) {
	if w := current; w != nil && w.opts.OnState != nil {
		safe(func() { w.opts.OnState(maximized != 0, focused != 0) })
	}
}

//export goDestroyed
func goDestroyed() {
	w := current
	current = nil
	if w != nil {
		w.j = nil
		if w.opts.OnClosed != nil {
			safe(w.opts.OnClosed)
		}
	}
}

func (w *Window) alive() bool { return w != nil && w.j != nil }

// LoadHTML shows the document (origin about:blank).
func (w *Window) LoadHTML(html string) {
	if w.alive() {
		s := C.CString(html)
		defer C.free(unsafe.Pointer(s))
		C.jw_load_html(w.j, s)
	}
}

// Eval runs JS in the page.
func (w *Window) Eval(js string) {
	if w.alive() {
		s := C.CString(js)
		defer C.free(unsafe.Pointer(s))
		C.jw_eval(w.j, s)
	}
}

// Present raises and focuses the window.
func (w *Window) Present() {
	if w.alive() {
		C.jw_present(w.j)
	}
}

// Iconify minimises the window.
func (w *Window) Iconify() {
	if w.alive() {
		C.jw_iconify(w.j)
	}
}

// ToggleMaximize maximises or restores the window.
func (w *Window) ToggleMaximize() {
	if w.alive() {
		C.jw_toggle_max(w.j)
	}
}

// Maximized reports whether the window is maximised.
func (w *Window) Maximized() bool { return w.alive() && C.jw_maximized(w.j) != 0 }

// Close destroys the window (OnClosed follows).
func (w *Window) Close() {
	if w.alive() {
		C.jw_close(w.j)
	}
}

// BeginMove lets the window manager move the window with the pointer from
// the last primary press in the page (the button is still down).
func (w *Window) BeginMove() {
	if w.alive() {
		C.jw_move(w.j)
	}
}

// GdkWindowEdge values.
var edges = map[string]C.int{"nw": 0, "n": 1, "ne": 2, "w": 3, "e": 4, "sw": 5, "s": 6, "se": 7}

// BeginResize starts an interactive resize from edge ("n", "se", …).
func (w *Window) BeginResize(edge string) {
	if e, ok := edges[edge]; ok && w.alive() {
		C.jw_resize(w.j, e)
	}
}

// WorkArea is the primary monitor's work area in logical pixels (0, 0 when
// unknown).
func WorkArea() (int, int) {
	var w, h C.int
	C.work_area(&w, &h)
	return int(w), int(h)
}

// SetDefaultIcon sets the icon of all windows from PNG bytes.
func SetDefaultIcon(png []byte) {
	if len(png) == 0 {
		return
	}
	C.set_default_icon((*C.guchar)(unsafe.Pointer(&png[0])), C.gsize(len(png)))
}

// SetPreferDark follows the desktop's dark style; the page sees it as
// prefers-color-scheme.
func SetPreferDark(dark bool) {
	d := C.int(0)
	if dark {
		d = 1
	}
	C.set_prefer_dark(d)
}

// MessageBox shows a modal error message (GTK thread, GTK initialised).
func MessageBox(title, text string) {
	t, m := C.CString(title), C.CString(text)
	defer C.free(unsafe.Pointer(t))
	defer C.free(unsafe.Pointer(m))
	C.message_box(t, m)
}

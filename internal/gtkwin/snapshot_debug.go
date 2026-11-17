//go:build linux && cgo && snapshotdebug

package gtkwin

/*
#include <stdlib.h>
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>

typedef struct { GtkWidget *win; WebKitWebView *view; } jwin_head;

static void snap_done(GObject *src, GAsyncResult *res, gpointer path) {
	cairo_surface_t *s = webkit_web_view_get_snapshot_finish(WEBKIT_WEB_VIEW(src), res, NULL);
	if (s) { cairo_surface_write_to_png(s, path); cairo_surface_destroy(s); }
	g_free(path);
}
static void snap(void *j, const char *path) {
	webkit_web_view_get_snapshot(((jwin_head *)j)->view, WEBKIT_SNAPSHOT_REGION_VISIBLE,
		WEBKIT_SNAPSHOT_OPTIONS_NONE, NULL, snap_done, g_strdup(path));
}
*/
import "C"

import "unsafe"

// Snapshot writes the visible page to a PNG file (debug builds only).
func (w *Window) Snapshot(path string) {
	if w.alive() {
		p := C.CString(path)
		defer C.free(unsafe.Pointer(p))
		C.snap(unsafe.Pointer(w.j), p)
	}
}

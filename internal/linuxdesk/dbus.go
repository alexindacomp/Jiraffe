//go:build linux

package linuxdesk

import (
	"log"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

// --- notifications -----------------------------------------------------------

const (
	notifyDest  = "org.freedesktop.Notifications"
	notifyPath  = "/org/freedesktop/Notifications"
	notifyIface = "org.freedesktop.Notifications"
)

// Notifier shows desktop notifications; clicking one calls onClick with the
// URL it was shown with ("" = open the app).
type Notifier struct {
	conn    *dbus.Conn
	onClick func(url string)

	mu   sync.Mutex
	urls map[uint32]string // open notifications
}

// NewNotifier connects to the session bus.
func NewNotifier(onClick func(url string)) (*Notifier, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	n := &Notifier{conn: conn, onClick: onClick, urls: map[uint32]string{}}
	for _, member := range []string{"ActionInvoked", "NotificationClosed"} {
		if err := conn.AddMatchSignal(dbus.WithMatchInterface(notifyIface), dbus.WithMatchMember(member)); err != nil {
			conn.Close()
			return nil, err
		}
	}
	ch := make(chan *dbus.Signal, 16)
	conn.Signal(ch)
	go n.listen(ch)
	return n, nil
}

func (n *Notifier) listen(ch <-chan *dbus.Signal) {
	for s := range ch {
		if len(s.Body) < 2 {
			continue
		}
		id, ok := s.Body[0].(uint32)
		if !ok {
			continue
		}
		n.mu.Lock()
		url, known := n.urls[id]
		if s.Name == notifyIface+".NotificationClosed" {
			delete(n.urls, id)
		}
		n.mu.Unlock()
		if action, _ := s.Body[1].(string); s.Name == notifyIface+".ActionInvoked" && known && action == "default" {
			n.onClick(url)
		}
	}
}

// markupEscaper escapes the body (servers may render a subset of HTML).
var markupEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Show shows a notification; errors are logged.
func (n *Notifier) Show(title, text, url string) {
	hints := map[string]dbus.Variant{"desktop-entry": dbus.MakeVariant("jiraffe")}
	var id uint32
	err := n.conn.Object(notifyDest, notifyPath).Call(notifyIface+".Notify", 0,
		"Jiraffe", uint32(0), "jiraffe", title, markupEscaper.Replace(text),
		[]string{"default", "Open"}, hints, int32(-1)).Store(&id)
	if err != nil {
		log.Printf("notification: %v", err)
		return
	}
	n.mu.Lock()
	if len(n.urls) > 200 { // closed signals were missed: forget the oldest state
		n.urls = map[uint32]string{}
	}
	n.urls[id] = url
	n.mu.Unlock()
}

// --- dark style -------------------------------------------------------------

const (
	portalDest  = "org.freedesktop.portal.Desktop"
	portalPath  = "/org/freedesktop/portal/desktop"
	portalIface = "org.freedesktop.portal.Settings"
	appearance  = "org.freedesktop.appearance"
)

// darkScheme reads a color-scheme value: 1 = prefer dark.
func darkScheme(v dbus.Variant) bool {
	for i := 0; i < 2; i++ { // Read (old portals) wraps the value once more
		if inner, ok := v.Value().(dbus.Variant); ok {
			v = inner
		}
	}
	u, ok := v.Value().(uint32)
	return ok && u == 1
}

// WatchDarkStyle reports the desktop's "prefer dark" setting now and calls
// onChange (from another goroutine) when it changes. Without the settings
// portal it reports false and never calls onChange.
func WatchDarkStyle(onChange func(dark bool)) bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Printf("dark style: %v", err)
		return false
	}
	obj := conn.Object(portalDest, portalPath)
	var v dbus.Variant
	if err := obj.Call(portalIface+".ReadOne", 0, appearance, "color-scheme").Store(&v); err != nil {
		if err := obj.Call(portalIface+".Read", 0, appearance, "color-scheme").Store(&v); err != nil {
			log.Printf("dark style: settings portal: %v", err)
			conn.Close()
			return false
		}
	}
	if err := conn.AddMatchSignal(dbus.WithMatchInterface(portalIface), dbus.WithMatchMember("SettingChanged")); err == nil {
		ch := make(chan *dbus.Signal, 4)
		conn.Signal(ch)
		go func() {
			for s := range ch {
				if len(s.Body) == 3 && s.Body[0] == appearance && s.Body[1] == "color-scheme" {
					if val, ok := s.Body[2].(dbus.Variant); ok {
						onChange(darkScheme(val))
					}
				}
			}
		}()
	}
	return darkScheme(v)
}

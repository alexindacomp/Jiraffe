//go:build windows || (linux && cgo)

package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"time"

	"jiraffe/internal/app"
	"jiraffe/internal/config"
	"jiraffe/internal/jira"
	"jiraffe/internal/scheduler"
	"jiraffe/internal/state"
)

// bridge is the JS ↔ Go call path shared by the Windows and Linux front ends:
// window.__jiraffe(id, method, paramsJSON, nonce) → app.Call on a goroutine →
// window.__jiraffeResolve(id, ok, payload), and app events as "jiraffe"
// CustomEvents. post runs a closure on the UI thread; eval runs JS in the
// open page (UI thread only, nothing without a window).
type bridge struct {
	nonce string
	app   *app.App
	post  func(func()) bool
	eval  func(js string)
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
func (b *bridge) resolve(id json.RawMessage, ok bool, payload []byte) {
	js := "window.__jiraffeResolve&&window.__jiraffeResolve(" + string(id) + "," + strconv.FormatBool(ok) + "," + string(payload) + ")"
	b.post(func() { b.eval(js) })
}

// binding is window.__jiraffe(id, method, paramsJSON, nonce). It runs on the
// UI thread, so it only starts a goroutine; the result is delivered through
// window.__jiraffeResolve(id, ok, payload). Calls without the document's
// nonce are rejected with {code:"forbidden"}.
func (b *bridge) binding(id json.RawMessage, method string, rest ...json.RawMessage) {
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
	if !app.CheckNonce(b.nonce, nonce) {
		log.Printf("rejected call %q: missing or invalid nonce", method)
		e, _ := json.Marshal(app.BadNonceError(b.app.Language()))
		b.resolve(id, false, e)
		return
	}
	go func() {
		ok, payload := false, []byte(`{"code":"internal","message":"internal error"}`)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("call %s panicked: %v\n%s", method, r, debug.Stack())
				ok, payload = false, []byte(`{"code":"internal","message":"internal error"}`)
			}
			b.resolve(id, ok, payload)
		}()
		ctx, cancel := context.WithTimeout(b.app.Context(), 5*time.Minute)
		defer cancel()
		res, err := b.app.Call(ctx, method, params)
		var v any = res
		if err != nil {
			e := b.app.ErrorFor(err)
			if e.Code == app.CodeInternal || e.Code == jira.CodeServer {
				log.Printf("call %s: %s", method, e.Message)
			}
			v = e
		}
		enc, merr := json.Marshal(v)
		if merr != nil {
			log.Printf("call %s: encode result: %v", method, merr)
			return
		}
		ok, payload = err == nil, enc
	}()
}

// emit dispatches a "jiraffe" CustomEvent in the page (dropped when closed).
func (b *bridge) emit(typ string, payload any) {
	enc, err := json.Marshal(struct {
		Type    string `json:"type"`
		Payload any    `json:"payload"`
	}{typ, payload})
	if err != nil {
		log.Printf("emit %s: %v", typ, err)
		return
	}
	js := `window.dispatchEvent(new CustomEvent("jiraffe",{detail:` + string(enc) + `}))`
	b.post(func() { b.eval(js) })
}

// headlessPlatform is used by --worklog-preview: no UI, nothing written.
type headlessPlatform struct{ t jira.Transport }

func (h headlessPlatform) Transport() jira.Transport           { return h.t }
func (headlessPlatform) OpenURL(string) error                  { return nil }
func (headlessPlatform) SetAutostart(bool) error               { return nil }
func (headlessPlatform) Notify(string, string, string)         {}
func (headlessPlatform) SettingsChanged(config.Settings, bool) {}
func (headlessPlatform) SetWindowTheme(app.WindowTheme)        {}

// headlessPreview computes today's worklog preview and writes it as JSON to
// out. JIRAFFE_URL / JIRAFFE_TOKEN override the saved configuration.
// newTransport opens the HTTP transport and returns its close function.
func headlessPreview(dataDir, out, date string, prot config.Protector, newTransport func() (jira.Transport, func(), error)) (code int) {
	code = 1
	var result any
	defer func() {
		if r := recover(); r != nil {
			log.Printf("preview panicked: %v\n%s", r, debug.Stack())
			result, code = map[string]string{"error": "internal error"}, 1
			_ = writeJSON(out, result)
		}
	}()

	cfg, err := config.Open(filepath.Join(dataDir, "config.json"), prot)
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

	transport, closeTransport, err := newTransport()
	if err != nil {
		result = map[string]string{"code": app.CodeInternal, "error": err.Error()}
		_ = writeJSON(out, result)
		return 1
	}
	defer closeTransport()

	a := app.New(cfg, st, headlessPlatform{t: transport}, scheduler.SystemClock{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	run, err := a.ComputeWorklogFor(ctx, date)
	result = run
	if e := (*app.Error)(nil); errors.As(err, &e) && e.Code == app.CodeBadParams {
		result = map[string]string{"code": e.Code, "error": e.Message}
	}
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

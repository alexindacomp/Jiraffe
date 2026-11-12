package app

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"jiraffe/internal/jira"
	"jiraffe/internal/state"
	"jiraffe/internal/update"
)

// UpdateHost is what the updater needs from the platform (Windows).
type UpdateHost interface {
	// WindowOpen reports whether the app window is open (safe from any
	// goroutine).
	WindowOpen() bool
	// Quit exits the app cleanly: window closed, tray icon removed,
	// background work stopped. It returns at once.
	Quit()
}

// Timing of the automatic update check (SPEC v0.4 §5).
const (
	updateEvery    = 24 * time.Hour
	cleanupRetries = 5
)

// Update loop timing (variables for tests): the first automatic check
// after start, then how often the loop looks whether a check is due.
var (
	updateStartDelay = 30 * time.Second
	updateTick       = 15 * time.Minute
)

// Cleanup timing (variables for tests): retries of a locked file, and how
// long the previous version (exe.old) is kept after a start, so a broken
// update can still be rolled back by hand.
var (
	cleanupRetry = time.Minute
	keepOldFor   = 10 * time.Minute
)

// Error codes of the update methods (updateFailed uses update.Code*).
const (
	CodeUpdateUnsupported = "update_unsupported" // development build or no updater
	CodeUpdateStale       = "update_stale"       // version is not the latest checked newer one
	CodeUpdateBusy        = "update_busy"        // an install is already running
)

// errUpdateRestart stops worklog runs once the restart into an update began.
var errUpdateRestart = errors.New("restarting into an update")

// UpdateReleaseDTO is a release as the UI sees it (latest, updateAvailable).
type UpdateReleaseDTO struct {
	Version     string `json:"version"`
	Notes       string `json:"notes"`
	PublishedAt string `json:"publishedAt"`
	Size        int64  `json:"size"`
	URL         string `json:"url"` // release page ("What's new")
}

// UpdateCheckDTO is UpdateStatus.lastCheck.
type UpdateCheckDTO struct {
	At    string `json:"at"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"` // localized
}

// UpdatedDTO is UpdateStatus.justUpdated and the "updated" event.
type UpdatedDTO struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// UpdateStatus is the getUpdateStatus / checkForUpdates / skipUpdate result.
type UpdateStatus struct {
	CurrentVersion string            `json:"currentVersion"`
	Supported      bool              `json:"supported"`
	Latest         *UpdateReleaseDTO `json:"latest,omitempty"`
	Available      bool              `json:"available"`
	Prompt         bool              `json:"prompt"`
	SkippedVersion string            `json:"skippedVersion,omitempty"`
	LastCheck      *UpdateCheckDTO   `json:"lastCheck,omitempty"`
	Installing     bool              `json:"installing"`
	JustUpdated    *UpdatedDTO       `json:"justUpdated,omitempty"`
}

// UpdateProgressDTO is the updateProgress event payload.
type UpdateProgressDTO struct {
	Phase    string `json:"phase"` // download | verify | install | restart
	Received *int64 `json:"received,omitempty"`
	Total    *int64 `json:"total,omitempty"`
}

// UpdateFailedDTO is the updateFailed event payload.
type UpdateFailedDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// EnableUpdates wires the updater; call it before Start. Without it (tests,
// headless mode) the update methods report update_unsupported.
func (a *App) EnableUpdates(u *update.Updater, host UpdateHost) {
	a.upd, a.updHost = u, host
}

func (a *App) updatesSupported() bool { return a.upd != nil && update.Supported(Version) }

func releaseDTO(r *update.Release) *UpdateReleaseDTO {
	if r == nil {
		return nil
	}
	return &UpdateReleaseDTO{Version: r.Version, Notes: r.Notes, PublishedAt: r.PublishedAt, Size: r.Exe.Size, URL: r.Page}
}

// updateStatus builds the status from the stored check and this session.
func (a *App) updateStatus() UpdateStatus {
	snap := a.st.Snapshot()
	lang := a.Language()
	st := UpdateStatus{CurrentVersion: Version, Supported: a.updatesSupported(), SkippedVersion: snap.SkippedVersion,
		Installing: a.updJob.Load()}
	if c := snap.UpdateCheck; c != nil {
		st.LastCheck = &UpdateCheckDTO{At: c.At, OK: c.OK}
		if !c.OK {
			st.LastCheck.Error = updateMessage(lang, c.Code, c.Detail)
		}
		if rel, err := c.Latest.Sanitized(); err == nil { // never trust state.json blindly
			st.Latest = releaseDTO(rel)
			st.Available = st.Supported && update.Newer(rel.Version, Version)
			st.Prompt = st.Available && rel.Version != snap.SkippedVersion
		}
	}
	a.mu.Lock()
	if a.justUpdated != nil {
		j := *a.justUpdated
		st.JustUpdated = &j
	}
	a.mu.Unlock()
	return st
}

// checkUpdates asks GitHub for the latest release, stores the result and
// announces a newer, not skipped version (event, and a balloon when the
// window is closed). Checks are serialized.
func (a *App) checkUpdates(ctx context.Context) (UpdateStatus, error) {
	if !a.updatesSupported() {
		return UpdateStatus{}, a.newErr(CodeUpdateUnsupported, "err_update_unsupported")
	}
	a.updCheckMu.Lock()
	defer a.updCheckMu.Unlock()
	rel, err := a.upd.Latest(ctx)
	if err != nil {
		log.Printf("update check: %v", err)
	}
	a.recordCheck(rel, err)
	if err == nil && update.Newer(rel.Version, Version) && rel.Version != a.st.Snapshot().SkippedVersion {
		a.announceUpdate(rel)
	}
	return a.updateStatus(), nil
}

// recordCheck stores the result of a check (a failure keeps the release
// known before).
func (a *App) recordCheck(rel *update.Release, err error) {
	now := a.clock.Now()
	if uerr := a.st.Update(func(tx state.Tx) {
		c := &state.UpdateCheck{At: now.Format(time.RFC3339), OK: err == nil, Latest: rel}
		if err != nil {
			c.Code, c.Detail = updateCode(err), updateDetail(err)
			if tx.UpdateCheck != nil {
				c.Latest = tx.UpdateCheck.Latest // keep what we knew
			}
		}
		tx.UpdateCheck = c
	}); uerr != nil {
		log.Printf("save update check: %v", uerr)
	}
}

// announceUpdate emits updateAvailable and, when the window is closed, shows
// one balloon per version and session (unless notifications are paused).
// Clicking it opens the window, whose status then has prompt = true.
func (a *App) announceUpdate(rel *update.Release) {
	d := releaseDTO(rel)
	a.emit("updateAvailable", d)
	if a.updHost == nil || a.updHost.WindowOpen() || a.Paused() {
		return
	}
	a.mu.Lock()
	first := a.updAnnounced != rel.Version
	a.updAnnounced = rel.Version
	a.mu.Unlock()
	if first {
		lang := a.Language()
		a.plat.Notify(T(lang, "updAvailableTitle", rel.Version), T(lang, "updAvailableText"), "")
	}
}

// autoCheckDue reports whether the automatic check should run at now: the
// setting is on, the build takes part in updates, and there was no
// automatic check in this session within updateEvery.
func (a *App) autoCheckDue(now time.Time) bool {
	if !a.cfg.Get().AutoCheckUpdates || !a.updatesSupported() {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	last := a.updLastAuto
	if !last.IsZero() && now.Sub(last) < updateEvery && !now.Before(last) {
		return false
	}
	a.updLastAuto = now
	return true
}

// autoCheck runs the automatic check when due; failures are only logged and
// stored (shown in Settings), never shown as balloons.
func (a *App) autoCheck(ctx context.Context) {
	if a.autoCheckDue(a.clock.Now()) {
		_, _ = a.checkUpdates(ctx)
	}
}

// updateLoop checks updateStartDelay after start and then once a day.
func (a *App) updateLoop(ctx context.Context) {
	wait := updateStartDelay
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		a.autoCheck(ctx)
		wait = updateTick
	}
}

// cleanupUpdates removes stale downloads at once and the previous version
// (exe.old) once this one has run for keepOldFor.
func (a *App) cleanupUpdates(ctx context.Context) {
	a.retryCleanup(ctx, "downloads", a.upd.CleanupDownloads)
	if !sleepCtx(ctx, keepOldFor) {
		return
	}
	a.retryCleanup(ctx, "previous version", a.upd.CleanupOld)
}

// retryCleanup runs fn up to cleanupRetries times while files are locked.
// A pass is skipped while an install job owns the update files.
func (a *App) retryCleanup(ctx context.Context, what string, fn func() error) {
	for i := 0; ; i++ {
		err := errors.New("an update is being installed")
		if a.updFilesMu.TryLock() {
			err = fn()
			a.updFilesMu.Unlock()
		}
		if err == nil {
			return
		}
		if i+1 >= cleanupRetries {
			log.Printf("update cleanup (%s): %v (giving up)", what, err)
			return
		}
		log.Printf("update cleanup (%s): %v (retrying)", what, err)
		if !sleepCtx(ctx, cleanupRetry) {
			return
		}
	}
}

// sleepCtx waits for d; false if ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// UpdatedFrom is called at startup after an update from version from: it
// clears skippedVersion, remembers the update for the status and shows a
// balloon.
func (a *App) UpdatedFrom(from string) {
	if from == "" || from == Version {
		return
	}
	log.Printf("updated from %s to %s", from, Version)
	if err := a.st.Update(func(tx state.Tx) { tx.SkippedVersion = "" }); err != nil {
		log.Printf("save state: %v", err)
	}
	d := UpdatedDTO{From: from, To: Version}
	a.mu.Lock()
	a.justUpdated = &d
	a.mu.Unlock()
	a.emit("updated", d)
	if !a.Paused() {
		lang := a.Language()
		a.plat.Notify(T(lang, "updDoneTitle"), T(lang, "updDoneText", Version), "")
	}
}

// skipUpdate stores version as skipped ("Later").
func (a *App) skipUpdate(version string) (UpdateStatus, error) {
	v, ok := update.ParseVersion(version)
	if !ok {
		return UpdateStatus{}, a.badParams(`version must be "X.Y.Z"`)
	}
	if err := a.st.Update(func(tx state.Tx) { tx.SkippedVersion = v.String() }); err != nil {
		log.Printf("save skipped version: %v", err)
	}
	return a.updateStatus(), nil
}

// installUpdate starts the install job for version, which must be the
// latest checked version and newer than this one.
func (a *App) installUpdate(version string) error {
	if !a.updatesSupported() || a.updHost == nil {
		return a.newErr(CodeUpdateUnsupported, "err_update_unsupported")
	}
	v, ok := update.ParseVersion(version)
	if !ok {
		return a.badParams(`version must be "X.Y.Z"`)
	}
	var known *update.Release
	if c := a.st.Snapshot().UpdateCheck; c != nil {
		known, _ = c.Latest.Sanitized()
	}
	if known == nil || known.Version != v.String() || !update.Newer(known.Version, Version) {
		return a.newErr(CodeUpdateStale, "err_update_stale")
	}
	if !a.updJob.CompareAndSwap(false, true) {
		return a.newErr(CodeUpdateBusy, "err_update_busy")
	}
	want := known.Version
	a.goBackground("update install", func(ctx context.Context) { a.runInstall(ctx, want) })
	return nil
}

// runInstall reads the latest release again (the stored one only chose
// the version), downloads, verifies (size, MZ, SHA-256, signature) and
// installs it, then starts the new exe and quits. Progress and failures are
// events; a failure leaves the current version installed and running, and
// the app never restarts once ctx ended (the user quit).
func (a *App) runInstall(ctx context.Context, version string) {
	a.updFilesMu.Lock() // cleanup keeps off the job's files
	defer a.updFilesMu.Unlock()
	// The job flag is cleared in exactly one place, before updateFailed is
	// emitted (installUpdate may be called again when the UI hears of it);
	// after a successful start it stays set until the process exits.
	var failed error
	defer func() {
		if a.updExiting.Load() {
			return
		}
		a.updJob.Store(false)
		if failed != nil {
			lang := a.Language()
			code := updateCode(failed)
			a.emit("updateFailed", UpdateFailedDTO{Code: code, Message: updateMessage(lang, code, updateDetail(failed))})
		}
	}()
	fail := func(err error) {
		log.Printf("update to %s failed: %v", version, err)
		failed = err
	}
	phase := func(p string) { a.emit("updateProgress", UpdateProgressDTO{Phase: p}) }
	if err := a.upd.CheckWritable(); err != nil {
		fail(err)
		return
	}
	var total int64
	report := func(received int64) {
		r, t := received, total
		a.emit("updateProgress", UpdateProgressDTO{Phase: "download", Received: &r, Total: &t})
	}
	rel, err := a.upd.Latest(ctx)
	a.recordCheck(rel, err)
	if err != nil {
		fail(err)
		return
	}
	if rel.Version != version {
		fail(&update.Error{Code: update.CodeChanged, Detail: "latest is now " + rel.Version})
		return
	}
	total = rel.Exe.Size
	report(0)
	lastPct := int64(0)
	f, err := a.upd.Download(ctx, rel, func(received, total int64) {
		if total <= 0 {
			return
		}
		if pct := received * 100 / total; pct > lastPct { // at most ~100 events
			lastPct = pct
			report(received)
		}
	})
	if err != nil {
		fail(err)
		return
	}
	phase("verify")
	if err := a.upd.VerifyDownload(f, rel); err != nil {
		fail(err)
		return
	}
	phase("install")
	// Never swap during a worklog run: wait for it, and keep new runs out
	// from the swap until this process exits.
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if err := a.swapAndStart(ctx, f, rel); err != nil {
		fail(err)
		return
	}
	log.Printf("update: started %s, exiting", rel.Version)
	a.updHost.Quit()
}

// swapAndStart installs the verified download and starts it. The whole
// section holds swapMu, which Stop waits for without a time limit: a swap
// is never cut short by quitting (it is bounded by the rename retries),
// so jiraffe.exe is never left missing. Quitting before or during the
// section cancels it (a finished swap is rolled back, nothing is started).
func (a *App) swapAndStart(ctx context.Context, f *update.Fetched, rel *update.Release) error {
	a.swapMu.Lock()
	defer a.swapMu.Unlock()
	cancelled := func() error { return &update.Error{Code: update.CodeCancelled, Err: ctx.Err()} }
	if ctx.Err() != nil {
		return cancelled()
	}
	swap, err := a.upd.Install(f.Path, rel.Exe.Size, f.Sum)
	if err != nil {
		return err
	}
	a.emit("updateProgress", UpdateProgressDTO{Phase: "restart"})
	undo := func(cause error) error {
		if rerr := swap.Rollback(); rerr != nil {
			log.Printf("update rollback: %v (after %v)", rerr, cause)
			return rerr // worse than the cause: report where the previous exe is
		}
		return cause
	}
	if ctx.Err() != nil { // the user quit meanwhile: no restart
		return undo(cancelled())
	}
	a.updExiting.Store(true)
	if err := a.upd.Start(update.RestartArgs(Version, os.Getpid(), !a.updHost.WindowOpen())); err != nil {
		a.updExiting.Store(false)
		return undo(err)
	}
	return nil
}

// updateCode is the code of an update failure.
func updateCode(err error) string {
	if c := update.CodeOf(err); c != "" {
		return c
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return update.CodeCancelled
	}
	return update.CodeInstall
}

// updateDetail is the English detail of an update failure, without the
// code: the network kind for transport errors, else the error text.
func updateDetail(err error) string {
	var ue *update.Error
	if errors.As(err, &ue) && ue.Previous != "" {
		return "rollback:" + ue.Previous
	}
	if errors.Is(err, update.ErrTimeout) {
		return "net:" + jira.NetTimeout
	}
	var te *jira.TransportError
	if errors.As(err, &te) {
		return "net:" + te.Kind
	}
	if errors.As(err, &ue) {
		return strings.TrimPrefix(strings.TrimPrefix(ue.Error(), "update: "+ue.Code), ": ")
	}
	return err.Error()
}

// updateMessage localizes an update failure.
func updateMessage(lang, code, detail string) string {
	if path, ok := strings.CutPrefix(detail, "rollback:"); ok {
		return T(lang, "upd_rollback_failed", path)
	}
	if kind, ok := strings.CutPrefix(detail, "net:"); ok {
		key := "upd_net_other"
		switch {
		case kind == jira.NetTimeout:
			key = "upd_net_timeout"
		case strings.HasPrefix(kind, "tls"):
			key = "upd_net_tls"
		case kind == jira.NetDNS || kind == jira.NetConnect || kind == jira.NetProxy:
			key = "upd_net_connect"
		}
		return T(lang, key)
	}
	key := "upd_" + code
	if !hasText(key) {
		key = "upd_install_failed"
	}
	msg := T(lang, key)
	switch code {
	case update.CodeHTTP, update.CodeRedirect, update.CodeBadRelease, update.CodeBadChecksum, update.CodeInstall,
		update.CodeStart, update.CodeFile:
		if detail != "" {
			msg += " (" + detail + ")"
		}
	}
	return msg
}

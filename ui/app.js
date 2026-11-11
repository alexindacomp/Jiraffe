/* Jiraffe UI: application logic.
   Plain ES2020 script (no modules): the Go side inlines index.html's <script src> files in order
   (i18n.js, mock.js, app.js) into one document. Talks to Go only through api() below. */
(function () {
  'use strict';

  const t = (key, params) => i18n.t(key, params);
  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

  /* ================================================================
   * Safe HTML templating: every interpolated value is escaped unless
   * it is itself the result of html`` (Safe).
   * ================================================================ */
  class Safe { constructor(s) { this.s = s; } }
  const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
  const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ESC[c]);
  function val(v) {
    if (v == null || v === false) return '';
    if (v instanceof Safe) return v.s;
    if (Array.isArray(v)) return v.map(val).join('');
    return esc(v);
  }
  function html(strings, ...vals) {
    let out = strings[0];
    for (let i = 0; i < vals.length; i++) out += val(vals[i]) + strings[i + 1];
    return new Safe(out);
  }
  const setHTML = (el, safe) => { el.innerHTML = safe.s; };
  const icon = (name, cls) => html`<svg class="icon${cls ? ' ' + cls : ''}" aria-hidden="true"><use href="#i-${name}"></use></svg>`;

  /* ================================================================
   * JS <-> Go bridge (SPEC "JS ↔ Go contract")
   * window.__jiraffe(id, method, paramsJSON, nonce) returns immediately;
   * Go answers with window.__jiraffeResolve(id, ok, payload).
   * The nonce is inlined by Go as window.__jiraffeNonce before any app script;
   * calls without it are rejected with code "forbidden".
   * ================================================================ */
  class ApiError extends Error {
    constructor(code, message, fields) {
      super(message || code || 'error');
      this.code = code || 'unknown';
      this.fields = Array.isArray(fields) ? fields : [];
    }
  }
  // A manual worklog run may wait for a scheduled run on the Go side (~6 min worst case).
  const CALL_TIMEOUT_MS = { worklogRun: 7 * 60000, worklogPreview: 3 * 60000 };
  const DEFAULT_TIMEOUT_MS = 120000;
  const pendingCalls = new Map();
  let callSeq = 0;

  function parsePayload(p) {
    if (typeof p !== 'string') return p;
    try { return JSON.parse(p); } catch { return p; }
  }

  window.__jiraffeResolve = function (id, ok, payload) {
    const key = Number(id);
    const entry = pendingCalls.get(key);
    if (!entry) return;
    pendingCalls.delete(key);
    clearTimeout(entry.timer);
    const data = parsePayload(payload);
    if (ok === true || ok === 1 || ok === 'true') {
      entry.resolve(data === undefined ? null : data);
    } else {
      const e = data && typeof data === 'object' ? data : { message: data == null ? '' : String(data) };
      entry.reject(new ApiError(e.code, e.message, e.fields));
    }
  };

  function api(method, params) {
    return new Promise((resolve, reject) => {
      if (typeof window.__jiraffe !== 'function') { reject(new ApiError('no_backend')); return; }
      const id = ++callSeq;
      const timer = setTimeout(() => {
        if (pendingCalls.delete(id)) reject(new ApiError('timeout'));
      }, CALL_TIMEOUT_MS[method] || DEFAULT_TIMEOUT_MS);
      pendingCalls.set(id, { resolve, reject, timer });
      const fail = (err) => {
        if (!pendingCalls.delete(id)) return;
        clearTimeout(timer);
        reject(new ApiError('binding', (err && err.message) || String(err)));
      };
      try {
        const ret = window.__jiraffe(id, method, JSON.stringify(params ?? {}), window.__jiraffeNonce);
        if (ret && typeof ret.then === 'function') ret.then(null, fail);
      } catch (err) { fail(err); }
    });
  }

  function errorText(err) {
    if (!err) return t('error.generic');
    if (err.code && i18n.has('error.' + err.code)) return t('error.' + err.code);
    return err.message || t('error.generic');
  }

  /* ================================================================
   * Small helpers
   * ================================================================ */
  const parseDate = i18n.parseDate;
  const nameOf = (x) => (x == null ? '' : typeof x === 'string' ? x : x.name || '');
  const projectOf = (issue) => {
    const p = issue.project;
    if (p && typeof p === 'object') return p.key || p.name || '';
    return p || String(issue.key || '').split('-')[0];
  };
  const initials = (name) => {
    const parts = String(name || '').trim().split(/\s+/).filter(Boolean);
    if (!parts.length) return '?';
    return ((parts[0][0] || '') + (parts.length > 1 ? parts[parts.length - 1][0] : '')).toUpperCase();
  };
  const dur = (sec) => i18n.duration(sec, (S.settings && S.settings.hoursPerDay) || 9);
  const isTyping = (el) => !!(el && el.closest && el.closest('input, textarea, select, [contenteditable="true"]'));
  const sameStatus = (a, b) => !!a && !!b && (
    (a.id != null && b.id != null && String(a.id) === String(b.id)) ||
    String(a.name || '').toLowerCase() === String(b.name || '').toLowerCase());
  const CAT_ORDER = { new: 0, indeterminate: 1, done: 2 };

  function setBusy(btn, busy) {
    if (!btn) return;
    btn.disabled = !!busy;
    if (busy) btn.setAttribute('aria-busy', 'true'); else btn.removeAttribute('aria-busy');
  }

  /* ================================================================
   * App state
   * ================================================================ */
  const S = {
    view: 'board',
    appState: null,          // getState result
    stateError: null,
    stateLoading: false,
    settings: null,          // last saved Settings from backend
    langOverride: null,      // language chosen in UI but not yet persisted

    board: null,             // { columns: [{status, issues, synthetic?}], fetchedAt }
    boardLoading: false,
    boardError: null,
    boardSeq: 0,
    boardStale: false,
    boardRenderDeferred: false,
    filter: { q: '', projects: new Set() },
    pendingKeys: new Set(),  // issues with a transition in flight
    transCache: new Map(),   // key -> Promise<[Transition]>
    drag: null,              // { key, fromId }
    drawer: null,            // { key, issue, transitions, loading, error, busyId }

    wl: { preview: null, previewLoading: false, previewError: null, previewSeq: 0,
          result: null, running: false,
          history: null, historyLoading: false, historyError: null },

    notif: { list: null, loading: false, error: null, markingAll: false },

    form: null,              // settings form working copy for list editors
    formLoading: false,
    formError: null,
    saving: false,
  };

  const isConfigured = () => !!(S.appState && S.appState.configured);

  /* ================================================================
   * Layers (drawer + dialogs). Non-modal <dialog>s; the app is made
   * inert while a layer is open so toasts remain interactive on top.
   * ================================================================ */
  const layers = {
    stack: [],
    open(el, opts = {}) {
      const entry = { el, opts, prevFocus: document.activeElement };
      this.stack.forEach((l) => { l.el.inert = true; });
      this.stack.push(entry);
      $('#app').inert = true;
      $('#scrim').hidden = false;
      el.inert = false;
      if (!el.open) el.show();
      const target = (opts.focus && $(opts.focus, el)) ||
        el.querySelector('[autofocus], button:not(:disabled), [href], input, select, textarea, [tabindex]:not([tabindex="-1"])');
      if (target) target.focus({ preventScroll: true });
    },
    close(el, result) {
      const i = this.stack.findIndex((l) => l.el === el);
      if (i < 0) return;
      const [entry] = this.stack.splice(i, 1);
      el.close();
      const top = this.stack[this.stack.length - 1];
      if (top) top.el.inert = false;
      else { $('#app').inert = false; $('#scrim').hidden = true; }
      if (entry.opts.onClose) entry.opts.onClose(result);
      const restore = entry.opts.restoreFocus ? entry.opts.restoreFocus() : entry.prevFocus;
      if (restore && document.contains(restore) && typeof restore.focus === 'function') restore.focus({ preventScroll: true });
    },
    top() { return this.stack[this.stack.length - 1] || null; },
    closeTop() { const top = this.top(); if (top) this.close(top.el); },
  };

  function confirmDialog({ title, body, okLabel }) {
    const dlg = $('#dlg-confirm');
    $('#confirm-title').textContent = title;
    $('#confirm-body').textContent = body;
    $('#confirm-ok').textContent = okLabel || t('common.ok');
    return new Promise((resolve) => {
      let done = false;
      const finish = (v) => { if (done) return; done = true; resolve(v); layers.close(dlg); };
      dlg.onclick = (e) => {
        const a = e.target.closest('[data-action]');
        if (a) finish(a.dataset.action === 'ok');
      };
      layers.open(dlg, { focus: '#confirm-ok', onClose: () => { if (!done) { done = true; resolve(false); } } });
    });
  }

  function pickTransition(matches, issue, statusName) {
    const dlg = $('#dlg-picker');
    $('#picker-text').textContent = t('picker.text', { key: issue.key, status: statusName });
    setHTML($('#picker-list'), html`${matches.map((tr, i) => html`
      <li><button type="button" class="tr-btn" data-pick="${i}">
        <span class="tr-name">${tr.name}</span>
        <span class="tr-to">${icon('arrow-right')}${statusPill(tr.to)}</span>
      </button></li>`)}`);
    return new Promise((resolve) => {
      let done = false;
      const finish = (v) => { if (done) return; done = true; resolve(v); layers.close(dlg); };
      dlg.onclick = (e) => {
        const p = e.target.closest('[data-pick]');
        if (p) { finish(matches[Number(p.dataset.pick)]); return; }
        if (e.target.closest('[data-action="cancel"]')) finish(null);
      };
      layers.open(dlg, { onClose: () => { if (!done) { done = true; resolve(null); } } });
    });
  }

  /* ================================================================
   * Toasts
   * ================================================================ */
  const TOAST_ICON = { info: 'info', success: 'check-circle', error: 'alert', warn: 'alert' };
  function toast(message, { tone = 'info', action, timeout } = {}) {
    const box = $('#toasts');
    // Same message already visible (e.g. repeated refresh failures): restart its timer instead of stacking.
    const dup = $$('.toast:not(.is-leaving)', box).find((x) => x.dataset.msg === message);
    if (dup && dup._rearm) { dup._rearm(); return; }
    const el = document.createElement('div');
    el.dataset.msg = message;
    el.className = 'toast toast-' + tone;
    if (tone === 'error') el.setAttribute('role', 'alert');
    setHTML(el, html`${icon(TOAST_ICON[tone] || 'info')}<span class="toast-msg">${message}</span>${action ? html`<button type="button" class="btn btn-sm btn-ghost" data-toast-action>${action.label}</button>` : ''}<button type="button" class="icon-btn icon-btn-sm" data-toast-close aria-label="${t('common.close')}">${icon('x')}</button>`);
    let timer = null;
    const remove = () => {
      clearTimeout(timer);
      if (!el.isConnected || el.classList.contains('is-leaving')) return;
      el.classList.add('is-leaving');
      setTimeout(() => el.remove(), 220);
    };
    const arm = () => { clearTimeout(timer); timer = setTimeout(remove, timeout || (tone === 'error' ? 9000 : 5000)); };
    el.addEventListener('click', (e) => {
      if (e.target.closest('[data-toast-close]')) remove();
      else if (e.target.closest('[data-toast-action]')) { remove(); action.run(); }
    });
    el.addEventListener('mouseenter', () => clearTimeout(timer));
    el.addEventListener('mouseleave', arm);
    el.addEventListener('focusin', () => clearTimeout(timer));
    el.addEventListener('focusout', arm);
    el._rearm = arm;
    box.append(el);
    while (box.children.length > 4) box.firstElementChild.remove();
    arm();
  }
  const toastError = (prefix, err) => toast(prefix ? `${prefix}: ${errorText(err)}` : errorText(err), { tone: 'error' });

  /* ================================================================
   * Shared render pieces
   * ================================================================ */
  function stateBlock({ iconName = 'info', tone = '', title, text, action, fill }) {
    return html`<div class="state${fill ? ' state-fill' : ''}" data-tone="${tone}">
      <div class="state-icon">${icon(iconName)}</div>
      ${title ? html`<p class="state-title">${title}</p>` : ''}
      ${text ? html`<p class="state-text">${text}</p>` : ''}
      ${action ? html`<button type="button" class="btn${action.primary ? ' btn-primary' : ''}" data-action="${action.id}">${action.label}</button>` : ''}
    </div>`;
  }

  function statusPill(status) {
    if (!status) return '';
    return html`<span class="status-pill" data-cat="${status.category || 'new'}">${status.name}</span>`;
  }

  const TYPE_MAP = [
    [/bug|ошиб|помил|дефект|defect|incident|инцидент|інцидент/i, 'bug'],
    [/story|истор|істор/i, 'story'],
    [/epic|эпик|епік/i, 'epic'],
    [/task|задач|завдан/i, 'task'],
  ];
  function typeBadge(type) {
    const name = nameOf(type);
    if (!name) return '';
    const kind = (TYPE_MAP.find(([re]) => re.test(name)) || [, 'other'])[1];
    return html`<span class="type-badge" data-type="${kind}">${name}</span>`;
  }

  const PRIO_MAP = [
    [/highest|blocker|critical|наивыс|найвищ|критич|блок/i, 'highest', 'chevrons-up'],
    [/high|major|высок|висок|важн/i, 'high', 'chevron-up'],
    [/lowest|trivial|наимен|найниж|тривиал/i, 'lowest', 'chevrons-down'],
    [/low|minor|низк|низьк|незнач/i, 'low', 'chevron-down'],
    [/medium|normal|средн|середн|обычн|звичайн/i, 'medium', 'equal'],
  ];
  function priorityIcon(priority) {
    const name = nameOf(priority);
    if (!name) return '';
    const m = PRIO_MAP.find(([re]) => re.test(name)) || [null, 'medium', 'equal'];
    const label = t('issue.priorityValue', { name });
    return html`<span class="prio" data-level="${m[1]}" title="${label}">${icon(m[2])}<span class="sr-only">${label}</span></span>`;
  }

  function relSpan(iso, key, cls) {
    if (!iso) return '';
    return html`<time class="${cls || ''}" datetime="${iso}" data-rel="${iso}" data-rel-key="${key || ''}" title="${i18n.dateTime(iso)}">${relText(iso, key)}</time>`;
  }
  function relText(iso, key) {
    const rel = i18n.relative(iso);
    return key ? t(key, { rel }) : rel;
  }

  /* ================================================================
   * Routing / chrome
   * ================================================================ */
  const VIEWS = ['board', 'worklog', 'notifications', 'settings'];

  function showView(name) {
    if (!VIEWS.includes(name)) name = 'board';
    S.view = name;
    renderView(true);
  }

  /** Rail state. During onboarding only Settings is reachable; the others are disabled. */
  function renderNav(sectionId) {
    const onboarding = sectionId === 'view-onboarding';
    $$('.nav-item').forEach((b) => {
      const disabled = onboarding && b.dataset.view !== 'settings';
      if (disabled) b.setAttribute('aria-disabled', 'true'); else b.removeAttribute('aria-disabled');
      if (b.dataset.view === S.view && !disabled) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current');
    });
  }

  /** Lifts the toast stack above the sticky save bar while Settings is shown (DR-1). */
  function updateToastOffset() {
    const bar = $('#savebar');
    const visible = S.view === 'settings' && !$('#view-settings').hidden && !bar.hidden;
    document.documentElement.style.setProperty('--toast-offset', visible ? bar.offsetHeight + 'px' : '0px');
  }

  function activeSectionId() {
    if (S.view === 'settings') return 'view-settings';
    if (!S.appState) return 'view-status';
    if (!S.appState.configured) return 'view-onboarding';
    return 'view-' + S.view;
  }

  function renderView(enter) {
    const id = activeSectionId();
    $$('#views > .view').forEach((sec) => { sec.hidden = sec.id !== id; });
    $('#view-title').textContent = id === 'view-onboarding' ? t('onboarding.viewTitle') : t('title.' + S.view);
    $('#board-tools').hidden = id !== 'view-board';
    $('#refresh-btn').hidden = id === 'view-onboarding';
    renderNav(id);
    updateToastOffset();
    renderRefreshState();
    switch (id) {
      case 'view-status': renderStatus(); break;
      case 'view-onboarding': if (enter) enterOnboarding(); break;
      case 'view-board': if (enter && (!S.board || S.boardStale)) loadBoard(false); else renderBoard(); break;
      case 'view-worklog': if (enter) enterWorklog(); else renderWorklog(); break;
      case 'view-notifications': if (enter) loadNotifications(); else renderNotifications(); break;
      case 'view-settings': if (enter) enterSettings(); else renderSettingsDynamic(); break;
    }
  }

  function renderStatus() {
    const el = $('#view-status');
    if (S.stateError) {
      setHTML(el, stateBlock({ iconName: 'alert', tone: 'error', fill: true, title: t('app.stateErrorTitle'),
        text: errorText(S.stateError), action: { id: 'retry-state', label: t('common.retry') } }));
    } else {
      setHTML(el, html`<div class="state state-fill"><span class="spinner" role="status" aria-label="${t('common.loading')}"></span></div>`);
    }
  }

  function renderChrome() {
    const st = S.appState;
    const avatar = $('#avatar');
    const dot = $('#avatar-dot');
    const btn = $('#avatar-btn');
    let label;
    if (st && st.configured && st.user) {
      avatar.textContent = initials(st.user.displayName || st.user.name);
      dot.dataset.tone = 'ok';
      label = t('chrome.connectedAs', { name: st.user.displayName || st.user.name });
    } else if (st && !st.configured) {
      setHTML(avatar, icon('plug'));
      dot.dataset.tone = 'warn';
      label = t('chrome.notConfigured');
    } else if (S.stateError) {
      setHTML(avatar, icon('alert'));
      dot.dataset.tone = 'error';
      label = t('chrome.stateError');
    } else {
      avatar.textContent = '';
      dot.dataset.tone = '';
      label = t('common.loading');
    }
    btn.setAttribute('aria-label', label + '. ' + t('chrome.openSettings'));
    btn.title = label;
    // Rail shows only the semver core ("v0.1.0"); the full build string goes to the tooltip.
    const version = st && st.version ? String(st.version).replace(/^v/, '') : '';
    const core = (version.match(/^\d+\.\d+\.\d+/) || [version])[0];
    $('#app-version').textContent = core ? 'v' + core : '';
    $('#app-version').title = version ? 'Jiraffe ' + version : '';
    renderBadge();
  }

  function renderBadge() {
    const unread = (S.appState && S.appState.unread) || 0;
    const badge = $('#nav-badge');
    badge.hidden = unread <= 0;
    badge.textContent = unread > 99 ? '99+' : String(unread);
    const nav = $('#nav-notifications');
    nav.setAttribute('aria-label', unread > 0 ? `${t('nav.notifications')}, ${t('notif.unreadCount', { n: unread })}` : t('nav.notifications'));
  }

  function viewIsLoading() {
    switch (S.view) {
      case 'board': return S.boardLoading;
      case 'worklog': return S.wl.previewLoading || S.wl.historyLoading;
      case 'notifications': return S.notif.loading;
      case 'settings': return S.formLoading;
    }
    return false;
  }
  function renderRefreshState() {
    const btn = $('#refresh-btn');
    const loading = viewIsLoading() || S.stateLoading;
    btn.classList.toggle('is-spinning', loading);
    btn.setAttribute('aria-busy', loading ? 'true' : 'false');
  }

  function refreshCurrent() {
    if (!S.appState) { loadState(); return; }
    if (S.view === 'settings') { if (!formDirty()) enterSettings(true); return; }
    if (!S.appState.configured) { loadState(); return; }
    if (S.view === 'board') loadBoard(true);
    else if (S.view === 'worklog') {
      if (!S.wl.running) { S.wl.result = null; loadPreview(); }
      loadHistory();
    }
    else if (S.view === 'notifications') loadNotifications();
  }

  /* ================================================================
   * App state (getState / state event)
   * ================================================================ */
  async function loadState() {
    S.stateLoading = true;
    renderRefreshState();
    try {
      const st = await api('getState');
      S.stateError = null;
      applyState(st);
    } catch (e) {
      S.stateError = e;
      if (!S.appState) { renderChrome(); renderView(false); }
      else toastError(t('app.stateErrorTitle'), e);
    } finally {
      S.stateLoading = false;
      renderRefreshState();
    }
  }

  function applyState(st) {
    if (!st || typeof st !== 'object') return;
    const prev = S.appState;
    S.appState = st;
    if (st.language && !S.langOverride && st.language !== i18n.lang) i18n.setLang(st.language); // re-renders via onChange
    renderChrome();
    if (!prev || !!prev.configured !== !!st.configured) renderView(true);
    else if (S.view === 'worklog' && isConfigured()) renderWorklogToday();
  }

  /* ================================================================
   * BOARD
   * ================================================================ */
  function normalizeBoard(data) {
    const columns = (data && Array.isArray(data.columns) ? data.columns : [])
      .filter((c) => c && c.status)
      .map((c) => ({ status: c.status, issues: Array.isArray(c.issues) ? c.issues.slice() : [] }));
    return { columns, fetchedAt: data && data.fetchedAt };
  }

  async function loadBoard(force) {
    if (S.drag || S.pendingKeys.size) { S.boardStale = true; return; }
    const seq = ++S.boardSeq;
    S.boardLoading = true;
    S.boardStale = false;
    if (!S.board) S.boardError = null;
    renderBoard();
    renderRefreshState();
    try {
      const data = await api('getBoard', force ? { force: true } : {});
      if (seq !== S.boardSeq) return;
      if (S.pendingKeys.size || S.drag) { S.boardStale = true; return; } // a move started meanwhile; refetch after it
      S.board = normalizeBoard(data);
      S.boardError = null;
      S.transCache.clear();
    } catch (e) {
      if (seq !== S.boardSeq) return;
      S.boardError = e;
      if (S.board) toastError(t('board.refreshFailed'), e);
    } finally {
      if (seq === S.boardSeq) {
        S.boardLoading = false;
        renderBoard();
        renderRefreshState();
        if (S.drawer) renderDrawer();
      }
    }
  }

  function afterBoardMutation() {
    if (S.boardStale && !S.drag && !S.pendingKeys.size && S.view === 'board') loadBoard(false);
  }

  function allIssues() {
    return S.board ? S.board.columns.flatMap((c) => c.issues) : [];
  }
  function findIssue(key) {
    if (!S.board) return null;
    for (const col of S.board.columns) {
      const idx = col.issues.findIndex((i) => i.key === key);
      if (idx >= 0) return { col, idx, issue: col.issues[idx] };
    }
    return null;
  }
  function findColumn(status) {
    return S.board && S.board.columns.find((c) => sameStatus(c.status, status));
  }
  function ensureColumn(status) {
    let col = findColumn(status);
    if (col) return col;
    col = { status, issues: [], synthetic: true };
    const rank = CAT_ORDER[status.category] ?? 1;
    const cols = S.board.columns;
    let at = cols.length;
    for (let i = 0; i < cols.length; i++) {
      if ((CAT_ORDER[cols[i].status.category] ?? 1) > rank) { at = i; break; }
    }
    cols.splice(at, 0, col);
    return col;
  }
  /** Moves an issue into the column of `status` (creating it if needed); returns the previous position. */
  function placeIssue(key, issue, status, index) {
    const found = findIssue(key);
    const prev = found ? { status: found.col.status, idx: found.idx } : null;
    if (found) {
      found.col.issues.splice(found.idx, 1);
      if (found.col.synthetic && !found.col.issues.length) S.board.columns.splice(S.board.columns.indexOf(found.col), 1);
    }
    const col = ensureColumn(status);
    const next = Object.assign({}, issue, { status });
    col.issues.splice(index == null ? 0 : Math.min(index, col.issues.length), 0, next);
    return prev;
  }

  function issueMatches(issue) {
    const q = S.filter.q.trim().toLowerCase();
    if (q && !(String(issue.key).toLowerCase().includes(q) || String(issue.summary || '').toLowerCase().includes(q))) return false;
    if (S.filter.projects.size && !S.filter.projects.has(projectOf(issue))) return false;
    return true;
  }

  function renderChips() {
    const box = $('#project-chips');
    const counts = new Map();
    allIssues().forEach((i) => { const p = projectOf(i); if (p) counts.set(p, (counts.get(p) || 0) + 1); });
    for (const p of [...S.filter.projects]) if (!counts.has(p)) S.filter.projects.delete(p);
    const projects = [...counts.keys()].sort((a, b) => a.localeCompare(b));
    box.hidden = projects.length < 2;
    if (box.hidden) { box.textContent = ''; return; }
    const focused = document.activeElement && box.contains(document.activeElement) ? document.activeElement.dataset.project : null;
    const none = S.filter.projects.size === 0;
    setHTML(box, html`<button type="button" class="chip-btn" data-project="" aria-pressed="${none ? 'true' : 'false'}">${t('board.allProjects')}</button>${projects.map((p) => html`<button type="button" class="chip-btn" data-project="${p}" aria-pressed="${S.filter.projects.has(p) ? 'true' : 'false'}">${p}<span class="chip-count">${counts.get(p)}</span></button>`)}`);
    if (focused != null) { const b = box.querySelector(`[data-project="${CSS.escape(focused)}"]`); if (b) b.focus(); }
  }

  function cardHTML(issue) {
    const pend = S.pendingKeys.has(issue.key);
    return html`<button type="button" class="card${pend ? ' is-pending' : ''}" draggable="${pend ? 'false' : 'true'}" data-key="${issue.key}" aria-busy="${pend ? 'true' : 'false'}" aria-haspopup="dialog">
      <span class="card-top"><span class="card-key">${issue.key}</span>${priorityIcon(issue.priority)}${pend ? html`<span class="spinner spinner-sm" aria-hidden="true"></span>` : ''}</span>
      <span class="card-sum">${issue.summary}</span>
      <span class="card-meta">${typeBadge(issue.type)}${relSpan(issue.updated, 'board.updated', 'card-upd')}</span>
    </button>`;
  }

  function boardSkeleton() {
    const col = (n) => html`<div class="col" aria-hidden="true"><div class="col-head"><span class="skeleton sk-line" style="width:45%"></span></div><div class="col-list">${Array.from({ length: n }, () => html`<div class="skeleton sk-card"></div>`)}</div></div>`;
    return html`<div class="board-skeleton" role="status" aria-label="${t('common.loading')}">${col(3)}${col(2)}${col(4)}${col(1)}</div>`;
  }

  function renderBoard() {
    if (S.view !== 'board' || activeSectionId() !== 'view-board') return;
    if (S.drag) { S.boardRenderDeferred = true; return; }
    const root = $('#board');
    renderChips();
    if (!S.board) {
      if (S.boardError && !S.boardLoading) {
        setHTML(root, stateBlock({ iconName: 'alert', tone: 'error', fill: true, title: t('board.errorTitle'),
          text: errorText(S.boardError), action: { id: 'board-retry', label: t('common.retry') } }));
      } else setHTML(root, boardSkeleton());
      return;
    }
    const total = allIssues().length;
    if (!total) {
      setHTML(root, stateBlock({ iconName: 'check-circle', tone: 'good', fill: true, title: t('board.emptyTitle'), text: t('board.emptyText') }));
      return;
    }
    // Preserve scroll + focus across re-render.
    const prevCols = $('.columns', root);
    const scrollX = prevCols ? prevCols.scrollLeft : 0;
    const scrollY = new Map($$('.col', root).map((c) => [c.dataset.status, $('.col-list', c).scrollTop]));
    const focusKey = document.activeElement && root.contains(document.activeElement) && document.activeElement.dataset.key;

    let visible = 0;
    const cols = S.board.columns.map((col) => {
      const issues = col.issues.filter(issueMatches);
      visible += issues.length;
      const sid = String(col.status.id ?? col.status.name);
      const hid = 'col-h-' + sid.replace(/[^\w-]/g, '_');
      return html`<section class="col" data-cat="${col.status.category || 'new'}" data-status="${sid}" aria-labelledby="${hid}">
        <header class="col-head"><span class="col-dot" aria-hidden="true"></span><h2 class="col-title" id="${hid}">${col.status.name}</h2><span class="col-count" aria-label="${t('board.count', { n: issues.length })}">${issues.length}</span></header>
        <p class="col-hint">${t('board.noTransitionHint')}</p>
        <div class="col-list">${issues.length ? issues.map(cardHTML) : html`<p class="col-empty">${t('board.columnEmpty')}</p>`}</div>
      </section>`;
    });
    const banner = visible === 0
      ? html`<div class="filter-empty" role="status">${icon('search')}<span>${t('board.noMatches')}</span><button type="button" class="link-btn" data-action="clear-filters">${t('board.clearFilters')}</button></div>`
      : '';
    setHTML(root, html`${banner}<div class="columns">${cols}</div>`);
    const cols2 = $('.columns', root);
    cols2.scrollLeft = scrollX;
    $$('.col', root).forEach((c) => { const y = scrollY.get(c.dataset.status); if (y) $('.col-list', c).scrollTop = y; });
    if (focusKey) { const card = root.querySelector(`.card[data-key="${CSS.escape(focusKey)}"]`); if (card) card.focus({ preventScroll: true }); }
  }

  function getTransitions(key, fresh) {
    if (fresh) S.transCache.delete(key);
    let p = S.transCache.get(key);
    if (!p) {
      p = api('getTransitions', { key }).then((list) => (Array.isArray(list) ? list : []));
      p.catch(() => { if (S.transCache.get(key) === p) S.transCache.delete(key); });
      S.transCache.set(key, p);
    }
    return p;
  }

  /* --- drag & drop --- */
  function colStatus(colEl) {
    const sid = colEl.dataset.status;
    const col = S.board.columns.find((c) => String(c.status.id ?? c.status.name) === sid);
    return col ? col.status : null;
  }

  function onDragStart(e) {
    const card = e.target.closest && e.target.closest('.card');
    if (!card || !S.board) return;
    const key = card.dataset.key;
    const found = findIssue(key);
    if (!found || S.pendingKeys.has(key)) { e.preventDefault(); return; }
    S.drag = { key, from: found.col.status };
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', key);
    card.classList.add('is-dragging');
    const colsEl = $('#board .columns');
    colsEl.classList.add('is-dragging');
    const srcCol = card.closest('.col');
    if (srcCol) srcCol.classList.add('is-source');
    // Prefetch transitions to dim columns that cannot be reached.
    const drag = S.drag;
    getTransitions(key).then((list) => {
      if (S.drag !== drag) return;
      $$('#board .col').forEach((c) => {
        const st = colStatus(c);
        if (!st || sameStatus(st, drag.from)) return;
        const blocked = !list.some((tr) => sameStatus(tr.to, st));
        c.classList.toggle('is-blocked', blocked);
        if (blocked) c.classList.remove('is-over'); // DR-15: pointer may already be inside it
      });
    }, () => {});
  }

  function endDrag() {
    S.drag = null;
    $$('#board .is-dragging, #board .is-source, #board .is-over, #board .is-blocked').forEach((el) => el.classList.remove('is-dragging', 'is-source', 'is-over', 'is-blocked'));
    if (S.boardRenderDeferred) { S.boardRenderDeferred = false; renderBoard(); }
    afterBoardMutation();
  }

  function onDragOver(e) {
    if (!S.drag) return;
    const colEl = e.target.closest && e.target.closest('.col');
    if (!colEl || colEl.classList.contains('is-source')) { S.drag.blockedOver = null; return; }
    e.preventDefault();
    if (colEl.classList.contains('is-blocked')) {
      // Known to have no transition: refuse the drop; dragend explains why with a toast.
      e.dataTransfer.dropEffect = 'none';
      S.drag.blockedOver = colStatus(colEl);
      $$('#board .col.is-over').forEach((c) => c.classList.remove('is-over'));
      return;
    }
    S.drag.blockedOver = null;
    e.dataTransfer.dropEffect = 'move';
    if (!colEl.classList.contains('is-over')) {
      $$('#board .col.is-over').forEach((c) => c.classList.remove('is-over'));
      colEl.classList.add('is-over');
    }
  }
  function onDragLeave(e) {
    const colEl = e.target.closest && e.target.closest('.col');
    if (!colEl) return;
    // Chromium often reports relatedTarget = null for drag events, so test the pointer against the column box.
    const r = colEl.getBoundingClientRect();
    const inside = e.clientX > r.left && e.clientX < r.right && e.clientY > r.top && e.clientY < r.bottom;
    if (!inside) colEl.classList.remove('is-over');
  }
  /** Drag ended without a drop. If it was released over a column known to be unreachable, say so. */
  function onDragEnd() {
    if (!S.drag) return;
    const { key, from, blockedOver } = S.drag;
    endDrag();
    if (blockedOver) toast(t('board.noTransition', { key, from: nameOf(from), to: blockedOver.name }), { tone: 'warn' });
  }

  /** CR-1: anything dragged in from outside (files, links, text) must never navigate the WebView. */
  function guardForeignDrag(e) {
    if (S.drag) {
      if (e.type === 'drop') e.preventDefault(); // internal card dropped outside a column: no-op
      else if (!(e.target.closest && e.target.closest('#board .col'))) S.drag.blockedOver = null;
      return;
    }
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'none';
  }
  /** Only board cards may be dragged; blocks links, images and selected text from being dragged out as URLs. */
  function guardDragStart(e) {
    const el = e.target && e.target.nodeType === 1 ? e.target : e.target && e.target.parentElement;
    if (!el || !el.closest('#board .card')) e.preventDefault();
  }

  function onDrop(e) {
    if (!S.drag) return;
    const colEl = e.target.closest && e.target.closest('.col');
    if (!colEl) return;
    e.preventDefault();
    const key = S.drag.key;
    const to = colStatus(colEl);
    endDrag();
    if (to) moveByDrop(key, to);
  }

  async function moveByDrop(key, toStatus) {
    const found = findIssue(key);
    if (!found || sameStatus(found.col.status, toStatus) || S.pendingKeys.has(key)) return;
    const issue = found.issue;
    S.pendingKeys.add(key);
    renderBoard();
    let list;
    try {
      list = await getTransitions(key);
    } catch (e) {
      S.pendingKeys.delete(key);
      renderBoard();
      toastError(t('board.transitionsFailed', { key }), e);
      afterBoardMutation();
      return;
    }
    const matches = list.filter((tr) => sameStatus(tr.to, toStatus));
    let tr = matches[0];
    if (matches.length > 1) {
      S.pendingKeys.delete(key);
      renderBoard();
      tr = await pickTransition(matches, issue, toStatus.name);
      if (!tr) { afterBoardMutation(); return; }
    }
    if (!tr) {
      S.pendingKeys.delete(key);
      renderBoard();
      toast(t('board.noTransition', { key, from: nameOf(issue.status), to: toStatus.name }), { tone: 'warn' });
      afterBoardMutation();
      return;
    }
    await runTransition(key, tr, toStatus);
  }

  /** Optimistically moves the card, runs the transition, reverts on error. */
  async function runTransition(key, tr, targetStatus) {
    const found = findIssue(key);
    if (!found) return;
    const original = found.issue;
    const origIdx = found.idx;
    const optimistic = targetStatus || (findColumn(tr.to) || {}).status || tr.to;
    S.pendingKeys.add(key);
    if (S.drawer && S.drawer.key === key) S.drawer.busyId = tr.id;
    placeIssue(key, original, optimistic, 0);
    renderBoard();
    if (S.drawer) renderDrawer();
    try {
      const fresh = await api('transition', { key, transitionId: tr.id });
      S.transCache.delete(key);
      // CR-18: the backend may fall back to a minimal Issue (key/url/project/status, other fields zero-valued);
      // keep the card's known fields in that case instead of wiping summary/type/priority/assignee/updated.
      const f = fresh && typeof fresh === 'object' ? fresh : {};
      const patch = f.summary ? f : { url: f.url || original.url, project: f.project || original.project };
      const freshIssue = Object.assign({}, original, patch, { status: f.status || optimistic });
      const status = freshIssue.status || optimistic;
      placeIssue(key, freshIssue, (findColumn(status) || {}).status || status, 0);
      toast(t('board.moved', { key, status: status.name }), { tone: 'success' });
      if (S.drawer && S.drawer.key === key) { S.drawer.issue = freshIssue; S.drawer.transitions = null; }
      return true;
    } catch (e) {
      placeIssue(key, original, original.status, origIdx);
      toastError(t('board.transitionFailed', { key }), e);
      return false;
    } finally {
      S.pendingKeys.delete(key);
      if (S.drawer && S.drawer.key === key) S.drawer.busyId = null;
      renderBoard();
      if (S.drawer && S.drawer.key === key) {
        if (!S.drawer.transitions) loadDrawerTransitions(); else renderDrawer();
      }
      afterBoardMutation();
    }
  }

  /* --- drawer --- */
  function openDrawer(key) {
    const found = findIssue(key);
    if (!found) return;
    S.drawer = { key, issue: found.issue, transitions: null, loading: true, error: null, busyId: null };
    renderDrawer();
    layers.open($('#drawer'), {
      focus: '#drawer-title',
      onClose: () => { S.drawer = null; },
      restoreFocus: () => document.querySelector(`#board .card[data-key="${CSS.escape(key)}"]`),
    });
    loadDrawerTransitions();
  }

  async function loadDrawerTransitions() {
    const d = S.drawer;
    if (!d) return;
    d.loading = true; d.error = null;
    renderDrawer();
    try {
      const list = await getTransitions(d.key, true);
      if (S.drawer !== d) return;
      d.transitions = list;
    } catch (e) {
      if (S.drawer !== d) return;
      d.error = e;
    }
    d.loading = false;
    renderDrawer();
  }

  function renderDrawer() {
    const d = S.drawer;
    const el = $('#drawer');
    if (!d) return;
    const live = findIssue(d.key);
    if (live) d.issue = live.issue;
    const is = d.issue;
    const focusedTr = el.contains(document.activeElement) ? document.activeElement.dataset.tr : null;
    const hadFocusInside = el.contains(document.activeElement);
    const busy = d.busyId != null || S.pendingKeys.has(d.key);

    let transitions;
    if (d.loading && !d.transitions) {
      transitions = html`<div class="tr-list" role="status" aria-label="${t('common.loading')}">${[0, 1, 2].map(() => html`<div class="skeleton sk-tr"></div>`)}</div>`;
    } else if (d.error) {
      transitions = html`<div class="inline-error">${icon('alert')}<span>${errorText(d.error)}</span><button type="button" class="btn btn-sm" data-action="tr-retry">${t('common.retry')}</button></div>`;
    } else if (!d.transitions || !d.transitions.length) {
      transitions = html`<p class="empty-line">${t('drawer.noTransitions')}</p>`;
    } else {
      transitions = html`<ul class="tr-list" role="list">${d.transitions.map((tr) => html`<li>
        <button type="button" class="tr-btn" data-tr="${tr.id}" ${busy ? html`disabled` : ''}>
          <span class="tr-name">${tr.name}</span>
          <span class="tr-to">${icon('arrow-right')}${statusPill(tr.to)}</span>
          ${d.busyId === tr.id ? html`<span class="spinner spinner-sm" aria-hidden="true"></span>` : ''}
        </button></li>`)}</ul>`;
    }

    setHTML(el, html`<div class="drawer-panel">
      <header class="drawer-head">
        <div class="drawer-crumbs">${typeBadge(is.type)}<span class="card-key">${is.key}</span></div>
        <button type="button" class="icon-btn" data-action="close" aria-label="${t('common.close')}" title="${t('common.closeEsc')}">${icon('x')}</button>
      </header>
      <div class="drawer-body">
        <h2 class="drawer-title" id="drawer-title" tabindex="-1">${is.summary}</h2>
        <dl class="props">
          <dt>${t('issue.status')}</dt><dd>${statusPill(is.status)}${S.pendingKeys.has(is.key) ? html` <span class="spinner spinner-sm" aria-hidden="true"></span>` : ''}</dd>
          <dt>${t('issue.type')}</dt><dd>${nameOf(is.type) || '—'}</dd>
          <dt>${t('issue.priority')}</dt><dd>${nameOf(is.priority) || '—'}</dd>
          <dt>${t('issue.project')}</dt><dd>${projectOf(is) || '—'}</dd>
          <dt>${t('issue.assignee')}</dt><dd>${(is.assignee && is.assignee.displayName) || t('issue.unassigned')}</dd>
          <dt>${t('issue.updated')}</dt><dd>${is.updated ? html`${i18n.dateTime(is.updated)} · ${relSpan(is.updated)}` : '—'}</dd>
        </dl>
        <section class="drawer-section" aria-labelledby="drawer-tr-h" aria-busy="${d.loading ? 'true' : 'false'}">
          <h3 id="drawer-tr-h">${t('drawer.transitions')}</h3>
          ${transitions}
        </section>
      </div>
      <footer class="drawer-foot">
        <button type="button" class="btn btn-primary" data-action="open-jira" ${is.url ? '' : html`disabled`}>${icon('external')}<span>${t('common.openInJira')}</span></button>
      </footer>
    </div>`);

    if (hadFocusInside) {
      const target = (focusedTr && el.querySelector(`[data-tr="${CSS.escape(focusedTr)}"]:not(:disabled)`)) || $('#drawer-title');
      if (target) target.focus({ preventScroll: true });
    }
  }

  function onDrawerClick(e) {
    const d = S.drawer;
    if (!d) return;
    const a = e.target.closest('[data-action], [data-tr]');
    if (!a) return;
    if (a.dataset.tr != null) {
      const tr = (d.transitions || []).find((x) => String(x.id) === a.dataset.tr);
      if (tr && d.busyId == null && !S.pendingKeys.has(d.key)) runTransition(d.key, tr, null);
      return;
    }
    switch (a.dataset.action) {
      case 'close': layers.close($('#drawer')); break;
      case 'tr-retry': loadDrawerTransitions(); break;
      case 'open-jira': openUrl(d.issue.url); break;
    }
  }

  async function openUrl(url) {
    if (!url) return;
    try { await api('openUrl', { url }); } catch (e) { toastError(t('common.openFailed'), e); }
  }

  /* ================================================================
   * WORKLOG
   * ================================================================ */
  async function ensureSettings() {
    if (S.settings) return S.settings;
    try { S.settings = normalizeSettings(await api('getSettings')); } catch { /* cap/workday facts fall back to "—" */ }
    return S.settings;
  }

  function enterWorklog() {
    renderWorklog();
    ensureSettings().then(() => { if (S.view === 'worklog') renderWorklog(); });
    if (!S.wl.running) S.wl.result = null; // re-entering the view shows a fresh preview
    if (!S.wl.previewLoading && !S.wl.running) loadPreview();
    if (!S.wl.historyLoading) loadHistory();
  }

  async function loadPreview() {
    const seq = ++S.wl.previewSeq;
    S.wl.previewLoading = true;
    renderWorklog();
    renderRefreshState();
    try {
      const run = await api('worklogPreview');
      if (seq !== S.wl.previewSeq) return;
      S.wl.preview = run;
      S.wl.previewFetchedAt = Date.now();
      S.wl.previewError = null;
    } catch (e) {
      if (seq !== S.wl.previewSeq) return;
      S.wl.previewError = e;
    } finally {
      if (seq === S.wl.previewSeq) { S.wl.previewLoading = false; renderWorklog(); renderRefreshState(); }
    }
  }

  async function loadHistory() {
    S.wl.historyLoading = true;
    renderWorklogHistory();
    renderRefreshState();
    try {
      const list = await api('getWorklogHistory');
      S.wl.history = Array.isArray(list) ? list : [];
      S.wl.historyError = null;
    } catch (e) {
      S.wl.historyError = e;
    } finally {
      S.wl.historyLoading = false;
      renderWorklogHistory();
      renderRefreshState();
    }
  }

  const isToday = (v) => { const d = parseDate(v); return !!d && d.toDateString() === new Date().toDateString(); };
  const sumOf = (items, field) => (items || []).reduce((s, it) => s + (Number(it[field]) || 0), 0);
  const capSeconds = () => Math.round(((S.settings && Number(S.settings.dailyCapHours)) || 8) * 3600);

  function renderWorklog() {
    if (S.view !== 'worklog' || !isConfigured()) return;
    renderWorklogToday();
    renderWorklogPreview();
    renderWorklogHistory();
  }

  function reasonText(reason) {
    return i18n.has('worklog.reason.' + reason) ? t('worklog.reason.' + reason) : reason;
  }

  function renderWorklogToday() {
    const el = $('#wl-today');
    const st = S.appState || {};
    const s = S.settings;
    const p = S.wl.preview;
    let chip;
    if (!p && S.wl.previewLoading) chip = html`<span class="skeleton sk-line" style="width:120px;height:22px"></span>`;
    else if (!p) chip = html`<span class="chip">${t('worklog.dayUnknown')}</span>`;
    else if (p.skippedReason) chip = html`<span class="chip chip-warn">${icon('info')}${reasonText(p.skippedReason)}</span>`;
    else chip = html`<span class="chip chip-good">${icon('check')}${t('worklog.workingDay')}</span>`;

    const next = st.nextWorklogAt
      ? html`${i18n.dayTime(st.nextWorklogAt)}${isToday(st.nextWorklogAt) ? html`<span class="sub">${relText(st.nextWorklogAt)}</span>` : ''}`
      : html`${t('worklog.notScheduled')}`;
    const hours = s
      ? html`${s.workdayStart}–${s.workdayEnd}<span class="sub">${s.lunchStart && s.lunchEnd ? t('worklog.lunchRange', { from: s.lunchStart, to: s.lunchEnd }) : t('worklog.noLunch')}</span>`
      : '—';
    const last = st.lastWorklog;
    const lastHtml = last
      ? html`${i18n.dayTime(last.at)}<span class="sub">${last.skippedReason ? reasonText(last.skippedReason) : dur(last.totalSeconds || sumOf(last.items, 'todoSeconds'))}</span>`
      : html`${t('worklog.never')}`;

    setHTML(el, html`
      <div class="today-main">
        <span class="eyebrow">${t('worklog.today')}</span>
        <span class="today-date">${i18n.weekdayDate(new Date(), 'long')}</span>
        <div>${chip}</div>
      </div>
      <dl class="facts">
        <div><dt>${t('worklog.nextRun')}</dt><dd>${next}</dd></div>
        <div><dt>${t('worklog.workHours')}</dt><dd>${hours}</dd></div>
        <div><dt>${t('worklog.dailyCap')}</dt><dd>${s ? dur(capSeconds()) : '—'}</dd></div>
        <div><dt>${t('worklog.lastRun')}</dt><dd>${lastHtml}</dd></div>
      </dl>`);
  }

  function resultChip(item) {
    switch (item.result) {
      case 'logged': return html`<span class="chip chip-good">${icon('check')}${t('worklog.result.logged')}</span>`;
      case 'error': return html`<span class="chip chip-bad" title="${item.error || ''}">${icon('alert')}${t('worklog.result.error')}</span>`;
      case 'skipped': return html`<span class="chip">${t('worklog.result.skipped')}</span>`;
      default: return '';
    }
  }

  function itemsTable(items, { showResult, compact } = {}) {
    const logged = sumOf(items, 'loggedSeconds');
    const todo = sumOf(items, 'todoSeconds');
    const share = sumOf(items, 'shareSeconds');
    const cols = (showResult ? 7 : 6) - (compact ? 2 : 0);
    return html`<div class="table-wrap"><table class="table">
      <thead><tr>
        <th scope="col">${t('worklog.col.issue')}</th>
        ${compact ? '' : html`<th scope="col">${t('worklog.col.status')}</th>`}
        ${compact ? '' : html`<th scope="col" class="num">${t('worklog.col.share')}</th>`}
        <th scope="col" class="num">${t('worklog.col.logged')}</th>
        <th scope="col" class="num">${t(compact ? 'worklog.col.logTotal' : showResult ? 'worklog.col.attempted' : 'worklog.col.todo')}</th>
        <th scope="col">${t('worklog.col.start')}</th>
        ${showResult ? html`<th scope="col">${t('worklog.col.result')}</th>` : ''}
      </tr></thead>
      <tbody>${items.map((it) => html`<tr>
          <td><span class="cell-issue"><span class="card-key">${it.key}</span><span class="sum" title="${it.summary || ''}">${it.summary || ''}</span></span></td>
          ${compact ? '' : html`<td class="nowrap">${nameOf(it.status)}</td>`}
          ${compact ? '' : html`<td class="num">${dur(it.shareSeconds)}</td>`}
          <td class="num muted">${it.loggedSeconds ? dur(it.loggedSeconds) : '—'}</td>
          <td class="num strong">${it.todoSeconds ? dur(it.todoSeconds) : '—'}</td>
          <td class="muted">${it.started ? i18n.time(it.started) : '—'}</td>
          ${showResult ? html`<td>${resultChip(it)}</td>` : ''}
        </tr>${it.error ? html`<tr class="row-error"><td colspan="${cols}">${it.error}</td></tr>` : ''}`)}</tbody>
      ${compact ? '' : html`<tfoot><tr>
        <td>${t('worklog.total')}</td><td></td>
        <td class="num">${dur(share)}</td>
        <td class="num">${logged ? dur(logged) : '—'}</td>
        <td class="num">${dur(todo)}</td>
        <td></td>${showResult ? html`<td></td>` : ''}
      </tr></tfoot>`}
    </table></div>`;
  }

  /** Total vs cap. For a finished run, successfully logged time counts as "already logged". */
  /** Time I already logged today on issues that are not rows of the run (CR-22; missing on old runs = 0). */
  const otherLogged = (run) => Math.max(0, Number(run && run.otherLoggedSeconds) || 0);
  function otherNote(run) {
    const other = otherLogged(run);
    return other ? html`<p class="wl-note">${icon('info')}<span>${t('worklog.otherLoggedNote', { v: dur(other) })}</span></p>` : '';
  }

  /** Daily cap covers all my worklogs: [other issues | rows already logged | will log | free]. */
  function capBar(run, items, isResult) {
    const cap = capSeconds();
    const other = otherLogged(run);
    const done = isResult ? items.filter((i) => i.result === 'logged').reduce((s, i) => s + (Number(i.todoSeconds) || 0), 0) : 0;
    const logged = sumOf(items, 'loggedSeconds') + done;
    const todo = isResult ? 0 : sumOf(items, 'todoSeconds');
    const total = other + logged + todo;
    const free = Math.max(0, cap - total);
    const pct = (v) => Math.max(0, Math.min(100, (v / cap) * 100)).toFixed(2) + '%';
    const left = (used) => Math.max(0, cap - used);
    const label = t('worklog.capAria', { other: dur(other), logged: dur(logged), todo: dur(todo), cap: dur(cap), free: dur(free) });
    return html`<div class="capbar">
      <div class="capbar-track" role="img" aria-label="${label}">${other ? html`<span class="capbar-other" style="--w:${pct(other)}"></span>` : ''}<span class="capbar-logged" style="--w:${pct(Math.min(logged, left(other)))}"></span><span class="capbar-todo" style="--w:${pct(Math.min(todo, left(other + logged)))}"></span></div>
      <div class="capbar-legend" aria-hidden="true">
        ${other ? html`<span class="lg lg-other">${t('worklog.legendOther', { v: dur(other) })}</span>` : ''}
        <span class="lg lg-logged">${t('worklog.legendLogged', { v: dur(logged) })}</span>
        ${isResult ? '' : html`<span class="lg lg-todo">${t('worklog.legendTodo', { v: dur(todo) })}</span>`}
        <span class="lg lg-cap">${t(free ? 'worklog.legendCapFree' : 'worklog.legendCap', { total: dur(total), cap: dur(cap), free: dur(free) })}</span>
      </div>
    </div>`;
  }

  function renderWorklogPreview() {
    const el = $('#wl-preview');
    const { preview, previewLoading, previewError, result, running } = S.wl;
    const run = result || preview;
    const items = (run && Array.isArray(run.items)) ? run.items : [];
    const todo = sumOf(items, 'todoSeconds');

    $('#wl-preview-title').textContent = result ? t('worklog.resultTitle') : t('worklog.previewTitle');
    $('#wl-preview-sub').textContent = run && run.at
      ? (result ? t('worklog.ranAt', { time: i18n.time(run.at) }) : t('worklog.calculatedAt', { time: i18n.time(run.at) }))
      : '';
    setBusy($('#wl-preview-btn'), previewLoading);
    $('#wl-preview-btn').disabled = previewLoading || running;
    setBusy($('#wl-run-btn'), running || S.wl.refreshingForRun);
    $('#wl-run-btn').disabled = running || S.wl.refreshingForRun || previewLoading || !!result || !preview || !!preview.skippedReason || !!preview.error || todo <= 0;

    if (!run && previewLoading) {
      setHTML(el, html`<div role="status" aria-label="${t('common.loading')}" style="display:flex;flex-direction:column;gap:8px">${[0, 1, 2, 3].map(() => html`<div class="skeleton" style="height:36px"></div>`)}</div>`);
      return;
    }
    if (!run && previewError) {
      setHTML(el, stateBlock({ iconName: 'alert', tone: 'error', title: t('worklog.previewError'), text: errorText(previewError), action: { id: 'wl-retry', label: t('common.retry') } }));
      return;
    }
    if (!run) { el.textContent = ''; return; }
    if (run.skippedReason) {
      setHTML(el, stateBlock({ iconName: 'calendar', tone: 'warn', title: reasonText(run.skippedReason), text: t('worklog.skippedText.' + run.skippedReason) }));
      return;
    }
    const errBanner = run.error ? html`<div class="inline-error" style="margin-bottom:12px">${icon('alert')}<span>${run.error}</span></div>` : '';
    if (!items.length) {
      setHTML(el, html`${errBanner}${otherNote(run)}${stateBlock({ iconName: 'inbox', title: t('worklog.nothingTitle'), text: t('worklog.nothingText') })}`);
      return;
    }
    const allDone = !result && todo <= 0
      ? html`<div class="inline-error inline-success">${icon('check-circle')}<span>${t('worklog.allLogged')}</span></div>`
      : '';
    setHTML(el, html`${errBanner}${allDone}${otherNote(run)}${itemsTable(items, { showResult: !!result })}${capBar(run, items, !!result)}`);
  }

  function runSummary(run) {
    const items = run.items || [];
    if (run.skippedReason) return reasonText(run.skippedReason);
    if (run.error) return run.error;
    const n = items.filter((i) => (i.result ? i.result === 'logged' : i.todoSeconds > 0)).length;
    return t('worklog.issuesCount', { n });
  }

  function renderWorklogHistory() {
    if (S.view !== 'worklog') return;
    const el = $('#wl-history');
    const { history, historyLoading, historyError } = S.wl;
    if (!history && historyLoading) {
      setHTML(el, html`<div role="status" aria-label="${t('common.loading')}" style="display:flex;flex-direction:column;gap:8px">${[0, 1, 2].map(() => html`<div class="skeleton" style="height:32px"></div>`)}</div>`);
      return;
    }
    if (!history && historyError) {
      setHTML(el, html`<div class="inline-error">${icon('alert')}<span>${errorText(historyError)}</span><button type="button" class="btn btn-sm" data-action="history-retry">${t('common.retry')}</button></div>`);
      return;
    }
    if (!history || !history.length) {
      setHTML(el, stateBlock({ iconName: 'history', title: t('worklog.historyEmpty'), text: t('worklog.historyEmptyText') }));
      return;
    }
    const open = new Set($$('details[open]', el).map((d) => d.dataset.at));
    setHTML(el, html`<ul class="history" role="list">${history.map((run) => {
      const items = run.items || [];
      const failed = items.some((i) => i.result === 'error') || run.error;
      const statusChip = run.skippedReason ? html`<span class="chip">${t('worklog.skipped')}</span>`
        : failed ? html`<span class="chip chip-bad">${icon('alert')}${t('worklog.hasErrors')}</span>`
        : html`<span class="chip chip-good">${icon('check')}${t('worklog.done')}</span>`;
      const total = run.skippedReason ? '—' : dur(run.totalSeconds != null ? run.totalSeconds : sumOf(items, 'todoSeconds'));
      const dateStr = i18n.weekdayDate(run.date ? run.date + 'T12:00:00' : run.at, 'short');
      return html`<li><details class="run" data-at="${run.at || ''}" ${open.has(run.at) ? html`open` : ''}>
        <summary>
          ${icon('chevron-down', 'chev')}
          <span class="run-date">${dateStr}</span>
          <span class="run-trigger"><span class="chip ${run.trigger === 'manual' ? 'chip-accent' : ''}">${t('worklog.trigger.' + (run.trigger || 'schedule'))}</span></span>
          <span class="run-meta">${i18n.time(run.at)} · ${runSummary(run)}</span>
          <span class="run-total">${total}</span>
          <span class="run-status">${statusChip}</span>
        </summary>
        <div class="run-body">${run.skippedReason ? '' : otherNote(run)}${run.skippedReason
          ? html`<p class="empty-line">${t('worklog.skippedText.' + run.skippedReason)}</p>`
          : items.length ? itemsTable(items, { showResult: true, compact: true }) : html`<p class="empty-line">${run.error || t('worklog.nothingTitle')}</p>`}</div>
      </details></li>`;
    })}</ul>`);
  }

  const PREVIEW_MAX_AGE_MS = 2 * 60000;

  async function logNow() {
    if (S.wl.running || S.wl.refreshingForRun || !S.wl.preview) return;
    // CR-9: never confirm totals from a stale preview.
    if (!S.wl.previewFetchedAt || Date.now() - S.wl.previewFetchedAt > PREVIEW_MAX_AGE_MS) {
      S.wl.refreshingForRun = true;
      renderWorklogPreview();
      try { await loadPreview(); } finally { S.wl.refreshingForRun = false; renderWorklogPreview(); }
      if (S.wl.previewError) { toastError(t('worklog.previewError'), S.wl.previewError); return; }
    }
    const p = S.wl.preview;
    if (!p || p.skippedReason || p.error || S.wl.result) return;
    const items = (p.items || []).filter((i) => i.todoSeconds > 0);
    const total = sumOf(items, 'todoSeconds');
    if (!total) return; // the refreshed preview now shows "all logged"
    const calcTime = i18n.time(p.at) || i18n.time(new Date(S.wl.previewFetchedAt));
    const ok = await confirmDialog({
      title: t('worklog.confirmTitle'),
      body: t('worklog.confirmBody', { total: dur(total), n: items.length, date: i18n.date(new Date(), { day: 'numeric', month: 'long' }) }) +
        ' ' + t('worklog.confirmCalc', { time: calcTime }),
      okLabel: t('worklog.logNow'),
    });
    if (!ok || S.wl.running) return;
    S.wl.running = true;
    renderWorklogPreview();
    try {
      const res = await api('worklogRun');
      S.wl.result = res; // preview is kept for the "working day" chip; the result view replaces the table
      const ritems = (res && res.items) || [];
      const errors = ritems.filter((i) => i.result === 'error').length;
      const loggedSec = ritems.filter((i) => i.result === 'logged').reduce((s, i) => s + (i.todoSeconds || 0), 0);
      if (res && res.error) toast(t('worklog.runFailed') + ': ' + res.error, { tone: 'error' });
      else if (errors) toast(t('worklog.runPartial', { total: dur(loggedSec), n: errors }), { tone: 'warn' });
      else toast(t('worklog.runDone', { total: dur(loggedSec || res.totalSeconds || 0) }), { tone: 'success' });
      loadHistory();
      loadState();
    } catch (e) {
      // CR-7: the Go side may still be working (e.g. waiting for a scheduled run); its `worklog` event fills history.
      if (e.code === 'timeout') toast(t('worklog.runStillRunning'), { timeout: 15000 });
      else toastError(t('worklog.runFailed'), e);
    } finally {
      S.wl.running = false;
      renderWorklog();
      // CR-8: refresh the preview after the run (keeps the result view, updates the Today card).
      loadPreview();
    }
  }

  /* ================================================================
   * NOTIFICATIONS
   * ================================================================ */
  async function loadNotifications() {
    S.notif.loading = true;
    renderNotifications();
    renderRefreshState();
    try {
      const list = await api('getNotifications');
      S.notif.list = Array.isArray(list) ? list : [];
      S.notif.error = null;
    } catch (e) {
      S.notif.error = e;
      if (S.notif.list) toastError(t('notif.loadFailed'), e);
    } finally {
      S.notif.loading = false;
      renderNotifications();
      renderRefreshState();
    }
  }

  function renderNotifications() {
    if (S.view !== 'notifications' || !isConfigured()) return;
    const el = $('#notif-list');
    const { list, loading, error } = S.notif;
    const unread = list ? list.filter((n) => !n.read).length : ((S.appState && S.appState.unread) || 0);
    $('#notif-summary').textContent = list ? (unread ? t('notif.unreadCount', { n: unread }) : t('notif.allRead')) : '';
    const markBtn = $('#notif-mark-all');
    setBusy(markBtn, S.notif.markingAll);
    markBtn.disabled = S.notif.markingAll || !list || unread === 0;

    if (!list) {
      if (error && !loading) setHTML(el, stateBlock({ iconName: 'alert', tone: 'error', title: t('notif.loadFailed'), text: errorText(error), action: { id: 'notif-retry', label: t('common.retry') } }));
      else setHTML(el, html`<div class="notif-list" role="status" aria-label="${t('common.loading')}">${[0, 1, 2, 3].map(() => html`<div class="skeleton" style="height:84px;border-radius:8px"></div>`)}</div>`);
      return;
    }
    if (!list.length) {
      const enabled = !S.settings || S.settings.notifyComments !== false;
      setHTML(el, stateBlock({ iconName: 'bell', title: t('notif.emptyTitle'), text: enabled ? t('notif.emptyText') : t('notif.disabledText') }));
      return;
    }
    const focusId = document.activeElement && el.contains(document.activeElement) ? document.activeElement.dataset.id : null;
    setHTML(el, html`<ul class="notif-list" role="list">${list.map((n) => html`<li>
      <button type="button" class="notif${n.read ? '' : ' is-unread'}" data-id="${n.id}" aria-label="${t(n.read ? 'notif.itemAria' : 'notif.itemAriaUnread', { author: n.author, key: n.issueKey })}" aria-describedby="nb-${n.id}">
        <span class="avatar avatar-sm" aria-hidden="true">${initials(n.author)}</span>
        <span class="notif-main">
          <span class="notif-line"><span class="notif-author">${n.author}</span>${relSpan(n.created, '', 'notif-time')}</span>
          <span class="notif-issue"><span class="card-key">${n.issueKey}</span><span class="sum">${n.issueSummary || ''}</span></span>
          <span class="notif-body" id="nb-${n.id}">${n.body || ''}</span>
        </span>
      </button></li>`)}</ul>`);
    if (focusId) { const b = el.querySelector(`[data-id="${CSS.escape(focusId)}"]`); if (b) b.focus({ preventScroll: true }); }
  }

  function setUnread(n) {
    if (!S.appState) return;
    S.appState.unread = Math.max(0, Number(n) || 0);
    renderBadge();
  }

  async function markRead(ids) {
    const list = S.notif.list || [];
    const targets = ids ? list.filter((n) => ids.includes(n.id) && !n.read) : list.filter((n) => !n.read);
    if (ids && !targets.length) return;
    targets.forEach((n) => { n.read = true; });
    renderNotifications();
    try {
      const res = await api('markNotificationsRead', ids ? { ids } : {});
      if (res && typeof res.unread === 'number') setUnread(res.unread);
    } catch (e) {
      targets.forEach((n) => { n.read = false; });
      toastError(t('notif.markFailed'), e);
    }
    renderNotifications();
  }

  async function markAllRead() {
    if (S.notif.markingAll) return;
    S.notif.markingAll = true;
    renderNotifications();
    try { await markRead(null); } finally { S.notif.markingAll = false; renderNotifications(); }
  }

  function openNotification(n) {
    openUrl(n.url);
    if (!n.read) markRead([n.id]);
  }

  let stateRefreshTimer = null;
  function onNotificationEvent(n) {
    if (!n || n.id == null) return;
    if (S.notif.list && !S.notif.list.some((x) => x.id === n.id)) S.notif.list.unshift(n);
    if (!n.read && S.appState) setUnread((S.appState.unread || 0) + 1);
    // Re-sync the authoritative unread counter shortly after (backend may also send a `state` event).
    clearTimeout(stateRefreshTimer);
    stateRefreshTimer = setTimeout(loadState, 800);
    renderNotifications();
    toast(t('notif.toast', { author: n.author, key: n.issueKey }), { action: { label: t('common.open'), run: () => openNotification(n) } });
  }

  /* ================================================================
   * SETTINGS
   * ================================================================ */
  const FORM_SCALARS = ['jiraUrl', 'language', 'worklogTime', 'workdayStart', 'workdayEnd', 'lunchStart', 'lunchEnd', 'boardJql'];
  const FORM_BOOLS = ['autostart', 'worklogEnabled', 'notifyComments'];
  const FORM_NUMS = ['dailyCapHours', 'hoursPerDay', 'commentPollMinutes'];
  const form = () => $('#settings-form');

  async function enterSettings(forceReload) {
    if (S.form && formDirty() && !forceReload) { renderSettingsDynamic(); return; }
    S.formLoading = true;
    S.formError = null;
    renderSettingsShell();
    renderRefreshState();
    try {
      S.settings = normalizeSettings(await api('getSettings'));
      fillSettingsForm(S.settings);
    } catch (e) {
      S.formError = e;
    } finally {
      S.formLoading = false;
      renderSettingsShell();
      renderRefreshState();
    }
  }

  function renderSettingsShell() {
    const loading = S.formLoading && !S.form;
    const failed = !S.form && S.formError;
    $('#settings-loading').hidden = !loading && !failed;
    $('#settings-body').hidden = loading || !!failed;
    $('#savebar').hidden = loading || !!failed;
    updateToastOffset();
    if (loading) setHTML($('#settings-loading'), html`<div role="status" aria-label="${t('common.loading')}" class="settings-loading">${[0, 1, 2, 3, 4, 5].map(() => html`<div class="skeleton"></div>`)}</div>`);
    else if (failed) setHTML($('#settings-loading'), stateBlock({ iconName: 'alert', tone: 'error', title: t('settings.loadFailed'), text: errorText(S.formError), action: { id: 'settings-retry', label: t('common.retry') } }));
    if (S.form) renderSettingsDynamic();
  }

  function fillSettingsForm(s) {
    const f = form();
    FORM_SCALARS.forEach((k) => { f.elements[k].value = s[k] == null ? '' : String(s[k]); });
    FORM_BOOLS.forEach((k) => { f.elements[k].checked = !!s[k]; });
    FORM_NUMS.forEach((k) => { f.elements[k].value = s[k] == null ? '' : String(s[k]); });
    if (S.langOverride) f.elements.language.value = S.langOverride;
    f.elements.token.value = '';
    S.form = {
      workDays: new Set((s.workDays || []).map(Number)),
      // from/to hold what the user sees/types (locale format); collectSettings() converts to ISO.
      vacations: (s.vacations || []).map((v) => ({ from: i18n.formatInputDate(v.from), to: i18n.formatInputDate(v.to), note: v.note || '' })),
      holidays: (s.holidays || []).slice().sort(),
      inProgressStatuses: (s.inProgressStatuses || []).slice(),
    };
    clearFieldErrors($('#settings-form'));
    $('#settings-alert').hidden = true;
    setTestResult($('#s-test-result'), null);
    renderSettingsDynamic();
  }

  function collectSettings() {
    const f = form();
    const out = Object.assign({}, S.settings || {});
    delete out.hasToken;
    delete out.token;
    FORM_SCALARS.forEach((k) => { out[k] = f.elements[k].value.trim(); });
    TIME_FIELDS.forEach((k) => { out[k] = normalizeTime(out[k]) ?? out[k]; });
    FORM_BOOLS.forEach((k) => { out[k] = f.elements[k].checked; });
    FORM_NUMS.forEach((k) => { const v = f.elements[k].value.trim(); out[k] = v === '' ? NaN : Number(v); });
    out.workDays = [...S.form.workDays].sort((a, b) => a - b);
    const iso = (text) => i18n.parseInputDate(text) || text.trim();
    out.vacations = S.form.vacations.map((v) => ({ from: iso(v.from), to: iso(v.to), note: v.note.trim() }));
    out.holidays = S.form.holidays.slice();
    out.inProgressStatuses = S.form.inProgressStatuses.slice();
    const token = f.elements.token.value.trim();
    if (token) out.token = token;
    return out;
  }

  function comparable(s) {
    const c = Object.assign({}, s);
    delete c.token; delete c.hasToken;
    if (Array.isArray(c.workDays)) c.workDays = c.workDays.map(Number).sort((a, b) => a - b);
    if (Array.isArray(c.holidays)) c.holidays = c.holidays.slice().sort();
    if (Array.isArray(c.vacations)) c.vacations = c.vacations.map((v) => ({ from: v.from || '', to: v.to || '', note: (v.note || '').trim() }));
    return JSON.stringify(Object.keys(c).sort().map((k) => [k, c[k]]));
  }

  function formDirty() {
    if (!S.form || !S.settings) return false;
    if (form().elements.token.value.trim()) return true;
    return comparable(collectSettings()) !== comparable(S.settings);
  }

  function renderSavebar() {
    const dirty = formDirty();
    const text = $('#savebar-text');
    text.dataset.dirty = dirty ? 'true' : 'false';
    text.textContent = S.saving ? t('settings.saving') : dirty ? t('settings.unsaved') : t('settings.allSaved');
    setBusy($('#s-save'), S.saving);
    $('#s-save').disabled = S.saving || !dirty;
    $('#s-discard').disabled = S.saving || !dirty;
  }

  function renderSettingsDynamic() {
    if (!S.form) return;
    const s = S.settings || {};
    $('#s-token').placeholder = s.hasToken ? t('settings.tokenSaved') : t('settings.tokenPlaceholder');
    $('#w-autostart').hidden = !S.autostartMismatch;

    // Work days Mon..Sun (ISO 1..7), labels from Intl in the current UI language.
    const fmt = new Intl.DateTimeFormat(i18n.locale, { weekday: 'short' });
    const fmtLong = new Intl.DateTimeFormat(i18n.locale, { weekday: 'long' });
    const monday = new Date(2024, 0, 1); // a Monday
    const days = $('#s-workDays');
    const focusedDay = days.contains(document.activeElement) ? document.activeElement.dataset.day : null;
    setHTML(days, html`${[1, 2, 3, 4, 5, 6, 7].map((d) => {
      const date = new Date(monday); date.setDate(monday.getDate() + d - 1);
      return html`<button type="button" class="day-btn" data-day="${d}" aria-pressed="${S.form.workDays.has(d) ? 'true' : 'false'}" aria-label="${fmtLong.format(date)}">${fmt.format(date).replace('.', '')}</button>`;
    })}`);
    if (focusedDay) $(`[data-day="${focusedDay}"]`, days).focus();

    renderTagList($('#s-statuses'), S.form.inProgressStatuses, 'status', (v) => v);
    const today = i18n.isoDate(new Date());
    renderTagList($('#s-holidays'), S.form.holidays, 'holiday', (v) => i18n.date(v + 'T12:00:00', { day: 'numeric', month: 'short', year: 'numeric' }), (v) => v < today);
    renderVacations();
    renderSavebar();
  }

  function renderTagList(el, values, kind, label, isPast) {
    const focusIdx = el.contains(document.activeElement) ? Number(document.activeElement.dataset.i) : -1;
    setHTML(el, html`${values.map((v, i) => html`<li class="tag${isPast && isPast(v) ? ' is-past' : ''}"><span>${label(v)}</span><button type="button" class="icon-btn icon-btn-sm" data-action="remove-${kind}" data-i="${i}" aria-label="${t('common.removeItem', { name: label(v) })}">${icon('x')}</button></li>`)}`);
    if (focusIdx >= 0) {
      const btns = $$('button', el);
      const target = btns[Math.min(focusIdx, btns.length - 1)];
      if (target) target.focus(); else $(kind === 'status' ? '#s-status-input' : '#s-holiday-input').focus();
    }
  }

  function renderVacations() {
    const el = $('#s-vacations');
    const vacs = S.form.vacations;
    if (!vacs.length) { setHTML(el, html`<p class="empty-line">${t('settings.noVacations')}</p>`); return; }
    const active = document.activeElement;
    const focus = el.contains(active) ? { i: active.dataset.i, f: active.dataset.vac || active.dataset.action } : null;
    const ph = t('settings.datePlaceholder');
    setHTML(el, html`${vacs.map((v, i) => html`<div class="vac-row" role="listitem">
      <input class="input input-date" type="text" inputmode="numeric" maxlength="10" autocomplete="off" data-mask="date" value="${v.from}" placeholder="${ph}" data-vac="from" data-i="${i}" aria-label="${t('settings.vacationFrom', { n: i + 1 })}" aria-describedby="e-vacations">
      <span class="range-sep" aria-hidden="true">–</span>
      <input class="input input-date" type="text" inputmode="numeric" maxlength="10" autocomplete="off" data-mask="date" value="${v.to}" placeholder="${ph}" data-vac="to" data-i="${i}" aria-label="${t('settings.vacationTo', { n: i + 1 })}" aria-describedby="e-vacations">
      <input class="input" type="text" value="${v.note}" data-vac="note" data-i="${i}" placeholder="${t('settings.vacationNote')}" aria-label="${t('settings.vacationNoteN', { n: i + 1 })}">
      <button type="button" class="icon-btn" data-action="remove-vacation" data-i="${i}" aria-label="${t('settings.removeVacation', { n: i + 1 })}" title="${t('common.remove')}">${icon('trash')}</button>
    </div>`)}`);
    if (focus) {
      const target = el.querySelector(`[data-i="${focus.i}"][data-vac="${focus.f}"], [data-i="${focus.i}"][data-action="${focus.f}"]`);
      if (target) target.focus();
    }
  }

  /** Clears the inline error of the field the user is editing. */
  function clearFieldError(root, control) {
    if (!control.getAttribute || control.getAttribute('aria-invalid') !== 'true') return;
    control.removeAttribute('aria-invalid');
    const base = control.name || (control.dataset.vac ? 'vacations' : control.id === 's-holiday-input' ? 'holidays' : '');
    const holder = base && root.querySelector(`[data-error-for="${CSS.escape(base)}"]`);
    // Several vacation inputs share one message: keep it while any of them is still invalid.
    const othersInvalid = base === 'vacations' && root.querySelector('[data-vac][aria-invalid="true"]');
    if (holder && !othersInvalid) { holder.hidden = true; holder.textContent = ''; }
  }

  function clearFieldErrors(root) {
    $$('.field-error', root).forEach((p) => { p.hidden = true; p.textContent = ''; });
    $$('[aria-invalid]', root).forEach((i) => i.removeAttribute('aria-invalid'));
  }

  /** Shows per-field errors ({field, message}); returns messages that had no matching field. */
  function showFieldErrors(root, fields) {
    clearFieldErrors(root);
    const unmatched = [];
    let first = null;
    for (const fe of fields) {
      const field = String(fe.field || '');
      const base = field.replace(/[[.].*$/, '');
      const holder = root.querySelector(`[data-error-for="${CSS.escape(base)}"]`);
      if (!holder) { unmatched.push(fe.message || field); continue; }
      holder.hidden = false;
      holder.textContent = holder.textContent ? holder.textContent + ' ' + fe.message : fe.message;
      let control = root.querySelector(`[name="${CSS.escape(base)}"]`);
      const m = field.match(/^vacations\W+(\d+)\W*(\w+)?/);
      if (m) control = root.querySelector(`[data-vac="${m[2] || 'from'}"][data-i="${m[1]}"]`) || control;
      if (!control) control = holder.closest('.setting-control, .field') && holder.closest('.setting-control, .field').querySelector('input, select, textarea, button');
      if (control) { control.setAttribute('aria-invalid', 'true'); first = first || control; }
    }
    if (first) {
      const adv = first.closest('details');
      if (adv) adv.open = true;
      first.focus();
      first.scrollIntoView({ block: 'center', behavior: 'smooth' });
    }
    return unmatched;
  }

  function showFormAlert(el, message, list) {
    if (!message) { el.hidden = true; el.textContent = ''; return; }
    setHTML(el, html`${icon('alert')}<div><strong>${message}</strong>${list && list.length ? html`<ul>${list.map((m) => html`<li>${m}</li>`)}</ul>` : ''}</div>`);
    el.hidden = false;
  }

  /* --- time / date text inputs (DR-5): independent of the OS locale; the backend always gets HH:MM / ISO dates --- */
  const TIME_FIELDS = ['worklogTime', 'workdayStart', 'workdayEnd', 'lunchStart', 'lunchEnd'];
  const OPTIONAL_TIME = ['lunchStart', 'lunchEnd']; // empty lunch = no lunch break

  /** "9:5"→null, "930"→"09:30", "9:30"→"09:30", "1800"→"18:00"; null when not a valid time. */
  function normalizeTime(v) {
    const m = String(v || '').trim().match(/^(\d{1,2}):?(\d{2})$/);
    if (!m) return null;
    const h = Number(m[1]); const min = Number(m[2]);
    if (h > 23 || min > 59) return null;
    return String(h).padStart(2, '0') + ':' + m[2];
  }

  /** Inserts separators while typing digits (time "1800"→"18:00", date "24122026"→"24.12.2026").
      Leaves free-form input like "1.1.2026" alone; validation happens on blur/save. */
  function autoMask(el, e) {
    if (!e.inputType || !e.inputType.startsWith('insert')) return;
    const isTime = el.dataset.mask === 'time';
    const groups = isTime ? [2, 2] : [2, 2, 4];
    const sep = isTime ? ':' : i18n.dateSep;
    const v = el.value;
    const canonical = [];
    groups.slice(0, -1).reduce((pos, g) => { canonical.push(pos + g); return pos + g + 1; }, 0);
    const seps = [...v].map((c, i) => (/\d/.test(c) ? -1 : i)).filter((i) => i >= 0);
    if (!seps.every((i) => canonical.includes(i))) return;
    const digits = v.replace(/\D/g, '');
    let out = '';
    let k = 0;
    for (let gi = 0; gi < groups.length && k < digits.length; gi++) {
      if (gi) out += sep;
      out += digits.slice(k, k + groups[gi]);
      k += groups[gi];
    }
    if (out !== v) { el.value = out; el.setSelectionRange(out.length, out.length); }
  }

  /** On blur: normalise valid values, flag invalid ones inline. */
  function validateMaskedInput(root, el) {
    const raw = el.value.trim();
    let message = null;
    if (el.dataset.mask === 'time') {
      const norm = normalizeTime(raw);
      if (norm) el.value = norm;
      else if (raw || !OPTIONAL_TIME.includes(el.name)) message = t('settings.timeInvalid');
    } else {
      const isoDate = i18n.parseInputDate(raw);
      if (isoDate) el.value = i18n.formatInputDate(isoDate);
      else if (raw || el.dataset.vac) message = t('settings.dateInvalid');
      if (el.dataset.vac && S.form) S.form.vacations[Number(el.dataset.i)][el.dataset.vac] = el.value;
    }
    if (!message) return;
    const base = el.name || (el.dataset.vac ? 'vacations' : el.id === 's-holiday-input' ? 'holidays' : '');
    const holder = base && root.querySelector(`[data-error-for="${CSS.escape(base)}"]`);
    el.setAttribute('aria-invalid', 'true');
    if (holder) { holder.textContent = message; holder.hidden = false; }
  }

  /** Client checks only for what the JSON/Go types need (range and business rules live in the backend). */
  const INT_FIELDS = ['commentPollMinutes'];
  function localErrors() {
    const f = form();
    const errs = [];
    FORM_NUMS.forEach((k) => {
      const raw = f.elements[k].value.trim();
      const n = Number(raw);
      if (raw === '' || !Number.isFinite(n)) errs.push({ field: k, message: t('settings.numberRequired') });
      else if (INT_FIELDS.includes(k) && !Number.isInteger(n)) errs.push({ field: k, message: t('settings.integerRequired') });
    });
    TIME_FIELDS.forEach((k) => {
      const raw = f.elements[k].value.trim();
      if (raw ? !normalizeTime(raw) : !OPTIONAL_TIME.includes(k)) errs.push({ field: k, message: t('settings.timeInvalid') });
    });
    S.form.vacations.forEach((v, i) => {
      ['from', 'to'].forEach((part) => {
        if (!i18n.parseInputDate(v[part])) errs.push({ field: `vacations[${i}].${part}`, message: t('settings.vacationDateInvalid', { n: i + 1 }) });
      });
    });
    return errs;
  }

  /** DR-14: the autostart switch always shows the *effective* state. The backend reports it either as
      `autostartEffective` (wins when present) or directly in `autostart`. `requested` is what we just asked for;
      a difference (or a stored-vs-effective difference) shows the inline warning under the switch. */
  function normalizeSettings(s, requested) {
    if (!s || typeof s !== 'object') return s;
    const out = Object.assign({}, s);
    const hasEffective = typeof out.autostartEffective === 'boolean';
    const effective = hasEffective ? out.autostartEffective : !!out.autostart;
    const asked = typeof requested === 'boolean' ? requested : !!out.autostart;
    delete out.autostartEffective;
    out.autostart = effective;
    S.autostartMismatch = asked !== effective;
    return out;
  }

  /** saveSettings may return `warnings` (already localized, e.g. autostart could not be set). */
  function takeWarnings(saved) {
    const { warnings, ...settings } = saved || {};
    (Array.isArray(warnings) ? warnings : []).forEach((w) => toast(String(w), { tone: 'warn', timeout: 12000 }));
    return settings;
  }

  async function saveSettings(e) {
    if (e) e.preventDefault();
    if (S.saving || !S.form) return;
    const root = $('#settings-form');
    const alertEl = $('#settings-alert');
    const local = localErrors();
    if (local.length) {
      showFieldErrors(root, local);
      showFormAlert(alertEl, t('settings.fixErrors'));
      return;
    }
    S.saving = true;
    renderSavebar();
    showFormAlert(alertEl, null);
    try {
      const payload = collectSettings();
      const saved = normalizeSettings(takeWarnings(await api('saveSettings', payload)), payload.autostart);
      S.settings = saved;
      S.langOverride = null;
      fillSettingsForm(saved); // save bar now reads "all changes saved" — no extra success toast (DR-1)
      if (saved.language && saved.language !== i18n.lang) i18n.setLang(saved.language);
      S.boardStale = true; // JQL / connection may have changed: refetch on next board visit
      S.wl.preview = null;
      loadState();
    } catch (err) {
      if (err.fields && err.fields.length) {
        const rest = showFieldErrors(root, err.fields);
        showFormAlert(alertEl, t('settings.fixErrors'), rest);
      } else {
        showFormAlert(alertEl, t('settings.saveFailed') + ': ' + errorText(err));
        alertEl.scrollIntoView({ block: 'nearest' });
      }
    } finally {
      S.saving = false;
      renderSavebar();
    }
  }

  function setTestResult(el, res) {
    if (!res) { el.textContent = ''; delete el.dataset.tone; return; }
    if (res.loading) { setHTML(el, html`<span class="spinner spinner-sm" aria-hidden="true"></span>${t('settings.testing')}`); delete el.dataset.tone; return; }
    el.dataset.tone = res.ok ? 'ok' : 'error';
    setHTML(el, html`${icon(res.ok ? 'check-circle' : 'alert')}<span>${res.text}</span>`);
  }

  async function testConnection(urlInput, tokenInput, resultEl, btn) {
    if (btn.disabled) return;
    const params = { jiraUrl: urlInput.value.trim() };
    const token = tokenInput.value.trim();
    if (token) params.token = token;
    setBusy(btn, true);
    setTestResult(resultEl, { loading: true });
    try {
      const res = await api('testConnection', params);
      if (res && res.ok) setTestResult(resultEl, { ok: true, text: t('settings.testOk', { name: res.displayName || '' }) });
      else setTestResult(resultEl, { ok: false, text: t('settings.testFailed') + ((res && res.error) ? ': ' + res.error : '') });
    } catch (e) {
      setTestResult(resultEl, { ok: false, text: t('settings.testFailed') + ': ' + errorText(e) });
    } finally {
      setBusy(btn, false);
    }
  }

  async function changeLanguage(lang) {
    if (!i18n.languages.includes(lang)) return;
    S.langOverride = lang;
    i18n.setLang(lang);
    $('#s-language').value = lang;
    $('#o-language').value = lang;
    try {
      const base = S.settings || normalizeSettings(await api('getSettings'));
      const payload = Object.assign({}, base, { language: lang });
      delete payload.hasToken;
      const saved = takeWarnings(await api('saveSettings', payload));
      S.settings = Object.assign({}, S.settings || saved, { language: saved.language });
      S.langOverride = null;
    } catch {
      // Not persisted yet (e.g. first start without URL): kept as override and saved with the form.
    }
    if (S.form) renderSavebar();
  }

  function onSettingsClick(e) {
    const a = e.target.closest('[data-action], [data-day]');
    if (!a || !S.form) return;
    if (a.dataset.day) {
      const d = Number(a.dataset.day);
      if (S.form.workDays.has(d)) S.form.workDays.delete(d); else S.form.workDays.add(d);
      renderSettingsDynamic();
      return;
    }
    const i = Number(a.dataset.i);
    switch (a.dataset.action) {
      case 'add-status': addStatus(); break;
      case 'remove-status': S.form.inProgressStatuses.splice(i, 1); renderSettingsDynamic(); break;
      case 'add-holiday': addHoliday(); break;
      case 'remove-holiday': S.form.holidays.splice(i, 1); renderSettingsDynamic(); break;
      case 'add-vacation': {
        const today = i18n.formatInputDate(i18n.isoDate(new Date()));
        S.form.vacations.push({ from: today, to: today, note: '' });
        renderSettingsDynamic();
        const rows = $$('#s-vacations .vac-row');
        const last = rows[rows.length - 1];
        if (last) $('input', last).focus();
        break;
      }
      case 'remove-vacation': {
        S.form.vacations.splice(i, 1);
        renderSettingsDynamic();
        const rows = $$('#s-vacations [data-action="remove-vacation"]');
        (rows[Math.min(i, rows.length - 1)] || $('[data-action="add-vacation"]')).focus();
        break;
      }
      case 'settings-retry': enterSettings(true); break;
    }
  }

  function addStatus() {
    const input = $('#s-status-input');
    const v = input.value.trim();
    if (!v) return;
    if (!S.form.inProgressStatuses.some((x) => x.toLowerCase() === v.toLowerCase())) S.form.inProgressStatuses.push(v);
    input.value = '';
    renderSettingsDynamic();
    input.focus();
  }
  function addHoliday() {
    const input = $('#s-holiday-input');
    if (!input.value.trim()) return;
    const v = i18n.parseInputDate(input.value);
    if (!v) { validateMaskedInput($('#settings-form'), input); input.focus(); return; }
    if (!S.form.holidays.includes(v)) { S.form.holidays.push(v); S.form.holidays.sort(); }
    input.value = '';
    renderSettingsDynamic();
    input.focus();
  }

  function toggleSecret(btn) {
    const input = document.getElementById(btn.dataset.target);
    const show = input.type === 'password';
    input.type = show ? 'text' : 'password';
    btn.setAttribute('aria-pressed', show ? 'true' : 'false');
    const label = t(show ? 'settings.hideToken' : 'settings.showToken');
    btn.setAttribute('aria-label', label);
    btn.title = label;
    btn.dataset.i18nAttr = `aria-label:${show ? 'settings.hideToken' : 'settings.showToken'};title:${show ? 'settings.hideToken' : 'settings.showToken'}`;
    setHTML(btn, icon(show ? 'eye-off' : 'eye'));
  }

  /* ================================================================
   * ONBOARDING
   * ================================================================ */
  function enterOnboarding() {
    $('#o-language').value = i18n.lang;
    if (!S.settings) ensureSettings().then((s) => { if (s && !$('#o-jiraUrl').value) $('#o-jiraUrl').value = s.jiraUrl || ''; });
    else if (!$('#o-jiraUrl').value) $('#o-jiraUrl').value = S.settings.jiraUrl || '';
  }

  async function saveOnboarding(e) {
    e.preventDefault();
    const btn = $('#o-save-btn');
    if (btn.disabled) return;
    const root = $('#onboarding-form');
    const alertEl = $('#ob-alert');
    showFormAlert(alertEl, null);
    clearFieldErrors(root);
    setBusy(btn, true);
    try {
      const base = await ensureSettings();
      if (!base) throw new ApiError('settings_unavailable');
      const payload = Object.assign({}, base, { jiraUrl: $('#o-jiraUrl').value.trim(), language: i18n.lang });
      delete payload.hasToken;
      const token = $('#o-token').value.trim();
      if (token) payload.token = token;
      S.settings = normalizeSettings(takeWarnings(await api('saveSettings', payload)), payload.autostart);
      S.langOverride = null;
      $('#o-token').value = '';
      await loadState();
      if (isConfigured()) toast(t('onboarding.done'), { tone: 'success' });
    } catch (err) {
      if (err.fields && err.fields.length) {
        const rest = showFieldErrors(root, err.fields);
        showFormAlert(alertEl, t('settings.fixErrors'), rest);
      } else showFormAlert(alertEl, t('settings.saveFailed') + ': ' + errorText(err));
    } finally {
      setBusy(btn, false);
    }
  }

  /* ================================================================
   * Events from Go
   * ================================================================ */
  window.addEventListener('jiraffe', (e) => {
    const detail = e.detail || {};
    const payload = parsePayload(detail.payload);
    switch (detail.type) {
      case 'notification': onNotificationEvent(payload); break;
      case 'state': S.stateError = null; applyState(payload); break;
      case 'board':
        if (S.view === 'board' && activeSectionId() === 'view-board') loadBoard(false);
        else S.boardStale = true;
        break;
      case 'worklog':
        if (payload && payload.trigger !== 'preview') {
          if (S.wl.history && !S.wl.history.some((r) => r.at === payload.at)) S.wl.history.unshift(payload);
          if (payload.trigger === 'schedule') {
            toast(payload.skippedReason ? t('worklog.autoSkipped', { reason: reasonText(payload.skippedReason) })
              : t('worklog.autoDone', { total: dur(payload.totalSeconds || 0) }), { tone: payload.error ? 'error' : 'success' });
          }
          // CR-8: keep the preview (Today card) intact; during a manual run logNow reloads it when the call resolves.
          if (S.view === 'worklog') {
            renderWorklogHistory();
            if (!S.wl.running && !S.wl.result) loadPreview();
          }
        }
        break;
    }
  });

  /* ================================================================
   * Static bindings
   * ================================================================ */
  function bind() {
    $('.nav').addEventListener('click', (e) => {
      const b = e.target.closest('.nav-item');
      if (b && b.getAttribute('aria-disabled') !== 'true') showView(b.dataset.view);
    });
    $('#avatar-btn').addEventListener('click', () => showView('settings'));
    $('#refresh-btn').addEventListener('click', refreshCurrent);

    // Search + project chips
    const search = $('#search');
    search.addEventListener('input', () => { S.filter.q = search.value; renderBoard(); });
    $('#project-chips').addEventListener('click', (e) => {
      const b = e.target.closest('[data-project]');
      if (!b) return;
      const p = b.dataset.project;
      if (!p) S.filter.projects.clear();
      else if (S.filter.projects.has(p)) S.filter.projects.delete(p);
      else S.filter.projects.add(p);
      renderBoard();
    });

    // Board
    const board = $('#board');
    board.addEventListener('click', (e) => {
      const card = e.target.closest('.card');
      if (card) { if (!S.pendingKeys.has(card.dataset.key)) openDrawer(card.dataset.key); return; }
      const a = e.target.closest('[data-action]');
      if (!a) return;
      if (a.dataset.action === 'board-retry') loadBoard(true);
      if (a.dataset.action === 'clear-filters') {
        S.filter.q = ''; S.filter.projects.clear(); search.value = ''; renderBoard();
      }
    });
    board.addEventListener('dragstart', onDragStart);
    board.addEventListener('dragend', onDragEnd);
    document.addEventListener('dragstart', guardDragStart, true);
    ['dragenter', 'dragover', 'drop'].forEach((type) => document.addEventListener(type, guardForeignDrag));
    board.addEventListener('dragenter', onDragOver); // highlight immediately; dragenter must also be cancelled for a valid target
    board.addEventListener('dragover', onDragOver);
    board.addEventListener('dragleave', onDragLeave);
    board.addEventListener('drop', onDrop);

    // Drawer / dialogs
    $('#drawer').addEventListener('click', onDrawerClick);
    $('#scrim').addEventListener('click', () => layers.closeTop());

    // Status view
    $('#view-status').addEventListener('click', (e) => { if (e.target.closest('[data-action="retry-state"]')) loadState(); });

    // Worklog
    $('#wl-preview-btn').addEventListener('click', () => { S.wl.result = null; loadPreview(); });
    $('#wl-run-btn').addEventListener('click', logNow);
    $('#view-worklog').addEventListener('click', (e) => {
      const a = e.target.closest('[data-action]');
      if (!a) return;
      if (a.dataset.action === 'wl-retry') loadPreview();
      if (a.dataset.action === 'history-retry') loadHistory();
    });

    // Notifications
    $('#notif-mark-all').addEventListener('click', markAllRead);
    $('#notif-list').addEventListener('click', (e) => {
      if (e.target.closest('[data-action="notif-retry"]')) { loadNotifications(); return; }
      const b = e.target.closest('.notif');
      if (!b) return;
      const n = (S.notif.list || []).find((x) => String(x.id) === b.dataset.id);
      if (n) openNotification(n);
    });

    // Settings
    const sf = $('#settings-form');
    sf.addEventListener('submit', saveSettings);
    sf.addEventListener('click', onSettingsClick);
    sf.addEventListener('input', (e) => {
      const el = e.target;
      if (el.dataset.mask) autoMask(el, e);
      if (el.dataset.vac && S.form) S.form.vacations[Number(el.dataset.i)][el.dataset.vac] = el.value;
      clearFieldError(sf, el);
      if (el.name === 'jiraUrl' || el.name === 'token') setTestResult($('#s-test-result'), null);
      renderSavebar();
    });
    sf.addEventListener('change', (e) => {
      if (e.target.id === 's-language') changeLanguage(e.target.value);
      if (e.target.id === 's-autostart' && S.autostartMismatch) { S.autostartMismatch = false; $('#w-autostart').hidden = true; }
      renderSavebar();
    });
    sf.addEventListener('focusout', (e) => {
      // Holiday input is validated when added, not on blur (empty after "Add" is fine).
      if (e.target.dataset && e.target.dataset.mask && e.target.id !== 's-holiday-input') { validateMaskedInput(sf, e.target); renderSavebar(); }
    });
    sf.addEventListener('keydown', (e) => {
      if (e.key !== 'Enter') return;
      if (e.target.id === 's-status-input') { e.preventDefault(); addStatus(); }
      else if (e.target.id === 's-holiday-input') { e.preventDefault(); addHoliday(); }
    });
    $('#s-discard').addEventListener('click', () => {
      if (S.settings) fillSettingsForm(S.settings); // save bar reflects the reset; no toast (DR-1)
    });
    $('#s-test-btn').addEventListener('click', () => testConnection($('#s-jiraUrl'), $('#s-token'), $('#s-test-result'), $('#s-test-btn')));
    $('#settings-loading').addEventListener('click', (e) => { if (e.target.closest('[data-action="settings-retry"]')) enterSettings(true); });

    // Onboarding
    const of = $('#onboarding-form');
    of.addEventListener('submit', saveOnboarding);
    of.addEventListener('change', (e) => { if (e.target.id === 'o-language') changeLanguage(e.target.value); });
    of.addEventListener('input', (e) => {
      clearFieldError(of, e.target);
      setTestResult($('#o-test-result'), null);
    });
    $('#o-test-btn').addEventListener('click', () => testConnection($('#o-jiraUrl'), $('#o-token'), $('#o-test-result'), $('#o-test-btn')));

    // Show/hide token (both forms)
    document.addEventListener('click', (e) => {
      const b = e.target.closest('[data-action="toggle-secret"]');
      if (b) toggleSecret(b);
    });

    // Keyboard shortcuts. e.code keeps them working on ru/uk layouts.
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && layers.top()) { e.preventDefault(); layers.closeTop(); return; }
      if (layers.top() || e.ctrlKey || e.metaKey || e.altKey || e.repeat) return;
      const target = e.target;
      if (target === search && e.key === 'Escape') {
        if (search.value) { search.value = ''; S.filter.q = ''; renderBoard(); } else search.blur();
        return;
      }
      if (isTyping(target)) return;
      if (e.key === '/' || (e.code === 'Slash' && !e.shiftKey)) {
        e.preventDefault();
        if (S.appState && !S.appState.configured) return; // onboarding: board is not reachable (DR-9)
        if (S.view !== 'board') showView('board');
        if (!$('#board-tools').hidden) { search.focus(); search.select(); }
      } else if (e.code === 'KeyR' && !e.shiftKey) {
        e.preventDefault();
        refreshCurrent();
      }
    });

    // Relative times tick.
    setInterval(() => {
      $$('[data-rel]').forEach((el) => { el.textContent = relText(el.dataset.rel, el.dataset.relKey); });
    }, 30000);

    i18n.onChange(() => {
      // Re-display valid vacation dates in the new language's input format (e.g. "." vs "/").
      if (S.form) S.form.vacations.forEach((v) => ['from', 'to'].forEach((k) => {
        const isoDate = i18n.parseInputDate(v[k]);
        if (isoDate) v[k] = i18n.formatInputDate(isoDate);
      }));
      renderChrome();
      renderView(false);
      if (S.drawer) renderDrawer();
      if (S.form) renderSettingsDynamic();
    });
  }

  function init() {
    i18n.apply();
    bind();
    showView('board');
    renderChrome();
    loadState();
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();

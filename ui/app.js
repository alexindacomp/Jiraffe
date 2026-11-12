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

  /** UI preferences, one JSON object. Stored by the Go side (getUiPrefs / setUiPrefs, whole object, ≤ 32 KB)
      because a WebView2 page loaded from a string may have no usable localStorage. If getUiPrefs fails
      (older backend) the object lives in localStorage when available, else in memory for this window. */
  const prefs = (() => {
    const LS_KEY = 'jiraffe.prefs';
    const MAX_BYTES = 24 * 1024; // stay well under the backend's 32 KB limit
    let data = {};
    let remote = false;
    let timer = null;
    let store = null;
    try { store = window.localStorage; store.setItem('jiraffe.probe', '1'); store.removeItem('jiraffe.probe'); } catch { store = null; }
    const isObj = (v) => !!v && typeof v === 'object' && !Array.isArray(v);
    function readLocal() {
      if (!store) return {};
      try { const v = JSON.parse(store.getItem(LS_KEY) || '{}'); return isObj(v) ? v : {}; } catch { return {}; }
    }
    function flush() {
      clearTimeout(timer);
      timer = null;
      if (remote) api('setUiPrefs', { prefs: data }).catch(() => { /* best effort: kept in memory for this window */ });
    }
    return {
      /** Loads once at startup; never blocks longer than a few seconds. */
      async load() {
        try {
          const res = await Promise.race([api('getUiPrefs'), new Promise((_, rej) => setTimeout(() => rej(new Error('timeout')), 4000))]);
          if (isObj(res)) { data = res; remote = true; return; }
        } catch { /* older backend or no bridge */ }
        remote = false;
        data = readLocal();
      },
      get(key, def) { return Object.prototype.hasOwnProperty.call(data, key) ? data[key] : def; },
      set(key, value) {
        if (JSON.stringify(data[key]) === JSON.stringify(value)) return; // unchanged: no save
        data[key] = value;
        // Only the expansion list can grow; trim the oldest entries if the object gets too big.
        while (Array.isArray(data['epics.expanded']) && data['epics.expanded'].length && JSON.stringify(data).length > MAX_BYTES) {
          data['epics.expanded'] = data['epics.expanded'].slice(Math.ceil(data['epics.expanded'].length / 4));
        }
        if (remote) { clearTimeout(timer); timer = setTimeout(flush, 500); }
        else if (store) { try { store.setItem(LS_KEY, JSON.stringify(data)); } catch { /* quota or denied: memory only */ } }
      },
      /** Sends a pending debounced save right away (window is going away). */
      flushNow() { if (timer) flush(); },
    };
  })();

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

    q: { board: '', sprints: '', epics: '', projects: '' }, // search box text per view

    project: '',             // shared project filter of Board / Sprints / Epics (uiPrefs.project; "" = all tracked)

    board: null,             // { key, project, columns: [Column], layout, fetchedAt, scope, truncated, hidden }
    boardReq: null,          // boardKey of the request in flight
    hiddenDismissed: null,   // projects signature of the "issues in untracked projects" banner closed this session
    boardScope: 'mine',      // preferences are applied in applyPrefs() once loaded
    boardCache: {},          // boards of other scope / project / board choices (by boardKey), kept while switching
    boardLoading: false,
    boardError: null,
    boardSeq: 0,
    boardStale: false,
    boardAnimate: false,
    renderDeferred: new Set(), // kanbans whose re-render waits for the drag to end
    pendingKeys: new Set(),  // issues with a transition in flight
    transCache: new Map(),   // key -> Promise<[Transition]>
    drag: null,              // { key, from, kb, blockedOver }
    drawer: null,            // { stack: [{key, seed}], detail, loading, error, tab, seq }

    // data = { key, available, groups: [Group], backlog: Group|null, truncated }; Group = SprintGroup + { id }
    sp: { data: null, loading: false, error: null, seq: 0, reqKey: null, stale: false, cache: new Map(), cacheGen: 0,
          mine: false, closed: false, expanded: {}, overrides: new Map(), opening: null, animate: false },

    ep: { mode: 'epics', includeDone: false, mine: false,
          cache: new Map(), cacheGen: 0, data: null, loading: false, error: null, seq: 0, reqKey: null, stale: false,
          expanded: new Set(), overrides: new Map(), opening: null, focusNode: null,
          // Epic dashboard (SPEC v0.4 §3): el/sig = the built element, reused across tree re-renders.
          dash: { collapsed: false, metric: 'spent', otherOpen: new Set(), el: null, sig: '', data: null, shownMetric: null, wasCollapsed: false } },

    pj: { list: null, loading: false, error: null, seq: 0, busy: new Set(), bulk: false, restoreFocus: null },

    wl: { preview: null, previewLoading: false, previewError: null, previewSeq: 0,
          result: null, running: false,
          history: null, historyLoading: false, historyError: null },

    notif: { list: null, loading: false, error: null, markingAll: false },

    // Updates (SPEC v0.4 §5): status = UpdateStatus; dlg = { mode: prompt|progress|failed, release, progress, error }
    cf: null,                // create / edit issue dialog (SPEC v0.5)
    noEditKeys: new Set(),   // issues whose edit meta had no fields (no permission), for this session
    cfDraft: null,           // { project, values } kept after the dialog was closed by an error

    // job: an install is known to run (started here, or `installing` at startup); progress events without it are ignored
    upd: { status: null, checking: false, dlg: null, toasted: false, job: false },

    form: null,              // settings form working copy for list editors
    formLoading: false,
    formError: null,
    saving: false,
  };

  const isConfigured = () => !!(S.appState && S.appState.configured);

  /** Remembered UI choices. Defaults: "Only mine" on for the board, off for sprints and epics. */
  function applyPrefs() {
    S.boardScope = prefs.get('board.mine', true) === false ? 'all' : 'mine';
    const project = prefs.get('project', '');
    S.project = typeof project === 'string' ? project : '';
    S.sp.mine = prefs.get('sprint.mine', false) === true;
    S.sp.closed = prefs.get('sprints.closed', false) === true;
    const spExp = prefs.get('sprints.expanded', {});
    S.sp.expanded = spExp && typeof spExp === 'object' && !Array.isArray(spExp) ? Object.assign({}, spExp) : {};
    S.ep.mode = prefs.get('epics.mode', 'epics') === 'stories' ? 'stories' : 'epics';
    S.ep.includeDone = prefs.get('epics.done', false) === true;
    S.ep.mine = prefs.get('epics.mine', false) === true;
    const exp = prefs.get('epics.expanded', []);
    S.ep.expanded = new Set((Array.isArray(exp) ? exp : []).map(String));
    S.ep.dash.collapsed = prefs.get('epics.dash.collapsed', false) === true;
    S.ep.dash.metric = prefs.get('epics.dash.metric', 'spent') === 'estimate' ? 'estimate' : 'spent';
  }

  /* ================================================================
   * Layers (drawer + dialogs). Non-modal <dialog>s; the app is made
   * inert while a layer is open so toasts remain interactive on top.
   * ================================================================ */
  const LAYER_Z = 50; // = --z-layer; layer n gets LAYER_Z + 2n, the scrim LAYER_Z + 2n − 1
  const layers = {
    stack: [],
    open(el, opts = {}) {
      const entry = { el, opts, prevFocus: document.activeElement };
      this.stack.forEach((l) => { l.el.inert = true; });
      this.stack.push(entry);
      const z = LAYER_Z + 2 * this.stack.length; // DR-16: a stacked dialog (e.g. the discard prompt) is always on top
      el.style.zIndex = String(z);
      $('#scrim').style.zIndex = String(z - 1);
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
      el.style.zIndex = '';
      const top = this.stack[this.stack.length - 1];
      if (top) { top.el.inert = false; $('#scrim').style.zIndex = String(Number(top.el.style.zIndex) - 1); }
      else { $('#app').inert = false; $('#scrim').hidden = true; $('#scrim').style.zIndex = ''; }
      if (entry.opts.onClose) entry.opts.onClose(result);
      const restore = (entry.opts.restoreFocus && entry.opts.restoreFocus()) || entry.prevFocus;
      if (restore && document.contains(restore) && typeof restore.focus === 'function') restore.focus({ preventScroll: true });
    },
    top() { return this.stack[this.stack.length - 1] || null; },
    /** Esc / scrim. A layer with opts.onDismiss decides itself (e.g. the update dialog: "Later", or nothing while installing). */
    closeTop(source) {
      const top = this.top();
      if (!top) return;
      if (top.opts.onDismiss) top.opts.onDismiss(source || 'esc'); else this.close(top.el);
    },
  };

  /** safeDefault: the cancel button ("Keep editing") is the primary, focused choice; OK is the destructive one. */
  function confirmDialog({ title, body, okLabel, cancelLabel, safeDefault }) {
    const dlg = $('#dlg-confirm');
    const cancel = $('#dlg-confirm [data-action="cancel"]');
    $('#confirm-title').textContent = title;
    $('#confirm-body').textContent = body;
    $('#confirm-ok').textContent = okLabel || t('common.ok');
    cancel.textContent = cancelLabel || t('common.cancel');
    $('#confirm-ok').className = safeDefault ? 'btn' : 'btn btn-primary';
    cancel.className = safeDefault ? 'btn btn-primary' : 'btn';
    return new Promise((resolve) => {
      let done = false;
      const finish = (v) => { if (done) return; done = true; resolve(v); layers.close(dlg); };
      dlg.onclick = (e) => {
        const a = e.target.closest('[data-action]');
        if (a) finish(a.dataset.action === 'ok');
      };
      layers.open(dlg, { focus: safeDefault ? '[data-action="cancel"]' : '#confirm-ok', onClose: () => { if (!done) { done = true; resolve(false); } } });
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

  const catOf = (st) => (st && CAT_ORDER[st.category] != null ? st.category : 'new');
  function statusPill(status) {
    if (!status) return '';
    return html`<span class="status-pill" data-cat="${catOf(status)}">${status.name}</span>`;
  }
  /** Status pill that opens the transitions dropdown (cards and drawer). */
  function statusButton(issue) {
    const pend = S.pendingKeys.has(issue.key);
    const name = nameOf(issue.status);
    return html`<button type="button" class="status-btn" data-cat="${catOf(issue.status)}" data-status-menu="${issue.key}" data-role="status" aria-haspopup="menu" aria-expanded="false" aria-label="${t('status.change', { status: name })}" title="${name} — ${t('status.changeTitle')}" ${pend ? html`disabled` : ''}><span class="status-btn-text">${name}</span>${pend ? html`<span class="spinner spinner-xs" aria-hidden="true"></span>` : icon('chevron-down', 'icon-xs')}</button>`;
  }

  /* Issue kinds (SPEC v0.2: type.kind; names are localized, so fall back to a name match). */
  function kindOf(type) {
    const k = type && type.kind;
    if (k === 'epic' || k === 'story' || k === 'task' || k === 'subtask') return k;
    const name = nameOf(type);
    if (/sub-?task|подзадач|підзадач/i.test(name)) return 'subtask';
    if (/epic|эпик|епік/i.test(name)) return 'epic';
    if (/story|истор|істор/i.test(name)) return 'story';
    return 'task';
  }
  const isBug = (type) => /bug|ошиб|помил|дефект|defect|incident|инцидент|інцидент/i.test(nameOf(type));
  /** Visual type (colour + icon): epic | story | task | bug | subtask. */
  const typeVisual = (type) => { const k = kindOf(type); return k === 'task' && isBug(type) ? 'bug' : k; };
  function typeIcon(type, decorative) {
    const v = typeVisual(type);
    const name = nameOf(type) || t('kind.' + kindOf(type));
    const a11y = decorative ? html`aria-hidden="true"` : html`role="img" aria-label="${name}" title="${name}"`;
    return html`<span class="tico" data-type="${v}" ${a11y}><svg aria-hidden="true"><use href="#t-${v}"></use></svg></span>`;
  }
  function typeBadge(type) {
    const name = nameOf(type);
    if (!name) return '';
    return html`<span class="type-badge">${typeIcon(type, true)}<span>${name}</span></span>`;
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

  /* Colours hashed from names (avatars, project badges) and Jira epic colours ghx-label-1..14. */
  function hash(s) {
    let h = 2166136261;
    for (const ch of String(s || '')) { h ^= ch.codePointAt(0); h = Math.imul(h, 16777619); }
    return h >>> 0;
  }
  const AVATAR_TONES = 10;
  const avatarTone = (name) => hash(String(name || '').trim().toLowerCase()) % AVATAR_TONES;
  function avatar(name, cls) {
    return html`<span class="avatar ${cls || 'avatar-xs'}" data-tone="${avatarTone(name)}" aria-hidden="true">${initials(name)}</span>`;
  }
  function assigneeAvatar(user) {
    const name = user && (user.displayName || user.name);
    if (!name) return '';
    const label = t('issue.assigneeValue', { name });
    return html`<span class="avatar avatar-xs" data-tone="${avatarTone(name)}" role="img" aria-label="${label}" title="${label}">${initials(name)}</span>`;
  }
  const EPIC_TONES = 14;
  function epicTone(color, key) {
    const m = /ghx-label-(\d+)/.exec(String(color || ''));
    const n = m ? Number(m[1]) : 0;
    return n >= 1 && n <= EPIC_TONES ? n : (hash(key) % EPIC_TONES) + 1;
  }
  function epicChip(epic) {
    if (!epic || !epic.key) return '';
    const name = epic.name || epic.summary || epic.key;
    const label = t('issue.epicValue', { name });
    return html`<span class="epic-chip" data-epic="${epicTone(epic.color, epic.key)}" title="${label}"><span class="sr-only">${label}</span><span class="epic-chip-text" aria-hidden="true">${name}</span></span>`;
  }
  const fmtNum = (n) => new Intl.NumberFormat(i18n.locale, { maximumFractionDigits: 1 }).format(Number(n) || 0);
  const reducedMotion = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  /** Progress by status category: done (green) + in progress (amber) over a "to do" track. */
  function progressBar(p, opts = {}) {
    const n = (v) => Math.max(0, Number(v) || 0);
    const done = n(p && p.done); const ind = n(p && p.inProgress); const todo = n(p && p.todo);
    const total = Math.max(n(p && p.total), done + ind + todo);
    const pct = (v) => (total ? (v / total * 100).toFixed(2) : '0') + '%';
    const label = t('progress.aria', { done, ind, todo, total });
    return html`<span class="progress${opts.size ? ' progress-' + opts.size : ''}" role="img" aria-label="${label}" title="${label}"><span class="progress-track"><span class="pg pg-done" style="--w:${pct(done)}"></span><span class="pg pg-ind" style="--w:${pct(ind)}"></span></span>${opts.counts === false ? '' : html`<span class="progress-count" aria-hidden="true">${done}/${total}</span>`}</span>`;
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
  const VIEWS = ['board', 'sprints', 'epics', 'worklog', 'notifications', 'projects', 'settings'];
  // Views with the top-bar search (placeholder / label keys) and with the "Only mine" switch.
  const SEARCH_VIEWS = { board: 'board', sprints: 'board', epics: 'epics', projects: 'projects' };
  const MINE_VIEWS = ['board', 'sprints', 'epics'];
  // Views sharing the single-select project filter (SPEC v0.3).
  const PROJECT_VIEWS = MINE_VIEWS;

  function showView(name) {
    if (!VIEWS.includes(name)) name = 'board';
    menu.close(false);
    S.view = name;
    renderView(true);
  }

  const mineFor = (view) => (view === 'board' ? S.boardScope === 'mine' : view === 'sprints' ? S.sp.mine : view === 'epics' ? S.ep.mine : false);

  /** Top-bar tools of the current view: search (text kept per view), "Only mine", project chips, board picker. */
  function renderTools(sectionId) {
    const v = sectionId === 'view-' + S.view ? S.view : null;
    const tools = $('#view-tools');
    const ns = v && SEARCH_VIEWS[v];
    tools.hidden = !ns;
    $('#create-btn').hidden = true;
    if (!ns) return;
    const search = $('#search');
    search.placeholder = t(ns + '.searchPlaceholder');
    search.setAttribute('aria-label', t(ns + '.searchLabel'));
    if (search.value !== S.q[v]) search.value = S.q[v];
    $('#mine-wrap').hidden = !MINE_VIEWS.includes(v);
    $('#mine-toggle').checked = mineFor(v);
    $('#create-btn').hidden = !(PROJECT_VIEWS.includes(v) && isConfigured());
    renderChips();
    renderBoardBar();
  }

  function setMine(view, on) {
    if (view === 'board') setBoardScope(on ? 'mine' : 'all');
    else if (view === 'sprints') {
      S.sp.mine = on;
      prefs.set('sprint.mine', on);
      enterSprints();
    } else if (view === 'epics') setEpicsOption({ mine: on });
  }

  function onSearchInput(value) {
    const v = S.view;
    if (!SEARCH_VIEWS[v]) return;
    S.q[v] = value;
    if (v === 'board') renderBoard();
    else if (v === 'sprints') { S.sp.overrides.clear(); renderSprints(); }
    else if (v === 'epics') { S.ep.overrides.clear(); renderEpics(); }
    else if (v === 'projects') renderProjects();
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
    $('#view-title').textContent = id === 'view-onboarding' ? t('onboarding.viewTitle')
      : t(S.view === 'board' && S.boardScope === 'all' ? 'title.boardAll' : 'title.' + S.view);
    $('#refresh-btn').hidden = id === 'view-onboarding';
    renderTools(id);
    renderNav(id);
    updateToastOffset();
    renderRefreshState();
    // The project chips need the tracked projects; views load with the remembered choice meanwhile.
    if (enter && id === 'view-' + S.view && PROJECT_VIEWS.includes(S.view) && !S.pj.list && !S.pj.loading) loadProjects(false);
    switch (id) {
      case 'view-status': renderStatus(); break;
      case 'view-onboarding': if (enter) enterOnboarding(); break;
      case 'view-board': if (enter) enterBoard(); else renderBoard(); break;
      case 'view-sprints': if (enter) enterSprints(); else renderSprints(); break;
      case 'view-epics': if (enter) enterEpics(); else renderEpics(); break;
      case 'view-projects': if (enter) loadProjects(false); else renderProjects(); break;
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
      avatar.dataset.tone = avatarTone(st.user.displayName || st.user.name); // DR-13: same colour as on cards
      dot.dataset.tone = 'ok';
      label = t('chrome.connectedAs', { name: st.user.displayName || st.user.name });
    } else if (st && !st.configured) {
      setHTML(avatar, icon('plug'));
      delete avatar.dataset.tone;
      dot.dataset.tone = 'warn';
      label = t('chrome.notConfigured');
    } else if (S.stateError) {
      setHTML(avatar, icon('alert'));
      delete avatar.dataset.tone;
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
      case 'sprints': return S.sp.loading;
      case 'epics': return S.ep.loading;
      case 'projects': return S.pj.loading || S.pj.bulk;
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
    if (PROJECT_VIEWS.includes(S.view) && !S.pj.list && !S.pj.loading) loadProjects(false); // chips failed to load earlier
    if (S.view === 'board') loadBoard(true);
    else if (S.view === 'sprints') enterSprints(true);
    else if (S.view === 'epics') loadEpics({ force: true, silent: !!S.ep.data });
    else if (S.view === 'projects') { if (!S.pj.bulk) loadProjects(true); }
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
   * KANBAN (Board): model = { project, columns: [Column] }
   * Column = { id, name, category, statuses: [Status], issues, status, synthetic? } (SPEC v0.3).
   * Status-mode columns (All projects / workflow fallback) hold one status; agile columns may hold several.
   * ================================================================ */
  function normalizeColumns(data) {
    return (data && Array.isArray(data.columns) ? data.columns : []).filter(Boolean).map((c, i) => {
      const statuses = (Array.isArray(c.statuses) && c.statuses.length ? c.statuses : [c.status]).filter((s) => s && typeof s === 'object');
      const status = c.status || statuses[0] || null;
      return {
        id: String(c.id != null && c.id !== '' ? c.id : status ? status.id ?? status.name : 'col-' + i),
        name: c.name != null ? String(c.name) : nameOf(status),
        category: CAT_ORDER[c.category] != null ? c.category : catOf(status),
        statuses,
        status,
        issues: Array.isArray(c.issues) ? c.issues.slice() : [],
      };
    }).filter((c) => c.statuses.length);
  }
  const kbIssues = (model) => (model ? model.columns.flatMap((c) => c.issues) : []);
  function kbFind(model, key) {
    if (!model) return null;
    for (const col of model.columns) {
      const idx = col.issues.findIndex((i) => i.key === key);
      if (idx >= 0) return { col, idx, issue: col.issues[idx] };
    }
    return null;
  }
  const colHas = (col, status) => col.statuses.some((st) => sameStatus(st, status));
  const kbColumn = (model, status) => model && model.columns.find((c) => colHas(c, status));
  const colLabel = (col) => col.name || (col.id === 'other' ? t('board.otherStatuses') : nameOf(col.status));
  /** Category of the "other" column: the statuses' shared category, else "indeterminate" (SPEC v0.3). */
  function sharedCategory(statuses) {
    const cats = new Set(statuses.map(catOf));
    return cats.size === 1 ? [...cats][0] : 'indeterminate';
  }
  /** Column holding `status`, created when missing: a project board collects unknown statuses in the trailing
      "other" column (as the backend does); a status board gets a synthetic column in category order (v0.2). */
  function kbEnsure(model, status) {
    let col = kbColumn(model, status);
    if (col) return col;
    if (model.project && !(model.layout && model.layout.source === 'issues')) {
      col = model.columns.find((c) => c.id === 'other');
      if (col) { col.statuses.push(status); col.category = sharedCategory(col.statuses); return col; }
      col = { id: 'other', name: '', category: catOf(status), statuses: [status], status, issues: [], synthetic: true };
      model.columns.push(col);
      return col;
    }
    col = { id: String(status.id ?? status.name), name: status.name, category: catOf(status), statuses: [status], status, issues: [], synthetic: true };
    const rank = CAT_ORDER[col.category];
    const cols = model.columns;
    let at = cols.length;
    for (let i = 0; i < cols.length; i++) {
      if ((CAT_ORDER[cols[i].category] ?? 1) > rank) { at = i; break; }
    }
    cols.splice(at, 0, col);
    return col;
  }
  /** Moves an issue into the column of `status` (creating it if needed). */
  function kbPlace(model, key, issue, status, index) {
    const found = kbFind(model, key);
    if (found) {
      found.col.issues.splice(found.idx, 1);
      if (found.col.synthetic && !found.col.issues.length) model.columns.splice(model.columns.indexOf(found.col), 1);
    }
    const col = kbEnsure(model, status);
    const next = Object.assign({}, issue, { status });
    col.issues.splice(index == null ? 0 : Math.min(index, col.issues.length), 0, next);
  }

  const KANBAN = {
    board: { model: () => S.board, render: () => renderBoard() },
  };
  const kanbanOf = (el) => { const root = el && el.closest && el.closest('[data-kanban]'); return root ? root.dataset.kanban : null; };

  /** Text filter for cards and sprint rows: key, summary, epic name, assignee. */
  function issueMatcher(query) {
    const q = String(query || '').trim().toLowerCase();
    return (issue) => {
      if (!q) return true;
      const hay = [issue.key, issue.summary, issue.epic && issue.epic.name, issue.assignee && issue.assignee.displayName]
        .map((x) => String(x || '').toLowerCase());
      return hay.some((h) => h.includes(q));
    };
  }

  /** name: the field the points belong to (default "Story points"), e.g. "Dev story points" (DR-3). */
  function spBadge(points, name) {
    if (points == null || points === '') return '';
    const n = Number.isFinite(Number(points)) ? fmtNum(points) : String(points); // sums are floats: 0.1 + 0.2 → "0.3"
    const label = name ? `${name}: ${n}` : t('issue.storyPointsValue', { n });
    return html`<span class="sp-badge" title="${label}"><span aria-hidden="true">${n}</span><span class="sr-only">${label}</span></span>`;
  }

  function cardHTML(issue, i, opts) {
    const pend = S.pendingKeys.has(issue.key);
    const mine = opts.highlightMine && issue.mine;
    const anim = opts.animate ? html` style="--i:${Math.min(i, 12)}"` : '';
    return html`<div class="card${pend ? ' is-pending' : ''}${mine ? ' is-mine' : ''}" data-type="${typeVisual(issue.type)}" data-key="${issue.key}" draggable="${pend ? 'false' : 'true'}" aria-busy="${pend ? 'true' : 'false'}"${anim}>
      <span class="card-top">${typeIcon(issue.type)}<span class="card-key" aria-hidden="true">${issue.key}</span><span class="card-top-end">${priorityIcon(issue.priority)}${spBadge(issue.storyPoints)}${opts.showAssignee ? assigneeAvatar(issue.assignee) : ''}</span></span>
      <button type="button" class="card-open" data-role="open" data-focus-key="${issue.key}" aria-haspopup="dialog"><span class="sr-only">${issue.key}${mine ? ' · ' + t('issue.mineMark') : ''}: </span><span class="card-sum">${issue.summary}</span></button>
      <span class="card-meta">${statusButton(issue)}${epicChip(issue.epic)}${relSpan(issue.updated, '', 'card-upd')}</span>
    </div>`;
  }

  function columnsHTML(model, filter, opts) {
    let visible = 0;
    const cols = model.columns.map((col) => {
      const issues = col.issues.filter(filter);
      visible += issues.length;
      const hid = opts.prefix + '-h-' + col.id.replace(/[^\w-]/g, '_');
      const label = colLabel(col);
      // Project boards: one muted line under every header (empty when there is nothing to say, so cards line up):
      // done columns say how far back they go, multi-status and "other" columns list their statuses (also as tooltip).
      const statuses = col.statuses.length > 1 || col.id === 'other' ? t('board.colStatuses', { list: col.statuses.map(nameOf).join(', ') }) : '';
      const doneHint = model.project && col.category === 'done' ? t('board.doneRecent') : '';
      const sub = model.project
        ? html`<p class="col-sub"${statuses ? html` title="${[doneHint, statuses].filter(Boolean).join(' · ')}"` : ''}>${doneHint || statuses}${doneHint && statuses ? html`<span class="sr-only"> · ${statuses}</span>` : ''}</p>`
        : '';
      return html`<section class="col${col.id === 'other' ? ' col-other' : ''}" data-cat="${col.category}" data-col="${col.id}" aria-labelledby="${hid}">
        <header class="col-head"><h2 class="col-title" id="${hid}" title="${label}">${label}</h2><span class="col-count" aria-label="${t('board.count', { n: issues.length })}">${issues.length}</span></header>
        ${sub}
        <p class="col-hint">${t('board.noTransitionHint')}</p>
        <div class="col-list">${issues.length ? issues.map((is, i) => cardHTML(is, i, opts)) : html`<p class="col-empty">${t('board.columnEmpty')}</p>`}</div>
      </section>`;
    });
    return { cols, visible };
  }

  function kanbanSkeleton() {
    const col = (n) => html`<div class="col" aria-hidden="true"><div class="col-head"><span class="skeleton sk-line" style="width:45%"></span></div><div class="col-list">${Array.from({ length: n }, () => html`<div class="skeleton sk-card"></div>`)}</div></div>`;
    return html`<div class="board-skeleton" role="status" aria-label="${t('common.loading')}">${col(3)}${col(2)}${col(4)}${col(1)}</div>`;
  }

  /** Re-renders a kanban root keeping horizontal/column scroll and the focused control of a card. */
  function preserveKanban(root, fn) {
    const prevCols = $('.columns', root);
    const scrollX = prevCols ? prevCols.scrollLeft : 0;
    const scrollY = new Map($$('.col', root).map((c) => [c.dataset.col, $('.col-list', c).scrollTop]));
    const a = document.activeElement;
    const card = a && root.contains(a) && a.closest('.card');
    const focus = card ? { key: card.dataset.key, role: a.dataset.role } : null;
    fn();
    const cols = $('.columns', root);
    if (cols) cols.scrollLeft = scrollX;
    $$('.col', root).forEach((c) => { const y = scrollY.get(c.dataset.col); if (y) $('.col-list', c).scrollTop = y; });
    if (focus) {
      const el = root.querySelector(`.card[data-key="${CSS.escape(focus.key)}"]`);
      const target = el && ((focus.role && el.querySelector(`[data-role="${focus.role}"]:not(:disabled)`)) || el.querySelector('[data-role="open"]'));
      if (target) target.focus({ preventScroll: true });
    }
    menu.sync();
  }

  function filterBanner() {
    return html`<div class="filter-empty" role="status">${icon('search')}<span>${t('board.noMatches')}</span><button type="button" class="link-btn" data-action="clear-filters">${t('board.clearFilters')}</button></div>`;
  }

  /* ================================================================
   * PROJECT FILTER (Board, Sprints, Epics & stories; SPEC v0.3): one shared single-select chip row,
   * "All projects" + the tracked projects. Persisted as uiPrefs.project ("" = all tracked projects).
   * ================================================================ */
  /** Tracked, available project keys (sorted) once getProjects has answered; null before that. */
  function trackedKeys() {
    if (!S.pj.list) return null;
    return S.pj.list.filter((p) => p.tracked && p.available !== false).map((p) => String(p.key)).sort((a, b) => a.localeCompare(b));
  }
  /** The project the views show: the chosen chip, or the only tracked project; "" = all tracked projects. */
  function effProject() {
    const keys = trackedKeys();
    if (!keys) return S.project; // not loaded (yet, or failed): the remembered choice
    if (keys.length === 1) return keys[0];
    return keys.includes(S.project) ? S.project : '';
  }
  const projectName = (key) => { const p = (S.pj.list || []).find((x) => x.key === key); return p && p.name && p.name !== key ? p.name : ''; };

  /** Chip row (role=radiogroup, roving tabindex); hidden with fewer than two tracked projects. When the chips
      (plus the board picker) do not fit, they are swapped for a compact "Project: KEY ▾" menu (DR-20). */
  function renderChips() {
    const box = $('#project-chips');
    const v = S.view;
    const keys = PROJECT_VIEWS.includes(v) && activeSectionId() === 'view-' + v ? trackedKeys() || [] : [];
    const hadFocus = box.contains(document.activeElement);
    box.hidden = keys.length < 2;
    if (box.hidden) box.textContent = '';
    else {
      const cur = effProject();
      setHTML(box, html`${['', ...keys].map((k) => {
        const on = k === cur;
        const name = k ? projectName(k) : '';
        return html`<button type="button" role="radio" class="chip-btn" data-project="${k}" aria-checked="${on ? 'true' : 'false'}" tabindex="${on ? '0' : '-1'}"${name ? html` title="${name}"` : ''}>${k || t('projectFilter.all')}</button>`;
      })}`);
      setHTML($('#project-menu-btn'), html`<span class="picker-label">${t('projectFilter.label')}</span><span class="picker-value">${cur || t('projectFilter.all')}</span>${icon('chevron-down', 'icon-xs')}`);
    }
    syncProjectFilter();
    if (hadFocus) {
      const target = $('#project-filter').classList.contains('is-compact') ? $('#project-menu-btn') : box.querySelector('[aria-checked="true"]');
      if (target) target.focus({ preventScroll: true });
    }
  }

  /** Chips, or the compact menu when they do not fit. The container's width does not depend on its content
      (flex: 1 1 0), so this cannot oscillate; re-run on resize (ResizeObserver). */
  function syncProjectFilter() {
    const wrap = $('#project-filter');
    const box = $('#project-chips');
    const btn = $('#project-menu-btn');
    if (box.hidden) { wrap.classList.remove('is-compact'); btn.hidden = true; return; }
    const compact = box.scrollWidth > wrap.clientWidth + 1;
    const focusChip = compact && box.contains(document.activeElement); // the chips are about to disappear
    const focusBtn = !compact && btn === document.activeElement;
    if (!compact && btn.getAttribute('aria-expanded') === 'true') menu.close(false);
    wrap.classList.toggle('is-compact', compact);
    btn.hidden = !compact;
    if (focusChip) btn.focus({ preventScroll: true });
    if (focusBtn) { const c = box.querySelector('[aria-checked="true"]'); if (c) c.focus({ preventScroll: true }); }
  }

  function openProjectMenu(btn) {
    const keys = trackedKeys() || [];
    const cur = effProject();
    menu.open(btn, {
      label: t('projectFilter.label'),
      radio: true,
      items: ['', ...keys],
      isSelected: (k) => k === cur,
      item: (k) => html`<span class="mi-main"><span class="mi-title">${k || t('projectFilter.all')}</span>${k && projectName(k) ? html`<span class="mi-sub">${projectName(k)}</span>` : ''}</span>`,
      onPick: (k) => setProject(k),
      anchorSel: '#project-menu-btn',
    });
  }

  function setProject(key) {
    key = String(key || '');
    if (key === S.project && key === effProject()) return;
    S.project = key;
    prefs.set('project', key);
    menu.close(false);
    renderChips();
    if (S.view === 'board') switchBoard();
    else if (S.view === 'sprints') { S.sp.overrides.clear(); enterSprints(); }
    else if (S.view === 'epics') { S.ep.overrides.clear(); enterEpics(); }
  }

  /** `project_unavailable` for the explicitly selected project (deleted, access lost): fall back to "All projects".
      Returns true when the caller should reload its view. */
  function dropBadProject(e, project) {
    if (!e || e.code !== 'project_unavailable' || !project || S.project !== project) return false;
    S.project = '';
    prefs.set('project', '');
    toast(t('projectFilter.unavailable', { key: project }), { tone: 'warn' });
    renderChips();
    return true;
  }

  function onChipsKey(e) {
    const b = e.target.closest && e.target.closest('[data-project]');
    if (!b || e.ctrlKey || e.metaKey || e.altKey || !['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(e.key)) return;
    e.preventDefault();
    const chips = $$('#project-chips [data-project]');
    const n = chips.length;
    const i = chips.indexOf(b);
    const next = e.key === 'Home' ? 0 : e.key === 'End' ? n - 1 : (i + (e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 1) + n) % n;
    setProject(chips[next].dataset.project); // re-renders the chips and focuses the checked one
  }

  /** Tracked projects arrived or changed: drop a choice that is no longer tracked, re-render the chips and
      reload the current view if what it shows (or is loading) belongs to another project. */
  function onProjectsChanged() {
    const keys = trackedKeys();
    if (keys && S.project && keys.length > 1 && !keys.includes(S.project)) { S.project = ''; prefs.set('project', ''); }
    if (!PROJECT_VIEWS.includes(S.view) || activeSectionId() !== 'view-' + S.view) return;
    renderChips();
    if (S.view === 'board') {
      const shown = S.boardLoading ? S.boardReq : S.board && S.board.key;
      if (shown !== boardKey()) switchBoard(); else renderBoard();
    } else if (S.view === 'sprints') {
      const shown = S.sp.loading ? S.sp.reqKey : S.sp.data && S.sp.data.key;
      if (shown !== spKey()) enterSprints(); else renderSprints();
    } else if (S.view === 'epics') {
      const shown = S.ep.loading ? S.ep.reqKey : S.ep.data && S.ep.data.key;
      if (shown !== epCacheKey()) enterEpics(); else renderEpics();
    }
  }

  /* ================================================================
   * BOARD: "All projects" = v0.2 status columns; a project = all columns of its agile board (or workflow)
   * ================================================================ */
  const boardChoices = () => { const v = prefs.get('boardByProject', {}); return v && typeof v === 'object' && !Array.isArray(v) ? v : {}; };
  const boardIdFor = (project) => (project && boardChoices()[project] != null ? boardChoices()[project] : null);
  /** Identifies what the board shows: scope, project and the board chosen for it. */
  const boardKey = () => { const p = effProject(); const b = boardIdFor(p); return `${S.boardScope}|${p}|${b == null ? '' : b}`; };
  const boardLayouts = {}; // project -> last layout, so the board picker stays put while another board loads

  function normalizeBoard(data, scope, key, project) {
    const h = data && data.hidden;
    const hidden = h && Number(h.count) > 0 ? { count: Number(h.count), projects: Array.isArray(h.projects) ? h.projects.map(String) : [] } : null;
    const l = project && data && data.layout && typeof data.layout === 'object' ? data.layout : null;
    const layout = l ? { source: l.source || 'agile', boardId: l.boardId, boardName: l.boardName || '', boards: Array.isArray(l.boards) ? l.boards.filter((b) => b && b.id != null) : [] } : null;
    return { key, project, layout, columns: normalizeColumns(data), fetchedAt: data && data.fetchedAt, scope: (data && data.scope) || scope, truncated: !!(data && data.truncated), hidden };
  }

  /** ORCH-1: "my" issues filtered out because their project is not tracked (mine scope only); closable for the session. */
  const hiddenSignature = (h) => [...h.projects].sort().join(',');
  function hiddenBanner() {
    const h = S.boardScope === 'mine' && S.board && S.board.hidden;
    if (!h || hiddenSignature(h) === S.hiddenDismissed) return '';
    const MAX = 5;
    const list = h.projects.slice(0, MAX).join(', ') + (h.projects.length > MAX ? ' ' + t('projects.andMore', { n: h.projects.length - MAX }) : '');
    // Singular only for exactly one project (plural rules would also pick "one" for 21), so no plural entry here.
    const np = h.projects.length;
    const where = np === 0 ? t('board.hiddenProjectsNone') : np === 1 ? t('board.hiddenProject', { list }) : t('board.hiddenProjects', { list });
    // DR-19: text and link share one inline box so the link flows after the text when it wraps.
    return html`<div class="notice notice-hidden" role="note">${icon('info')}<span class="notice-text">${t('board.hiddenIssues', { n: h.count })} ${where} <button type="button" class="link-btn" data-action="go-projects">${t('board.hiddenConfigure')}</button></span><button type="button" class="icon-btn icon-btn-sm" data-action="hidden-dismiss" aria-label="${t('board.hiddenDismiss')}" title="${t('board.hiddenDismiss')}">${icon('x')}</button></div>`;
  }
  /** "All projects": tell once (dismissible, remembered) that a project shows all its columns. */
  function projectHint() {
    const keys = trackedKeys();
    if (!S.board || S.board.project || !keys || keys.length < 2 || prefs.get('board.projectHintOff', false) === true) return '';
    return html`<div class="notice notice-hidden notice-tip" role="note">${icon('info')}<span class="notice-text">${t('board.projectHint')}</span><button type="button" class="icon-btn icon-btn-sm" data-action="project-hint-dismiss" aria-label="${t('board.hiddenDismiss')}" title="${t('board.hiddenDismiss')}">${icon('x')}</button></div>`;
  }

  function enterBoard() {
    if (S.board && S.board.key !== boardKey()) { switchBoard(); return; }
    if (!S.board || S.boardStale) loadBoard(false); else renderBoard();
  }

  /** Scope, project or board choice changed: show the cached board for the new choice (if any) and refetch. */
  function switchBoard() {
    if (S.drag) return;
    const key = boardKey();
    if (!S.board || S.board.key !== key) {
      if (S.board) S.boardCache[S.board.key] = S.board;
      S.board = S.boardCache[key] || null;
      delete S.boardCache[key];
      S.boardError = null;
    }
    renderView(false);
    loadBoard(false);
  }

  async function loadBoard(force) {
    if (S.drag || S.pendingKeys.size) { S.boardStale = true; return; }
    const seq = ++S.boardSeq;
    const scope = S.boardScope;
    const project = effProject();
    const boardId = boardIdFor(project);
    const key = boardKey();
    S.boardReq = key;
    S.boardLoading = true;
    S.boardStale = false;
    if (!S.board) S.boardError = null;
    renderBoard();
    renderRefreshState();
    try {
      const params = { scope };
      if (project) params.project = project;
      if (project && boardId != null) params.boardId = boardId;
      if (force) params.force = true;
      const data = await api('getBoard', params);
      if (seq !== S.boardSeq) return;
      if (S.pendingKeys.size || S.drag) { S.boardStale = true; return; } // a move started meanwhile; refetch after it
      S.board = normalizeBoard(data, scope, key, project);
      const l = S.board.layout;
      // FE-CR-6: an empty list may be a temporary Agile API outage, so only a non-empty list proves the board is gone.
      if (l && boardId != null && l.boards.length > 0 && !l.boards.some((b) => String(b.id) === String(boardId))) {
        // FE-CR-4: the remembered board is gone; the backend chose the default. Forget it and keep the key in step.
        const choices = Object.assign({}, boardChoices());
        delete choices[project];
        prefs.set('boardByProject', choices);
        S.board.key = boardKey();
      }
      if (project && S.board.layout) boardLayouts[project] = S.board.layout;
      S.boardError = null;
      S.boardAnimate = true;
      S.transCache.clear();
    } catch (e) {
      if (seq !== S.boardSeq) return;
      if (dropBadProject(e, project)) { S.boardLoading = false; if (S.view === 'board') switchBoard(); return; }
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

  function setBoardScope(scope) {
    if (scope === S.boardScope || S.drag) return;
    S.boardScope = scope;
    prefs.set('board.mine', scope === 'mine');
    switchBoard();
  }

  function afterBoardMutation() {
    if (S.boardStale && !S.drag && !S.pendingKeys.size && S.view === 'board') loadBoard(false);
  }

  /* Board bar under the top bar (static, so focus survives board re-renders): the board picker ("Board: CMA dev
     board ▾") whenever the project has several agile boards, and the note on workflow / issue-status columns. */
  const boardLayout = (project) => (S.board && S.board.project === project && S.board.layout) || boardLayouts[project] || null;
  function currentBoardId(project, l) {
    const b = boardIdFor(project);
    return b != null && l && l.boards.some((x) => String(x.id) === String(b)) ? b : l && l.boardId;
  }
  function renderBoardBar() {
    const bar = $('#board-bar');
    const btn = $('#board-picker');
    const note = $('#board-note');
    const project = S.view === 'board' && activeSectionId() === 'view-board' ? effProject() : '';
    const l = project ? boardLayout(project) : null;
    const boards = l ? l.boards : []; // also when the chosen board fell back to workflow columns
    btn.hidden = boards.length < 2;
    if (btn.hidden) btn.textContent = '';
    else {
      const id = currentBoardId(project, l);
      const cur = boards.find((b) => String(b.id) === String(id));
      const name = cur ? cur.name : l.boardName || '—';
      setHTML(btn, html`<span class="picker-label">${t('board.pickerLabel')}</span><span class="picker-value">${name}</span>${icon('chevron-down', 'icon-xs')}`);
      btn.title = name;
    }
    const source = l && l.source;
    note.hidden = source !== 'workflow' && source !== 'issues';
    if (note.hidden) note.textContent = '';
    else setHTML(note, html`${icon('info', 'icon-sm')}<span>${t(source === 'workflow' ? 'board.layoutWorkflow' : 'board.layoutIssues')}</span>`);
    bar.hidden = btn.hidden && note.hidden;
    menu.sync();
  }
  function openBoardPicker(btn) {
    const project = effProject();
    const l = boardLayout(project);
    if (!project || !l) return;
    const id = currentBoardId(project, l);
    menu.open(btn, {
      label: t('board.pickerLabel'),
      radio: true,
      items: l.boards,
      isSelected: (b) => String(b.id) === String(id),
      item: (b) => html`<span class="mi-main"><span class="mi-title">${b.name}</span>${b.projectKey && b.projectKey !== project ? html`<span class="mi-sub">${t('board.ownerProject', { key: b.projectKey })}</span>` : ''}</span><span class="state-badge" data-state="${b.type}">${t('board.type.' + (b.type === 'kanban' ? 'kanban' : 'scrum'))}</span>`,
      onPick: (b) => {
        if (String(b.id) === String(id) || S.drag) return;
        prefs.set('boardByProject', Object.assign({}, boardChoices(), { [project]: b.id }));
        switchBoard();
      },
      anchorSel: '#board-picker',
    });
  }

  function renderBoard() {
    if (S.view !== 'board' || activeSectionId() !== 'view-board') return;
    if (S.drag) { S.renderDeferred.add('board'); return; }
    const root = $('#board');
    renderBoardBar();
    if (!S.board) {
      if (S.boardError && !S.boardLoading) {
        setHTML(root, stateBlock({ iconName: 'alert', tone: 'error', fill: true, title: t('board.errorTitle'),
          text: errorText(S.boardError), action: { id: 'board-retry', label: t('common.retry') } }));
      } else setHTML(root, kanbanSkeleton());
      menu.sync();
      return;
    }
    const all = S.boardScope === 'all';
    const total = kbIssues(S.board).length;
    // A project board shows its columns even when they are all empty.
    if (!total && !(S.board.project && S.board.columns.length)) {
      setHTML(root, S.board.project
        ? stateBlock({ iconName: 'board', fill: true, title: all ? t('board.emptyAllTitle') : t('board.emptyTitle') })
        : all
          ? html`${projectHint()}${stateBlock({ iconName: 'folder', fill: true, title: t('board.emptyAllTitle'), text: t('board.emptyAllText'), action: { id: 'go-projects', label: t('board.goProjects') } })}`
          : S.board.hidden // DR-18: not "all done" — my issues exist but are filtered out by the tracked projects
            ? html`${hiddenBanner()}${stateBlock({ iconName: 'folder', fill: true, title: t('board.emptyHiddenTitle'), text: t('board.emptyTextHidden'), action: { id: 'go-projects', label: t('board.hiddenConfigure') } })}`
            : html`${projectHint()}${stateBlock({ iconName: 'check-circle', tone: 'good', fill: true, title: t('board.emptyTitle'), text: t('board.emptyText') })}`);
      menu.sync();
      return;
    }
    preserveKanban(root, () => {
      const animate = S.boardAnimate;
      S.boardAnimate = false;
      const { cols, visible } = columnsHTML(S.board, issueMatcher(S.q.board), { prefix: 'b', showAssignee: all, highlightMine: all, animate });
      const trunc = S.board.truncated ? html`<div class="notice" role="note">${icon('info')}<span>${t('board.truncated', { n: total })}</span></div>` : '';
      setHTML(root, html`${hiddenBanner()}${projectHint()}${trunc}${visible === 0 && total ? filterBanner() : ''}<div class="columns${S.board.project ? ' is-project' : ''}${animate ? ' is-entering' : ''}">${cols}</div>`);
    });
  }

  /* ================================================================
   * SPRINTS (SPEC v0.3): Jira-backlog-like list. One collapsible section per sprint (active, future and,
   * optionally, the last closed ones), then the Backlog. Rows: dense issue lines with the status dropdown.
   * getSprintGroups { project, scope, includeClosed, force } → { groups, backlog, truncated, available }.
   * ================================================================ */
  const spKey = () => `${effProject()}|${S.sp.mine ? 'mine' : 'all'}|${S.sp.closed ? 1 : 0}`;
  /** Drops cached sprint lists; answers already in flight are not cached afterwards (as invalidateEpics). */
  function invalidateSprints() {
    S.sp.cache.clear();
    S.sp.cacheGen++;
  }

  function normalizeGroup(g, id) {
    return { id, sprint: id === 'backlog' ? null : g.sprint, issues: Array.isArray(g.issues) ? g.issues.slice() : [],
      progress: g.progress || null, storyPoints: g.storyPoints || null, truncated: !!g.truncated };
  }
  function normalizeSprints(res, key) {
    const r = res && typeof res === 'object' ? res : {};
    const groups = (Array.isArray(r.groups) ? r.groups : []).filter((g) => g && g.sprint && g.sprint.id != null).map((g) => normalizeGroup(g, String(g.sprint.id)));
    const b = r.backlog && Array.isArray(r.backlog.issues) && r.backlog.issues.length ? normalizeGroup(r.backlog, 'backlog') : null;
    return { key, available: r.available !== false, groups, backlog: b, truncated: !!r.truncated, closedUnavailable: !!r.closedUnavailable, fetchedAt: r.fetchedAt };
  }
  const spGroups = (d) => (d ? (d.backlog ? [...d.groups, d.backlog] : d.groups) : []);
  const spGroup = (id) => spGroups(S.sp.data).find((g) => g.id === id) || null;

  function enterSprints(force) {
    const key = spKey();
    const cached = (S.sp.data && S.sp.data.key === key ? S.sp.data : S.sp.cache.get(key)) || null;
    if (cached && !S.sp.stale && !force) {
      ++S.sp.seq; // a request still in flight for other options must not overwrite this view
      S.sp.loading = false;
      S.sp.data = cached;
      S.sp.error = null;
      renderSprints(); renderRefreshState();
      return;
    }
    S.sp.data = cached;
    loadSprints({ force, silent: !!cached });
  }

  async function loadSprints({ force, silent } = {}) {
    if (S.pendingKeys.size) { S.sp.stale = true; renderSprints(); renderRefreshState(); return; } // reloaded when the transition settles
    const key = spKey();
    const gen = S.sp.cacheGen;
    const params = { scope: S.sp.mine ? 'mine' : 'all', includeClosed: S.sp.closed };
    const project = effProject();
    if (project) params.project = project;
    if (force) params.force = true;
    const seq = ++S.sp.seq;
    S.sp.reqKey = key;
    S.sp.loading = true;
    S.sp.stale = false;
    if (!silent) { S.sp.data = null; S.sp.error = null; }
    renderSprints(); renderRefreshState();
    try {
      const data = normalizeSprints(await api('getSprintGroups', params), key);
      if (gen === S.sp.cacheGen) S.sp.cache.set(key, data);
      if (seq !== S.sp.seq) return;
      if (S.pendingKeys.size) { S.sp.stale = true; return; } // a transition started meanwhile; refetch after it
      S.sp.animate = !S.sp.data;
      S.sp.data = data;
      S.sp.error = null;
    } catch (e) {
      if (seq !== S.sp.seq) return;
      if (dropBadProject(e, project)) { S.sp.loading = false; if (S.view === 'sprints') enterSprints(); return; }
      S.sp.error = e;
      if (S.sp.data) toastError(t('sprints.loadFailed'), e);
    } finally {
      if (seq === S.sp.seq) { S.sp.loading = false; renderSprints(); renderRefreshState(); }
    }
  }

  function setSprintsClosed(on) {
    S.sp.closed = on;
    prefs.set('sprints.closed', on);
    enterSprints();
  }

  function sprintStateBadge(state) {
    if (!state) return '';
    return html`<span class="state-badge" data-state="${state}">${t('sprints.state.' + state)}</span>`;
  }
  const dayDiff = (iso) => { const d = parseDate(iso); if (!d) return null; const a = new Date(d); a.setHours(0, 0, 0, 0); const b = new Date(); b.setHours(0, 0, 0, 0); return Math.round((a - b) / 86400000); };
  const shortDate = (iso) => i18n.date(iso, { day: 'numeric', month: 'short' });

  function sprintDates(sp) {
    const range = sp.startDate || sp.endDate ? `${shortDate(sp.startDate) || '…'} – ${shortDate(sp.endDate) || '…'}` : '';
    let rel = '';
    let tone = '';
    if (sp.state === 'active' && sp.endDate) {
      const n = dayDiff(sp.endDate);
      rel = n > 0 ? t('sprints.daysLeft', { n }) : n === 0 ? t('sprints.endsToday') : t('sprints.overdue', { n: -n });
      tone = n < 0 ? 'chip-bad' : 'chip-accent';
    } else if (sp.state === 'future' && sp.startDate) {
      const n = dayDiff(sp.startDate);
      rel = n > 0 ? t('sprints.startsIn', { n }) : t('sprints.notStarted');
    } // closed: the badge and the date range say it all (DR-27)
    return { range, rel, tone };
  }

  /** Expanded? Search hits force sections open (toggles then override for this query); otherwise the
      remembered choice per sprint id; by default active sprints are open (and the Backlog when nothing is open). */
  function spIsOpen(g, forced) {
    if (S.sp.overrides.has(g.id)) return S.sp.overrides.get(g.id);
    if (forced) return true;
    const v = S.sp.expanded[g.id];
    if (typeof v === 'boolean') return v;
    // DR-22: with no active / planned sprint the Backlog is the content, so it starts open.
    if (!g.sprint) return !(S.sp.data && S.sp.data.groups.some((x) => x.sprint.state !== 'closed'));
    return g.sprint.state === 'active';
  }
  function saveSpExpanded() {
    // Integer-like keys enumerate in ascending order, so the oldest sprints are dropped first.
    const keys = Object.keys(S.sp.expanded);
    if (keys.length > 100) keys.slice(0, keys.length - 100).forEach((k) => { delete S.sp.expanded[k]; });
    prefs.set('sprints.expanded', Object.assign({}, S.sp.expanded));
  }
  function toggleSprint(id) {
    const g = spGroup(id);
    if (!g) return;
    const open = !spIsOpen(g, !!String(S.q.sprints || '').trim());
    if (String(S.q.sprints || '').trim()) S.sp.overrides.set(id, open);
    else { S.sp.expanded[id] = open; saveSpExpanded(); }
    S.sp.opening = open ? id : null;
    renderSprints();
  }

  const domId = (s) => String(s).replace(/[^\w-]/g, '_');
  function sprintRow(issue, gid) {
    const pend = S.pendingKeys.has(issue.key);
    const mine = !S.sp.mine && issue.mine;
    return html`<li class="sp-row${catOf(issue.status) === 'done' ? ' is-done' : ''}${mine ? ' is-mine' : ''}${pend ? ' is-pending' : ''}" data-row="${gid}:${issue.key}" data-key="${issue.key}">
      ${typeIcon(issue.type)}<span class="sp-key" aria-hidden="true">${issue.key}</span>
      <span class="sp-main"><button type="button" class="sp-open" data-role="open" data-focus-key="${issue.key}" aria-haspopup="dialog"><span class="sr-only">${issue.key}${mine ? ' · ' + t('issue.mineMark') : ''}: </span><span class="sp-sum" title="${issue.summary}">${issue.summary}</span></button>${epicChip(issue.epic)}</span>
      <span class="sp-st">${statusButton(issue)}</span><span class="sp-prio">${priorityIcon(issue.priority)}</span><span class="sp-pts">${spBadge(issue.storyPoints)}</span><span class="sp-av">${assigneeAvatar(issue.assignee)}</span>
    </li>`;
  }

  function sprintSection(g, rows, { forced, allProjects, i }) {
    const s = g.sprint;
    const open = spIsOpen(g, forced);
    const id = domId(g.id);
    const d = s ? sprintDates(s) : null;
    const keys = allProjects ? [...new Set(g.issues.map(projectOf).filter(Boolean))].sort((a, b) => a.localeCompare(b)) : [];
    const pts = s && g.storyPoints && Number(g.storyPoints.total) > 0 ? { done: fmtNum(g.storyPoints.done), total: fmtNum(g.storyPoints.total) } : null;
    const described = [keys.length ? 'sp-p-' + id : '', 'sp-m-' + id].filter(Boolean).join(' ');
    const head = html`<div class="sp-head" data-sec="${g.id}">
      <h2 class="sp-title"><button type="button" class="sp-toggle" id="sp-t-${id}" data-role="toggle" aria-expanded="${open ? 'true' : 'false'}" aria-controls="sp-b-${id}" aria-describedby="${described}">${icon('chevron-right', 'sp-chev')}<span class="sp-name">${s ? s.name : t('sprints.backlog')}</span>${s ? sprintStateBadge(s.state) : ''}</button></h2>
      ${keys.length ? html`<span class="sp-pkeys" id="sp-p-${id}"><span class="sr-only">${t('sprints.projects', { list: keys.join(', ') })}</span>${keys.map((k) => html`<span class="sp-pkey" aria-hidden="true">${k}</span>`)}</span>` : ''}
      <span class="sp-meta" id="sp-m-${id}">
        ${d && d.range ? html`<span class="sp-dates">${icon('calendar', 'icon-sm')}<span>${d.range}</span></span>` : ''}${d && d.rel ? html`<span class="chip${d.tone ? ' ' + d.tone : ''}">${d.rel}</span>` : ''}
        <span class="sp-count">${t('board.count', { n: g.issues.length })}</span>
        ${s && s.state !== 'closed' ? html`<button type="button" class="icon-btn icon-btn-sm sp-add" data-sp-create="${g.id}" aria-label="${t('cf.createInSprint', { name: s.name })}" title="${t('cf.createInSprint', { name: s.name })}">${icon('plus')}</button>` : ''}
        ${pts ? html`<span class="sp-points" title="${t('sprints.points', pts)}"><span aria-hidden="true">${t('sprints.pointsShort', pts)}</span><span class="sr-only">${t('sprints.points', pts)}</span></span>` : ''}
        ${s && g.progress ? html`<span class="sp-progress">${progressBar(g.progress, { size: 'md' })}</span>` : ''}
      </span>
    </div>`;
    const goal = s && s.goal ? html`<p class="sp-goal${open ? ' is-open' : ''}"${open ? '' : html` title="${s.goal}"`}>${icon('target', 'icon-sm')}<span><span class="sr-only">${t('sprints.goal')}: </span>${s.goal}</span></p>` : '';
    const body = open
      ? html`<div class="sp-body${S.sp.opening === g.id ? ' is-opening' : ''}" id="sp-b-${id}"><ul class="sp-rows" role="list">${rows.map((is) => sprintRow(is, g.id))}</ul>${g.truncated ? html`<p class="sp-foot">${t('sprints.groupTruncated', { n: g.issues.length })}</p>` : ''}</div>`
      : html`<div id="sp-b-${id}" hidden></div>`;
    return html`<section class="sp-sec${open ? ' is-open' : ''}" data-state="${s ? s.state : 'backlog'}" style="--i:${Math.min(i, 8)}">${head}${goal}${body}</section>`;
  }

  const closedNote = () => html`<p class="sp-empty sp-closed-note" role="note">${icon('alert')}<span>${t('sprints.closedUnavailable')}</span></p>`;

  function renderSprints() {
    if (S.view !== 'sprints' || activeSectionId() !== 'view-sprints') return;
    const sp = S.sp;
    const el = $('#sp-list');
    const summary = $('#sp-summary');
    const d = sp.data;
    $('#sp-closed').checked = sp.closed;
    $('#sp-closed-wrap').hidden = !!(d && !d.available);
    if (!d) {
      summary.textContent = '';
      setHTML(el, sp.error && !sp.loading
        ? stateBlock({ iconName: 'alert', tone: 'error', fill: true, title: t('sprints.loadFailed'), text: errorText(sp.error), action: { id: 'sp-retry', label: t('common.retry') } })
        : treeSkeleton());
      menu.sync();
      return;
    }
    if (!d.available) {
      $('#view-tools').hidden = true; // DR-12: nothing to search or filter
      summary.textContent = '';
      setHTML(el, stateBlock({ iconName: 'sprint', fill: true, title: t('sprints.unavailableTitle'), text: t('sprints.unavailableText') }));
      menu.sync();
      return;
    }
    const groups = spGroups(d);
    const total = groups.reduce((n, g) => n + g.issues.length, 0);
    const nSprints = d.groups.length;
    summary.textContent = groups.length ? [nSprints ? t('sprints.sprintsCount', { n: nSprints }) : '', t('board.count', { n: total })].filter(Boolean).join(' · ') : '';
    if (!groups.length) {
      setHTML(el, html`${d.closedUnavailable && sp.closed ? closedNote() : ''}${stateBlock({ iconName: 'sprint', fill: true, title: t(sp.mine ? 'sprints.emptyMineTitle' : 'sprints.emptyTitle'), text: t(sp.mine ? 'sprints.emptyMineText' : 'sprints.emptyText') })}`);
      menu.sync();
      return;
    }
    const q = String(S.q.sprints || '').trim();
    const match = issueMatcher(q);
    const a = document.activeElement;
    const holder = a && el.contains(a) && a.closest('[data-row], [data-sec]');
    const focus = holder ? { sel: holder.dataset.row != null ? `[data-row="${CSS.escape(holder.dataset.row)}"]` : `[data-sec="${CSS.escape(holder.dataset.sec)}"]`, role: a.dataset.role } : null;
    const scroller = $('#sp-scroll');
    const scrollTop = scroller.scrollTop;
    const animate = sp.animate;
    sp.animate = false;
    let shown = 0;
    const sections = groups.map((g, i) => {
      const rows = q ? g.issues.filter(match) : g.issues;
      if (q && !rows.length) return '';
      shown += rows.length;
      return sprintSection(g, rows, { forced: !!q, allProjects: !effProject(), i });
    });
    // The closed-sprint part failed: say so where the closed sections would be (after the open ones, before the backlog).
    if (d.closedUnavailable && sp.closed) sections.splice(d.groups.length, 0, closedNote());
    sp.opening = null;
    const noOpen = !d.groups.some((g) => g.sprint.state !== 'closed')
      ? html`<div class="sp-empty" role="note">${icon('sprint')}<span><strong>${t('sprints.noOpenTitle')}</strong> ${d.backlog ? t('sprints.noOpenText') : ''}</span></div>` : '';
    const trunc = d.truncated ? html`<div class="notice" role="note">${icon('info')}<span>${t('sprints.truncated')}</span></div>` : '';
    setHTML(el, html`${trunc}${q && !shown ? filterBanner() : noOpen}<div class="sp-sections${animate ? ' is-entering' : ''}">${sections}</div>${shown ? html`<p class="tree-hint" aria-hidden="true">${t('sprints.keyboardHint')}</p>` : ''}`);
    scroller.scrollTop = scrollTop;
    if (focus) {
      const box = el.querySelector(focus.sel);
      const target = box && ((focus.role && box.querySelector(`[data-role="${focus.role}"]:not(:disabled)`)) || box.querySelector('[data-role="open"], [data-role="toggle"]'));
      if (target) target.focus({ preventScroll: true });
    }
    menu.sync();
  }

  /** Sprint "+": the project of the sprint's board (agile boards are cached by the backend), else the filter / the
      sprint's issues; the Sprint field is pre-filled. */
  async function createInSprint(g, gid) {
    if (S.cf) return;
    let project = effProject();
    if (!project) {
      try {
        const boards = await api('getAgileBoards');
        const b = (boards || []).find((x) => String(x.id) === String(g.sprint.boardId));
        if (b && b.projectKey) project = b.projectKey;
      } catch { /* fall back to the issues */ }
    }
    if (!project) {
      const counts = new Map();
      g.issues.forEach((is) => { const k = projectOf(is); counts.set(k, (counts.get(k) || 0) + 1); });
      project = [...counts].sort((a, b) => b[1] - a[1]).map((x) => x[0])[0] || '';
    }
    openCreate({ project, sprint: { id: g.sprint.id, name: g.sprint.name }, returnSel: `[data-sp-create="${CSS.escape(String(gid))}"]` });
  }

  function onSprintsClick(e) {
    const add = e.target.closest('[data-sp-create]');
    if (add) {
      const g = spGroups(S.sp.data).find((x) => String(x.id) === add.dataset.spCreate);
      if (g && g.sprint) createInSprint(g, add.dataset.spCreate);
      return;
    }
    const statusBtn = e.target.closest('[data-status-menu]');
    if (statusBtn) { openStatusMenu(statusBtn); return; }
    const toggle = e.target.closest('.sp-toggle');
    if (toggle) { toggleSprint(toggle.closest('[data-sec]').dataset.sec); return; }
    const open = e.target.closest('.sp-open');
    if (open) {
      const row = open.closest('[data-row]');
      const g = spGroup(row.dataset.row.slice(0, row.dataset.row.indexOf(':')));
      openIssue(row.dataset.key, g && g.issues.find((is) => is.key === row.dataset.key));
      return;
    }
    const a = e.target.closest('[data-action]');
    if (!a) return;
    if (a.dataset.action === 'sp-retry') enterSprints(true);
    else if (a.dataset.action === 'clear-filters') { S.q.sprints = ''; $('#search').value = ''; S.sp.overrides.clear(); renderSprints(); }
  }

  /** ↑/↓/Home/End move between section headers and rows; → opens / ← closes a section, ← on a row goes to its header. */
  function onSprintsKey(e) {
    const el = e.target;
    if (e.ctrlKey || e.metaKey || e.altKey || !el.matches || !el.matches('.sp-toggle, .sp-open')) return;
    const items = $$('#sp-list .sp-toggle, #sp-list .sp-open');
    const i = items.indexOf(el);
    const isHead = el.classList.contains('sp-toggle');
    const expanded = isHead && el.getAttribute('aria-expanded') === 'true';
    const go = (x) => { if (x) x.focus(); };
    switch (e.key) {
      case 'ArrowDown': go(items[i + 1]); break;
      case 'ArrowUp': go(items[i - 1]); break;
      case 'Home': go(items[0]); break;
      case 'End': go(items[items.length - 1]); break;
      case 'ArrowRight': if (isHead && !expanded) toggleSprint(el.closest('[data-sec]').dataset.sec); else if (isHead) go(items[i + 1]); break;
      case 'ArrowLeft':
        if (isHead && expanded) toggleSprint(el.closest('[data-sec]').dataset.sec);
        else if (!isHead) go($('.sp-toggle', el.closest('.sp-sec')));
        break;
      default: return;
    }
    e.preventDefault();
  }

  /* ================================================================
   * Transitions: card / drawer dropdown, drag & drop; optimistic everywhere
   * ================================================================ */
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

  function openStatusMenu(btn) {
    const key = btn.dataset.statusMenu;
    if (!key || S.pendingKeys.has(key)) return;
    const inDrawer = !!btn.closest('#drawer');
    const row = btn.closest('[data-row]'); // sprint rows: an issue may be listed in two sprints
    menu.open(btn, {
      label: t('status.menuLabel', { key }),
      load: () => getTransitions(key, true),
      loadingText: t('status.loading'),
      empty: t('drawer.noTransitions'),
      item: (tr) => {
        const to = tr.to || {};
        const differs = String(tr.name || '').toLowerCase() !== String(to.name || '').toLowerCase();
        return html`<span class="mi-dot" data-cat="${catOf(to)}" aria-hidden="true"></span><span class="mi-main"><span class="mi-title">${tr.name || to.name}</span>${differs && to.name ? html`<span class="mi-sub">${icon('arrow-right', 'icon-xs')}${to.name}</span>` : ''}</span>`;
      },
      onPick: (tr) => runTransition(key, tr, null),
      anchorSel: inDrawer ? `#drawer [data-status-menu="${CSS.escape(key)}"]` : `#${activeSectionId()} ${row ? `[data-row="${CSS.escape(row.dataset.row)}"]` : `.card[data-key="${CSS.escape(key)}"]`} [data-status-menu]`,
    });
  }

  /** Every loaded IssueRef-like copy of `key` outside the board model and sprint rows (epic trees, drawer details). */
  function forEachRef(key, fn) {
    const visit = (r) => { if (r && r.key === key) fn(r); };
    const visitStory = (s) => { if (!s) return; visit(s.story); (s.tasks || []).forEach(visit); };
    const ep = S.ep.data;
    if (ep) {
      (ep.epics || []).forEach((n) => { visit(n.epic); (n.stories || []).forEach(visitStory); (n.tasks || []).forEach(visit); });
      (ep.orphanStories || []).forEach(visitStory);
      (ep.stories || []).forEach(visitStory);
    }
    for (const det of detailCache.values()) {
      visit(det.issue); visit(det.epic); visit(det.parent);
      (det.stories || []).forEach(visit); (det.children || []).forEach(visit);
      (det.links || []).forEach((l) => visit(l && l.issue));
    }
  }

  /** Puts `status` on every loaded copy of `key`; returns an undo function. */
  function applyStatus(key, status, patch) {
    const undo = [];
    const model = S.board;
    const f = kbFind(model, key);
    if (f) {
      const prev = { issue: f.issue, idx: f.idx };
      const col = kbColumn(model, status);
      kbPlace(model, key, Object.assign({}, f.issue, patch || {}), (col && col.statuses.find((st) => sameStatus(st, status))) || status, 0);
      undo.push(() => kbPlace(model, key, prev.issue, prev.issue.status, prev.idx));
    }
    spGroups(S.sp.data).forEach((g) => g.issues.forEach((is, i) => {
      if (is.key !== key) return;
      g.issues[i] = Object.assign({}, is, patch || {}, { status });
      undo.push(() => { g.issues[i] = is; });
    }));
    forEachRef(key, (ref) => { const old = ref.status; ref.status = status; undo.push(() => { ref.status = old; }); });
    return () => undo.reverse().forEach((fn) => fn());
  }

  function renderIssueViews() {
    renderBoard();
    renderSprints();
    renderEpics();
    if (S.drawer) renderDrawer();
  }

  /** After a successful status change: other caches are outdated (other board scope, progress, changelog). */
  function afterIssueChanged(key) {
    S.boardCache = {};
    invalidateSprints();
    if (S.view === 'sprints') loadSprints({ silent: !!S.sp.data }); else S.sp.stale = true;
    invalidateEpics();
    if (S.view === 'epics' && S.ep.data) loadEpics({ silent: true }); else S.ep.stale = true;
    detailCache.delete(key);
    if (S.drawer && drawerKey() === key) loadDetail({ silent: true });
  }

  /** Optimistically changes the status everywhere, runs the transition, reverts on error. */
  async function runTransition(key, tr, targetStatus) {
    if (!tr || S.pendingKeys.has(key)) return false;
    const optimistic = targetStatus || tr.to;
    const undo = applyStatus(key, optimistic);
    S.pendingKeys.add(key);
    renderIssueViews();
    try {
      const fresh = await api('transition', { key, transitionId: tr.id });
      S.transCache.delete(key);
      // CR-18: the backend may fall back to a minimal Issue (key/url/project/status); keep known fields then.
      const f = fresh && typeof fresh === 'object' ? fresh : {};
      const status = f.status || optimistic;
      applyStatus(key, status, f.summary ? f : (f.url ? { url: f.url } : null));
      toast(t('board.moved', { key, status: status.name }), { tone: 'success' });
      afterIssueChanged(key);
      return true;
    } catch (e) {
      undo();
      toastError(t('board.transitionFailed', { key }), e);
      return false;
    } finally {
      S.pendingKeys.delete(key);
      renderIssueViews();
      afterBoardMutation();
      // FE-CR-1: also when there is nothing shown yet (filters changed while the transition was pending).
      if (S.sp.stale && S.view === 'sprints' && !S.pendingKeys.size) loadSprints({ silent: !!S.sp.data });
    }
  }

  /* --- drag & drop (board). A column accepts transitions into any of its statuses (SPEC v0.3). --- */
  function colOf(colEl) {
    const name = kanbanOf(colEl);
    const model = name && KANBAN[name].model();
    return model ? model.columns.find((c) => c.id === colEl.dataset.col) || null : null;
  }
  const transitionsInto = (list, col) => list.filter((tr) => tr.to && colHas(col, tr.to));

  function onDragStart(e) {
    const card = e.target.closest && e.target.closest('.card');
    const kb = kanbanOf(card);
    if (!card || !kb) return;
    const model = KANBAN[kb].model();
    const key = card.dataset.key;
    const found = kbFind(model, key);
    if (!found || S.pendingKeys.has(key)) { e.preventDefault(); return; }
    menu.close(false);
    S.drag = { key, from: found.issue.status, fromCol: found.col.id, kb };
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', key);
    card.classList.add('is-dragging');
    const root = card.closest('[data-kanban]');
    $('.columns', root).classList.add('is-dragging');
    const srcCol = card.closest('.col');
    if (srcCol) srcCol.classList.add('is-source');
    // Prefetch transitions to dim columns that cannot be reached.
    const drag = S.drag;
    getTransitions(key).then((list) => {
      if (S.drag !== drag) return;
      $$('.col', root).forEach((c) => {
        const col = colOf(c);
        if (!col || col.id === drag.fromCol) return;
        const blocked = !transitionsInto(list, col).length;
        c.classList.toggle('is-blocked', blocked);
        if (blocked) c.classList.remove('is-over'); // DR-15: pointer may already be inside it
      });
    }, () => {});
  }

  function endDrag() {
    S.drag = null;
    $$('.kanban .is-dragging, .kanban .is-source, .kanban .is-over, .kanban .is-blocked').forEach((el) => el.classList.remove('is-dragging', 'is-source', 'is-over', 'is-blocked'));
    const deferred = [...S.renderDeferred];
    S.renderDeferred.clear();
    deferred.forEach((name) => KANBAN[name].render());
    afterBoardMutation();
  }

  function onDragOver(e) {
    if (!S.drag) return;
    const colEl = e.target.closest && e.target.closest('.col');
    if (!colEl || kanbanOf(colEl) !== S.drag.kb || colEl.classList.contains('is-source')) { S.drag.blockedOver = null; return; }
    e.preventDefault();
    if (colEl.classList.contains('is-blocked')) {
      // Known to have no transition: refuse the drop; dragend explains why with a toast.
      e.dataTransfer.dropEffect = 'none';
      S.drag.blockedOver = colOf(colEl);
      $$('.kanban .col.is-over').forEach((c) => c.classList.remove('is-over'));
      return;
    }
    S.drag.blockedOver = null;
    e.dataTransfer.dropEffect = 'move';
    if (!colEl.classList.contains('is-over')) {
      $$('.kanban .col.is-over').forEach((c) => c.classList.remove('is-over'));
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
    if (blockedOver) toast(t('board.noTransition', { key, from: nameOf(from), to: colLabel(blockedOver) }), { tone: 'warn' });
  }

  /** CR-1: anything dragged in from outside (files, links, text) must never navigate the WebView. */
  function guardForeignDrag(e) {
    if (S.drag) {
      if (e.type === 'drop') e.preventDefault(); // internal card dropped outside a column: no-op
      else if (!(e.target.closest && e.target.closest('.kanban .col'))) S.drag.blockedOver = null;
      return;
    }
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'none';
  }
  /** Only kanban cards may be dragged; blocks links, images and selected text from being dragged out as URLs. */
  function guardDragStart(e) {
    const el = e.target && e.target.nodeType === 1 ? e.target : e.target && e.target.parentElement;
    if (!el || !el.closest('.kanban .card')) e.preventDefault();
  }

  function onDrop(e) {
    if (!S.drag) return;
    const colEl = e.target.closest && e.target.closest('.col');
    if (!colEl || kanbanOf(colEl) !== S.drag.kb) return;
    e.preventDefault();
    const { key, kb } = S.drag;
    const to = colOf(colEl);
    endDrag();
    if (to) moveByDrop(kb, key, to);
  }

  /** Transitions into any of the column's statuses: one ⇒ run it, several ⇒ picker, none ⇒ toast. */
  async function moveByDrop(kb, key, col) {
    const found = kbFind(KANBAN[kb].model(), key);
    if (!found || found.col.id === col.id || S.pendingKeys.has(key)) return;
    const issue = found.issue;
    const label = colLabel(col);
    S.pendingKeys.add(key);
    KANBAN[kb].render();
    let list;
    try {
      list = await getTransitions(key);
    } catch (e) {
      S.pendingKeys.delete(key);
      KANBAN[kb].render();
      toastError(t('board.transitionsFailed', { key }), e);
      afterBoardMutation();
      return;
    }
    const matches = transitionsInto(list, col);
    let tr = matches[0];
    S.pendingKeys.delete(key);
    if (matches.length > 1) {
      KANBAN[kb].render();
      tr = await pickTransition(matches, issue, label);
      if (!tr) { afterBoardMutation(); return; }
    }
    if (!tr) {
      KANBAN[kb].render();
      toast(t('board.noTransition', { key, from: nameOf(issue.status), to: label }), { tone: 'warn' });
      afterBoardMutation();
      return;
    }
    await runTransition(key, tr, col.statuses.find((st) => sameStatus(st, tr.to)) || tr.to);
  }

  function onKanbanClick(e) {
    const statusBtn = e.target.closest('[data-status-menu]');
    if (statusBtn) { openStatusMenu(statusBtn); return; }
    const open = e.target.closest('.card-open');
    if (open) {
      const card = open.closest('.card');
      const kb = kanbanOf(card);
      const f = kbFind(KANBAN[kb].model(), card.dataset.key);
      openIssue(card.dataset.key, f && f.issue);
      return;
    }
    const a = e.target.closest('[data-action]');
    if (!a) return;
    const focusNext = () => {
      const next = $('#board .card [data-role="open"]') || $('#board .state .btn') || $('#search');
      if (next) next.focus({ preventScroll: true });
    };
    switch (a.dataset.action) {
      case 'board-retry': loadBoard(true); break;
      case 'go-projects': showView('projects'); break;
      case 'hidden-dismiss':
        if (S.board && S.board.hidden) S.hiddenDismissed = hiddenSignature(S.board.hidden);
        renderBoard();
        focusNext();
        break;
      case 'project-hint-dismiss':
        prefs.set('board.projectHintOff', true);
        renderBoard();
        focusNext();
        break;
      case 'clear-filters':
        S.q.board = '';
        $('#search').value = '';
        renderBoard();
        break;
    }
  }

  /* ================================================================
   * Dropdown menu (role=menu): transitions and the board picker.
   * Lives at <body> level so it stays usable while the app is inert under the drawer.
   * Keyboard: ↑/↓/Home/End move, Enter/Space pick, Esc/Tab close (focus returns to the trigger).
   * ================================================================ */
  const menu = (() => {
    let cur = null; // { anchor, opts, items, state, error, seq }
    const box = () => $('#menu');

    function position() {
      const m = box();
      const r = cur.anchor.getBoundingClientRect();
      m.style.minWidth = Math.max(Math.round(r.width), 220) + 'px';
      const scrollTop = m.scrollTop; // FE-CR-7: re-measuring below resets the scroll; keep the user's position
      m.style.maxHeight = ''; // natural height, capped by the stylesheet (70vh)
      const below = innerHeight - r.bottom - 14;
      const above = r.top - 14;
      // DR-31: open below unless it does not fit there and there is more room above; scroll only in the space we have.
      const up = m.offsetHeight > below && above > below;
      m.style.maxHeight = Math.max(120, Math.floor(up ? above : below)) + 'px';
      m.scrollTop = scrollTop;
      const mh = m.offsetHeight; const mw = m.offsetWidth;
      let top = up ? r.top - 6 - mh : r.bottom + 6;
      const origin = up ? 'bottom' : 'top';
      top = Math.max(8, Math.min(top, innerHeight - mh - 8));
      const left = Math.max(8, Math.min(r.left, innerWidth - mw - 8));
      m.style.top = Math.round(top) + 'px';
      m.style.left = Math.round(left) + 'px';
      m.dataset.origin = origin;
    }
    const itemEls = () => $$('[role^="menuitem"]', box());
    function focusItem(i) {
      const items = itemEls();
      if (!items.length) { box().focus({ preventScroll: true }); return; }
      const k = ((i % items.length) + items.length) % items.length;
      items[k].focus({ preventScroll: false });
    }
    const selectedIndex = () => (cur.opts.isSelected ? Math.max(0, cur.items.findIndex(cur.opts.isSelected)) : 0);
    function render() {
      const o = cur.opts;
      let body;
      if (cur.state === 'loading') {
        body = html`<div class="menu-state" role="status"><span class="spinner spinner-sm" aria-hidden="true"></span><span>${o.loadingText || t('common.loading')}</span></div>`;
      } else if (cur.state === 'error') {
        body = html`<div class="menu-state menu-error">${icon('alert')}<span>${errorText(cur.error)}</span></div><button type="button" role="menuitem" class="menu-item" data-retry tabindex="-1">${icon('refresh')}<span class="mi-main">${t('common.retry')}</span></button>`;
      } else if (!cur.items.length) {
        body = html`<div class="menu-state">${o.empty || ''}</div>`;
      } else {
        body = cur.items.map((it, i) => html`<button type="button" role="${o.radio ? 'menuitemradio' : 'menuitem'}" class="menu-item" data-i="${i}" tabindex="-1"${o.radio ? html` aria-checked="${o.isSelected(it) ? 'true' : 'false'}"` : ''}>${o.item(it)}${o.radio && o.isSelected(it) ? icon('check', 'mi-check') : ''}</button>`);
      }
      const m = box();
      setHTML(m, html`${body}`);
      m.setAttribute('aria-busy', cur.state === 'loading' ? 'true' : 'false');
      position();
    }
    async function load() {
      const seq = ++cur.seq;
      const mine = cur;
      cur.state = 'loading';
      render();
      try {
        const items = await mine.opts.load();
        if (cur !== mine || seq !== mine.seq) return;
        mine.items = Array.isArray(items) ? items : [];
        mine.state = 'ready';
      } catch (e) {
        if (cur !== mine || seq !== mine.seq) return;
        mine.error = e;
        mine.state = 'error';
      }
      const focusInside = box().contains(document.activeElement);
      render();
      if (focusInside) focusItem(selectedIndex());
    }
    function open(anchor, opts) {
      if (cur && cur.anchor === anchor) { close(true); return; } // second click on the trigger closes
      close(false);
      cur = { anchor, opts, items: [], state: 'ready', error: null, seq: 0 };
      const m = box();
      m.hidden = false;
      m.setAttribute('role', 'menu');
      m.setAttribute('aria-label', opts.label || '');
      m.tabIndex = -1;
      anchor.setAttribute('aria-expanded', 'true');
      anchor.setAttribute('aria-controls', 'menu');
      m.classList.remove('is-open');
      if (Array.isArray(opts.items)) { cur.items = opts.items; render(); focusItem(selectedIndex()); }
      else { load(); m.focus({ preventScroll: true }); }
      void m.offsetWidth; // restart the scale-in animation
      m.classList.add('is-open');
    }
    function close(restoreFocus) {
      if (!cur) return;
      const a = cur.anchor;
      cur = null;
      const m = box();
      m.hidden = true;
      m.classList.remove('is-open');
      m.textContent = '';
      if (a && a.isConnected) {
        a.setAttribute('aria-expanded', 'false');
        if (restoreFocus) a.focus({ preventScroll: true });
      }
    }
    function pick(i) {
      const has = i >= 0 && i < cur.items.length; // items may be falsy ("" = All projects)
      const it = cur.items[i];
      const fn = cur.opts.onPick;
      close(true);
      if (has) fn(it);
    }
    function onKey(e) {
      if (!cur) return;
      const items = itemEls();
      const i = items.indexOf(document.activeElement);
      switch (e.key) {
        case 'ArrowDown': e.preventDefault(); focusItem(i + 1); break;
        case 'ArrowUp': e.preventDefault(); focusItem(i < 0 ? -1 : i - 1); break;
        case 'Home': e.preventDefault(); focusItem(0); break;
        case 'End': e.preventDefault(); focusItem(-1); break;
        case 'Escape': e.preventDefault(); e.stopPropagation(); close(true); break;
        case 'Tab': e.preventDefault(); close(true); break;
        default: return;
      }
    }
    function onClick(e) {
      if (!cur) return;
      if (e.target.closest('[data-retry]')) { load(); box().focus({ preventScroll: true }); return; }
      const b = e.target.closest('[data-i]');
      if (b) pick(Number(b.dataset.i));
    }
    /** Called after re-renders: follow a re-created trigger, or close if it is gone. */
    function sync() {
      if (!cur) return;
      if (!cur.anchor.isConnected) {
        const a = cur.opts.anchorSel && document.querySelector(cur.opts.anchorSel);
        if (!a || a.disabled) { close(false); return; }
        cur.anchor = a;
        a.setAttribute('aria-expanded', 'true');
        a.setAttribute('aria-controls', 'menu');
      }
      position();
    }
    function onOutside(e) {
      if (!cur) return;
      if (box().contains(e.target) || cur.anchor.contains(e.target)) return;
      close(false);
    }
    return { open, close, sync, onKey, onClick, onOutside, isOpen: () => !!cur };
  })();

  /* ================================================================
   * Safe Jira wiki markup subset → DOM (never innerHTML of Jira content).
   * Blocks: h1.–h6., {code}/{noformat}, {quote}, bq., * / # / - lists (nested), ----, paragraphs.
   * Inline: *bold*, _italic_, {{mono}}, [text|url], [url], [ISSUE-1], [~user]; {color} markers dropped;
   * everything else is plain text.
   * ================================================================ */
  const wiki = (() => {
    const ISSUE_KEY = /^[A-Z][A-Z0-9_]+-\d+$/;
    const HTTP_URL = /^https?:\/\/[^\s]+$/i;
    const INLINE = /\{\{([^\n]+?)\}\}|\[([^[\]\n]+)\]|(?<![\p{L}\p{N}*])\*(?=\S)([^\n*]*?\S)\*(?![\p{L}\p{N}*])|(?<![\p{L}\p{N}_])_(?=\S)([^\n_]*?\S)_(?![\p{L}\p{N}_])|\{color(?::[^}\n]*)?\}/gu;
    const mk = (tag, cls) => { const el = document.createElement(tag); if (cls) el.className = cls; return el; };

    function text(parent, s) {
      String(s).split('\\\\').forEach((part, i) => {
        if (i) parent.append(mk('br'));
        if (part) parent.append(document.createTextNode(part));
      });
    }
    function link(parent, inner, raw) {
      if (inner.startsWith('~')) {
        const m = mk('span', 'mention');
        m.textContent = '@' + inner.slice(1).replace(/^accountid:/i, '');
        parent.append(m);
        return;
      }
      const bar = inner.lastIndexOf('|');
      const label = (bar >= 0 ? inner.slice(0, bar) : inner).trim();
      const target = (bar >= 0 ? inner.slice(bar + 1) : inner).trim();
      const b = linkButton(target, label);
      parent.append(b || document.createTextNode(raw));
    }
    /** Link button: issue key ⇒ opens the drawer, http(s) ⇒ openUrl; anything else ⇒ null (caller shows text). */
    function linkButton(target, label) {
      let b = null;
      if (ISSUE_KEY.test(target)) { b = mk('button', 'wiki-link'); b.dataset.openKey = target; }
      else if (HTTP_URL.test(target)) { b = mk('button', 'wiki-link'); b.dataset.url = target; }
      if (!b) return null;
      b.type = 'button';
      b.title = target;
      b.textContent = label || target;
      return b;
    }
    function inline(parent, s) {
      let last = 0;
      for (const m of String(s).matchAll(INLINE)) {
        if (m.index > last) text(parent, s.slice(last, m.index));
        if (m[1] != null) { const c = mk('code', 'wiki-mono'); c.textContent = m[1]; parent.append(c); }
        else if (m[2] != null) link(parent, m[2], m[0]);
        else if (m[3] != null) { const b = mk('strong'); inline(b, m[3]); parent.append(b); }
        else if (m[4] != null) { const it = mk('em'); inline(it, m[4]); parent.append(it); }
        last = m.index + m[0].length;
      }
      if (last < s.length) text(parent, s.slice(last));
    }

    function render(src) {
      const root = mk('div', 'wiki');
      const lines = String(src || '').replace(/\r\n?/g, '\n').split('\n');
      let quote = null;
      let para = null;
      let list = null; // stack of { el, type }
      const into = () => quote || root;
      const reset = () => { para = null; list = null; };
      const paragraph = (s) => {
        if (!para) { para = mk('p'); into().append(para); } else para.append(mk('br'));
        inline(para, s);
      };
      const listItem = (markers, s) => {
        para = null;
        if (!list) list = [];
        const depth = markers.length;
        if (list.length > depth) list.length = depth;
        for (let d = 0; d < depth; d++) {
          const type = markers[d] === '#' ? 'ol' : 'ul';
          if (list[d] && list[d].type === type) continue;
          list.length = d;
          const L = mk(type, 'wiki-list');
          if (d === 0) into().append(L);
          else {
            let li = list[d - 1].el.lastElementChild;
            if (!li) { li = mk('li'); list[d - 1].el.append(li); }
            li.append(L);
          }
          list.push({ el: L, type });
        }
        const li = mk('li');
        inline(li, s);
        list[depth - 1].el.append(li);
      };

      for (let i = 0; i < lines.length; i++) {
        const line = lines[i];
        const tr = line.trim();
        let m;
        if ((m = /^\{(code|noformat)(?::[^}]*)?\}(.*)$/.exec(tr))) {
          reset();
          const close = '{' + m[1] + '}';
          const buf = [];
          let rest = m[2];
          let trailing = '';
          for (;;) {
            const c = rest.indexOf(close);
            if (c >= 0) { buf.push(rest.slice(0, c)); trailing = rest.slice(c + close.length); break; }
            buf.push(rest);
            if (++i >= lines.length) break;
            rest = lines[i];
          }
          const pre = mk('pre', 'wiki-pre');
          const code = mk('code');
          code.textContent = buf.join('\n').replace(/^\n+|\s+$/g, '');
          pre.append(code);
          into().append(pre);
          if (trailing.trim()) paragraph(trailing.trim());
          continue;
        }
        if ((m = /^\{quote\}(.*)$/.exec(tr))) {
          reset();
          if (quote) quote = null;
          else { quote = mk('blockquote', 'wiki-quote'); root.append(quote); }
          let after = m[1];
          if (quote && after.includes('{quote}')) { const [inner, tail] = after.split('{quote}'); if (inner.trim()) paragraph(inner.trim()); quote = null; para = null; after = tail; }
          if (after.trim()) paragraph(after.trim());
          continue;
        }
        if (!tr) { reset(); continue; }
        if ((m = /^h([1-6])\.\s*(.*)$/.exec(tr))) {
          reset();
          const h = mk('h' + Math.min(6, Number(m[1]) + 2), 'wiki-h wiki-h' + m[1]);
          inline(h, m[2]);
          into().append(h);
          continue;
        }
        if ((m = /^bq\.\s+(.*)$/.exec(tr))) {
          reset();
          const q = mk('blockquote', 'wiki-quote');
          const p = mk('p');
          inline(p, m[1]);
          q.append(p);
          into().append(q);
          continue;
        }
        if (/^-{4,}$/.test(tr)) { reset(); into().append(mk('hr')); continue; }
        if ((m = /^([*#]+|-)\s+(.*)$/.exec(tr))) { listItem(m[1] === '-' ? '*' : m[1], m[2]); continue; }
        list = null;
        paragraph(line);
      }
      return root;
    }

    /* ---- HTML bodies (JEditor plugin; SPEC v0.4 §1) ----
       Parsed into an inert DOMParser document (no scripts run, nothing loads) and rebuilt node by node with
       createElement / text nodes. Only the tag name is kept; no attribute is copied except td/th colspan/rowspan
       and ol start. The JEditor panel classes are only read to pick a .desc-panel variant. */
    const HTML_TAG = /<\/?(?:p|div|br|span|b|strong|i|em|u|s|strike|del|ins|code|pre|ul|ol|li|a|img|table|thead|tbody|tfoot|tr|td|th|h[1-6]|hr|blockquote|sub|sup|font)\b[^>]*>/i;
    // Wiki code samples may legitimately contain HTML: they must not switch the whole body to the HTML renderer.
    const WIKI_LITERAL = /\{(code|noformat)(?::[^}\n]*)?\}[\s\S]*?\{\1\}|\{\{[^\n]*?\}\}/g;
    const isHtml = (s) => HTML_TAG.test(String(s).replace(WIKI_LITERAL, ''));
    const KEEP = new Set(['p', 'br', 'b', 'strong', 'i', 'em', 'u', 's', 'del', 'ins', 'sub', 'sup', 'code', 'pre', 'ul', 'ol', 'li',
      'table', 'thead', 'tbody', 'tfoot', 'tr', 'td', 'th', 'hr', 'blockquote']);
    const DROP = new Set(['script', 'style', 'iframe', 'frame', 'frameset', 'object', 'embed', 'form', 'input', 'button', 'select',
      'textarea', 'svg', 'math', 'template', 'noscript', 'link', 'meta', 'head', 'title', 'canvas', 'video', 'audio',
      'colgroup', 'col', 'caption', 'option', 'datalist', 'base', 'param', 'source', 'track', 'picture', 'map', 'area']);
    // Table structure: elements allowed directly inside each part (whitespace text and other elements are dropped there).
    const TABLE_KIDS = { table: ['thead', 'tbody', 'tfoot', 'tr'], thead: ['tr'], tbody: ['tr'], tfoot: ['tr'], tr: ['td', 'th'] };
    const MAX_NODES = 5000;
    const MAX_DEPTH = 64;
    const INT_ATTR = /^\s*\d{1,6}\s*$/;
    const blank = (el) => !el.querySelector('img, .desc-img, .ed-img, hr, table, li, pre') && !/[^\s\u00a0\u200b]/.test(el.textContent);

    function panelVariant(cls) {
      if (/_blue\b/i.test(cls)) return 'info';
      if (/_(yell|red)\w*\b/i.test(cls)) return 'warning';
      return 'neutral';
    }
    function imageButton(src, alt) {
      const label = alt ? t('drawer.imageAlt', { alt }) : t('drawer.image');
      const http = HTTP_URL.test(src);
      const b = mk(http ? 'button' : 'span', 'desc-img');
      if (http) { b.type = 'button'; b.dataset.url = src; b.title = src; }
      const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
      svg.setAttribute('class', 'icon icon-sm');
      svg.setAttribute('aria-hidden', 'true');
      const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
      use.setAttribute('href', '#i-image');
      svg.append(use);
      const span = mk('span');
      span.textContent = label;
      b.append(svg, span);
      return b;
    }

    const XHTML = 'http://www.w3.org/1999/xhtml';
    const C0 = /[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]/g;
    /** Visible text of a parsed subtree: skips dropped elements (script, style, …) and SVG / MathML (CR-2). */
    function plainText(node) {
      let out = '';
      for (let n = node.firstChild; n; n = n.nextSibling) {
        if (n.nodeType === Node.TEXT_NODE) out += n.data;
        else if (n.nodeType === Node.ELEMENT_NODE && n.namespaceURI === XHTML && !DROP.has(n.localName)) out += plainText(n);
      }
      return out;
    }

    /** ed (v0.5 editor mode): the same walk, allow-list and drop-list, but the output is the editor's model (bare
        tags, a[data-href], image chips keeping src/alt/width/height, h3/h4 only, no table wrapper) and the result
        is { root, lossy }: lossy = something meaningful could not be kept (colours/fonts, frames, embedded
        objects, unknown elements with text, sub/sup, invalid spans / numbering, non-http links/images, truncation). */
    function renderHtml(src, ed) {
      const root = mk('div', ed ? '' : 'wiki wiki-html');
      const doc = new DOMParser().parseFromString(String(src), 'text/html');
      let count = 0;
      let full = false;
      let lossy = false;
      const room = () => { if (count >= MAX_NODES) full = true; return !full; };
      const add = (parent, node) => { count++; parent.append(node); return node; };
      const QUIET = /^(script|style|link|meta|head|title|template|noscript|base|colgroup|col|param|source|track)$/;
      const styled = (n) => {
        const st = n.getAttribute('style');
        if (!st || n.localName === 'img' || /(^|\s)(jePanel|userTemplate|image-wrap)/.test(n.getAttribute('class') || '')) return false;
        return /(^|;)\s*(color|background|font)/i.test(st);
      };
      function editorImage(n) {
        const chip = mk('span', 'ed-img');
        chip.setAttribute('contenteditable', 'false');
        const src = String(n.getAttribute('src') || '').trim();
        if (JiraffeEditor.isHttpUrl(src)) chip.dataset.src = src; else lossy = true;
        const alt = String(n.getAttribute('alt') || '').trim().slice(0, 200);
        if (alt) chip.dataset.alt = alt;
        ['width', 'height'].forEach((a) => { const v = JiraffeEditor.dimension(String(n.getAttribute(a) || '').trim().replace(/px$/i, '')); if (v) chip.dataset[a] = v; });
        return chip;
      }

      /** Copies the children of `from` into `to` (an element we created). */
      function kids(from, to, depth, inPre) {
        for (let n = from.firstChild; n && room(); n = n.nextSibling) walk(n, to, depth, inPre);
      }
      function walk(n, out, depth, inPre) {
        const structure = TABLE_KIDS[out.tagName.toLowerCase()];
        if (n.nodeType === Node.TEXT_NODE) {
          if (structure && !/\S/.test(n.data)) return; // whitespace between table rows / cells
          add(out, document.createTextNode(n.data.replace(C0, ''))); // C0 controls (not \t \n \r) dropped (S-F1)
          return;
        }
        if (n.nodeType !== Node.ELEMENT_NODE) return; // comments, processing instructions, CDATA
        const tag = n.localName;
        if (DROP.has(tag) || n.namespaceURI !== XHTML) { if (ed && !QUIET.test(tag)) lossy = true; return; }
        if (ed && styled(n)) lossy = true;
        if (structure && !structure.includes(tag)) {
          // A stray element inside the table structure: keep the rows / cells under it, drop the rest.
          if (!KEEP.has(tag) && depth < MAX_DEPTH) kids(n, out, depth + 1, inPre);
          return;
        }
        if (depth >= MAX_DEPTH) { lossy = true; add(out, document.createTextNode(plainText(n))); return; } // pathological nesting
        if (tag === 'br') { add(out, mk('br')); return; }
        if (tag === 'hr') { add(out, mk('hr')); return; }
        if (tag === 'img' && ed) { add(out, editorImage(n)); return; }
        if (tag === 'img') { add(out, imageButton(String(n.getAttribute('src') || '').trim(), String(n.getAttribute('alt') || '').trim().slice(0, 80))); return; }
        if (tag === 'a' && ed) {
          const href = String(n.getAttribute('href') || '').trim();
          if (!JiraffeEditor.isHttpUrl(href)) { if (href && !href.startsWith('#')) lossy = true; kids(n, out, depth + 1, inPre); return; }
          const a = add(out, mk('a'));
          a.dataset.href = href;
          kids(n, a, depth + 1, inPre);
          return;
        }
        if (tag === 'a') {
          const href = String(n.getAttribute('href') || '').trim();
          const label = plainText(n).replace(/\s+/g, ' ').trim();
          const b = href && (label || !n.querySelector('img')) ? linkButton(href, label) : null;
          if (b) { add(out, b); return; }
          kids(n, out, depth + 1, inPre); // other schemes, anchors, linked images: the content only
          return;
        }
        if (/^h[1-6]$/.test(tag) && ed) { const h = add(out, mk(Number(tag[1]) <= 3 ? 'h3' : 'h4')); kids(n, h, depth + 1, inPre); return; }
        if (/^h[1-6]$/.test(tag)) {
          // Demoted to the drawer scale: h1 → 16 px, h2 → 14 px bold, h3–h6 → 13 px (same classes as wiki h2.–h4.).
          const level = Number(tag[1]);
          const h = add(out, mk('h' + Math.min(6, level + 2), 'wiki-h wiki-h' + Math.min(4, level + 1)));
          kids(n, h, depth + 1, inPre);
          return;
        }
        if (tag === 'div') {
          const cls = String(n.getAttribute('class') || '');
          let el;
          if (/(^|\s)jePanelHeader/.test(cls)) {
            el = mk('div', 'desc-panel-head');
            if (/(^|\s)jePanelHeader_red\b/.test(cls)) el.dataset.tone = 'danger';
          }
          else if (/(^|\s)jePanelContent/.test(cls)) el = mk('div', 'desc-panel-body');
          else if (/(^|\s)jePanel/.test(cls)) { el = mk('div', 'desc-panel'); el.dataset.variant = panelVariant(cls); }
          else el = mk('div'); // unwrapped: no attributes, only the block boundary is kept
          add(out, el);
          kids(n, el, depth + 1, inPre);
          if (blank(el)) { el.remove(); count--; } // empty wrappers and "&nbsp;" blocks
          return;
        }
        if (!KEEP.has(tag) && tag !== 'strike') { // span, font, unknown: unwrapped
          if (ed && (tag === 'font' ? ['color', 'face', 'size'].some((a) => n.hasAttribute(a)) : tag !== 'span' && /\S/.test(plainText(n)))) lossy = true;
          kids(n, out, depth + 1, inPre);
          return;
        }
        if (ed && (tag === 'sub' || tag === 'sup')) { lossy = true; kids(n, out, depth + 1, inPre); return; }
        if (ed) {
          const map = { strike: 's', del: 's', ins: 'u', tfoot: 'tbody', b: 'strong', i: 'em' };
          const e2 = add(out, mk(map[tag] || tag));
          // DR-18: merged cells and list numbering are kept (validated like the backend); invalid values ⇒ lossy
          const keepInt = (attr, max) => {
            const v = n.getAttribute(attr);
            if (v == null || v.trim() === '1') return;
            if (/^[1-9]\d{0,5}$/.test(v.trim()) && Number(v) <= max) e2.setAttribute(attr, v.trim()); else lossy = true;
          };
          if (tag === 'td' || tag === 'th') { keepInt('colspan', 50); keepInt('rowspan', 50); }
          if (tag === 'ol') keepInt('start', 100000);
          kids(n, e2, depth + 1, inPre || tag === 'pre');
          if (tag === 'p' && blank(e2)) { e2.remove(); count--; }
          return;
        }
        const el = mk(tag === 'strike' ? 's' : tag);
        if (tag === 'ul' || tag === 'ol') el.className = 'wiki-list';
        else if (tag === 'pre') el.className = 'wiki-pre';
        else if (tag === 'code' && !inPre) el.className = 'wiki-mono';
        else if (tag === 'blockquote') el.className = 'wiki-quote';
        if (tag === 'ol' && INT_ATTR.test(n.getAttribute('start') || '')) el.setAttribute('start', String(Number(n.getAttribute('start'))));
        if (tag === 'td' || tag === 'th') {
          ['colspan', 'rowspan'].forEach((a) => {
            const v = n.getAttribute(a);
            if (v != null && INT_ATTR.test(v) && Number(v) >= 1 && Number(v) <= 50) el.setAttribute(a, String(Number(v)));
          });
        }
        if (tag === 'table') {
          const wrap = add(out, mk('div', 'desc-table-wrap')); // made focusable by syncTableScroll() when it scrolls
          add(wrap, el);
        } else add(out, el);
        kids(n, el, depth + 1, inPre || tag === 'pre');
        // JEditor separates blocks with "&nbsp;" paragraphs: drop empty ones (spacing comes from CSS).
        if (tag === 'p' && blank(el)) { el.remove(); count--; }
      }

      kids(doc.body, root, 0, false);
      // Collapse <br> runs (max two in a row) and trim <br> at the edges of every block (no tall blank gaps).
      // "Edge" is judged at the nearest block (CR-1): <b>line<br></b>next keeps its break.
      const BLOCK = /^(DIV|P|UL|OL|LI|TD|TH|TABLE|PRE|BLOCKQUOTE|HR|H\d)$/;
      const sib = (node, dir) => { let x = node[dir]; while (x && x.nodeType === Node.TEXT_NODE && !/[^\s\u00a0]/.test(x.data)) x = x[dir]; return x; };
      /** The nearest non-blank node in `dir` within the same block, climbing out of inline parents; null at a block edge. */
      const near = (node, dir) => {
        for (let x = node; x && x !== root; x = x.parentNode) {
          const y = sib(x, dir);
          if (y) return y;
          if (x.parentNode === root || BLOCK.test(x.parentNode.nodeName)) return null;
        }
        return null;
      };
      root.querySelectorAll('br').forEach((br) => {
        const prev = near(br, 'previousSibling');
        const next = near(br, 'nextSibling');
        const before = prev && prev.nodeName === 'BR' ? sib(prev, 'previousSibling') : null;
        if (!prev || !next || BLOCK.test(prev.nodeName) || BLOCK.test(next.nodeName) || (before && before.nodeName === 'BR')) br.remove();
      });
      if (full && !ed) { const more = mk('p', 'desc-more'); more.textContent = '…'; root.append(more); }
      return ed ? { root, lossy: lossy || full } : root;
    }

    return { render: (src) => (isHtml(src || '') ? renderHtml(src) : render(src)), isHtml, sanitizeForEditor: (src) => renderHtml(src, true) };
  })();

  /* ================================================================
   * ISSUE DRAWER: header (type, key, summary, status dropdown, open in Jira),
   * tabs Details / Comments / History, related issues navigable with a back stack.
   * ================================================================ */
  const detailCache = new Map(); // key -> IssueDetail, for back navigation; cleared when the drawer closes
  const drawerKey = () => (S.drawer ? S.drawer.stack[S.drawer.stack.length - 1].key : null);

  function findIssueAnywhere(key) {
    const f = kbFind(S.board, key);
    if (f) return f.issue;
    let hit = null;
    spGroups(S.sp.data).forEach((g) => { if (!hit) hit = g.issues.find((is) => is.key === key) || null; });
    if (hit) return hit;
    forEachRef(key, (r) => { if (!hit) hit = r; });
    return hit;
  }

  function normalizeDetail(d) {
    const out = Object.assign({}, d || {});
    ['labels', 'components', 'fixVersions', 'sprints', 'stories', 'children', 'links', 'comments', 'changelog'].forEach((k) => { if (!Array.isArray(out[k])) out[k] = []; });
    out.issue = out.issue || {};
    out.timeTracking = out.timeTracking || {};
    return out;
  }

  /** returnFocus: optional () => Element to focus when the drawer closes (default: the issue's row in the view). */
  function openIssue(key, seed, { push = false, returnFocus = null } = {}) {
    if (!key) return;
    menu.close(false);
    const d = S.drawer;
    if (d) {
      if (drawerKey() === key) return;
      if (push) d.stack.push({ key, seed });
      else d.stack = [{ key, seed }];
      d.tab = 'details';
      loadDetail({ navigated: true });
      return;
    }
    S.drawer = { stack: [{ key, seed }], detail: null, loading: true, error: null, tab: 'details', seq: 0 };
    renderDrawer();
    layers.open($('#drawer'), {
      focus: '#drawer-title',
      onClose: () => { S.drawer = null; detailCache.clear(); menu.close(false); },
      restoreFocus: () => (returnFocus && returnFocus()) || $(`#${activeSectionId()} [data-focus-key="${CSS.escape(key)}"]`),
    });
    loadDetail();
  }

  function drawerBack() {
    const d = S.drawer;
    if (!d || d.stack.length < 2) return;
    d.stack.pop();
    d.tab = 'details';
    loadDetail({ navigated: true });
  }

  /** Drawer "Edit": the edit meta is fetched on click only (no prefetch on drawer open). No fields ⇒ no permission:
      remembered for the issue for this session, so the button is shown disabled without another request. */
  async function editFromDrawer() {
    const d = S.drawer;
    const key = drawerKey();
    if (!d || !key || S.cf || d.editLoading) return;
    if (S.noEditKeys.has(key)) { toast(t('cf.noPermission')); return; }
    d.editLoading = true;
    renderDrawer(); // spinner; the button stays focusable (aria-busy), clicks are ignored meanwhile
    let meta = null;
    try {
      meta = await api('getEditMeta', { key });
    } catch (e) {
      toastError(t('cf.editLoadFailed'), e);
    } finally {
      d.editLoading = false;
    }
    if (S.drawer !== d || drawerKey() !== key) return;
    if (meta && !(Array.isArray(meta.fields) && meta.fields.length)) {
      S.noEditKeys.add(key);
      toast(t('cf.noPermission'));
      meta = null;
    }
    renderDrawer(); // the button: busy off, or disabled for this issue
    if (meta) openEdit(key, meta);
  }

  async function loadDetail({ navigated, silent } = {}) {
    const d = S.drawer;
    if (!d) return;
    const key = drawerKey();
    const seq = ++d.seq;
    const cached = !silent && detailCache.get(key);
    if (cached) { d.detail = cached; d.loading = false; d.error = null; renderDrawer(navigated); return; }
    if (!silent) { d.detail = null; d.error = null; }
    d.loading = true;
    renderDrawer(navigated);
    try {
      const det = normalizeDetail(await api('getIssue', { key }));
      if (S.drawer !== d || seq !== d.seq) return;
      d.detail = det;
      d.error = null;
      detailCache.set(key, det);
    } catch (e) {
      if (S.drawer !== d || seq !== d.seq) return;
      if (!silent || !d.detail) d.error = e;
    }
    d.loading = false;
    renderDrawer();
  }

  /* --- drawer pieces --- */
  function personHTML(user, emptyKey) {
    if (!user || !(user.displayName || user.name)) return html`<span class="muted">${t(emptyKey || 'issue.unassigned')}</span>`;
    const name = user.displayName || user.name;
    return html`<span class="person">${avatar(name, 'avatar-xs')}<span>${name}</span></span>`;
  }

  function relRow(ref) {
    if (!ref || !ref.key) return '';
    const done = catOf(ref.status) === 'done';
    const tone = ref.color || ref.name ? epicTone(ref.color, ref.key) : null;
    return html`<li><button type="button" class="rel-row${ref.mine ? ' is-mine' : ''}${done ? ' is-done' : ''}" data-open-key="${ref.key}"${tone ? html` data-epic="${tone}"` : ''}>
      ${ref.type ? typeIcon(ref.type) : ''}<span class="card-key">${ref.key}</span><span class="rel-sum">${ref.name && ref.name !== ref.summary ? html`<strong>${ref.name}</strong> · ` : ''}${ref.summary || ''}</span>${ref.assignee ? assigneeAvatar(ref.assignee) : ''}${statusPill(ref.status)}
    </button></li>`;
  }
  function relSection(id, title, refs) {
    if (!refs || !refs.length) return '';
    return html`<section class="d-sec" aria-labelledby="${id}"><h3 id="${id}">${title}<span class="d-count">${refs.length}</span></h3><ul class="rel-list" role="list">${refs.map(relRow)}</ul></section>`;
  }

  function estimatesHTML(det) {
    const tt = det.timeTracking || {};
    const num = (v) => (v == null || v === '' ? null : Number(v));
    const orig = num(tt.originalSeconds); const rem = num(tt.remainingSeconds); const spent = num(tt.spentSeconds);
    const sp = det.storyPoints != null ? det.storyPoints : det.issue.storyPoints;
    const points = [['drawer.storyPoints', sp], ['drawer.devStoryPoints', det.devStoryPoints], ['drawer.qaStoryPoints', det.qaStoryPoints]]
      .filter(([, v]) => v != null && v !== '' && Number.isFinite(Number(v)));
    if (orig == null && rem == null && spent == null && !points.length) return '';
    const max = Math.max(orig || 0, (spent || 0) + (rem || 0), 1);
    const row = (cls, label, sec, str) => (sec == null && !str ? '' : html`<div class="est-row ${cls}">
      <span class="est-label">${label}</span>
      <span class="est-track" aria-hidden="true"><span class="est-fill" style="--w:${(Math.min(sec || 0, max) / max * 100).toFixed(2)}%"></span></span>
      <span class="est-val">${sec != null ? dur(sec) : str}</span></div>`);
    return html`<section class="d-sec" aria-labelledby="d-est"><h3 id="d-est">${t('drawer.estimates')}</h3>
      <div class="estimates">
        ${row('est-orig', t('drawer.estOriginal'), orig, tt.original)}
        ${row('est-spent', t('drawer.estSpent'), spent, tt.spent)}
        ${row('est-rem', t('drawer.estRemaining'), rem, tt.remaining)}
        ${points.map(([key, v]) => html`<div class="est-row est-sp"><span class="est-label">${t(key)}</span><span class="est-sp-val">${spBadge(Number(v), t(key))}</span></div>`)}
      </div></section>`;
  }

  function chipsSection(id, title, values) {
    const list = (values || []).map((v) => nameOf(v)).filter(Boolean);
    if (!list.length) return '';
    return html`<div class="tagline"><dt id="${id}">${title}</dt><dd><ul class="tag-chips" role="list" aria-labelledby="${id}">${list.map((v) => html`<li class="chip">${v}</li>`)}</ul></dd></div>`;
  }

  function childrenTitle(det) {
    switch (kindOf(det.issue.type)) {
      case 'epic': return t('drawer.childrenEpic');
      case 'story': return t('drawer.childrenStory');
      default: return t('drawer.subtasks');
    }
  }

  function detailsHTML(det) {
    const is = det.issue;
    const links = new Map();
    det.links.forEach((l) => { if (!l || !l.issue) return; const r = l.relation || t('drawer.related'); if (!links.has(r)) links.set(r, []); links.get(r).push(l.issue); });
    const sprints = det.sprints.length ? html`<section class="d-sec" aria-labelledby="d-sprints"><h3 id="d-sprints">${t('drawer.sprints')}</h3><ul class="sprint-chips" role="list">${det.sprints.map((s) => html`<li class="sprint-chip">${icon('sprint', 'icon-xs')}<span>${s.name}</span>${sprintStateBadge(s.state)}</li>`)}</ul></section>` : '';
    const dateRow = (label, iso, rel) => (iso ? html`<div><dt>${label}</dt><dd>${i18n.dateTime(iso)}${rel ? html` · ${relSpan(iso)}` : ''}</dd></div>` : '');
    return html`
      <section class="d-sec" aria-labelledby="d-desc"><h3 id="d-desc">${t('drawer.description')}</h3>
        ${det.description && det.description.trim() ? html`<div data-wiki="desc"></div>` : html`<p class="empty-line">${t('drawer.noDescription')}</p>`}
      </section>
      <dl class="props props-grid">
        <div><dt>${t('issue.assignee')}</dt><dd>${personHTML(is.assignee)}</dd></div>
        <div><dt>${t('issue.reporter')}</dt><dd>${personHTML(det.reporter, 'issue.nobody')}</dd></div>
        <div><dt>${t('issue.priority')}</dt><dd>${nameOf(det.priority || is.priority) ? html`<span class="person">${priorityIcon(det.priority || is.priority)}<span>${nameOf(det.priority || is.priority)}</span></span>` : '—'}</dd></div>
        <div><dt>${t('issue.type')}</dt><dd>${is.type ? typeBadge(is.type) : '—'}</dd></div>
        <div><dt>${t('issue.project')}</dt><dd>${projectOf(is) || '—'}</dd></div>
        ${det.resolution ? html`<div><dt>${t('issue.resolution')}</dt><dd>${nameOf(det.resolution)}</dd></div>` : ''}
        ${det.dueDate ? html`<div><dt>${t('issue.due')}</dt><dd>${i18n.date(det.dueDate.length <= 10 ? det.dueDate + 'T12:00:00' : det.dueDate)}</dd></div>` : ''}
        ${dateRow(t('issue.created'), det.created, false)}
        ${dateRow(t('issue.updated'), det.updated || is.updated, true)}
        ${det.watchers != null ? html`<div><dt>${t('issue.watchers')}</dt><dd>${det.watchers}</dd></div>` : ''}
      </dl>
      ${estimatesHTML(det)}
      ${sprints}
      ${det.epic ? relSection('d-epic', t('drawer.epic'), [det.epic]) : ''}
      ${det.parent ? relSection('d-parent', t('drawer.parent'), [det.parent]) : ''}
      ${relSection('d-stories', t('drawer.stories'), det.stories)}
      ${relSection('d-children', childrenTitle(det), det.children)}
      ${links.size ? html`<section class="d-sec" aria-labelledby="d-links"><h3 id="d-links">${t('drawer.links')}<span class="d-count">${det.links.length}</span></h3>
        ${[...links].map(([rel, refs]) => html`<h4 class="d-sub">${rel}</h4><ul class="rel-list" role="list">${refs.map(relRow)}</ul>`)}</section>` : ''}
      ${det.labels.length || det.components.length || det.fixVersions.length ? html`<dl class="taglines">
        ${chipsSection('d-labels', t('drawer.labels'), det.labels)}
        ${chipsSection('d-components', t('drawer.components'), det.components)}
        ${chipsSection('d-versions', t('drawer.versions'), det.fixVersions)}
      </dl>` : ''}`;
  }

  function commentsHTML(det) {
    if (!det.comments.length) return stateBlock({ iconName: 'message', title: t('drawer.noComments') });
    return html`<ol class="comments" role="list">${det.comments.map((c, i) => {
      const who = (c.author && (c.author.displayName || c.author.name)) || t('issue.nobody');
      const edited = c.updated && c.created && c.updated !== c.created;
      return html`<li class="comment">${avatar(who, 'avatar-sm')}<div class="comment-main">
        <div class="comment-head"><span class="comment-author">${who}</span>${relSpan(c.created, '', 'comment-time')}${edited ? html`<span class="muted">· ${t('drawer.edited')}</span>` : ''}</div>
        <div data-wiki="c${i}"></div></div></li>`;
    })}</ol>`;
  }

  const FIELD_KEYS = { status: 'issue.status', assignee: 'issue.assignee', priority: 'issue.priority', resolution: 'issue.resolution', summary: 'drawer.fieldSummary', description: 'drawer.description', labels: 'drawer.labels', component: 'drawer.components', 'fix version': 'drawer.versions', sprint: 'drawer.sprintField', 'story points': 'drawer.storyPoints', 'epic link': 'drawer.epic', link: 'drawer.links', issuetype: 'issue.type', reporter: 'issue.reporter', duedate: 'issue.due', timeestimate: 'drawer.estRemaining', timeoriginalestimate: 'drawer.estOriginal', timespent: 'drawer.estSpent' };
  function fieldName(f) {
    const k = FIELD_KEYS[String(f || '').toLowerCase()];
    return k ? t(k) : String(f || '');
  }
  function historyHTML(det) {
    if (!det.changelog.length) return stateBlock({ iconName: 'history', title: t('drawer.noHistory') });
    return html`<ol class="changes" role="list">${det.changelog.map((ch) => {
      const who = (ch.author && (ch.author.displayName || ch.author.name)) || t('issue.nobody');
      return html`<li class="change">${avatar(who, 'avatar-sm')}<div class="change-main">
        <div class="comment-head"><span class="comment-author">${who}</span>${relSpan(ch.created, '', 'comment-time')}</div>
        <ul class="change-items" role="list">${(ch.items || []).map((it) => html`<li><span class="ch-field">${fieldName(it.field)}</span><span class="ch-vals"><span class="ch-from">${it.from || '—'}</span>${icon('arrow-right', 'icon-xs')}<span class="ch-to">${it.to || '—'}</span></span></li>`)}</ul>
      </div></li>`;
    })}</ol>`;
  }

  function fillWiki(el, det) {
    const desc = $('[data-wiki="desc"]', el);
    if (desc) desc.replaceWith(wiki.render(det.description));
    $$('[data-wiki^="c"]', el).forEach((slot) => {
      const c = det.comments[Number(slot.dataset.wiki.slice(1))];
      slot.replaceWith(wiki.render(c ? c.body : ''));
    });
    syncTableScroll(el);
  }

  /** DR-4: a description table is a focusable, labelled region only while it scrolls horizontally. */
  function syncTableScroll(root) {
    $$('.desc-table-wrap', root).forEach((w) => {
      if (w.scrollWidth > w.clientWidth + 1) {
        w.tabIndex = 0;
        w.setAttribute('role', 'region');
        w.setAttribute('aria-label', t('drawer.table'));
      } else {
        w.removeAttribute('tabindex');
        w.removeAttribute('role');
        w.removeAttribute('aria-label');
      }
    });
  }

  const DRAWER_TABS = ['details', 'comments', 'history'];
  function renderDrawer(navigated) {
    const d = S.drawer;
    if (!d) return;
    const el = $('#drawer');
    const top = d.stack[d.stack.length - 1];
    const det = d.detail;
    const is = Object.assign({ key: top.key }, top.seed || {}, findIssueAnywhere(top.key) || {}, det ? det.issue : {});
    const active = document.activeElement;
    const hadFocus = el.contains(active);
    const focusSel = hadFocus && !navigated ? (active.id ? '#' + CSS.escape(active.id) : active.dataset.action ? `[data-action="${active.dataset.action}"]` : active.dataset.statusMenu ? '[data-status-menu]' : null) : null;
    const prevBody = $('.drawer-body', el);
    const scrollTop = prevBody && !navigated ? prevBody.scrollTop : 0;

    let body;
    if (!det && d.error) {
      body = html`<div class="inline-error">${icon('alert')}<span>${errorText(d.error)}</span><button type="button" class="btn btn-sm" data-action="detail-retry">${t('common.retry')}</button></div>`;
    } else if (!det) {
      body = html`<div class="d-skeleton" role="status" aria-label="${t('common.loading')}">${[90, 75, 82, 40].map((w) => html`<span class="skeleton sk-line" style="width:${w}%"></span>`)}<span class="skeleton" style="height:120px"></span></div>`;
    } else if (d.tab === 'comments') body = commentsHTML(det);
    else if (d.tab === 'history') body = historyHTML(det);
    else body = detailsHTML(det);

    const prev = d.stack.length > 1 ? d.stack[d.stack.length - 2] : null;
    const tabLabel = (tab) => tab === 'comments' && det ? t('drawer.tabCommentsN', { n: det.comments.length }) : t('drawer.tab.' + tab);
    setHTML(el, html`<div class="drawer-panel" data-type="${typeVisual(is.type)}">
      <header class="drawer-head">
        ${prev ? html`<button type="button" class="icon-btn" data-action="back" aria-label="${t('drawer.back', { key: prev.key })}" title="${t('drawer.back', { key: prev.key })}">${icon('arrow-left')}</button>` : ''}
        <div class="drawer-crumbs">${is.type ? typeIcon(is.type) : ''}<span class="card-key">${is.key}</span>${d.loading && det ? html`<span class="spinner spinner-sm" aria-hidden="true"></span>` : ''}</div>
        <div class="drawer-head-end">
          ${det ? (() => { const no = S.noEditKeys.has(top.key); return html`<button type="button" class="icon-btn" data-action="edit" aria-label="${no ? t('cf.noPermission') : t('drawer.edit')}" title="${no ? t('cf.noPermission') : t('drawer.editTitle')}"${no ? html` aria-disabled="true"` : ''}${d.editLoading ? html` aria-busy="true"` : ''}>${d.editLoading ? html`<span class="spinner spinner-sm" aria-hidden="true"></span>` : icon('pencil')}</button>`; })() : ''}
          <button type="button" class="icon-btn" data-action="open-jira" aria-label="${t('common.openInJira')}" title="${t('common.openInJira')}" ${is.url ? '' : html`disabled`}>${icon('external')}</button>
          <button type="button" class="icon-btn" data-action="close" aria-label="${t('common.close')}" title="${t('common.closeEsc')}">${icon('x')}</button>
        </div>
      </header>
      <div class="drawer-hero">
        <h2 class="drawer-title" id="drawer-title" tabindex="-1">${is.summary || is.key}</h2>
        <div class="drawer-status">${is.status ? statusButton(is) : ''}${is.epic ? epicChip(is.epic) : ''}${spBadge(is.storyPoints)}</div>
      </div>
      <div class="tabs" role="tablist" aria-label="${t('drawer.tabsLabel')}">${DRAWER_TABS.map((tab) => html`<button type="button" role="tab" class="tab" id="dtab-${tab}" data-tab="${tab}" aria-selected="${d.tab === tab ? 'true' : 'false'}" aria-controls="dpanel" tabindex="${d.tab === tab ? '0' : '-1'}">${tabLabel(tab)}</button>`)}</div>
      <div class="drawer-body" id="dpanel" role="tabpanel" aria-labelledby="dtab-${d.tab}" tabindex="0" aria-busy="${d.loading ? 'true' : 'false'}">${body}</div>
    </div>`);
    if (det) fillWiki(el, det);
    const nb = $('.drawer-body', el);
    if (nb) nb.scrollTop = scrollTop;
    if (navigated) $('#drawer-title').focus({ preventScroll: true });
    else if (hadFocus) {
      const target = (focusSel && el.querySelector(focusSel)) || $('#drawer-title');
      if (target && !target.disabled) target.focus({ preventScroll: true }); else $('#drawer-title').focus({ preventScroll: true });
    }
    menu.sync();
  }

  function selectTab(tab, focus) {
    const d = S.drawer;
    if (!d || !DRAWER_TABS.includes(tab)) return;
    d.tab = tab;
    renderDrawer();
    if (focus) { const b = $(`#dtab-${tab}`); if (b) b.focus(); }
  }

  function seedFromDetail(key) {
    const det = S.drawer && S.drawer.detail;
    if (!det) return null;
    const pool = [det.epic, det.parent, ...det.stories, ...det.children, ...det.links.map((l) => l && l.issue)];
    return pool.find((r) => r && r.key === key) || null;
  }

  function onDrawerClick(e) {
    const d = S.drawer;
    if (!d) return;
    const statusBtn = e.target.closest('[data-status-menu]');
    if (statusBtn) { openStatusMenu(statusBtn); return; }
    const tab = e.target.closest('[data-tab]');
    if (tab) { selectTab(tab.dataset.tab, true); return; }
    const rel = e.target.closest('[data-open-key]');
    if (rel) { openIssue(rel.dataset.openKey, seedFromDetail(rel.dataset.openKey), { push: true }); return; }
    const url = e.target.closest('[data-url]');
    if (url) { openUrl(url.dataset.url); return; }
    const a = e.target.closest('[data-action]');
    if (!a) return;
    switch (a.dataset.action) {
      case 'close': layers.close($('#drawer')); break;
      case 'back': drawerBack(); break;
      case 'detail-retry': loadDetail(); break;
      case 'edit':
        if (a.getAttribute('aria-disabled') === 'true') toast(t('cf.noPermission'));
        else editFromDrawer();
        break;
      case 'open-jira': {
        const cur = (d.detail && d.detail.issue) || findIssueAnywhere(drawerKey()) || {};
        openUrl(cur.url);
        break;
      }
    }
  }

  function onDrawerKey(e) {
    const tab = e.target.closest && e.target.closest('[role="tab"]');
    if (tab) {
      const i = DRAWER_TABS.indexOf(tab.dataset.tab);
      const go = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: DRAWER_TABS.length - 1 }[e.key];
      if (go != null) { e.preventDefault(); selectTab(DRAWER_TABS[(go + DRAWER_TABS.length) % DRAWER_TABS.length], true); }
      return;
    }
    if (e.key === 'Backspace' && e.altKey) { e.preventDefault(); drawerBack(); }
  }

  async function openUrl(url) {
    if (!url) return;
    try { await api('openUrl', { url }); } catch (e) { toastError(t('common.openFailed'), e); }
  }

  /* ================================================================
   * CREATE / EDIT ISSUE (SPEC v0.5): one dialog for both. Fields come from getCreateMeta / getEditMeta and are
   * rendered generically by kind; values are FieldValues (the backend maps them to Jira JSON). Edit sends only the
   * changed fields with expectedUpdated. The HTML description uses the rich-text editor (editor.js) fed by the
   * v0.4 sanitizer; wiki descriptions stay raw markup with a preview.
   * ================================================================ */
  const CF_WIDE = new Set(['summary', 'description', 'textarea', 'labels', 'multiselect', 'components', 'versions', 'users', 'unsupported', 'estimate']);
  const CF_LOCAL = new Set(['multiselect', 'components', 'versions']);
  const CF_MULTI = new Set(['multiselect', 'components', 'versions', 'users']);
  const CF_KEEP_ON_SWITCH = ['summary', 'description', 'labels', 'assignee']; // SPEC: project / type change keeps these
  const DURATION = /^\s*(\d+(?:[.,]\d+)?\s*[wdhm]\s*)+$/i;
  const cfId = (fid) => 'cf-' + domId(fid);
  /** A number as typed back: no grouping, full precision, the locale's decimal separator (1234 → "1234", 0.25 → "0,25"). */
  const cfNumText = (v) => (v == null || v === '' ? '' : nf({ useGrouping: false, maximumFractionDigits: 20 }).format(Number(v)));
  /** Parses typed numbers: spaces / NBSP (grouping) ignored, "," or "." as the decimal separator. */
  function cfParseNum(text) {
    const s1 = String(text).replace(/[\s\u00a0\u202f]/g, '').replace(',', '.');
    return /^-?\d+(\.\d+)?$/.test(s1) ? Number(s1) : NaN;
  }
  const cfField = (fid) => S.cf && S.cf.fields.find((f) => f.id === fid);
  const isEmptyVal = (v) => v == null || v === '' || (Array.isArray(v) && !v.length) || (typeof v === 'object' && !Array.isArray(v) && !Object.values(v).some((x) => x != null && x !== ''));
  const me = () => (S.appState && S.appState.user) || null;

  /** Text as compared / sent: CRLF → LF (Jira Server answers with CRLF, a textarea gives LF); one-line kinds trimmed,
      multi-line kinds lose trailing whitespace only. */
  const cfText = (kind, v) => {
    const s1 = String(v == null ? '' : v).replace(/\r\n?/g, '\n');
    return kind === 'textarea' || kind === 'description' ? s1.replace(/\s+$/, '') : s1.trim();
  };
  /** Canonical form of a value for comparisons (edit: changed fields only; conflict reload). */
  function cfCanon(kind, v) {
    if (kind === 'summary' || kind === 'text' || kind === 'epicName' || kind === 'textarea' || kind === 'description') return cfText(kind, v);
    if (isEmptyVal(v)) return '';
    switch (kind) {
      case 'user': return v.name || '';
      case 'users': return v.map((u) => u.name).sort().join('\n');
      case 'epic': case 'parent': return v.key || '';
      case 'select': case 'priority': return v.id != null ? String(v.id) : '';
      case 'storyPoints': return typeof v === 'object' ? 'id:' + v.id : String(Number(v));
      case 'multiselect': case 'components': case 'versions': case 'labels': return [...v].map(String).sort().join('\n');
      case 'sprint': case 'number': return String(Number(v));
      case 'datetime': { const ms = Date.parse(v); return Number.isNaN(ms) ? String(v) : String(ms); } // the same instant, any notation
      case 'estimate': return `${v.originalEstimate || ''}|${v.remainingEstimate || ''}`;
      default: return typeof v === 'string' ? v : JSON.stringify(v);
    }
  }

  /* --- opening / loading --- */
  function cfDefaultProject() {
    const keys = trackedKeys() || [];
    const last = prefs.get('create.project', '');
    return effProject() || (keys.includes(last) ? last : '') || keys[0] || last || '';
  }
  function canCreate() { return isConfigured() && PROJECT_VIEWS.includes(S.view) && activeSectionId() === 'view-' + S.view; }

  /** ctx: { project?, epic?: {key, summary}, sprint?: {id, name} } — context "+" actions pre-fill Epic Link / Sprint. */
  function openCreate(ctx = {}) {
    if (!isConfigured() || S.cf) return;
    const project = ctx.project || cfDefaultProject();
    S.cf = { mode: 'create', project, issueTypeId: null, prefill: { epic: ctx.epic || null, sprint: ctx.sprint || null }, returnSel: ctx.returnSel || null,
      fields: [], values: {}, raw: {}, initial: {}, errors: {}, marks: {}, cb: {}, formError: '', loading: true, loadError: null,
      saving: false, dirty: false, createAnother: false, wikiPreview: false, editor: null, lossy: false, conflict: false, seq: 0 };
    if (S.cfDraft && S.cfDraft.project === project) { S.cf.carry = S.cfDraft.values; S.cf.dirty = true; }
    S.cfDraft = null;
    cfOpenDialog();
    cfLoad();
  }
  /** meta: an edit meta just fetched by the caller (the drawer's Edit button), so the dialog needs no second request. */
  function openEdit(key, meta) {
    if (!isConfigured() || S.cf || !key) return;
    S.cf = { mode: 'edit', key, fields: [], values: {}, raw: {}, initial: {}, errors: {}, marks: {}, cb: {}, formError: '', loading: true,
      loadError: null, saving: false, dirty: false, wikiPreview: false, editor: null, lossy: false, conflict: false, seq: 0 };
    if (meta) {
      cfApplyMeta(meta);
      S.cf.loading = false;
      cfOpenDialog();
      cfFocusFirst();
      return;
    }
    cfOpenDialog();
    cfLoad();
  }
  function cfOpenDialog() {
    const mode = S.cf.mode;
    const returnSel = S.cf.returnSel; // the "+" / tree row that opened it (re-found: lists re-render after a create)
    cfRender();
    layers.open($('#dlg-issue'), {
      focus: '#cf-title',
      onDismiss: () => { if (!S.cf || !S.cf.saving) cfClose(false); }, // CR-13: Esc / scrim do nothing while saving
      onClose: () => { if (S.cf && S.cf.editor) S.cf.editor.destroy(); S.cf = null; },
      // edit: back to the drawer's Edit button; create: the top-bar button (else the element that opened it)
      restoreFocus: () => (mode === 'edit' ? $('#drawer [data-action="edit"]') : (returnSel && $(returnSel)) || $('#create-btn:not([hidden])')) || null,
    });
  }

  async function cfLoad(opts = {}) {
    const cf = S.cf;
    if (!cf) return;
    const seq = ++cf.seq;
    cf.loading = true;
    cf.loadError = null;
    cfRender();
    try {
      let meta;
      if (cf.mode === 'edit') meta = await api('getEditMeta', { key: cf.key });
      else {
        if (!cf.project) { // no filter / remembered project yet: wait for the tracked projects
          if (!S.pj.list) await loadProjects(false);
          if (S.cf !== cf || seq !== cf.seq) return;
          cf.project = cfDefaultProject();
          if (!cf.project) throw new ApiError('no_project', t('cf.noProject'));
        }
        const params = { project: cf.project };
        const typeId = opts.issueTypeId || (prefs.get('create.types', {}) || {})[cf.project];
        if (typeId) params.issueTypeId = String(typeId);
        meta = await api('getCreateMeta', params);
      }
      if (S.cf !== cf || seq !== cf.seq) return;
      cfApplyMeta(meta, opts.keep);
    } catch (e) {
      if (S.cf !== cf || seq !== cf.seq) return;
      cf.loadError = e;
      if (cf.dirty) cf.keepDraft = true; // closed by an error: the draft stays in memory for the next Create
    } finally {
      if (S.cf === cf && seq === cf.seq) {
        cf.loading = false;
        cfRender();
        const back = cf.focusAfterLoad && $(cf.focusAfterLoad);
        cf.focusAfterLoad = null;
        if (back) back.focus({ preventScroll: true }); else cfFocusFirst();
      }
    }
  }

  function cfApplyMeta(meta, keep) {
    const cf = S.cf;
    cf.fields = Array.isArray(meta.fields) ? meta.fields : [];
    cf.descriptionFormat = meta.descriptionFormat === 'wiki' ? 'wiki' : 'html';
    cf.projectName = (meta.project && meta.project.name) || cf.project;
    cf.errors = {};
    cf.marks = {};
    cf.cb = {};
    if (cf.mode === 'edit') {
      cf.project = (meta.project && meta.project.key) || String(cf.key).split('-')[0];
      cf.issueTypeId = meta.issueTypeId;
      cf.updated = meta.updated;
      cf.values = Object.assign({}, meta.values || {});
      cf.initial = Object.assign({}, meta.values || {});
      cf.noPermission = !cf.fields.length;
      cf.url = (findIssueAnywhere(cf.key) || {}).url || (S.drawer && S.drawer.detail && S.drawer.detail.issue && S.drawer.detail.issue.key === cf.key ? S.drawer.detail.issue.url : '');
      return;
    }
    cf.project = (meta.project && meta.project.key) || cf.project;
    cf.issueTypes = Array.isArray(meta.issueTypes) ? meta.issueTypes : [];
    cf.issueTypeId = meta.issueTypeId || (cf.issueTypes[0] && cf.issueTypes[0].id);
    const values = {};
    cf.fields.forEach((f) => { if (f.default != null) values[f.id] = f.default; });
    const kept = keep || cf.carry || {};
    cf.carry = null;
    // never convert between description formats: a description typed for the other format is not carried over
    cf.droppedDescription = !!(kept.descriptionFormat && kept.descriptionFormat !== cf.descriptionFormat && !isEmptyVal(kept.description));
    cf.fields.forEach((f) => {
      const k = f.kind === 'summary' ? 'summary' : f.kind === 'description' ? 'description' : f.id;
      if (k === 'description' && cf.droppedDescription) return;
      if (Object.prototype.hasOwnProperty.call(kept, k) && !isEmptyVal(kept[k])) values[f.id] = kept[k];
      if (f.kind === 'epic' && cf.prefill.epic) values[f.id] = cf.prefill.epic;
      if (f.kind === 'sprint' && cf.prefill.sprint) {
        if ((f.options || []).some((o) => String(o.id) === String(cf.prefill.sprint.id))) values[f.id] = Number(cf.prefill.sprint.id);
        else cf.marks[f.id] = t('cf.sprintNotOffered', { sprint: cf.prefill.sprint.name, project: cf.project });
      }
    });
    cf.values = values;
    cf.initial = {};
  }

  /** Values kept when the project / type changes (description only if the format stays the same). */
  function cfKeepValues() {
    const cf = S.cf;
    const out = {};
    cfCollectRaw();
    cf.fields.forEach((f) => {
      const k = f.kind === 'summary' ? 'summary' : f.kind === 'description' ? 'description' : f.id;
      if (CF_KEEP_ON_SWITCH.includes(k) || f.kind === 'labels' || f.kind === 'user' && f.id === 'assignee') out[k] = cf.values[f.id];
    });
    out.descriptionFormat = cf.descriptionFormat;
    return out;
  }

  /* --- rendering --- */
  function cfTitle(cf) {
    if (cf.mode === 'edit') return t('cf.editTitle', { key: cf.key });
    return t('cf.createTitle');
  }
  function cfRender() {
    const cf = S.cf;
    if (!cf) return;
    $('#cf-title').textContent = cfTitle(cf);
    const body = $('#cf-body');
    if (cf.editor) { cf.editor.destroy(); cf.editor = null; }
    let content;
    const keepFocus = document.activeElement && document.activeElement.id === 'cf-project';
    if (cf.loading && !cf.fields.length && cf.mode === 'create' && cf.project) {
      content = html`${cfHeadHTML(cf)}<div class="cf-skeleton" role="status" aria-label="${t('common.loading')}">${[92, 100, 60, 60].map((w) => html`<span class="skeleton sk-line" style="width:${w}%"></span>`)}<span class="skeleton" style="height:140px"></span></div>`;
    } else if (cf.loading && !cf.fields.length) {
      content = html`<div class="cf-skeleton" role="status" aria-label="${t('common.loading')}">${[40, 92, 100, 60, 60].map((w) => html`<span class="skeleton sk-line" style="width:${w}%"></span>`)}<span class="skeleton" style="height:140px"></span></div>`;
    } else if (cf.loadError && !cf.fields.length) {
      content = stateBlock({ iconName: 'alert', tone: 'error', title: t(cf.mode === 'edit' ? 'cf.editLoadFailed' : 'cf.loadFailed'), text: errorText(cf.loadError), action: { id: 'cf-retry', label: t('common.retry') } });
    } else if (cf.mode === 'edit' && cf.noPermission) {
      content = stateBlock({ iconName: 'alert', tone: 'warn', title: t('cf.noPermission'), text: t('cf.noPermissionText') });
    } else content = cfFormHTML(cf);
    setHTML(body, content);
    if (keepFocus && $('#cf-project')) $('#cf-project').focus({ preventScroll: true });
    body.setAttribute('aria-busy', cf.loading ? 'true' : 'false');
    cfAfterRender();
    cfRenderFooter();
  }
  function cfHeadHTML(cf) {
    const projects = (S.pj.list || []).filter((p) => p.available !== false);
    const projOpts = projects.length ? projects : [{ key: cf.project, name: cf.projectName || cf.project, tracked: true }];
    if (!projOpts.some((p) => p.key === cf.project)) projOpts.unshift({ key: cf.project, name: cf.projectName || cf.project, tracked: true });
    const tracked = projOpts.filter((p) => p.tracked);
    const others = projOpts.filter((p) => !p.tracked);
    const opt = (p) => html`<option value="${p.key}"${p.key === cf.project ? html` selected` : ''}>${p.key} — ${p.name || p.key}</option>`;
    const head = cf.mode === 'create'
      ? html`<div class="cf-row cf-head">
          <div class="cf-field">
            <label class="cf-label" for="cf-project">${t('cf.project')}</label>
            <select class="input select" id="cf-project" data-cf-control="project">${others.length ? html`<optgroup label="${t('cf.projectsTracked')}">${tracked.map(opt)}</optgroup><optgroup label="${t('cf.projectsOther')}">${others.map(opt)}</optgroup>` : projOpts.map(opt)}</select>
          </div>
          <div class="cf-field cf-types-field">
            <span class="cf-label" id="cf-type-l">${t('cf.issueType')}</span>
            <div class="cf-types" role="radiogroup" aria-labelledby="cf-type-l">${(cf.issueTypes || []).map((ty) => {
              const on = String(ty.id) === String(cf.issueTypeId);
              return html`<button type="button" role="radio" class="cf-type" data-cf-type="${ty.id}" aria-checked="${on ? 'true' : 'false'}" tabindex="${on ? '0' : '-1'}"${cf.loading ? html` disabled` : ''}>${typeIcon({ name: ty.name, kind: ty.subtask ? 'subtask' : ty.kind }, true)}<span>${ty.name}</span></button>`;
            })}</div>
          </div>
        </div>`
      : html`<p class="cf-context">${t('cf.editContext', { project: cf.projectName || cf.project })}</p>`;
    return head;
  }
  function cfFormHTML(cf) {
    const unsupported = cf.fields.filter((f) => f.kind === 'unsupported' && f.required);
    const head = cfHeadHTML(cf);
    const created = cf.created && cf.created.key
      ? html`<div class="notice cf-created">${icon('check-circle')}<span class="notice-text">${t('cf.created', { key: cf.created.key })}${cf.created.warning ? html`<span class="cf-created-warn">${cf.created.warning}</span>` : ''}</span><button type="button" class="link-btn" data-cf="open-created" data-key="${cf.created.key}">${t('common.open')}</button></div>`
      : '';
    const notice = unsupported.length
      ? html`<div class="notice notice-warn cf-unsupported-notice" role="note">${icon('alert')}<span>${t('cf.unsupportedNotice', { n: unsupported.length })}</span><button type="button" class="link-btn" data-cf="open-jira">${t('cf.openInJira')}</button></div>`
      : '';
    const conflict = cf.conflict
      ? html`<div class="notice notice-warn cf-conflict" role="alert">${icon('alert')}<span class="notice-text">${t('cf.conflict')}</span><button type="button" class="btn btn-sm" data-cf="reload">${icon('refresh')}<span>${t('cf.reload')}</span></button><button type="button" class="btn btn-sm btn-ghost" data-cf="cancel">${t('common.cancel')}</button></div>`
      : '';
    const formErr = cf.formError ? html`<div class="form-alert" role="alert" id="cf-form-error">${icon('alert')}<span>${cf.formError}</span></div>` : '';
    // DR-25: consecutive story-point fields (SP, Dev SP, QA SP) share one row of equal compact controls
    const parts = [];
    let sp = [];
    const flush = () => { if (sp.length > 1) parts.push(html`<div class="cf-sp-row">${sp.map(cfFieldHTML)}</div>`); else sp.forEach((f) => parts.push(cfFieldHTML(f))); sp = []; };
    cf.fields.forEach((f) => { if (f.kind === 'storyPoints') sp.push(f); else { flush(); parts.push(cfFieldHTML(f)); } });
    flush();
    return html`${created}${head}${notice}${conflict}${formErr}<div class="cf-fields">${parts}</div>`;
  }

  function cfFieldHTML(f) {
    const cf = S.cf;
    const id = cfId(f.id);
    const v = cf.values[f.id];
    const err = cf.errors[f.id];
    const mark = cf.marks[f.id];
    const wide = CF_WIDE.has(f.kind) || (f.kind === 'text' && (f.max || 255) > 100 && f.main === false);
    const descIds = [err ? id + '-e' : '', mark ? id + '-m' : '', id + '-h'].filter(Boolean).join(' ');
    const a11y = html`id="${id}" data-field="${f.id}" aria-describedby="${descIds}"${f.required ? html` aria-required="true"` : ''}${err ? html` aria-invalid="true"` : ''}`;
    let control;
    let hint = '';
    let labelFor = id;
    switch (f.kind) {
      case 'summary': case 'text': case 'epicName':
        control = html`<input class="input" type="text" ${a11y} maxlength="${f.max || (f.kind === 'summary' ? 255 : 255)}" value="${v || ''}" autocomplete="off">`;
        break;
      case 'textarea':
        control = html`<textarea class="input textarea" rows="3" ${a11y}>${v || ''}</textarea>`;
        break;
      case 'description':
        if (cf.descriptionFormat === 'wiki') {
          const pv = cf.wikiPreview;
          control = html`<div class="cf-wiki">
            <div class="cf-wiki-bar"><span class="cf-wiki-note">${t('cf.wikiNote')}</span><button type="button" class="btn btn-sm${pv ? ' is-on' : ''}" data-cf="preview" aria-pressed="${pv ? 'true' : 'false'}" aria-controls="${id}-pv">${icon('eye')}<span>${t('cf.preview')}</span></button></div>
            <textarea class="input textarea mono cf-wiki-text" rows="8" ${a11y} spellcheck="false"${pv ? html` hidden` : ''}>${v || ''}</textarea>
            <div class="cf-wiki-preview" id="${id}-pv" role="region" aria-label="${t('cf.preview')}" tabindex="0"${pv ? '' : html` hidden`}></div>
          </div>`;
        } else {
          labelFor = '';
          control = html`${cf.lossy ? html`<div class="notice notice-tip cf-lossy" role="note">${icon('info')}<span class="notice-text">${t('cf.lossy')}</span>${cf.url ? html`<button type="button" class="link-btn" data-cf="open-jira">${t('cf.editInJira')}</button>` : ''}</div>` : ''}<div class="cf-editor" data-editor="${f.id}"></div>`;
        }
        break;
      case 'number':
        control = html`<input class="input input-num cf-num" type="text" inputmode="decimal" ${a11y} value="${cf.raw[f.id] != null ? cf.raw[f.id] : cfNumText(v)}" autocomplete="off">`;
        break;
      case 'storyPoints':
        if (f.options && f.options.length) control = cfSelect(f, a11y, v && v.id);
        else control = html`<input class="input input-num cf-num" type="text" inputmode="decimal" ${a11y} value="${cf.raw[f.id] != null ? cf.raw[f.id] : cfNumText(v)}" autocomplete="off">`;
        break;
      case 'select': case 'priority':
        control = cfSelect(f, a11y, v && v.id);
        break;
      case 'sprint':
        control = cfSelect(f, a11y, v, t('cf.backlog'));
        break;
      case 'date':
        control = html`<input class="input input-date" type="text" inputmode="numeric" maxlength="10" data-mask="date" ${a11y} value="${cf.raw[f.id] != null ? cf.raw[f.id] : v ? i18n.formatInputDate(v) : ''}" placeholder="${t('settings.datePlaceholder')}" autocomplete="off">`;
        break;
      case 'datetime':
        control = html`<input class="input cf-datetime" type="text" ${a11y} value="${cf.raw[f.id] != null ? cf.raw[f.id] : v ? cfFormatDateTime(v) : ''}" placeholder="${t('cf.datetimePlaceholder', { date: t('settings.datePlaceholder') })}" autocomplete="off">`;
        break;
      case 'labels':
        control = cfChips(f, id, a11y, (v || []).map((x) => ({ key: x, label: x })), t('cf.labelsPlaceholder'));
        hint = t('cf.labelsHint');
        break;
      case 'multiselect': case 'components': case 'versions': {
        const byId = new Map((f.options || []).map((o) => [String(o.id), o.name]));
        control = cfChips(f, id, a11y, (v || []).map((x) => ({ key: String(x), label: byId.get(String(x)) || String(x) })), t('cf.choose'));
        break;
      }
      case 'users':
        control = cfChips(f, id, a11y, (v || []).map((u) => ({ key: u.name, label: u.displayName || u.name })), t('cf.searchPeople'));
        break;
      case 'user': {
        control = cfCombo(f, id, a11y, v ? v.displayName || v.name : '', t('cf.searchPeople'), !!v);
        const self = me();
        control = html`${control}<div class="cf-quick">${self && (!v || v.name !== self.name) ? html`<button type="button" class="link-btn" data-cf="assign-me" data-field="${f.id}">${t('cf.assignToMe')}</button>` : ''}${!f.required && v ? html`<button type="button" class="link-btn" data-cf="unassign" data-field="${f.id}">${t('cf.unassign')}</button>` : ''}</div>`;
        break;
      }
      case 'epic': case 'parent':
        control = cfCombo(f, id, a11y, v ? `${v.key}${v.summary ? ' ' + v.summary : ''}` : '', t(f.kind === 'epic' ? 'cf.searchEpic' : 'cf.searchParent'), !!v);
        break;
      case 'estimate': {
        const raw = cf.raw[f.id] || {};
        const val = v || {};
        labelFor = id;
        control = html`<div class="cf-estimate">
          <label class="cf-sublabel"><span>${t('drawer.estOriginal')}</span><input class="input cf-est" type="text" ${a11y} data-part="originalEstimate" value="${raw.originalEstimate != null ? raw.originalEstimate : val.originalEstimate || ''}" placeholder="1d 2h" autocomplete="off"></label>
          <label class="cf-sublabel"><span>${t('drawer.estRemaining')}</span><input class="input cf-est" type="text" id="${id}-rem" data-field="${f.id}" data-part="remainingEstimate" aria-describedby="${descIds}"${err ? html` aria-invalid="true"` : ''} value="${raw.remainingEstimate != null ? raw.remainingEstimate : val.remainingEstimate || ''}" placeholder="1d" autocomplete="off"></label>
        </div>`;
        hint = t('cf.estimateHint', { h: fmtNum((S.settings && S.settings.hoursPerDay) || 8) });
        break;
      }
      case 'unsupported':
        labelFor = '';
        control = html`<div class="cf-unsupported" id="${id}" data-field="${f.id}" aria-describedby="${descIds}">${icon('external', 'icon-sm')}<span>${t('cf.unsupported')}</span><button type="button" class="link-btn" data-cf="open-jira">${t('cf.openInJira')}</button></div>`;
        break;
      default:
        control = html`<input class="input" type="text" ${a11y} value="${typeof v === 'string' ? v : ''}">`;
    }
    const req = f.required ? html`<span class="cf-req" aria-hidden="true">*</span><span class="sr-only">${t('cf.requiredSr')}</span>` : '';
    const label = labelFor ? html`<label class="cf-label" for="${labelFor}" id="${id}-l">${f.name}${req}</label>` : html`<span class="cf-label" id="${id}-l">${f.name}${req}</span>`;
    return html`<div class="cf-field${wide ? ' is-wide' : ''}" data-fwrap="${f.id}" data-kind="${f.kind}">
      ${label}${control}
      <p class="hint cf-hint" id="${id}-h"${hint ? '' : html` hidden`}>${hint}</p>
      <p class="cf-mark" id="${id}-m"${mark ? '' : html` hidden`}>${icon('alert', 'icon-sm')}<span>${mark || ''}</span></p>
      <p class="field-error" id="${id}-e" data-cf-error="${f.id}"${err ? '' : html` hidden`}>${err || ''}</p>
    </div>`;
  }
  function cfSelect(f, a11y, cur, emptyLabel) {
    const sel = cur == null ? '' : String(cur);
    const opts = f.options || [];
    return html`<select class="input select" ${a11y}>
      ${!f.required || !sel ? html`<option value=""${sel ? '' : html` selected`}>${emptyLabel || t('cf.none')}</option>` : ''}
      ${opts.map((o) => html`<option value="${o.id}"${String(o.id) === sel ? html` selected` : ''}>${o.name}${o.state === 'active' ? ' · ' + t('sprints.state.active') : ''}</option>`)}
    </select>`;
  }
  function cfCombo(f, id, a11y, text, placeholder, hasValue) {
    return html`<div class="cb" data-cb="${f.id}">
      <div class="cb-box">
        <input class="input cb-input" type="text" ${a11y} role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="${id}-lb" value="${text}" placeholder="${placeholder}" autocomplete="off" spellcheck="false">
        ${hasValue && !f.required ? html`<button type="button" class="icon-btn icon-btn-sm cb-clear" data-cf="clear" data-field="${f.id}" aria-label="${t('cf.clearField', { name: f.name })}" title="${t('cf.clearField', { name: f.name })}">${icon('x')}</button>` : ''}
      </div>
      <ul class="cb-list" id="${id}-lb" role="listbox" aria-label="${f.name}" hidden></ul>
    </div>`;
  }
  function cfChips(f, id, a11y, chips, placeholder) {
    const listbox = f.kind !== 'labels';
    return html`<div class="cb cb-multi" data-cb="${f.id}">
      <div class="cb-box">
        <ul class="cb-chips" role="list" aria-label="${f.name}">${chips.map((c) => html`<li class="chip cb-chip"><span>${c.label}</span><button type="button" class="cb-chip-x" data-cf="chip-remove" data-field="${f.id}" data-key="${c.key}" aria-label="${t('cf.removeValue', { value: c.label })}">${icon('x')}</button></li>`)}</ul>
        <input class="cb-input cb-inline" type="text" ${a11y}${listbox ? html` role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="${id}-lb"` : ''} placeholder="${placeholder}" autocomplete="off" spellcheck="false">
      </div>
      ${listbox ? html`<ul class="cb-list" id="${id}-lb" role="listbox" aria-label="${f.name}" aria-multiselectable="true" hidden></ul>` : ''}
    </div>`;
  }

  /** Editor / preview after the field markup is in place. */
  function cfAfterRender() {
    const cf = S.cf;
    const host = $('#cf-body [data-editor]');
    if (host) {
      const fid = host.dataset.editor;
      const id = cfId(fid);
      const f = cfField(fid);
      cf.editor = JiraffeEditor.create({
        host, label: f ? f.name : t('drawer.description'), describedBy: `${id}-e ${id}-h`, t,
        sanitize: (src) => wiki.sanitizeForEditor(src),
        onChange: () => { cf.dirty = true; cfSetError(fid, null); },
        onNotice: (msg) => toast(msg, { tone: 'warn' }),
      });
      cf.editor.el.id = id;
      cf.editor.el.dataset.field = fid;
      const res = cf.editor.load(cf.values[fid] || '');
      if (cf.mode === 'edit' && cf.lossyKnown == null) { cf.lossyKnown = true; if (res.lossy && !cf.lossy) { cf.lossy = true; cfRender(); return; } }
      if (cf.errors[fid]) cf.editor.setInvalid(true, `${id}-e ${id}-h`);
    }
    const pv = $('#cf-body .cf-wiki-preview:not([hidden])');
    if (pv) pv.replaceChildren(wiki.render($('#cf-body .cf-wiki-text').value || ''));
  }

  function cfRenderFooter() {
    const cf = S.cf;
    const foot = $('#cf-foot');
    if (!cf) return;
    const x = $('#dlg-issue [data-cf="close"]');
    if (cf.saving) x.setAttribute('aria-disabled', 'true'); else x.removeAttribute('aria-disabled');
    const ready = !cf.loading && !cf.loadError && cf.fields.length && !(cf.mode === 'edit' && cf.noPermission);
    const blocked = cf.mode === 'create' && cf.fields.some((f) => f.kind === 'unsupported' && f.required);
    const primary = cf.mode === 'create' ? t('cf.create') : t('common.save');
    setHTML(foot, html`
      ${cf.mode === 'create' ? html`<label class="mine-toggle cf-another"><input type="checkbox" role="switch" class="switch" id="cf-another"${cf.createAnother ? html` checked` : ''}><span>${t('cf.createAnother')}</span></label>` : ''}
      <span class="cf-foot-note" id="cf-foot-note">${blocked ? t('cf.blockedNote') : ''}</span>
      <button type="button" class="btn" data-cf="cancel"${cf.saving ? html` aria-disabled="true"` : ''}>${t('common.cancel')}</button>
      <button type="button" class="btn btn-primary" data-cf="submit"${cf.saving ? html` aria-busy="true"` : ''}${!ready || blocked ? html` aria-disabled="true"` : ''}${blocked ? html` aria-describedby="cf-foot-note"` : ''}>${primary}</button>`);
  }

  /** Re-renders one field (pickers, chips, preview); keeps focus on its main control. */
  function cfRerenderField(fid, focusSel) {
    const cf = S.cf;
    const f = cfField(fid);
    const wrap = $(`#cf-body [data-fwrap="${CSS.escape(fid)}"]`);
    if (!f || !wrap || f.kind === 'description' && cf.descriptionFormat !== 'wiki') return;
    const box = document.createElement('div');
    setHTML(box, cfFieldHTML(f));
    const fresh = box.firstElementChild;
    cf.replacing = true; // removing the focused control fires focusout: don't react to it
    try { wrap.replaceWith(fresh); } finally { cf.replacing = false; }
    const pv = $('.cf-wiki-preview:not([hidden])', fresh);
    if (pv) pv.replaceChildren(wiki.render($('.cf-wiki-text', fresh).value || ''));
    const target = (focusSel && $(focusSel, fresh)) || $(`#${CSS.escape(cfId(fid))}`, fresh);
    if (target && focusSel !== false) target.focus({ preventScroll: true });
  }

  function cfSetError(fid, msg) {
    const cf = S.cf;
    if (!cf) return;
    if (msg) cf.errors[fid] = msg; else delete cf.errors[fid];
    const id = cfId(fid);
    const p = $(`#cf-body [data-cf-error="${CSS.escape(fid)}"]`);
    if (p) { p.textContent = msg || ''; p.hidden = !msg; }
    $$(`#cf-body [data-field="${CSS.escape(fid)}"]`).forEach((c) => {
      if (c.classList.contains('rte-content')) return;
      if (msg) c.setAttribute('aria-invalid', 'true'); else c.removeAttribute('aria-invalid');
      const ids = [msg ? id + '-e' : '', cf.marks[fid] ? id + '-m' : '', id + '-h'].filter(Boolean).join(' ');
      c.setAttribute('aria-describedby', ids);
    });
    if (cf.editor && cf.editor.el.dataset.field === fid) cf.editor.setInvalid(!!msg, `${id}-e ${id}-h`);
  }
  function cfFocusFirst(invalidOnly) {
    const cf = S.cf;
    if (!cf || !$('#dlg-issue').open) return;
    const body = $('#cf-body');
    if (invalidOnly) {
      const fid = cf.fields.map((f) => f.id).find((x) => cf.errors[x]);
      const target = fid ? (fid === 'description' || (cfField(fid) || {}).kind === 'description') && cf.editor ? cf.editor.el : $(`[data-field="${CSS.escape(fid)}"]`, body) : $('#cf-form-error');
      if (target) { if (target.id === 'cf-form-error') target.setAttribute('tabindex', '-1'); target.focus(); target.scrollIntoView({ block: 'nearest' }); }
      return;
    }
    if (body.contains(document.activeElement)) return;
    const first = $('[data-field]:not([disabled])', body) || $('#cf-retry, [data-action="cf-retry"]', body);
    if (first) first.focus({ preventScroll: true });
  }

  /* --- values --- */
  /** Reads the plain inputs into cf.raw / cf.values (pickers and chips update values directly). */
  function cfCollectRaw() {
    const cf = S.cf;
    $$('#cf-body [data-field]').forEach((c) => {
      const fid = c.dataset.field;
      const f = cfField(fid);
      if (!f || c.classList.contains('cb-input') || c.classList.contains('rte-content')) return;
      if (c.tagName === 'SELECT') {
        const val = c.value;
        if (f.kind === 'sprint') cf.values[fid] = val ? Number(val) : null;
        else cf.values[fid] = val ? { id: val } : null;
        return;
      }
      // number / date / datetime / estimate: cf.raw is set by the input handler only when the user typed there (CR-11);
      // an untouched control keeps the value Jira sent.
      if (f.kind === 'estimate' || ['number', 'storyPoints', 'date', 'datetime'].includes(f.kind)) return;
      if (c.tagName === 'INPUT' || c.tagName === 'TEXTAREA') cf.values[fid] = c.value;
    });
    if (cf.editor) {
      const fid = cf.editor.el.dataset.field;
      cf.values[fid] = cf.editor.getHTML();
    }
  }
  /** The current value of one field and its error: typed text is parsed only for touched controls (cf.raw). */
  function cfFieldValue(f) {
    const cf = S.cf;
    const fid = f.id;
    let v = cf.values[fid];
    const raw = cf.raw[fid];
    let err = null;
    switch (f.kind) {
      case 'number': case 'storyPoints':
        if (f.kind === 'storyPoints' && f.options && f.options.length) break;
        if (raw != null) {
          if (!String(raw).trim()) v = null;
          else { const n = cfParseNum(raw); if (Number.isNaN(n)) err = t('cf.numberInvalid'); else v = n; }
        }
        break;
      case 'date':
        if (raw != null) { const s1 = raw.trim(); if (!s1) v = null; else { const iso = i18n.parseInputDate(s1); if (iso) v = iso; else err = t('settings.dateInvalid'); } }
        break;
      case 'datetime':
        if (raw != null) { const s1 = raw.trim(); if (!s1) v = null; else { const iso = cfParseDateTime(s1); if (iso) v = iso; else err = t('cf.datetimeInvalid'); } }
        break;
      case 'estimate': {
        const init = cf.initial[fid] || {};
        const r = Object.assign({ originalEstimate: (v || {}).originalEstimate || '', remainingEstimate: (v || {}).remainingEstimate || '' }, raw || {});
        const o = String(r.originalEstimate || '').trim(); const rem = String(r.remainingEstimate || '').trim();
        if ((o && !DURATION.test(o)) || (rem && !DURATION.test(rem))) err = t('cf.estimateInvalid');
        // CR-17: a part that had a value can't be cleared (Jira would keep the old value)
        else if (cf.mode === 'edit' && ((!o && init.originalEstimate) || (!rem && init.remainingEstimate))) err = t('cf.estimateNoClear');
        v = o || rem ? Object.assign(o ? { originalEstimate: o } : {}, rem ? { remainingEstimate: rem } : {}) : null;
        break;
      }
      default:
    }
    return { v, err };
  }

  /** Parses and validates everything; returns { values, errors }. */
  function cfValidate() {
    const cf = S.cf;
    cfCollectRaw();
    const errors = {};
    const values = {};
    cf.fields.forEach((f) => {
      const fid = f.id;
      const parsed = cfFieldValue(f);
      let v = parsed.v;
      if (parsed.err) errors[fid] = parsed.err;
      switch (f.kind) {
        case 'summary': case 'text': case 'textarea': case 'epicName':
          v = typeof v === 'string' ? (f.kind === 'textarea' ? v.replace(/\s+$/, '') : v.trim()) : v;
          if (v && v.length > (f.max || 255) && f.kind !== 'textarea') errors[fid] = t('cf.tooLong', { n: f.max || 255 });
          break;
        case 'description':
          if (typeof v === 'string' && v.length > 32000) errors[fid] = t('cf.tooLong', { n: 32000 });
          break;
        default:
      }
      if (f.required && f.kind !== 'unsupported' && !errors[fid] && isEmptyVal(f.kind === 'description' && cf.editor ? (cf.editor.isEmpty() ? '' : v) : v)) errors[fid] = t('cf.required');
      values[fid] = isEmptyVal(v) ? null : v;
    });
    return { values, errors };
  }
  /** FieldValue as written (SPEC "Contract additions (v0.5)"): users / issues by name / key only. */
  function cfWire(kind, v) {
    if (v == null) return v;
    if (kind === 'user') return { name: v.name };
    if (kind === 'users') return v.map((u) => ({ name: u.name }));
    if (kind === 'epic' || kind === 'parent') return { key: v.key };
    return v;
  }
  /** Create: non-empty values; edit: the changed fields only (unchanged description is never re-sent). */
  function cfPayload(values) {
    const cf = S.cf;
    const out = {};
    cf.fields.forEach((f) => {
      if (f.kind === 'unsupported') return;
      const v = cfWire(f.kind, values[f.id]);
      if (cf.mode === 'create') { if (!isEmptyVal(v)) out[f.id] = v; return; }
      if (f.kind === 'description') {
        const changed = cf.editor ? cf.editor.isChanged() || cf.descForced : cfCanon('description', v) !== cfCanon('description', cf.initial[f.id]);
        if (changed) out[f.id] = v == null ? '' : v;
        return;
      }
      if (cfCanon(f.kind, v) !== cfCanon(f.kind, cf.initial[f.id])) out[f.id] = v;
    });
    return out;
  }
  function cfParseDateTime(s) {
    const m = /^(\d{1,2})[./-](\d{1,2})[./-](\d{4})\s+(\d{1,2}):(\d{2})$/.exec(s) || /^(\d{4})-(\d{2})-(\d{2})[T\s](\d{2}):(\d{2})/.exec(s);
    if (!m) return null;
    const [y, mo, d] = m[1].length === 4 ? [m[1], m[2], m[3]] : [m[3], m[2], m[1]];
    const dt = new Date(Number(y), Number(mo) - 1, Number(d), Number(m[4]), Number(m[5]));
    if (isNaN(dt.getTime()) || dt.getDate() !== Number(d)) return null;
    const off = -dt.getTimezoneOffset();
    const p2 = (n) => String(Math.abs(n)).padStart(2, '0');
    return `${y}-${p2(mo)}-${p2(d)}T${p2(m[4])}:${m[5]}:00${off >= 0 ? '+' : '-'}${p2(Math.floor(Math.abs(off) / 60))}:${p2(Math.abs(off) % 60)}`;
  }
  function cfFormatDateTime(iso) {
    const d = parseDate(iso);
    if (!d) return String(iso || '');
    const p2 = (n) => String(n).padStart(2, '0');
    return `${i18n.formatInputDate(i18n.isoDate(d))} ${p2(d.getHours())}:${p2(d.getMinutes())}`;
  }

  function cfSetValue(fid, v, focusSel) {
    const cf = S.cf;
    cf.values[fid] = v;
    cf.dirty = true;
    delete cf.errors[fid];
    cfRerenderField(fid, focusSel);
  }

  /* --- submit --- */
  async function cfSubmit() {
    const cf = S.cf;
    if (!cf || cf.saving || cf.loading) return;
    const btn = $('#cf-foot [data-cf="submit"]');
    if (btn && btn.getAttribute('aria-disabled') === 'true') return;
    const { values, errors } = cfValidate();
    cf.formError = '';
    if (cf.created) { cf.created = null; const n = $('#cf-body .cf-created'); if (n) n.remove(); }
    Object.keys(cf.errors).forEach((k) => cfSetError(k, null));
    Object.entries(errors).forEach(([k, m]) => cfSetError(k, m));
    const fe = $('#cf-form-error'); if (fe) fe.remove();
    cf.errorLinks = [];
    if (Object.keys(errors).length) {
      const order = cf.fields.map((f) => f.id).filter((x) => errors[x]);
      const req = order.filter((x) => errors[x] === t('cf.required')).length;
      const other = order.length - req;
      cf.formError = [req ? t('cf.fillRequired', { n: req }) : '', other ? t('cf.fixInvalid', { n: other }) : ''].filter(Boolean).join(' ');
      cf.formErrorQuiet = true; // announced through #cf-live below (no second role=alert)
      cf.errorLinks = order;
      cfRefreshNotices();
      $('#cf-live').textContent = cf.formError;
      cfFocusFirst(true);
      return;
    }
    cf.formErrorQuiet = false;
    const payload = cfPayload(values);
    if (cf.mode === 'edit' && !Object.keys(payload).length) { cf.dirty = false; cfClose(true); toast(t('cf.nothingChanged')); return; }
    cf.saving = true;
    cfRenderFooter();
    try {
      if (cf.mode === 'create') {
        const res = await api('createIssue', { project: cf.project, issueTypeId: String(cf.issueTypeId), values: payload });
        prefs.set('create.project', cf.project);
        prefs.set('create.types', Object.assign({}, prefs.get('create.types', {}) || {}, { [cf.project]: String(cf.issueTypeId) }));
        const key = res && res.key;
        cfAfterChange(cf.project, null);
        if (cf.createAnother && S.cf === cf) {
          // DR-30: the form stays open — the result is a notice at the top of the form (a toast would cover the dialog)
          cf.created = { key, warning: (res && res.warning) || '' };
          cfResetForAnother();
          $('#cf-live').textContent = [t('cf.created', { key }), cf.created.warning].filter(Boolean).join('. ');
          return;
        }
        toast(t('cf.created', { key }), { tone: 'success', action: key ? { label: t('common.open'), run: () => openIssue(key) } : null });
        if (res && res.warning) toast(res.warning, { tone: 'warn' }); // created, but e.g. the sprint move failed
        cf.dirty = false;
        cfClose(true);
      } else {
        const res = await api('updateIssue', { key: cf.key, values: payload, expectedUpdated: cf.updated });
        toast(t('cf.saved', { key: cf.key }), { tone: 'success' });
        if (res && res.warning) toast(res.warning, { tone: 'warn' }); // saved, but e.g. the sprint move failed
        cfAfterChange(cf.project, cf.key);
        cf.dirty = false;
        cfClose(true);
      }
    } catch (e) {
      if (S.cf !== cf) return;
      if (e.code === 'validation' && e.fields.length) {
        const known = e.fields.filter((x) => cfField(x.field));
        const rest = e.fields.filter((x) => !cfField(x.field));
        known.forEach((x) => cfSetError(x.field, x.message));
        cf.errorLinks = cf.fields.map((f) => f.id).filter((x) => known.some((k) => k.field === x));
        // the message names the problem and carries Jira's errorMessages / errors of other fields
        cf.formError = [e.message, ...rest.map((x) => x.message).filter((m) => !String(e.message).includes(m))].filter(Boolean).join(' ');
      } else if (e.code === 'validation') cf.formError = e.message || t('cf.saveFailed');
      else if (e.code === 'edit_conflict') { cf.conflict = true; cf.formError = ''; }
      else cf.formError = `${t(cf.mode === 'create' ? 'cf.createFailed' : 'cf.saveFailed')}: ${errorText(e)}`;
      cfRefreshNotices();
      if (cf.conflict) { const r = $('#cf-body [data-cf="reload"]'); if (r) r.focus(); } else cfFocusFirst(true);
    } finally {
      if (S.cf === cf) { cf.saving = false; cfRenderFooter(); }
    }
  }
  /** Conflict / form error banners without re-rendering the fields (the editor keeps its state). */
  function cfRefreshNotices() {
    const cf = S.cf;
    const body = $('#cf-body');
    $$('.cf-conflict, #cf-form-error', body).forEach((x) => x.remove());
    const fields = $('.cf-fields', body);
    if (!fields) return;
    const box = document.createElement('div');
    const parts = [];
    if (cf.conflict) parts.push(html`<div class="notice notice-warn cf-conflict" role="alert">${icon('alert')}<span class="notice-text">${t('cf.conflict')}</span><button type="button" class="btn btn-sm" data-cf="reload">${icon('refresh')}<span>${t('cf.reload')}</span></button><button type="button" class="btn btn-sm btn-ghost" data-cf="cancel">${t('common.cancel')}</button></div>`);
    if (cf.formError) {
      const links = (cf.errorLinks || []).map((fid) => cfField(fid)).filter(Boolean);
      parts.push(html`<div class="form-alert cf-form-alert"${cf.formErrorQuiet ? '' : html` role="alert"`} id="cf-form-error">${icon('alert')}<div><span>${cf.formError}</span>${links.length ? html`<ul class="cf-error-links" role="list">${links.map((f) => html`<li><button type="button" class="link-btn" data-cf="goto" data-field="${f.id}">${f.name}</button></li>`)}</ul>` : ''}</div></div>`);
    }
    setHTML(box, html`${parts}`);
    fields.before(...box.childNodes);
  }
  function cfAfterChange(project, key) {
    // The backend emits `board` (views reload); the drawer re-reads the edited issue.
    if (key) {
      detailCache.delete(key);
      if (S.drawer && drawerKey() === key) loadDetail({ silent: true });
    }
  }
  function cfResetForAnother() {
    const cf = S.cf;
    const keep = {};
    cf.fields.forEach((f) => { if (f.kind === 'epic' || f.kind === 'sprint') keep[f.id] = cf.values[f.id]; });
    cf.values = Object.assign({}, keep);
    cf.fields.forEach((f) => { if (f.default != null && cf.values[f.id] == null) cf.values[f.id] = f.default; });
    cf.raw = {};
    cf.errors = {};
    cf.formError = '';
    cf.saving = false;
    cfRender();
    cf.dirty = false; // DR-22: the reset form is the new baseline (closing it doesn't prompt)
    const s = $('#cf-body [data-field="summary"]');
    if (s) s.focus();
  }

  /* --- edit conflict: reload --- */
  async function cfReloadConflict() {
    const cf = S.cf;
    if (!cf || cf.mode !== 'edit') return;
    cfCollectRaw();
    let meta;
    try { meta = await api('getEditMeta', { key: cf.key }); } catch (e) { toastError(t('cf.reloadFailed'), e); return; }
    if (S.cf !== cf) return;
    const fresh = meta.values || {};
    const oldInitial = cf.initial;
    const marks = {};
    cf.fields.forEach((f) => {
      const fid = f.id;
      const serverChanged = cfCanon(f.kind, fresh[fid]) !== cfCanon(f.kind, oldInitial[fid]);
      const cur = cfFieldValue(f); // typed numbers / dates / estimates live in cf.raw until parsed (CR-14)
      const userChanged = f.kind === 'description' && cf.editor
        ? cf.editor.isChanged()
        : cur.err != null || cfCanon(f.kind, cur.v) !== cfCanon(f.kind, oldInitial[fid]);
      if (!serverChanged) return;
      if (userChanged) { marks[fid] = t('cf.conflictField'); if (f.kind === 'description') cf.descForced = true; return; }
      cf.values[fid] = fresh[fid];
      delete cf.raw[fid];
      if (f.kind === 'description' && cf.editor) { cf.editor.load(fresh[fid] || ''); return; }
      cfRerenderField(fid, false);
    });
    cf.initial = Object.assign({}, fresh);
    cf.updated = meta.updated;
    cf.conflict = false;
    cf.marks = marks;
    Object.keys(marks).forEach((fid) => {
      const m = $(`#${CSS.escape(cfId(fid))}-m`);
      if (m) { m.hidden = false; $('span', m).textContent = marks[fid]; }
      cfSetError(fid, cf.errors[fid] || null); // refresh aria-describedby with the mark
    });
    cfRefreshNotices();
    const live = $('#cf-live');
    live.textContent = Object.keys(marks).length ? t('cf.reloadedMarked', { n: Object.keys(marks).length }) : t('cf.reloaded');
    const first = Object.keys(marks)[0];
    const target = first ? $(`#cf-body [data-field="${CSS.escape(first)}"]`) : $('#cf-foot [data-cf="submit"]');
    if (target) target.focus();
  }

  /* --- closing --- */
  async function cfClose(force) {
    const cf = S.cf;
    if (!cf || (cf.saving && !force)) return; // a running save decides (success closes, failure keeps the form)
    if (!force && !cf.saving) {
      cfCollectRaw();
      const changed = cf.dirty || (cf.editor && cf.editor.isChanged());
      if (changed && !cf.keepDraft) {
        const ok = await confirmDialog({ title: t('cf.discardTitle'), body: t('cf.discardText'), okLabel: t('cf.discard'), cancelLabel: t('cf.keepEditing'), safeDefault: true });
        if (!ok || S.cf !== cf) return;
      }
    }
    if (cf.keepDraft && cf.mode === 'create') {
      const kept = {};
      cf.fields.forEach((f) => { const k = f.kind === 'summary' ? 'summary' : f.kind === 'description' ? 'description' : f.id; if (!isEmptyVal(cf.values[f.id])) kept[k] = cf.values[f.id]; });
      S.cfDraft = { project: cf.project, values: kept };
    }
    layers.close($('#dlg-issue'));
  }

  async function cfOpenInJira() {
    const cf = S.cf;
    if (!cf) return;
    if (cf.mode === 'edit' && cf.url) { openUrl(cf.url); return; }
    const s = await ensureSettings();
    const base = s && s.jiraUrl ? String(s.jiraUrl).replace(/\/+$/, '') : '';
    if (base) openUrl(`${base}/secure/CreateIssue!default.jspa`);
  }

  /* --- pickers (combobox) --- */
  function cbState(fid) { const cf = S.cf; if (!cf.cb[fid]) cf.cb[fid] = { open: false, options: [], active: -1, seq: 0, loading: false, timer: null }; return cf.cb[fid]; }
  function cbSelected(f) {
    const v = S.cf.values[f.id];
    if (f.kind === 'users') return new Set((v || []).map((u) => u.name));
    if (CF_LOCAL.has(f.kind)) return new Set((v || []).map(String));
    return new Set();
  }
  function cbRenderList(fid) {
    const cf = S.cf;
    const f = cfField(fid);
    const st = cbState(fid);
    const id = cfId(fid);
    const list = $(`#${CSS.escape(id)}-lb`);
    const input = $(`#${CSS.escape(id)}`);
    if (!list || !input) return;
    list.hidden = !st.open;
    input.setAttribute('aria-expanded', st.open ? 'true' : 'false');
    if (!st.open) { input.removeAttribute('aria-activedescendant'); return; }
    let items;
    if (st.loading && !st.options.length) items = html`<li class="cb-state" role="option" aria-disabled="true">${t('common.loading')}</li>`;
    else if (st.error) items = html`<li class="cb-state cb-error" role="option" aria-disabled="true">${st.error}</li>`;
    else if (!st.options.length) items = html`<li class="cb-state" role="option" aria-disabled="true">${t('cf.noMatches')}</li>`;
    else {
      const sel = cbSelected(f);
      items = st.options.map((o, i) => html`<li class="cb-opt${i === st.active ? ' is-active' : ''}" role="option" id="${id}-o${i}" data-i="${i}" aria-selected="${sel.has(o.key) || i === st.active ? 'true' : 'false'}">
        ${o.avatar ? avatar(o.avatar, 'avatar-xs') : ''}${o.type ? typeIcon(o.type, true) : ''}<span class="cb-opt-main">${o.keyText ? html`<span class="cb-key">${o.keyText}</span>` : ''}<span class="cb-label">${o.label}</span>${o.sub ? html`<span class="cb-sub">${o.sub}</span>` : ''}</span>${o.status ? statusPill(o.status) : ''}</li>`);
    }
    setHTML(list, html`${items}`);
    list.classList.remove('is-flip');
    const body = list.closest('.cf-body');
    if (body && list.getBoundingClientRect().right > body.getBoundingClientRect().right - 4) list.classList.add('is-flip');
    if (st.active >= 0 && st.options[st.active]) { input.setAttribute('aria-activedescendant', `${id}-o${st.active}`); const a = $(`#${CSS.escape(id)}-o${st.active}`); if (a) a.scrollIntoView({ block: 'nearest' }); } else input.removeAttribute('aria-activedescendant');
  }
  function cbSearch(fid, query) {
    const cf = S.cf;
    const f = cfField(fid);
    const st = cbState(fid);
    const seq = ++st.seq;
    st.open = true;
    st.error = '';
    clearTimeout(st.timer);
    if (CF_LOCAL.has(f.kind)) {
      const q = query.trim().toLowerCase();
      const sel = cbSelected(f);
      st.options = (f.options || []).filter((o) => !sel.has(String(o.id)) && (!q || String(o.name).toLowerCase().includes(q))).map((o) => ({ key: String(o.id), label: o.name, value: String(o.id) }));
      st.active = st.options.length ? 0 : -1;
      cbRenderList(fid);
      return;
    }
    st.loading = true;
    cbRenderList(fid);
    st.timer = setTimeout(async () => {
      try {
        let opts;
        if (f.kind === 'user' || f.kind === 'users') {
          const users = await api('searchAssignable', cf.mode === 'edit' ? { key: cf.key, query } : { project: cf.project, query });
          const sel = cbSelected(f);
          opts = (users || []).filter((u) => !sel.has(u.name)).map((u) => ({ key: u.name, label: u.displayName || u.name, sub: u.name, avatar: u.displayName || u.name, value: { name: u.name, displayName: u.displayName || u.name } }));
          if (f.kind === 'user' && !f.required && !query.trim()) opts.unshift({ key: '__none', label: t('issue.unassigned'), value: null });
        } else {
          const refs = await api('searchIssuesFor', { purpose: f.kind === 'epic' ? 'epic' : 'parent', project: cf.project, query });
          opts = (refs || []).map((r) => ({ key: r.key, keyText: r.key, label: r.name || r.summary || '', sub: r.name && r.summary && r.name !== r.summary ? r.summary : '', type: r.type, status: r.status, value: { key: r.key, summary: r.summary || r.name || '' } }));
        }
        if (S.cf !== cf || seq !== st.seq) return;
        st.options = opts;
        st.active = opts.length ? 0 : -1;
      } catch (e) {
        if (S.cf !== cf || seq !== st.seq) return;
        st.options = [];
        st.error = errorText(e);
      }
      st.loading = false;
      cbRenderList(fid);
    }, query ? 220 : 0);
  }
  function cbClose(fid, revert) {
    const st = cbState(fid);
    st.open = false;
    clearTimeout(st.timer);
    st.seq++;
    cbRenderList(fid);
    if (revert) {
      const f = cfField(fid);
      const input = $(`#${CSS.escape(cfId(fid))}`);
      const v = S.cf.values[fid];
      if (input && (f.kind === 'user' || f.kind === 'epic' || f.kind === 'parent')) input.value = v ? (f.kind === 'user' ? v.displayName || v.name : `${v.key}${v.summary ? ' ' + v.summary : ''}`) : '';
    }
  }
  function cbPick(fid, i) {
    const f = cfField(fid);
    const st = cbState(fid);
    const o = st.options[i];
    if (!o) return;
    st.open = false;
    if (CF_MULTI.has(f.kind)) {
      const cur = Array.isArray(S.cf.values[fid]) ? S.cf.values[fid].slice() : [];
      cur.push(o.value);
      cfSetValue(fid, cur, '.cb-input');
    } else cfSetValue(fid, o.value, '.cb-input');
    $('#cf-live').textContent = t('cf.picked', { value: o.keyText ? `${o.keyText} ${o.label}` : o.label });
  }
  function addLabel(fid, input) {
    const raw = input.value.replace(/,/g, ' ').trim();
    if (!raw) return false;
    const parts = raw.split(/\s+/);
    if (parts.length > 1 && input.value.includes(' ') && !input.value.includes(',')) { cfSetError(fid, t('cf.labelSpaces')); return true; }
    const cur = Array.isArray(S.cf.values[fid]) ? S.cf.values[fid].slice() : [];
    parts.forEach((p) => { if (!cur.includes(p)) cur.push(p); });
    cfSetValue(fid, cur, '.cb-input');
    return true;
  }

  /* --- events --- */
  function onCfInput(e) {
    const cf = S.cf;
    if (!cf) return;
    const c = e.target;
    const fid = c.dataset && c.dataset.field;
    if (c.dataset && c.dataset.mask) autoMask(c, e);
    if (!fid) return;
    cf.dirty = true;
    if (cf.errors[fid]) cfSetError(fid, null);
    const f0 = cfField(fid);
    if (f0 && f0.kind === 'estimate') cf.raw[fid] = Object.assign({}, cf.raw[fid] || {}, { [c.dataset.part]: c.value });
    else if (f0 && ['number', 'storyPoints', 'date', 'datetime'].includes(f0.kind) && c.tagName === 'INPUT') cf.raw[fid] = c.value;
    if (c.classList.contains('cb-input')) {
      const f = cfField(fid);
      if (f.kind !== 'labels') cbSearch(fid, c.value);
      return;
    }
    if (c.classList.contains('cf-wiki-text')) cf.values[fid] = c.value;
  }
  function onCfChange(e) {
    const cf = S.cf;
    if (!cf) return;
    const c = e.target;
    if (c.id === 'cf-another') { cf.createAnother = c.checked; return; }
    if (c.dataset.cfControl === 'project' && c.value !== cf.project) {
      const keep = cfKeepValues();
      cf.project = c.value;
      cf.fields = [];
      cf.issueTypes = [];
      cf.focusAfterLoad = '#cf-project';
      cfLoad({ keep }).then(() => {
        if (S.cf === cf && cf.droppedDescription) toast(t('cf.descDropped', { project: cf.project }), { tone: 'warn' });
      });
      return;
    }
    if (c.dataset.field) { cf.dirty = true; cfCollectRaw(); }
  }
  function onCfFocusOut(e) {
    const cf = S.cf;
    if (!cf || cf.replacing) return;
    const c = e.target;
    const fid = c.dataset && c.dataset.field;
    const cb = c.closest && c.closest('.cb');
    if (cb && !cb.contains(e.relatedTarget)) {
      const f = cfField(cb.dataset.cb);
      if (f && f.kind === 'labels' && c.value.trim()) addLabel(f.id, c);
      else if (f) cbClose(f.id, true);
    }
    if (!fid) return;
    const f = cfField(fid);
    if (!f) return;
    // on-blur checks with the same rules as submit
    if (c.dataset.mask === 'date' && c.value.trim()) {
      const iso = i18n.parseInputDate(c.value.trim());
      if (iso) c.value = i18n.formatInputDate(iso); else cfSetError(fid, t('settings.dateInvalid'));
    }
    if (f.kind === 'estimate') {
      const v = c.value.trim();
      if (v && !DURATION.test(v)) cfSetError(fid, t('cf.estimateInvalid'));
    }
    if ((f.kind === 'number' || (f.kind === 'storyPoints' && !(f.options && f.options.length))) && c.value.trim() && !/^-?\d+([.,]\d+)?$/.test(c.value.trim())) cfSetError(fid, t('cf.numberInvalid'));
  }
  function onCfKey(e) {
    const cf = S.cf;
    if (!cf) return;
    const c = e.target;
    // issue type radios: arrows
    const ty = c.closest && c.closest('[data-cf-type]');
    if (ty && ['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(e.key)) {
      e.preventDefault();
      const all = $$('#cf-body [data-cf-type]');
      const i = all.indexOf(ty);
      const n = e.key === 'Home' ? 0 : e.key === 'End' ? all.length - 1 : (i + (e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 1) + all.length) % all.length;
      cfPickType(all[n].dataset.cfType, true);
      return;
    }
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); cfSubmit(); return; }
    if (!c.classList || !c.classList.contains('cb-input')) {
      if (e.key === 'Enter' && c.tagName === 'INPUT' && c.type !== 'checkbox') e.preventDefault(); // no implicit submit
      return;
    }
    const fid = c.dataset.field;
    const f = cfField(fid);
    if (f.kind === 'labels') {
      if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); addLabel(fid, c); }
      else if (e.key === 'Backspace' && !c.value) { const cur = (cf.values[fid] || []).slice(); if (cur.length) { cur.pop(); cfSetValue(fid, cur, '.cb-input'); } }
      return;
    }
    const st = cbState(fid);
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        if (!st.open) { cbSearch(fid, CF_MULTI.has(f.kind) ? c.value : ''); return; }
        st.active = Math.min(st.options.length - 1, st.active + 1); cbRenderList(fid); break;
      case 'ArrowUp':
        e.preventDefault();
        if (st.open) { st.active = Math.max(0, st.active - 1); cbRenderList(fid); } break;
      case 'Enter':
        e.preventDefault();
        if (st.open && st.active >= 0) cbPick(fid, st.active);
        break;
      case 'Escape':
        if (st.open) { e.preventDefault(); e.stopPropagation(); cbClose(fid, true); }
        break;
      case 'Backspace':
        if (CF_MULTI.has(f.kind) && !c.value) { const cur = (cf.values[fid] || []).slice(); if (cur.length) { cur.pop(); cfSetValue(fid, cur, '.cb-input'); } }
        break;
      default:
    }
  }
  function cfPickType(typeId, focus) {
    const cf = S.cf;
    if (!cf || String(typeId) === String(cf.issueTypeId) || cf.loading) return;
    const keep = cfKeepValues();
    cf.issueTypeId = typeId;
    cf.fields = [];
    cfLoad({ issueTypeId: typeId, keep }).then(() => {
      if (focus) { const b = $(`#cf-body [data-cf-type="${CSS.escape(String(typeId))}"]`); if (b) b.focus(); }
    });
  }
  function onCfClick(e) {
    const cf = S.cf;
    if (!cf) return;
    const opt = e.target.closest('.cb-opt');
    if (opt) { const cb = opt.closest('.cb'); cbPick(cb.dataset.cb, Number(opt.dataset.i)); return; }
    const ty = e.target.closest('[data-cf-type]');
    if (ty) { cfPickType(ty.dataset.cfType, true); return; }
    const a = e.target.closest('[data-cf], [data-action="cf-retry"]');
    if (!a) {
      const box = e.target.closest('.cb-box');
      const inp = box && $('.cb-input', box);
      if (!inp) return;
      if (e.target !== inp) inp.focus();
      const f = cfField(inp.dataset.field);
      // DR-21: a click shows the choices (the full list; single pickers: the first matches for an empty query)
      if (f && f.kind !== 'labels' && !cbState(f.id).open) cbSearch(f.id, CF_MULTI.has(f.kind) ? inp.value : '');
      return;
    }
    if (a.dataset.action === 'cf-retry') { cfLoad({ keep: cf.mode === 'create' ? cfKeepValues() : null }); return; }
    const fid = a.dataset.field;
    switch (a.dataset.cf) {
      case 'close': case 'cancel': cfClose(false); break;
      case 'submit': cfSubmit(); break;
      case 'reload': cfReloadConflict(); break;
      case 'open-created': openIssue(a.dataset.key); break;
      case 'goto': { // DR-24: links in the error summary
        const target = $(`#${CSS.escape(cfId(fid))}`) || $(`#cf-body [data-field="${CSS.escape(fid)}"]:not(.link-btn):not(.cb-chip-x)`);
        if (target) { target.focus(); target.scrollIntoView({ block: 'nearest' }); }
        break;
      }
      case 'open-jira': cfOpenInJira(); break;
      case 'assign-me': { const u = me(); if (u) { cfSetValue(fid, { name: u.name, displayName: u.displayName || u.name }, '.cb-input'); $('#cf-live').textContent = t('cf.picked', { value: u.displayName || u.name }); } break; }
      case 'unassign': cfSetValue(fid, null, '.cb-input'); break;
      case 'clear': cfSetValue(fid, null, '.cb-input'); break;
      case 'chip-remove': {
        const f = cfField(fid);
        const cur = (cf.values[fid] || []).filter((x) => (f.kind === 'users' ? x.name : String(x)) !== a.dataset.key);
        cfSetValue(fid, cur, '.cb-input');
        $('#cf-live').textContent = t('cf.removed', { value: a.dataset.key });
        break;
      }
      case 'preview': {
        const ta = $('#cf-body .cf-wiki-text');
        if (ta) cf.values[ta.dataset.field] = ta.value;
        cf.wikiPreview = !cf.wikiPreview;
        cfRerenderField(ta.dataset.field, '[data-cf="preview"]');
        break;
      }
      default:
    }
  }

  /* ================================================================
   * EPICS & STORIES: expandable tree (ARIA tree, roving tabindex).
   * Epics mode: epic → stories → tasks, then the epic's direct tasks; "Stories without epic" section.
   * Stories mode: story → tasks. Expansion is remembered.
   * ================================================================ */
  /** Drops cached epic/story trees; answers already in flight are not cached afterwards (CR-14). */
  function invalidateEpics() {
    S.ep.cache.clear();
    S.ep.cacheGen++;
  }
  const epCacheKey = () => `${S.ep.mode}|${S.ep.includeDone ? 1 : 0}|${S.ep.mine ? 'mine' : 'all'}|${effProject()}`;

  function enterEpics() {
    const key = epCacheKey();
    const cached = S.ep.cache.get(key) || (S.ep.data && S.ep.data.key === key ? S.ep.data : null);
    if (cached && !S.ep.stale) {
      // CR-8: a request still in flight for other options must not overwrite this view.
      ++S.ep.seq;
      S.ep.loading = false;
      S.ep.data = cached;
      S.ep.error = null;
      renderEpics(); renderRefreshState();
      return;
    }
    if (cached) S.ep.data = cached;
    loadEpics({ silent: !!cached });
  }

  async function loadEpics({ force, silent } = {}) {
    // CR-8: options are captured now; the response is shaped and cached for them, whatever the UI shows later.
    const key = epCacheKey();
    const gen = S.ep.cacheGen; // CR-14: an invalidation while in flight means this answer may be outdated: don't cache it
    const mode = S.ep.mode;
    const params = { includeDone: S.ep.includeDone, scope: S.ep.mine ? 'mine' : 'all' };
    const project = effProject();
    if (project) params.project = project;
    if (force) params.force = true;
    const seq = ++S.ep.seq;
    S.ep.reqKey = key;
    S.ep.loading = true;
    S.ep.stale = false;
    const cached = S.ep.cache.get(key);
    if (!silent || !S.ep.data || S.ep.data.key !== key) { S.ep.data = cached || null; S.ep.error = null; }
    renderEpics(); renderRefreshState();
    try {
      const res = await api(mode === 'stories' ? 'getStories' : 'getEpics', params);
      const data = mode === 'stories'
        ? { key, mode, stories: (res && res.stories) || [], fetchedAt: res && res.fetchedAt, truncated: !!(res && res.truncated) }
        : { key, mode, epics: (res && res.epics) || [], orphanStories: (res && res.orphanStories) || [], fetchedAt: res && res.fetchedAt, truncated: !!(res && res.truncated), timePartial: !!(res && res.timePartial) };
      if (gen === S.ep.cacheGen) S.ep.cache.set(key, data);
      if (seq !== S.ep.seq) return;
      S.ep.data = data;
      S.ep.error = null;
    } catch (e) {
      if (seq !== S.ep.seq) return;
      if (dropBadProject(e, project)) { S.ep.loading = false; if (S.view === 'epics') enterEpics(); return; }
      S.ep.error = e;
      if (S.ep.data) toastError(t('epics.loadFailed'), e);
    } finally {
      if (seq === S.ep.seq) { S.ep.loading = false; renderEpics(); renderRefreshState(); }
    }
  }

  function setEpicsOption(patch) {
    Object.assign(S.ep, patch);
    prefs.set('epics.mode', S.ep.mode);
    prefs.set('epics.done', S.ep.includeDone);
    prefs.set('epics.mine', S.ep.mine);
    S.ep.overrides.clear();
    enterEpics();
  }

  const refText = (r) => [r && r.key, r && r.summary, r && r.name].map((x) => String(x || '').toLowerCase());
  const refHit = (r, q) => !q || refText(r).some((s) => s.includes(q));
  function filterStory(node, q) {
    const tasks = node.tasks || [];
    if (!q || refHit(node.story, q)) return { node, tasks, forced: false };
    const hits = tasks.filter((r) => refHit(r, q));
    return hits.length ? { node, tasks: hits, forced: true } : null;
  }
  function filterEpic(node, q) {
    const stories = node.stories || [];
    const tasks = node.tasks || [];
    if (!q || refHit(node.epic, q)) return { node, stories: stories.map((s) => ({ node: s, tasks: s.tasks || [], forced: false })), tasks, forced: false };
    const fs = stories.map((s) => filterStory(s, q)).filter(Boolean);
    const ft = tasks.filter((r) => refHit(r, q));
    return fs.length || ft.length ? { node, stories: fs, tasks: ft, forced: true } : null;
  }
  function isOpen(id, forced) {
    if (S.ep.overrides.has(id)) return S.ep.overrides.get(id);
    return forced || S.ep.expanded.has(id);
  }

  function treeBuilder() {
    let seq = 0;
    const lid = () => 'tl-' + (++seq);
    const kids = (id, inner) => html`<div class="tkids-wrap${S.ep.opening === id ? ' is-opening' : ''}"><ul role="group" class="tkids">${inner}</ul></div>`;
    const chev = (expandable) => (expandable ? html`<span class="tchev" aria-hidden="true">${icon('chevron-right')}</span>` : html`<span class="tchev tchev-leaf" aria-hidden="true"></span>`);
    const mineCls = (r) => (r && r.mine ? ' is-mine' : '');
    /** DR-11: fixed slots (extra · assignee · progress · status) so bars and pills line up across levels. */
    const meta = (extra, av, pg, status) => html`<span class="tmeta"><span class="tm-extra">${extra}</span><span class="tm-av">${av}</span><span class="tm-pg">${pg}</span><span class="tm-st">${statusPill(status)}</span></span>`;
    const doneCls = (r) => (catOf(r && r.status) === 'done' ? ' is-done' : '');

    function taskRow(r, parentId, level) {
      const id = 't:' + parentId + '/' + r.key;
      const l = lid();
      return html`<li role="treeitem" class="tnode tnode-task${mineCls(r)}${doneCls(r)}" aria-level="${level}" aria-labelledby="${l}" tabindex="-1" data-node="${id}" data-key="${r.key}" data-focus-key="${r.key}">
        <div class="trow">${chev(false)}${typeIcon(r.type)}<span class="tlabel" id="${l}" data-act="open"><span class="tkey">${r.key}</span><span class="tsum" title="${r.summary || ''}">${r.summary || ''}</span></span>
          ${meta('', assigneeAvatar(r.assignee), '', r.status)}</div></li>`;
    }
    function storyRow(f, level, showEpic) {
      const s = f.node;
      const r = s.story || {};
      const id = 's:' + r.key;
      const tasks = f.tasks;
      const open = tasks.length > 0 && isOpen(id, f.forced);
      const l = lid();
      return html`<li role="treeitem" class="tnode tnode-story${mineCls(r)}${doneCls(r)}" aria-level="${level}" ${tasks.length ? html`aria-expanded="${open ? 'true' : 'false'}"` : ''} aria-labelledby="${l}" tabindex="-1" data-node="${id}" data-key="${r.key}" data-focus-key="${r.key}">
        <div class="trow">${chev(tasks.length > 0)}${typeIcon(r.type || { kind: 'story' })}<span class="tlabel" id="${l}" data-act="open"><span class="tkey">${r.key}</span><span class="tsum" title="${r.summary || ''}">${r.summary || ''}</span></span>
          ${meta(html`${showEpic && s.epic ? epicChip(s.epic) : ''}${spBadge(s.storyPoints != null ? s.storyPoints : r.storyPoints)}${(s.tasks || []).length ? html`<span class="tcount">${t('epics.tasksCount', { n: s.tasks.length })}</span>` : ''}`, assigneeAvatar(r.assignee), (s.tasks || []).length ? progressBar(s.progress, { size: 'sm' }) : '', r.status)}</div>
        ${open ? kids(id, tasks.map((tk) => taskRow(tk, r.key, level + 1))) : ''}</li>`;
    }
    function epicRow(f) {
      const n = f.node;
      const e = n.epic || {};
      const id = 'e:' + e.key;
      const has = f.stories.length + f.tasks.length > 0;
      const open = has && isOpen(id, f.forced);
      const l = lid();
      const counts = [(n.stories || []).length ? t('epics.storiesCount', { n: n.stories.length }) : '', (n.tasks || []).length ? t('epics.tasksCount', { n: n.tasks.length }) : ''].filter(Boolean).join(' · ') || t('epics.noChildren');
      return html`<li role="treeitem" class="tnode tnode-epic${mineCls(e)}${doneCls(e)}" aria-level="1" ${has ? html`aria-expanded="${open ? 'true' : 'false'}"` : ''} aria-labelledby="${l}" tabindex="-1" data-node="${id}" data-key="${e.key}" data-focus-key="${e.key}" data-epic="${epicTone(e.color, e.key)}">
        <div class="trow">${chev(has)}${typeIcon(e.type || { kind: 'epic' })}<span class="tlabel" id="${l}" data-act="open"><span class="tname" title="${e.summary && e.summary !== e.name ? (e.name || '') + ' · ' + e.summary : e.name || e.summary || ''}">${e.name || e.summary || e.key}</span><span class="tkey">${e.key}</span></span>
          ${meta(html`<span class="tcount">${counts}</span>`, html`<button type="button" class="icon-btn icon-btn-sm t-add" data-ep-create="${e.key}" tabindex="-1" aria-label="${t('cf.createInEpic', { name: e.name || e.summary || e.key })}" title="${t('cf.createInEpicTitle', { name: e.name || e.summary || e.key })}">${icon('plus')}</button>`, progressBar(n.progress, { size: 'sm' }), e.status)}</div>
        ${open ? kids(id, html`${f.stories.map((s) => storyRow(s, 2, false))}${f.tasks.length && f.stories.length ? html`<li role="none" class="tdivider"><span>${t('epics.directTasks')}</span></li>` : ''}${f.tasks.map((tk) => taskRow(tk, e.key, 2))}`) : ''}</li>`;
    }
    return { epicRow, storyRow };
  }

  function treeSkeleton() {
    return html`<div class="tree-skeleton" role="status" aria-label="${t('common.loading')}">${[70, 55, 80, 62, 48, 74].map((w) => html`<div class="sk-row"><span class="skeleton sk-dot"></span><span class="skeleton sk-line" style="width:${w}%"></span><span class="skeleton sk-bar"></span></div>`)}</div>`;
  }

  /* ================================================================
   * EPIC DASHBOARD (SPEC v0.4 §3): per project, a donut of each epic's share of the logged time
   * (or the estimate) with a legend. Same data as the tree (Show done, Only mine, project filter;
   * not the search). Built once per data + options and reused across tree re-renders, so hover and
   * focus survive expanding tree rows.
   * ================================================================ */
  const DASH_SLICES = 8;          // epics with their own slice; the rest go to "Other"
  const DASH_MIN_SHARE = 0.02;    // so do slices under 2 %
  const DONUT = { c: 60, r: 43 }; // viewBox 0 0 120 120; ring width is .epd-slice stroke-width
  const nfCache = new Map();
  function nf(opts) {
    const k = i18n.locale + JSON.stringify(opts);
    if (!nfCache.has(k)) nfCache.set(k, new Intl.NumberFormat(i18n.locale, opts));
    return nfCache.get(k);
  }
  const fmtHours = (sec) => t('epics.dash.hours', { n: nf({ minimumFractionDigits: 1, maximumFractionDigits: 1 }).format((Number(sec) || 0) / 3600) });
  /** pct: whole percent when already rounded (top-level rows), else rounded here; non-zero below 1 % ⇒ "<1 %". */
  function fmtShare(part, total, pct) {
    const f = nf({ style: 'percent', maximumFractionDigits: 0 });
    const p = pct != null ? pct : Math.round(total > 0 ? (part / total) * 100 : 0);
    return part > 0 && p < 1 ? '<' + f.format(0.01) : f.format(p / 100);
  }
  /** CR-3: whole percents that add up to exactly 100 (largest remainder). */
  function roundShares(values, total) {
    if (!(total > 0)) return values.map(() => 0);
    const raw = values.map((v) => (v / total) * 100);
    const out = raw.map(Math.floor);
    let left = 100 - out.reduce((a, v) => a + v, 0);
    raw.map((v, i) => [v - out[i], i]).sort((a, b) => b[0] - a[0]).forEach(([, i]) => { if (left > 0) { out[i]++; left--; } });
    return out;
  }
  const dashMetricName = (m) => t(m === 'estimate' ? 'epics.dash.estimate' : 'epics.dash.logged');

  /** Cards by project key: top slices, "Other" (beyond the top 8 or < 2 %) and zero-time epics. */
  function dashModel(data, metric) {
    const field = metric === 'estimate' ? 'estimateSeconds' : 'spentSeconds';
    const groups = new Map();
    (data.epics || []).forEach((n) => {
      const e = n && n.epic;
      if (!e || !e.key) return;
      const tm = n.time || {};
      const pk = projectOf(e);
      if (!groups.has(pk)) groups.set(pk, []);
      groups.get(pk).push({ key: e.key, name: e.name || e.summary || e.key, tone: epicTone(e.color, e.key),
        sec: Math.max(0, Number(tm[field]) || 0), issues: Math.max(0, Number(tm.issues) || 0) });
    });
    const byKey = (a, b) => a.key.localeCompare(b.key, undefined, { numeric: true });
    return [...groups].sort((a, b) => a[0].localeCompare(b[0])).map(([key, items]) => {
      const total = items.reduce((a, x) => a + x.sec, 0);
      const timed = items.filter((x) => x.sec > 0).sort((a, b) => b.sec - a.sec || byKey(a, b));
      const slices = timed.filter((x, i) => i < DASH_SLICES && x.sec / total >= DASH_MIN_SHARE); // a prefix: sorted by size
      const other = timed.slice(slices.length);
      const otherSec = other.reduce((a, x) => a + x.sec, 0);
      const pcts = roundShares([...slices.map((x) => x.sec), otherSec], total);
      slices.forEach((x, i) => { x.pct = pcts[i]; });
      return { key, name: projectName(key), total, epics: items.length, slices, other, otherPct: pcts[slices.length],
        otherSec, otherIssues: other.reduce((a, x) => a + x.issues, 0),
        zero: items.filter((x) => x.sec <= 0).sort(byKey) };
    });
  }

  function donutSVG(c, label) {
    const parts = c.slices.map((x, i) => ({ id: String(i), sec: x.sec, tone: x.tone }));
    if (c.otherSec > 0) parts.push({ id: 'other', sec: c.otherSec, tone: null });
    const { c: C, r: R } = DONUT;
    const pt = (a) => `${(C + R * Math.sin(a)).toFixed(2)} ${(C - R * Math.cos(a)).toFixed(2)}`; // 0 = top, clockwise
    const gap = parts.length > 1 ? 0.03 : 0; // thin gap between slices (radians)
    const MIN_ARC = 0.02;                    // CR-5: drawn length of the tiniest part, taken from the largest one
    const sweeps = parts.map((p) => (p.sec / c.total) * 2 * Math.PI);
    if (parts.length > 1) {
      let need = 0;
      sweeps.forEach((v, i) => { if (v < MIN_ARC + gap) { need += MIN_ARC + gap - v; sweeps[i] = MIN_ARC + gap; } });
      const big = sweeps.indexOf(Math.max(...sweeps));
      sweeps[big] -= need;
    }
    let start = 0;
    const shapes = parts.map((p, i) => {
      const sweep = sweeps[i];
      const a0 = start + gap / 2;
      const a1 = start + sweep - gap / 2;
      start += sweep;
      const attrs = html`class="epd-slice${p.tone ? '' : ' is-other'}" data-slice="${p.id}"${p.tone ? html` data-epic="${p.tone}"` : ''} data-mid="${((a0 + a1) / 2).toFixed(4)}"`;
      return parts.length === 1
        ? html`<circle cx="${C}" cy="${C}" r="${R}" ${attrs}></circle>`
        : html`<path d="M${pt(a0)}A${R} ${R} 0 ${a1 - a0 > Math.PI ? 1 : 0} 1 ${pt(a1)}" ${attrs}></path>`;
    });
    return html`<svg class="epd-donut" viewBox="0 0 120 120" role="img" aria-label="${label}">${shapes}</svg>`;
  }

  function dashCard(c, metric) {
    const id = 'epd-' + domId(c.key);
    const metricName = dashMetricName(metric);
    const row = (x, slice, cls) => {
      const hours = fmtHours(x.sec);
      const share = fmtShare(x.sec, c.total, slice != null && slice !== 'other' ? x.pct : null);
      const label = t('epics.dash.rowLabel', { name: x.name, key: x.key, hours, share, issues: t('board.count', { n: x.issues }) });
      return html`<li><button type="button" class="epd-row${cls ? ' ' + cls : ''}" tabindex="-1" data-dash-open="${x.key}" data-dfocus="row:${x.key}"${cls === 'is-other' ? '' : html` data-epic="${x.tone}"`}${slice != null ? html` data-slice="${slice}" data-tip="${x.name}" data-tip-val="${hours} · ${share}"` : ''} aria-label="${label}">
        <span class="epd-dot" aria-hidden="true"></span><span class="epd-name" aria-hidden="true"><span class="epd-ename">${x.name}</span><span class="epd-key">${x.key}</span></span><span class="epd-num" aria-hidden="true">${hours}</span><span class="epd-num" aria-hidden="true">${share}</span><span class="epd-num" aria-hidden="true">${x.issues}</span></button></li>`;
    };
    let otherRow = '';
    if (c.other.length) {
      const open = S.ep.dash.otherOpen.has(c.key);
      const name = `${t('epics.dash.other')} · ${t('epics.epicsTotal', { n: c.other.length })}`;
      const hours = fmtHours(c.otherSec);
      const share = fmtShare(c.otherSec, c.total, c.otherPct);
      otherRow = html`<li><button type="button" class="epd-row is-other is-group" tabindex="-1" data-dash-other="${c.key}" data-dfocus="other:${c.key}" data-slice="other" data-tip="${name}" data-tip-val="${hours} · ${share}" aria-expanded="${open ? 'true' : 'false'}" aria-controls="${id}-other" aria-label="${t('epics.dash.otherLabel', { name, hours, share, issues: t('board.count', { n: c.otherIssues }) })}">
          <span class="epd-dot" aria-hidden="true"></span><span class="epd-name" aria-hidden="true"><span class="epd-ename">${name}</span>${icon('chevron-down', 'epd-chev')}</span><span class="epd-num" aria-hidden="true">${hours}</span><span class="epd-num" aria-hidden="true">${share}</span><span class="epd-num" aria-hidden="true">${c.otherIssues}</span></button>
        <ul class="epd-rows epd-subrows" role="list" id="${id}-other"${open ? '' : html` hidden`}>${c.other.map((x) => row(x, 'other', 'is-other'))}</ul></li>`;
    }
    const sliceNames = [...c.slices.map((x) => `${x.name} ${fmtShare(x.sec, c.total, x.pct)}`), ...(c.otherSec > 0 ? [`${t('epics.dash.other')} ${fmtShare(c.otherSec, c.total, c.otherPct)}`] : [])];
    const chartLabel = t('epics.dash.chartLabel', { metric: metricName, project: c.key, total: fmtHours(c.total), list: sliceNames.join(', ') });
    const total = fmtHours(c.total);
    const body = c.total > 0
      ? html`<div class="epd-card-body">
          <div class="epd-chart">${donutSVG(c, chartLabel)}<div class="epd-center" aria-hidden="true"><span class="epd-total${total.length > 8 ? ' is-long' : ''}">${total}</span><span class="epd-metric">${metricName}</span></div></div>
          <div class="epd-legend">
            <div class="epd-lhead" aria-hidden="true"><span></span><span>${t('epics.dash.colEpic')}</span><span>${t('epics.dash.colHours')}</span><span>${t('epics.dash.colShare')}</span><span>${t('epics.dash.colIssues')}</span></div>
            <ul class="epd-rows" role="list" aria-label="${t('epics.dash.legendLabel', { project: c.key })}">${c.slices.map((x, i) => row(x, i))}${otherRow}${c.zero.map((x) => row(x, null, 'is-zero'))}</ul>
          </div>
        </div>
        <div class="epd-tip" aria-hidden="true" hidden></div>`
      : html`<p class="epd-empty">${icon('clock')}<span>${t(metric === 'estimate' ? 'epics.dash.noEstimate' : 'epics.dash.noLogged')}</span></p>`;
    return html`<article class="epd-card" aria-labelledby="${id}-t">
      <header class="epd-card-head">${c.name ? html`<span class="sp-pkey">${c.key}</span>` : ''}<h3 class="epd-card-title" id="${id}-t">${c.name || c.key}</h3><span class="epd-card-meta">${t('epics.epicsTotal', { n: c.epics })}</span></header>
      ${body}
    </article>`;
  }

  function dashHTML(data, entering) {
    const d = S.ep.dash;
    const cards = dashModel(data, d.metric);
    const total = cards.reduce((a, c) => a + c.total, 0);
    const seg = html`<div class="segmented segmented-sm" role="radiogroup" aria-label="${t('epics.dash.metricLabel')}">${['spent', 'estimate'].map((m) => html`<button type="button" role="radio" class="seg-btn" data-dash-metric="${m}" data-dfocus="metric:${m}" aria-checked="${d.metric === m ? 'true' : 'false'}" tabindex="${d.metric === m ? '0' : '-1'}">${dashMetricName(m)}</button>`)}</div>`;
    return html`<section class="epd${d.collapsed ? '' : ' is-open'}" aria-labelledby="epd-toggle">
      <div class="epd-head">
        <h2 class="epd-title"><button type="button" class="epd-toggle" id="epd-toggle" data-dash-toggle data-dfocus="toggle" aria-expanded="${d.collapsed ? 'false' : 'true'}" aria-controls="epd-body">${icon('chevron-right', 'epd-chev')}<span>${t('epics.dash.title')}</span></button></h2>
        <span class="epd-sum">${t('epics.dash.summary', { metric: dashMetricName(d.metric), total: fmtHours(total) })}</span>
        ${d.collapsed ? '' : seg}
      </div>
      <div class="epd-body" id="epd-body"${d.collapsed ? html` hidden` : ''}>
        ${data.timePartial ? html`<div class="notice notice-tip" role="note">${icon('info')}<span>${t('epics.dash.partial')}</span></div>` : ''}
        <div class="epd-cards${entering ? ' is-entering' : ''}">${cards.map((c) => dashCard(c, d.metric))}</div>
      </div>
    </section>`;
  }

  /** The dashboard element for this data (cached), or null: no epics, or an older backend without `time`. */
  function epicDashboard(data) {
    const d = S.ep.dash;
    const epics = data.epics || [];
    if (!epics.some((n) => n && n.time)) return null;
    const names = [...new Set(epics.map((n) => projectOf(n.epic || {})))].map(projectName).join(',');
    const sig = [d.metric, d.collapsed, [...d.otherOpen].join(','), i18n.lang, names].join('|');
    if (d.el && d.data === data && d.sig === sig) return d.el;
    // CR-4: animate when the view changes (options / project), not on a silent reload of the same view.
    const entering = !d.data || d.data.key !== data.key || d.shownMetric !== d.metric || (d.wasCollapsed && !d.collapsed);
    const box = document.createElement('div');
    setHTML(box, dashHTML(data, entering));
    const el = box.firstElementChild;
    // Roving tabindex: one tab stop per legend (top-level rows; "Other" members join when shown).
    $$('.epd-legend > .epd-rows', el).forEach((list) => { const first = $('.epd-row', list); if (first) first.tabIndex = 0; });
    const cards = $('.epd-cards', el);
    if (entering) setTimeout(() => cards.classList.remove('is-entering'), 600); // re-inserting must not replay it
    el.addEventListener('pointerover', (e) => dashHighlight(e.target.closest('.epd-slice, .epd-row')));
    el.addEventListener('pointerleave', () => dashHighlight(el.contains(document.activeElement) ? document.activeElement.closest('.epd-row') : null));
    el.addEventListener('focusin', (e) => dashHighlight(e.target.closest('.epd-row')));
    el.addEventListener('focusout', (e) => { if (!el.contains(e.relatedTarget)) dashHighlight(null); });
    Object.assign(d, { el, data, sig, shownMetric: d.metric, wasCollapsed: d.collapsed });
    return el;
  }

  /** Highlights a slice and its legend row(s) and shows the tooltip at the slice; null clears. */
  function dashHighlight(target) {
    const root = S.ep.dash.el;
    if (!root) return;
    $$('.epd-card.has-hl', root).forEach((card) => {
      card.classList.remove('has-hl');
      $$('.is-hl', card).forEach((x) => x.classList.remove('is-hl'));
      const tip = $('.epd-tip', card);
      if (tip) tip.hidden = true;
    });
    const card = target && target.closest('.epd-card');
    const slice = target && target.dataset.slice;
    if (!card || slice == null) return;
    const path = $(`.epd-slice[data-slice="${slice}"]`, card);
    const main = $(`.epd-row[data-slice="${slice}"]`, card);
    if (!path || !main) return;
    card.classList.add('has-hl');
    [path, main, target].forEach((x) => x.classList.add('is-hl'));
    const from = target.classList.contains('epd-row') ? target : main;
    const tip = $('.epd-tip', card);
    setHTML(tip, html`<strong>${from.dataset.tip}</strong><span>${from.dataset.tipVal}</span>`);
    tip.hidden = false;
    // Anchor: the middle of the slice on the ring, clamped to the card.
    const box = path.ownerSVGElement.getBoundingClientRect();
    const cr = card.getBoundingClientRect();
    const k = box.width / 120;
    const a = Number(path.dataset.mid) || 0;
    // DR-5: anchored just outside the ring and pushed away from it (right / left, above / below), so the centre
    // total and the card title stay visible; clamped to the card.
    const w = tip.offsetWidth;
    const h = tip.offsetHeight;
    const R = DONUT.r + 12; // ring radius + half the highlighted stroke + a little air
    const x = box.left - cr.left + (DONUT.c + R * Math.sin(a)) * k;
    const y = box.top - cr.top + (DONUT.c - R * Math.cos(a)) * k;
    const left = Math.sin(a) > 0 ? x + 4 : x - w - 4;
    const top = Math.cos(a) < 0 ? y + 4 : y - h - 4;
    tip.style.left = Math.max(4, Math.min(cr.width - w - 4, left)) + 'px';
    tip.style.top = Math.max(4, Math.min(cr.height - h - 4, top)) + 'px';
  }

  function openDashEpic(key, row) {
    let seed = null;
    forEachRef(key, (r) => { if (!seed) seed = r; });
    const focusKey = row && row.dataset.dfocus;
    openIssue(key, seed, { returnFocus: () => {
      const el = focusKey && S.ep.dash.el ? S.ep.dash.el.querySelector(`[data-dfocus="${CSS.escape(focusKey)}"]`) : null;
      return el && el.isConnected ? el : null;
    } });
  }

  function setDashMetric(m, focus) {
    const d = S.ep.dash;
    if (m !== 'spent' && m !== 'estimate') return;
    if (m !== d.metric) { d.metric = m; prefs.set('epics.dash.metric', m); renderEpics(); }
    if (focus && d.el) { const b = $(`[data-dash-metric="${m}"]`, d.el); if (b) b.focus(); }
  }

  /** Clicks inside the dashboard (delegated from #ep-list); true when handled. */
  function onDashClick(e) {
    const d = S.ep.dash;
    if (!d.el || !d.el.contains(e.target)) return false;
    if (e.target.closest('[data-dash-toggle]')) {
      d.collapsed = !d.collapsed;
      prefs.set('epics.dash.collapsed', d.collapsed);
      renderEpics();
      return true;
    }
    const metric = e.target.closest('[data-dash-metric]');
    if (metric) { setDashMetric(metric.dataset.dashMetric, false); return true; }
    const slice = e.target.closest('.epd-slice');
    const row = slice ? $(`.epd-row[data-slice="${slice.dataset.slice}"]`, slice.closest('.epd-card')) : e.target.closest('.epd-row');
    if (!row) return true;
    if (row.dataset.dashOther) {
      const key = row.dataset.dashOther;
      if (d.otherOpen.has(key)) d.otherOpen.delete(key); else d.otherOpen.add(key);
      renderEpics();
    } else if (row.dataset.dashOpen) openDashEpic(row.dataset.dashOpen, row);
    return true;
  }

  function focusDashRow(b, opts) {
    if (!b) return;
    $$('.epd-row', b.closest('.epd-legend')).forEach((x) => { x.tabIndex = -1; });
    b.tabIndex = 0;
    b.focus(opts);
  }

  /** Keys inside the dashboard: arrows in the metric switch and between legend rows; true when handled. */
  function onDashKey(e) {
    const d = S.ep.dash;
    if (!d.el || !d.el.contains(e.target) || e.ctrlKey || e.metaKey || e.altKey) return false;
    const seg = e.target.closest('[data-dash-metric]');
    if (seg) {
      if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(e.key)) return false;
      e.preventDefault();
      setDashMetric(e.key === 'Home' ? 'spent' : e.key === 'End' ? 'estimate' : seg.dataset.dashMetric === 'spent' ? 'estimate' : 'spent', true);
      return true;
    }
    const row = e.target.closest('.epd-row');
    if (!row) return false;
    const rows = $$('.epd-row', row.closest('.epd-legend')).filter((b) => !b.closest('[hidden]'));
    const i = rows.indexOf(row);
    const go = { ArrowDown: i + 1, ArrowUp: i - 1, Home: 0, End: rows.length - 1 }[e.key];
    if (go == null) return false;
    e.preventDefault();
    focusDashRow(rows[Math.max(0, Math.min(rows.length - 1, go))]);
    return true;
  }

  function renderEpics() {
    if (S.view !== 'epics' || activeSectionId() !== 'view-epics') return;
    $$('#ep-mode [data-mode]').forEach((b) => {
      const on = b.dataset.mode === S.ep.mode;
      b.setAttribute('aria-checked', on ? 'true' : 'false');
      b.tabIndex = on ? 0 : -1;
    });
    $('#ep-done').checked = S.ep.includeDone;
    const el = $('#ep-list');
    const summary = $('#ep-summary');
    const data = S.ep.data && S.ep.data.key === epCacheKey() ? S.ep.data : null;
    if (!data) {
      summary.textContent = '';
      if (S.ep.error && !S.ep.loading) setHTML(el, stateBlock({ iconName: 'alert', tone: 'error', fill: true, title: t('epics.loadFailed'), text: errorText(S.ep.error), action: { id: 'ep-retry', label: t('common.retry') } }));
      else setHTML(el, treeSkeleton());
      return;
    }
    const q = String(S.q.epics || '').trim().toLowerCase();
    const b = treeBuilder();
    const active = document.activeElement;
    const focusNode = active && el.contains(active) && active.dataset.node ? active.dataset.node : S.ep.focusNode;
    const inDash = !!(S.ep.dash.el && S.ep.dash.el.contains(active));
    const dashFocus = inDash ? active.dataset.dfocus || 'toggle' : null;
    const hadFocus = active && el.contains(active) && !inDash;
    const dash = S.ep.mode === 'epics' ? epicDashboard(data) : null;
    const slot = dash ? html`<div data-slot="dash"></div>` : '';
    /** Puts the (reused) dashboard element back in place and returns focus to it if it had it. */
    const placeDash = () => {
      if (!dash) return;
      $('[data-slot="dash"]', el).replaceWith(dash);
      if (!dashFocus) return;
      const f = dash.querySelector(`[data-dfocus="${CSS.escape(dashFocus)}"]`) || $('#epd-toggle', dash);
      if (f.classList.contains('epd-row')) focusDashRow(f, { preventScroll: true }); else f.focus({ preventScroll: true });
    };
    const scroller = $('#ep-scroll');
    const scrollTop = scroller.scrollTop;

    const sections = [];
    let shown = 0;
    if (S.ep.mode === 'stories') {
      const list = (data.stories || []).map((s) => filterStory(s, q)).filter(Boolean);
      shown = list.length;
      summary.textContent = t('epics.storiesTotal', { n: (data.stories || []).length });
      if (list.length) sections.push(html`<section class="tsec" aria-labelledby="ts-stories"><h2 class="tsec-head" id="ts-stories">${t('epics.sectionStories')}<span class="d-count">${list.length}</span></h2><ul role="tree" class="tree" aria-labelledby="ts-stories">${list.map((f) => b.storyRow(f, 1, true))}</ul></section>`);
    } else {
      const epics = (data.epics || []).map((n) => filterEpic(n, q)).filter(Boolean);
      const orphans = (data.orphanStories || []).map((s) => filterStory(s, q)).filter(Boolean);
      shown = epics.length + orphans.length;
      summary.textContent = t('epics.epicsTotal', { n: (data.epics || []).length });
      if (epics.length) sections.push(html`<section class="tsec" aria-labelledby="ts-epics"><h2 class="tsec-head" id="ts-epics">${t('epics.sectionEpics')}<span class="d-count">${epics.length}</span></h2><ul role="tree" class="tree" aria-labelledby="ts-epics">${epics.map(b.epicRow)}</ul></section>`);
      if (orphans.length) sections.push(html`<section class="tsec" aria-labelledby="ts-orphans"><h2 class="tsec-head" id="ts-orphans">${t('epics.sectionOrphans')}<span class="d-count">${orphans.length}</span></h2><ul role="tree" class="tree" aria-labelledby="ts-orphans">${orphans.map((f) => b.storyRow(f, 1, false))}</ul></section>`);
    }
    S.ep.opening = null;
    if (!shown) {
      if (!q) summary.textContent = '';
      const empty = q
        ? html`<div class="filter-empty" role="status">${icon('search')}<span>${t('epics.noMatches')}</span><button type="button" class="link-btn" data-action="ep-clear">${t('board.clearFilters')}</button></div>`
        : stateBlock({ iconName: 'layers', fill: true, title: S.ep.mode === 'stories' ? t('epics.emptyStoriesTitle') : t('epics.emptyTitle'),
          text: S.ep.mine ? t('epics.emptyMineText') : effProject() ? t('epics.emptyProjectText', { key: effProject() }) : t('epics.emptyText'),
          action: S.ep.mine || effProject() ? null : { id: 'go-projects', label: t('board.goProjects') } });
      setHTML(el, html`${slot}${empty}`);
      placeDash();
      return;
    }
    const trunc = data.truncated ? html`<div class="notice" role="note">${icon('info')}<span>${t('epics.truncated')}</span></div>` : '';
    setHTML(el, html`${slot}${trunc}${sections}<p class="tree-hint" aria-hidden="true">${t('epics.keyboardHint')}</p>`);
    placeDash();
    scroller.scrollTop = scrollTop;
    // Roving tabindex: one tab stop per tree, on the remembered node when present.
    $$('.tree', el).forEach((tree) => {
      const items = $$('[role="treeitem"]', tree);
      const target = items.find((li) => li.dataset.node === focusNode) || items[0];
      if (target) target.tabIndex = 0;
    });
    if (hadFocus && focusNode) {
      const li = el.querySelector(`[role="treeitem"][data-node="${CSS.escape(focusNode)}"]`);
      if (li) li.focus({ preventScroll: true });
    }
  }

  function saveExpanded() {
    if (S.ep.expanded.size > 300) S.ep.expanded = new Set([...S.ep.expanded].slice(-300));
    const list = [...S.ep.expanded];
    prefs.set('epics.expanded', list.slice(-300));
  }

  function toggleNode(li) {
    if (!li || !li.hasAttribute('aria-expanded')) return false;
    const id = li.dataset.node;
    const open = li.getAttribute('aria-expanded') !== 'true';
    if (String(S.q.epics || '').trim()) S.ep.overrides.set(id, open);
    else { if (open) S.ep.expanded.add(id); else S.ep.expanded.delete(id); saveExpanded(); }
    S.ep.focusNode = id;
    const wrap = li.querySelector(':scope > .tkids-wrap');
    if (!open && wrap && !reducedMotion()) {
      li.setAttribute('aria-expanded', 'false');
      wrap.classList.add('is-closing');
      setTimeout(renderEpics, 170);
      return true;
    }
    S.ep.opening = open ? id : null;
    renderEpics();
    return true;
  }

  function openTreeIssue(li) {
    const key = li.dataset.key;
    let seed = null;
    forEachRef(key, (r) => { if (!seed) seed = r; });
    openIssue(key, seed);
  }

  function focusTreeItem(li) {
    if (!li) return;
    const tree = li.closest('[role="tree"]');
    $$('[role="treeitem"][tabindex="0"]', tree).forEach((x) => { x.tabIndex = -1; });
    li.tabIndex = 0;
    S.ep.focusNode = li.dataset.node;
    li.focus();
  }

  function onTreeKey(e) {
    if (onDashKey(e)) return;
    const li = e.target.closest && e.target.closest('[role="treeitem"]');
    if (!li || e.target !== li || e.ctrlKey || e.metaKey || e.altKey) return;
    const tree = li.closest('[role="tree"]');
    const items = $$('[role="treeitem"]', tree).filter((x) => !x.closest('.is-closing'));
    const i = items.indexOf(li);
    const expanded = li.getAttribute('aria-expanded');
    let handled = true;
    switch (e.key) {
      case 'ArrowDown': focusTreeItem(items[i + 1]); break;
      case 'ArrowUp': focusTreeItem(items[i - 1]); break;
      case 'Home': focusTreeItem(items[0]); break;
      case 'End': focusTreeItem(items[items.length - 1]); break;
      case 'ArrowRight':
        if (expanded === 'false') toggleNode(li);
        else if (expanded === 'true') focusTreeItem($('[role="treeitem"]', li));
        break;
      case 'ArrowLeft':
        if (expanded === 'true') toggleNode(li);
        else focusTreeItem(li.parentElement.closest('[role="treeitem"]'));
        break;
      case 'Enter':
        if (e.shiftKey || expanded == null) openTreeIssue(li); else toggleNode(li);
        break;
      case ' ':
        if (expanded == null) openTreeIssue(li); else toggleNode(li);
        break;
      default:
        if (e.code === 'KeyO' && !e.shiftKey) openTreeIssue(li);
        else if (e.code === 'KeyC' && !e.shiftKey && li.classList.contains('tnode-epic')) createInEpic(li.dataset.key); // "+ issue" in this epic
        else handled = false;
    }
    if (handled) e.preventDefault();
  }

  function createInEpic(key) {
    let ref = null;
    forEachRef(key, (r) => { if (!ref) ref = r; });
    openCreate({ project: String(key).split('-')[0], epic: { key, summary: (ref && (ref.summary || ref.name)) || '' }, returnSel: `[role="treeitem"][data-key="${CSS.escape(key)}"]` });
  }
  function onTreeClick(e) {
    if (onDashClick(e)) return;
    const add = e.target.closest('[data-ep-create]');
    if (add) { createInEpic(add.dataset.epCreate); return; }
    const a = e.target.closest('[data-action]');
    if (a) {
      if (a.dataset.action === 'ep-retry') loadEpics({ force: true });
      if (a.dataset.action === 'go-projects') showView('projects');
      if (a.dataset.action === 'ep-clear') { S.q.epics = ''; $('#search').value = ''; S.ep.overrides.clear(); renderEpics(); }
      return;
    }
    const li = e.target.closest('[role="treeitem"]');
    if (!li) return;
    // The li of a parent row also contains its children: act on the row that was clicked.
    const row = e.target.closest('.trow');
    if (!row || row.parentElement !== li) return;
    focusTreeItem(li);
    if (e.target.closest('[data-act="open"]') || !li.hasAttribute('aria-expanded')) openTreeIssue(li);
    else toggleNode(li);
  }

  function onSegKey(e) {
    const btn = e.target.closest('[data-mode]');
    if (!btn) return;
    const modes = ['epics', 'stories'];
    if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End'].includes(e.key)) return;
    e.preventDefault();
    const i = modes.indexOf(btn.dataset.mode);
    const next = e.key === 'Home' ? 0 : e.key === 'End' ? 1 : (i + (e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 1) + 2) % 2;
    setEpicsOption({ mode: modes[next] });
    $(`#ep-mode [data-mode="${modes[next]}"]`).focus();
  }

  /* ================================================================
   * PROJECTS: tracked projects filter the board, sprints, epics and alerts (not the worklog).
   * ================================================================ */
  async function loadProjects(force) {
    const seq = ++S.pj.seq;
    S.pj.loading = true;
    renderProjects(); renderRefreshState();
    try {
      const list = await api('getProjects', force ? { force: true } : {});
      if (seq !== S.pj.seq) return;
      S.pj.list = Array.isArray(list) ? list.slice() : [];
      S.pj.error = null;
      syncTrackedSetting();
      onProjectsChanged();
    } catch (e) {
      if (seq !== S.pj.seq) return;
      S.pj.error = e;
      if (S.pj.list) toastError(t('projects.loadFailed'), e);
    } finally {
      if (seq === S.pj.seq) { S.pj.loading = false; renderProjects(); renderRefreshState(); }
    }
  }

  /** Applies a fresh [Project] without re-sorting rows under the pointer (order changes on the next visit).
      With `onlyKey` (CR-10) only that row's `tracked` is taken from the answer: answers to concurrent toggles
      may arrive out of order, so other rows keep their own (possibly optimistic) tracked state. */
  function mergeProjects(list, onlyKey) {
    if (!Array.isArray(list)) return;
    const byKey = new Map(list.map((p) => [p.key, p]));
    const cur = S.pj.list || [];
    const merged = cur.filter((p) => byKey.has(p.key) || (onlyKey && p.key !== onlyKey)).map((p) => {
      const next = byKey.get(p.key);
      if (!next) return p;
      return onlyKey && p.key !== onlyKey ? Object.assign({}, p, next, { tracked: p.tracked }) : Object.assign({}, p, next);
    });
    list.forEach((p) => { if (!cur.some((c) => c.key === p.key)) merged.push(p); });
    S.pj.list = merged;
    syncTrackedSetting();
    onProjectsChanged();
  }

  /** Keeps the cached Settings in step so a later saveSettings does not send an old trackedProjects list. */
  function syncTrackedSetting() {
    if (S.settings && S.pj.list) S.settings.trackedProjects = S.pj.list.filter((p) => p.tracked).map((p) => p.key);
  }

  function afterTrackingChange() {
    S.boardCache = {};
    S.boardStale = true;
    invalidateEpics();
    S.ep.stale = true;
    invalidateSprints();
    S.sp.stale = true;
  }

  async function setTracked(key, tracked) {
    const p = (S.pj.list || []).find((x) => x.key === key);
    if (!p || S.pj.busy.has(key) || S.pj.bulk) return;
    const prev = !!p.tracked;
    p.tracked = tracked;
    S.pj.restoreFocus = key;
    S.pj.busy.add(key);
    renderProjects();
    try {
      mergeProjects(await api('setProjectTracked', { key, tracked }), key);
      afterTrackingChange();
    } catch (e) {
      const row = (S.pj.list || []).find((x) => x.key === key);
      if (row) row.tracked = prev;
      trackingError(key, e);
    } finally {
      S.pj.busy.delete(key);
      renderProjects();
    }
  }

  /** setProjectTracked errors; `validation` (e.g. too many tracked projects) carries a ready, localized message. */
  function trackingError(key, e) {
    if (e && e.code === 'validation' && e.message) toast(e.message, { tone: 'error' });
    else toastError(t('projects.toggleFailed', { key }), e);
  }
  /** CR-13: a tracked key Jira no longer lists comes back with `available: false`. */
  const projectUnavailable = (p) => p.available === false;

  async function trackOnlyMine() {
    const list = S.pj.list || [];
    if (S.pj.bulk || !list.length) return;
    const withMine = list.filter((p) => (Number(p.myOpen) || 0) > 0);
    if (!withMine.length) { toast(t('projects.bulkNone'), { tone: 'warn' }); return; }
    // Track first, untrack after, so there is never a moment with nothing tracked.
    const changes = list.filter((p) => (Number(p.myOpen) || 0) > 0 !== !!p.tracked)
      .sort((a, b) => Number(!!a.tracked) - Number(!!b.tracked));
    if (!changes.length) { toast(t('projects.bulkNothing'), { tone: 'success' }); return; }
    const keys = (arr) => {
      const shown = arr.slice(0, 12).map((p) => p.key).join(', ');
      return arr.length > 12 ? shown + ' ' + t('projects.andMore', { n: arr.length - 12 }) : shown;
    };
    const on = changes.filter((p) => !p.tracked);
    const off = changes.filter((p) => p.tracked);
    const ok = await confirmDialog({
      title: t('projects.bulkTitle'),
      body: [t('projects.bulkBody', { n: withMine.length }),
        on.length ? t('projects.bulkAdd', { n: on.length, list: keys(on) }) : '',
        off.length ? t('projects.bulkOff', { n: off.length, list: keys(off) }) : ''].filter(Boolean).join(' '),
      okLabel: t('projects.bulkOk'),
    });
    if (!ok) return;
    S.pj.bulk = true;
    renderProjects();
    let failed = 0;
    let limitError = null;
    for (const p of changes) {
      try { mergeProjects(await api('setProjectTracked', { key: p.key, tracked: !p.tracked })); } catch (e) {
        failed++;
        if (e && e.code === 'validation' && e.message) limitError = e;
      }
      renderProjects();
    }
    S.pj.bulk = false;
    afterTrackingChange();
    renderProjects();
    if (limitError) toast(limitError.message, { tone: 'error' });
    if (failed) toast(t('projects.bulkPartial', { n: failed }), { tone: 'error' });
    else toast(t('projects.bulkDone', { n: withMine.length }), { tone: 'success' });
  }

  function renderProjects() {
    if (S.view !== 'projects' || activeSectionId() !== 'view-projects') return;
    const el = $('#pj-list');
    const { list, loading, error } = S.pj;
    const bulk = $('#pj-bulk');
    setBusy(bulk, S.pj.bulk);
    bulk.disabled = S.pj.bulk || !list || !list.length;
    if (!list) {
      $('#pj-summary').textContent = '';
      if (error && !loading) setHTML(el, stateBlock({ iconName: 'alert', tone: 'error', title: t('projects.loadFailed'), text: errorText(error), action: { id: 'pj-retry', label: t('common.retry') } }));
      else setHTML(el, html`<div class="pj-list" role="status" aria-label="${t('common.loading')}">${[0, 1, 2, 3, 4].map(() => html`<div class="skeleton" style="height:60px;border-radius:10px"></div>`)}</div>`);
      return;
    }
    const tracked = list.filter((p) => p.tracked).length;
    $('#pj-summary').textContent = t('projects.summary', { n: tracked, total: list.length });
    if (!list.length) { setHTML(el, stateBlock({ iconName: 'folder', title: t('projects.emptyTitle'), text: t('projects.emptyText') })); return; }
    const q = String(S.q.projects || '').trim().toLowerCase();
    const shown = list.filter((p) => !q || [p.key, p.name, p.category, p.lead].some((x) => String(x || '').toLowerCase().includes(q)));
    const focusKey = (document.activeElement && el.contains(document.activeElement) ? document.activeElement.dataset.project : null) || S.pj.restoreFocus;
    const warn = tracked === 0 ? html`<div class="notice notice-warn" role="note">${icon('alert')}<span>${t('projects.noneTracked')}</span></div>` : '';
    if (!shown.length) {
      setHTML(el, html`${warn}<div class="filter-empty" role="status">${icon('search')}<span>${t('projects.noMatches')}</span><button type="button" class="link-btn" data-action="pj-clear">${t('board.clearFilters')}</button></div>`);
      return;
    }
    setHTML(el, html`${warn}<div class="pj-head" aria-hidden="true"><span>${t('projects.colProject')}</span><span>${t('projects.colMine')}</span><span>${t('projects.colTracked')}</span></div>
      <ul class="pj-list" role="list">${shown.map((p) => {
        const busy = S.pj.busy.has(p.key) || S.pj.bulk;
        const mine = Number(p.myOpen) || 0;
        const gone = projectUnavailable(p);
        const meta = gone ? t('projects.unavailable')
          : [p.key, nameOf(p.category), p.lead ? t('projects.lead', { name: nameOf(p.lead) }) : ''].filter(Boolean).join(' · ');
        return html`<li class="pj-row${p.tracked ? ' is-tracked' : ''}${gone ? ' is-unavailable' : ''}">
          <span class="pj-avatar" data-tone="${avatarTone(p.key)}" aria-hidden="true">${String(p.key).slice(0, 3)}</span>
          <span class="pj-main"><span class="pj-name">${p.name || p.key}</span><span class="pj-meta">${gone ? icon('alert', 'icon-xs') : ''}${meta}</span></span>
          <span class="pj-mine${mine ? '' : ' is-zero'}" title="${t('projects.myOpen', { n: mine })}"><span aria-hidden="true">${mine}</span><span class="sr-only">${t('projects.myOpen', { n: mine })}</span></span>
          <span class="pj-track">${S.pj.busy.has(p.key) ? html`<span class="spinner spinner-sm" aria-hidden="true"></span>` : ''}<input type="checkbox" role="switch" class="switch" data-project="${p.key}" aria-label="${t('projects.trackAria', { name: p.name || p.key })}" ${p.tracked ? html`checked` : ''} ${busy ? html`disabled` : ''}></span>
        </li>`;
      })}</ul>`);
    if (focusKey) {
      const sw = el.querySelector(`[data-project="${CSS.escape(focusKey)}"]`);
      // A switch is disabled while its request runs; focus comes back to it when it is enabled again.
      if (sw && !sw.disabled) { sw.focus({ preventScroll: true }); S.pj.restoreFocus = null; } else if (sw) S.pj.restoreFocus = focusKey;
    }
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
  /** Load factor 0..1 from settings (missing / invalid = 1, as in v0.1). */
  const loadFactorOf = (s) => { const v = Number(s && s.loadFactor); return s && s.loadFactor != null && Number.isFinite(v) && v >= 0 && v <= 1 ? v : 1; };
  /** Effective daily cap: WorklogRun.capSeconds when the backend sent it, else dailyCapHours × loadFactor. */
  function capSeconds(run) {
    const c = run && Number(run.capSeconds);
    if (run && run.capSeconds != null && Number.isFinite(c) && c >= 0) return c;
    return Math.round(((S.settings && Number(S.settings.dailyCapHours)) || 8) * loadFactorOf(S.settings) * 3600);
  }
  const capKnown = (run) => !!S.settings || !!(run && run.capSeconds != null);

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
        <div><dt>${t('worklog.dailyCap')}</dt><dd>${capKnown(p) ? dur(capSeconds(p)) : '—'}${s && loadFactorOf(s) < 1 ? html`<span class="sub">${t('worklog.capFactor', { cap: dur(Math.round((Number(s.dailyCapHours) || 0) * 3600)), pct: Math.round(loadFactorOf(s) * 100) })}</span>` : ''}</dd></div>
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
    const cap = capSeconds(run);
    const other = otherLogged(run);
    const done = isResult ? items.filter((i) => i.result === 'logged').reduce((s, i) => s + (Number(i.todoSeconds) || 0), 0) : 0;
    const logged = sumOf(items, 'loggedSeconds') + done;
    const todo = isResult ? 0 : sumOf(items, 'todoSeconds');
    const total = other + logged + todo;
    const free = Math.max(0, cap - total);
    const pct = (v) => (cap > 0 ? Math.max(0, Math.min(100, (v / cap) * 100)) : v > 0 ? 100 : 0).toFixed(2) + '%';
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
        ${avatar(n.author, 'avatar-sm')}
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
  const FORM_BOOLS = ['autostart', 'worklogEnabled', 'notifyComments', 'autoCheckUpdates'];
  const FORM_NUMS = ['dailyCapHours', 'loadFactor', 'hoursPerDay', 'commentPollMinutes'];
  const form = () => $('#settings-form');

  async function enterSettings(forceReload) {
    loadUpdateStatus(false); // no network; the Updates block re-renders when it arrives
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
    syncLoadFactor('number');
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
    delete c.trackedProjects; // changed from the Projects page, not by this form
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

  /** Slider ⇄ number field for loadFactor, plus the live "max per day" hint (cap × factor). */
  function syncLoadFactor(from) {
    const num = $('#s-loadFactor');
    const range = $('#s-loadFactor-range');
    if (from === 'range') num.value = String(Number(range.value));
    else {
      const v = Number(num.value);
      if (num.value.trim() !== '' && Number.isFinite(v)) range.value = String(Math.min(1, Math.max(0, v)));
    }
    const lf = Number(num.value);
    const cap = Number($('#s-dailyCapHours').value);
    const valid = num.value.trim() !== '' && Number.isFinite(lf) && lf >= 0 && lf <= 1;
    range.setAttribute('aria-valuetext', valid ? Math.round(lf * 100) + ' %' : num.value);
    $('#lf-max').textContent = valid && Number.isFinite(cap) && cap > 0
      ? t('settings.loadFactorMax', { v: dur(Math.round(cap * lf * 3600)) })
      : '';
  }

  function renderSettingsDynamic() {
    if (!S.form) return;
    syncLoadFactor('number');
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
    renderUpdateSettings();
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
      first.scrollIntoView({ block: 'center', behavior: reducedMotion() ? 'auto' : 'smooth' });
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
    if (out.loadFactor == null) out.loadFactor = 1; // v0.1 backends do not send it
    if (typeof out.autoCheckUpdates !== 'boolean') out.autoCheckUpdates = true; // SPEC default; older backends do not send it
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
   * UPDATES (SPEC v0.4 §5): prompt dialog with release notes, install progress, Settings → Updates,
   * "Updated to X.Y.Z" toast. Release notes are untrusted plain text: textContent only (no markup, no wiki).
   * ================================================================ */
  const UPD_PHASES = ['download', 'verify', 'install', 'restart'];
  const fmtMB = (bytes) => t('update.mb', { n: nf({ minimumFractionDigits: 1, maximumFractionDigits: 1 }).format((Number(bytes) || 0) / 1048576) });
  const updRelease = () => (S.upd.dlg && S.upd.dlg.release) || (S.upd.status && S.upd.status.latest) || null;

  /** getUpdateStatus (no network). startup: "Updated to" toast, prompt or a running install. */
  async function loadUpdateStatus(startup) {
    let st;
    try { st = await api('getUpdateStatus'); } catch { return; } // older backend: no updater UI
    applyUpdateStatus(st);
    if (!startup || !st) return;
    if (st.justUpdated && st.justUpdated.to) showUpdated(st.justUpdated.to);
    if (st.installing) { S.upd.job = true; openUpdateDialog(st.latest, 'progress'); }
    else if (st.prompt && st.latest) openUpdateDialog(st.latest);
  }
  function applyUpdateStatus(st) {
    if (!st || typeof st !== 'object') return;
    S.upd.status = st;
    renderUpdateSettings();
  }
  function showUpdated(version) {
    if (S.upd.toasted) return; // once per session (status at startup and the `updated` event say the same)
    S.upd.toasted = true;
    toast(t('update.updatedTo', { version }), { tone: 'success' });
  }

  /* --- dialog --- */
  /** trigger: data-upd-act of the Settings button that opened it (focus returns there). Without one, a dialog that
      opens while a manual check runs or while focus is in Settings → Updates (Go emits updateAvailable inside
      checkForUpdates, before it returns) takes that button; otherwise it is unasked (startup / background check). */
  function openUpdateDialog(release, mode = 'prompt', trigger = null) {
    const d = S.upd.dlg;
    if (!d && !trigger) {
      const a = document.activeElement;
      trigger = S.upd.checking ? 'check' : $('#upd-settings').contains(a) && a.dataset.updAct ? a.dataset.updAct : null;
    }
    if (d) {
      if (release && d.mode !== 'progress') d.release = release;
      if (mode === 'progress' && d.mode !== 'progress') { d.mode = 'progress'; d.progress = { phase: 'download' }; }
      renderUpdateDialog();
      return;
    }
    const rel = release || (S.upd.status && S.upd.status.latest);
    if (!rel) return;
    // Only "Update to X" starts on "Update now"; a prompt found by a manual check starts on the title like an unasked one.
    S.upd.dlg = { mode, release: rel, progress: mode === 'progress' ? { phase: 'download' } : null, error: null, said: '', userOpened: trigger === 'install' };
    renderUpdateDialog();
    layers.open($('#dlg-update'), {
      // DR-7: an unasked prompt starts on the title, so a stray Enter / Space can't install and restart.
      focus: mode === 'prompt' && trigger === 'install' ? '[data-upd="install"]' : '#upd-title',
      // DR-8: the scrim does nothing here. Esc: "Later" in the prompt (the hint says so), close after a failure,
      // nothing while installing.
      onDismiss: (source) => {
        const m = S.upd.dlg && S.upd.dlg.mode;
        if (source === 'scrim') return;
        if (m === 'prompt') skipUpdate(); else if (m === 'failed') closeUpdateDialog();
      },
      onClose: () => { S.upd.dlg = null; $('#upd-live').textContent = ''; },
      // CR-7: back to the Settings button that opened it (the box is re-rendered meanwhile, so look it up again).
      restoreFocus: () => {
        if (!trigger) return null;
        const el = $(`#upd-settings [data-upd-act="${trigger}"]`) || $('#upd-settings [data-upd-act="check"]');
        return el && el.isConnected ? el : null;
      },
    });
  }
  function closeUpdateDialog() { if (S.upd.dlg) layers.close($('#dlg-update')); }

  /** DR-12: release notes (Markdown from GitHub) shown as text only: "## x" lines become bold paragraphs, leading
      "- " / "* " become "• ". Built with createElement / textContent; no markup is interpreted. */
  function fillReleaseNotes(box, notes) {
    box.textContent = '';
    const text = String(notes || '').replace(/\r\n?/g, '\n').trim();
    box.classList.toggle('is-empty', !text);
    if (!text) { box.textContent = t('update.noNotes'); return; }
    let run = [];
    const flush = () => {
      const body = run.join('\n').replace(/^\n+|\s+$/g, '');
      run = [];
      if (!body) return;
      const p = document.createElement('p');
      p.className = 'upd-notes-text';
      p.textContent = body;
      box.append(p);
    };
    text.split('\n').forEach((line) => {
      const h = /^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$/.exec(line);
      if (!h) { run.push(line.replace(/^(\s*)[-*]\s+/, '$1• ')); return; }
      flush();
      const p = document.createElement('p');
      p.className = 'upd-notes-h';
      p.textContent = h[1];
      box.append(p);
    });
    flush();
  }

  function updProgressHTML(d) {
    const r = d.release || {};
    const p = d.progress || { phase: 'download' };
    const i = Math.max(0, UPD_PHASES.indexOf(p.phase));
    return html`<h2 class="modal-title" id="upd-title" tabindex="-1">${t('update.installing', { version: r.version })}</h2>
      <p class="upd-meta" id="upd-sub"></p>
      <div class="upd-progress" role="progressbar" aria-labelledby="upd-sub" aria-valuemin="0" aria-valuemax="100"><span class="upd-progress-fill"></span></div>
      <p class="upd-detail" id="upd-detail"></p>
      <ol class="upd-steps" aria-hidden="true">${UPD_PHASES.map((ph, k) => html`<li data-step="${ph}" data-state="${k < i ? 'done' : k === i ? 'current' : 'todo'}"><span class="upd-step-mark">${icon('check')}</span>${t('update.step.' + ph)}</li>`)}</ol>`;
  }
  /** Progress is updated in place (up to ~100 events): the bar keeps its transition and focus stays put. */
  function syncUpdateProgress(body, d) {
    const r = d.release || {};
    const p = d.progress || { phase: 'download' };
    const total = Number(p.total) || Number(r.size) || 0;
    const pct = p.phase === 'download' && total > 0 && p.received != null ? Math.min(100, (Number(p.received) / total) * 100) : null;
    const bar = $('.upd-progress', body);
    const detail = pct != null ? t('update.received', { received: fmtMB(p.received), total: fmtMB(total) })
      : p.phase === 'install' ? t('update.installWait') : p.phase === 'restart' ? t('update.restartHint') : '';
    $('#upd-sub', body).textContent = t('update.phase.' + (UPD_PHASES.includes(p.phase) ? p.phase : 'download'));
    $('#upd-detail', body).textContent = detail;
    bar.classList.toggle('is-indeterminate', pct == null && p.phase !== 'restart');
    bar.style.setProperty('--w', (pct == null ? (p.phase === 'restart' ? 100 : 0) : pct).toFixed(1) + '%');
    if (pct != null) { bar.setAttribute('aria-valuenow', String(Math.round(pct))); bar.setAttribute('aria-valuetext', detail); } else { bar.removeAttribute('aria-valuenow'); bar.removeAttribute('aria-valuetext'); }
    const i = Math.max(0, UPD_PHASES.indexOf(p.phase));
    $$('[data-step]', body).forEach((li, k) => { li.dataset.state = k < i ? 'done' : k === i ? 'current' : 'todo'; });
    // Live region: phase changes and download quarters only, not every event.
    const say = pct != null && pct > 0 ? t('update.liveDownload', { pct: nf({ style: 'percent', maximumFractionDigits: 0 }).format(Math.floor(pct / 25) / 4) }) : $('#upd-sub', body).textContent;
    if (say !== d.said) { d.said = say; $('#upd-live').textContent = say; }
  }

  function renderUpdateDialog() {
    const d = S.upd.dlg;
    if (!d) return;
    const body = $('#upd-body');
    if (d.mode === 'progress' && body.dataset.mode === 'progress') { syncUpdateProgress(body, d); return; }
    const r = d.release || {};
    const st = S.upd.status || {};
    const active = document.activeElement;
    const hadFocus = $('#dlg-update').contains(active);
    const focusKey = hadFocus && active.dataset ? active.dataset.upd : null;
    let content;
    if (d.mode === 'progress') content = updProgressHTML(d);
    else if (d.mode === 'failed') {
      const e = d.error || {};
      const noWrite = e.code === 'no_write_access' && r.url;
      const badSig = e.code === 'bad_signature'; // DR-9: retrying the same release won't help; Close is the main action
      const btn = (act, label, primary) => html`<button type="button" class="btn${primary ? ' btn-primary' : ''}" data-upd="${act}">${label}</button>`;
      content = html`<h2 class="modal-title" id="upd-title" tabindex="-1">${t('update.failedTitle')}</h2>
        <p class="upd-meta" id="upd-sub">${t('update.failedSub', { version: st.currentVersion || '' })}</p>
        <div class="inline-error upd-error" role="alert">${icon('alert')}<span data-text="error"></span></div>
        ${noWrite ? html`<p class="upd-hint">${t('update.noWriteHint')}</p>` : ''}
        <div class="modal-actions">
          ${noWrite ? html`${btn('close', t('common.close'))}${btn('retry', t('common.retry'))}<button type="button" class="btn btn-primary" data-upd="manual">${icon('external')}<span>${t('update.manual')}</span></button>`
            : badSig ? html`${btn('retry', t('common.retry'))}${btn('close', t('common.close'), true)}`
              : html`${btn('close', t('common.close'))}${btn('retry', t('common.retry'), true)}`}
        </div>`;
    } else {
      const from = st.currentVersion && st.currentVersion !== r.version ? st.currentVersion : '';
      const meta = [from ? `${from} → ${r.version}` : r.version, r.publishedAt ? i18n.date(r.publishedAt) : '', Number(r.size) > 0 ? fmtMB(r.size) : ''].filter(Boolean).join(' · ');
      content = html`<h2 class="modal-title" id="upd-title" tabindex="-1">${t('update.title', { version: r.version })}</h2>
        <p class="upd-meta" id="upd-sub">${meta}</p>
        <div class="upd-notes-head"><span id="upd-notes-label">${t('update.notes')}</span>${r.url ? html`<button type="button" class="link-btn" data-upd="page">${t('update.whatsNew')}${icon('external', 'icon-sm')}</button>` : ''}</div>
        <div class="upd-notes" id="upd-notes" tabindex="0" role="region" aria-labelledby="upd-notes-label"></div>
        <div class="modal-actions">
          <button type="button" class="btn" data-upd="later" aria-describedby="upd-later-hint">${t('update.later')}</button>
          <button type="button" class="btn btn-primary" data-upd="install">${t('update.now')}</button>
        </div>
        <p class="upd-later-hint" id="upd-later-hint">${t('update.laterHint', { version: r.version })}</p>`;
    }
    setHTML(body, content);
    body.dataset.mode = d.mode;
    if (d.mode === 'progress') syncUpdateProgress(body, d);
    const notes = $('#upd-notes', body);
    if (notes) fillReleaseNotes(notes, r.notes); // untrusted: text only
    const errText = $('[data-text="error"]', body);
    if (errText) errText.textContent = (d.error && d.error.message) || t('update.failedGeneric'); // CR-10: text, not markup
    if (!hadFocus) return;
    const target = (focusKey && $(`[data-upd="${focusKey}"]`, body))
      || (d.mode === 'failed' ? $('.btn-primary[data-upd]', body) : d.mode === 'prompt' && d.userOpened ? $('[data-upd="install"]', body) : $('#upd-title', body));
    if (target) target.focus({ preventScroll: true });
  }

  async function startUpdate() {
    const d = S.upd.dlg;
    if (!d || !d.release || d.mode === 'progress') return;
    const version = d.release.version;
    Object.assign(d, { mode: 'progress', progress: { phase: 'download', received: 0, total: d.release.size }, error: null, said: '' });
    S.upd.job = true;
    renderUpdateDialog();
    $('#upd-title').focus({ preventScroll: true });
    if (S.upd.status) { S.upd.status.installing = true; renderUpdateSettings(); }
    try {
      await api('installUpdate', { version });
    } catch (e) {
      if (e.code === 'update_busy') return; // a job runs already: its events drive the dialog
      S.upd.job = false;
      if (S.upd.status) S.upd.status.installing = false;
      if (S.upd.dlg === d) { Object.assign(d, { mode: 'failed', error: { code: e.code, message: errorText(e) } }); renderUpdateDialog(); }
      loadUpdateStatus(false);
    }
  }

  async function skipUpdate() {
    const d = S.upd.dlg;
    const version = d && d.release && d.release.version;
    closeUpdateDialog();
    if (!version) return;
    try { applyUpdateStatus(await api('skipUpdate', { version })); } catch (e) { toastError(t('update.skipFailed'), e); }
  }

  function onUpdateDialogClick(e) {
    const b = e.target.closest('[data-upd]');
    if (!b) return;
    const r = updRelease();
    switch (b.dataset.upd) {
      case 'install': case 'retry': startUpdate(); break;
      case 'later': skipUpdate(); break;
      case 'close': closeUpdateDialog(); break;
      case 'page': case 'manual': if (r && r.url) openUrl(r.url); break;
    }
  }

  /* --- events --- */
  function onUpdateAvailable(rel) {
    if (rel && rel.version) openUpdateDialog(rel);
    loadUpdateStatus(false);
  }
  function onUpdateProgress(p) {
    if (!p || !p.phase) return;
    // CR-8: late events after a failure don't reopen the (undismissable) progress view; Retry switches to it itself.
    if (S.upd.dlg ? S.upd.dlg.mode === 'failed' : !S.upd.job) return;
    if (!S.upd.dlg) openUpdateDialog(null, 'progress');
    const d = S.upd.dlg;
    if (!d) return;
    d.mode = 'progress';
    d.progress = Object.assign({ total: d.progress && d.progress.total }, p);
    renderUpdateDialog();
    if (S.upd.status && !S.upd.status.installing) { S.upd.status.installing = true; renderUpdateSettings(); }
  }
  function onUpdateFailed(f) {
    S.upd.job = false;
    if (!S.upd.dlg) openUpdateDialog(null, 'failed');
    const d = S.upd.dlg;
    if (d) { Object.assign(d, { mode: 'failed', error: { code: f && f.code, message: f && f.message } }); renderUpdateDialog(); }
    if (S.upd.status) S.upd.status.installing = false;
    loadUpdateStatus(false);
  }

  /* --- Settings → Updates --- */
  function renderUpdateSettings() {
    const box = $('#upd-settings');
    if (!box) return;
    const st = S.upd.status;
    const section = $('#sec-upd').closest('.settings-section');
    section.hidden = !st; // older backend: no updater
    if (!st) return;
    const active = document.activeElement;
    const focusKey = box.contains(active) ? active.dataset.updAct : null;
    const r = st.latest;
    const lc = st.lastCheck;
    const busy = S.upd.checking;
    const installing = !!st.installing;
    let badge = '';
    if (st.supported && st.available && r) badge = html`<span class="chip chip-accent">${t('settings.updAvailable', { version: r.version })}</span>`;
    else if (st.supported && lc && lc.ok) badge = html`<span class="chip chip-good">${icon('check')}${t('settings.updUpToDate')}</span>`;
    const latestLine = r ? html`<p class="hint upd-line">${r.publishedAt ? t('settings.updLatest', { version: r.version, date: i18n.date(r.publishedAt) }) : t('settings.updLatestNoDate', { version: r.version })}${r.url ? html`<button type="button" class="link-btn" data-upd-act="page">${t('update.whatsNew')}</button>` : ''}</p>` : '';
    const checkLine = !st.supported ? html`<p class="hint upd-line">${icon('info', 'icon-sm')}<span>${t('settings.updDev')}</span></p>`
      : !lc ? html`<p class="hint upd-line">${t('settings.updNever')}</p>`
        : lc.ok ? html`<p class="hint upd-line">${relSpan(lc.at, 'settings.updChecked')}</p>`
          : html`<p class="hint upd-line upd-check-error">${icon('alert', 'icon-sm')}<span data-text="check-error"></span></p>`;
    const skipped = st.supported && st.available && r && st.skippedVersion === r.version ? html`<p class="hint upd-line">${t('settings.updSkippedHint')}</p>` : '';
    setHTML(box, html`<div class="setting-text">
        <span class="label upd-current">${t('settings.updCurrent', { version: st.currentVersion || '—' })}${badge}</span>
        ${latestLine}${checkLine}${skipped}
      </div>
      <div class="setting-control upd-actions">
        <button type="button" class="btn" data-upd-act="check"${!st.supported ? html` disabled` : busy || installing ? html` aria-disabled="true"` : ''}${busy ? html` aria-busy="true"` : ''}>${icon('refresh')}<span>${t('settings.updCheck')}</span></button>
        ${st.supported && st.available && r ? html`<button type="button" class="btn btn-primary" data-upd-act="install"${installing ? html` aria-disabled="true"` : ''}>${installing ? t('settings.updInstalling') : t('settings.updInstall', { version: r.version })}</button>` : ''}
      </div>`);
    const err = $('[data-text="check-error"]', box);
    if (err) err.textContent = t('settings.updCheckFailed', { rel: relText(lc.at) || '—', error: lc.error || t('update.failedGeneric') }); // CR-10
    // CR-7: busy / installing buttons are aria-disabled (not disabled), so they keep keyboard focus across re-renders.
    if (focusKey) {
      const f = $(`[data-upd-act="${focusKey}"]`, box);
      if (f) f.focus({ preventScroll: true });
    }
  }

  async function checkUpdatesNow() {
    if (S.upd.checking) return;
    S.upd.checking = true;
    renderUpdateSettings();
    const live = $('#upd-settings-live');
    live.textContent = '';
    try {
      const st = await api('checkForUpdates');
      applyUpdateStatus(st);
      const lc = st && st.lastCheck;
      live.textContent = lc && !lc.ok ? t('settings.updLiveFailed', { error: lc.error || t('update.failedGeneric') })
        : st && st.available && st.latest ? t('settings.updLiveAvailable', { version: st.latest.version }) : t('settings.updLiveUpToDate');
    } catch (e) {
      toastError(t('settings.updCheck'), e);
    } finally {
      S.upd.checking = false;
      renderUpdateSettings();
    }
  }

  function onUpdateSettingsClick(e) {
    const b = e.target.closest('[data-upd-act]');
    if (!b || b.disabled || b.getAttribute('aria-disabled') === 'true') return; // busy: ignored, focus stays
    const r = S.upd.status && S.upd.status.latest;
    switch (b.dataset.updAct) {
      case 'check': checkUpdatesNow(); break;
      case 'page': if (r && r.url) openUrl(r.url); break;
      case 'install': if (r) { openUpdateDialog(r, 'prompt', 'install'); startUpdate(); } break;
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
      case 'updateAvailable': onUpdateAvailable(payload); break;
      case 'updateProgress': onUpdateProgress(payload); break;
      case 'updateFailed': onUpdateFailed(payload); break;
      case 'updated': if (payload && payload.to) showUpdated(payload.to); loadUpdateStatus(false); break;
      case 'state': S.stateError = null; applyState(payload); break;
      case 'board': {
        // Board data, tracked projects or issues changed on the Go side: everything issue-based may be stale.
        const here = (v) => S.view === v && activeSectionId() === 'view-' + v;
        S.boardCache = {};
        if (here('board')) loadBoard(false); else S.boardStale = true;
        invalidateEpics();
        if (here('epics') && S.ep.data) loadEpics({ silent: true }); else S.ep.stale = true;
        invalidateSprints();
        if (here('sprints') && S.sp.data) loadSprints({ silent: true }); else S.sp.stale = true;
        break;
      }
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

    // Search (per view) + "Only mine" + project chips
    const search = $('#search');
    search.addEventListener('input', () => onSearchInput(search.value));
    $('#mine-toggle').addEventListener('change', (e) => setMine(S.view, e.target.checked));
    if (typeof ResizeObserver === 'function') new ResizeObserver(() => syncProjectFilter()).observe($('#project-filter'));
    else window.addEventListener('resize', syncProjectFilter);
    $('#project-menu-btn').addEventListener('click', (e) => openProjectMenu(e.currentTarget));
    $('#project-chips').addEventListener('click', (e) => {
      const b = e.target.closest('[data-project]');
      if (b) setProject(b.dataset.project);
    });
    $('#project-chips').addEventListener('keydown', onChipsKey);
    $('#board-picker').addEventListener('click', (e) => openBoardPicker(e.currentTarget));

    // Kanban (board): open card, status dropdown, drag & drop
    $$('.kanban').forEach((root) => {
      root.addEventListener('click', onKanbanClick);
      root.addEventListener('dragstart', onDragStart);
      root.addEventListener('dragend', onDragEnd);
      root.addEventListener('dragenter', onDragOver); // highlight immediately; dragenter must also be cancelled for a valid target
      root.addEventListener('dragover', onDragOver);
      root.addEventListener('dragleave', onDragLeave);
      root.addEventListener('drop', onDrop);
    });
    document.addEventListener('dragstart', guardDragStart, true);
    ['dragenter', 'dragover', 'drop'].forEach((type) => document.addEventListener(type, guardForeignDrag));

    // Sprints: grouped list
    $('#sp-closed').addEventListener('change', (e) => setSprintsClosed(e.target.checked));
    $('#sp-list').addEventListener('click', onSprintsClick);
    $('#sp-list').addEventListener('keydown', onSprintsKey);

    // Epics & stories
    $('#ep-mode').addEventListener('click', (e) => {
      const b = e.target.closest('[data-mode]');
      if (b && b.dataset.mode !== S.ep.mode) setEpicsOption({ mode: b.dataset.mode });
    });
    $('#ep-mode').addEventListener('keydown', onSegKey);
    $('#ep-done').addEventListener('change', (e) => setEpicsOption({ includeDone: e.target.checked }));
    $('#ep-list').addEventListener('click', onTreeClick);
    $('#ep-list').addEventListener('keydown', onTreeKey);
    window.addEventListener('resize', () => syncTableScroll($('#drawer')));
    window.addEventListener('resize', () => { // the tooltip is placed once: re-place it for the focused row, drop a hover one
      const a = document.activeElement;
      dashHighlight(S.ep.dash.el && S.ep.dash.el.contains(a) ? a.closest('.epd-row') : null);
    });

    // Projects
    $('#pj-bulk').addEventListener('click', trackOnlyMine);
    $('#pj-list').addEventListener('change', (e) => {
      const sw = e.target.closest('[data-project]');
      if (sw) setTracked(sw.dataset.project, sw.checked);
    });
    $('#pj-list').addEventListener('click', (e) => {
      const a = e.target.closest('[data-action]');
      if (!a) return;
      if (a.dataset.action === 'pj-retry') loadProjects(true);
      if (a.dataset.action === 'pj-clear') { S.q.projects = ''; search.value = ''; renderProjects(); }
    });

    // Dropdown menu
    const menuEl = $('#menu');
    menuEl.addEventListener('keydown', menu.onKey);
    menuEl.addEventListener('click', menu.onClick);
    document.addEventListener('pointerdown', menu.onOutside, true);
    window.addEventListener('resize', () => menu.close(false));
    document.addEventListener('scroll', (e) => { if (!menuEl.contains(e.target)) menu.sync(); }, true);

    // Create / edit issue
    $('#create-btn').addEventListener('click', () => openCreate());
    const dlgIssue = $('#dlg-issue');
    dlgIssue.addEventListener('input', onCfInput);
    dlgIssue.addEventListener('change', onCfChange);
    dlgIssue.addEventListener('focusout', onCfFocusOut);
    dlgIssue.addEventListener('keydown', onCfKey);
    dlgIssue.addEventListener('click', onCfClick);
    dlgIssue.addEventListener('mousedown', (e) => { if (e.target.closest('.cb-opt')) e.preventDefault(); }); // keep focus in the picker
    dlgIssue.addEventListener('submit', (e) => e.preventDefault());

    // Drawer / dialogs
    $('#dlg-update').addEventListener('click', onUpdateDialogClick);
    $('#upd-settings').addEventListener('click', onUpdateSettingsClick);
    $('#drawer').addEventListener('click', onDrawerClick);
    $('#drawer').addEventListener('keydown', onDrawerKey);
    $('#scrim').addEventListener('click', () => layers.closeTop('scrim'));

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
      if (el.id === 's-loadFactor-range') { syncLoadFactor('range'); clearFieldError(sf, $('#s-loadFactor')); }
      else if (el.id === 's-loadFactor' || el.id === 's-dailyCapHours') syncLoadFactor('number');
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
      if (e.key === 'Escape' && menu.isOpen()) { e.preventDefault(); menu.close(true); return; }
      if (e.key === 'Escape' && layers.top()) { e.preventDefault(); layers.closeTop(); return; }
      if (layers.top() || menu.isOpen() || e.ctrlKey || e.metaKey || e.altKey || e.repeat) return;
      const target = e.target;
      if (target === search && e.key === 'Escape') {
        if (search.value) { search.value = ''; onSearchInput(''); } else search.blur();
        return;
      }
      if (isTyping(target)) return;
      if (e.key === '/' || (e.code === 'Slash' && !e.shiftKey)) {
        e.preventDefault();
        if (S.appState && !S.appState.configured) return; // onboarding: board is not reachable (DR-9)
        if (!SEARCH_VIEWS[S.view]) showView('board'); // views with a search box keep their own
        if (!$('#view-tools').hidden) { search.focus(); search.select(); }
      } else if (e.code === 'KeyR' && !e.shiftKey) {
        e.preventDefault();
        refreshCurrent();
      } else if (e.code === 'KeyC' && !e.shiftKey && !e.defaultPrevented && canCreate()) {
        e.preventDefault();
        openCreate();
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
      if (S.upd.dlg) { $('#upd-body').dataset.mode = ''; renderUpdateDialog(); }
      if (S.cf && !S.cf.loading) { if (S.cf.editor && S.cf.editor.isChanged()) S.cf.descForced = true; cfCollectRaw(); cfRender(); }
      renderUpdateSettings();
      menu.close(false);
      if (S.form) renderSettingsDynamic();
    });
  }

  /* Native title bar (SPEC v0.4 §2): dark flag + caption colour = the top bar background. Best effort. */
  const darkQuery = window.matchMedia('(prefers-color-scheme: dark)');
  /** "rgb(30, 31, 34)" / "rgb(30 31 34 / 1)" / "color(srgb 0.1 0.2 0.3)" → "#1e1f22"; null if not opaque or unknown. */
  function cssColorHex(value) {
    const s = String(value || '').trim();
    let m = /^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$/i.exec(s);
    let rgb = m && [m[1], m[2], m[3]].map(Number);
    if (!m) {
      m = /^color\(srgb\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)(?:\s*\/\s*([\d.]+%?))?\s*\)$/i.exec(s);
      rgb = m && [m[1], m[2], m[3]].map((v) => Number(v) * 255);
    }
    if (!m) return null;
    const alpha = m[4] == null ? 1 : m[4].endsWith('%') ? parseFloat(m[4]) / 100 : Number(m[4]);
    if (!(alpha >= 0.99)) return null;
    return '#' + rgb.map((v) => Math.max(0, Math.min(255, Math.round(v))).toString(16).padStart(2, '0')).join('');
  }
  function syncWindowTheme() {
    const params = { dark: darkQuery.matches };
    const caption = cssColorHex(getComputedStyle($('.topbar')).backgroundColor);
    if (caption) params.caption = caption;
    api('setWindowTheme', params).catch(() => { /* older backend / not Windows: the title bar keeps its colour */ });
  }

  async function init() {
    i18n.apply();
    bind();
    syncWindowTheme();
    darkQuery.addEventListener('change', syncWindowTheme);
    renderChrome();
    await prefs.load(); // before the first render, so remembered choices apply from the start
    applyPrefs();
    window.addEventListener('pagehide', () => prefs.flushNow());
    showView('board');
    loadState();
    loadUpdateStatus(true); // "Updated to" toast, prompt dialog
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();

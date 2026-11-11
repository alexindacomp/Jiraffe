/* Jiraffe UI: fake backend for development / review in a normal browser.
   Installed only when the Go binding window.__jiraffe is absent. Implements every contract method
   with latency, occasional errors and periodic `notification` events.
   URL flags (?mock=a,b): unconfigured | empty | errors (frequent failures) | noerrors | slow | fast (notification every 10 s)
   | warnings (toggling autostart "fails": warning + autostartEffective = previous value). */
(function () {
  'use strict';
  if (typeof window.__jiraffe === 'function') return;

  const flags = new Set((new URLSearchParams(location.search).get('mock') || '').split(',').map((s) => s.trim()).filter(Boolean));
  const ERROR_RATE = flags.has('noerrors') ? 0 : flags.has('errors') ? 0.5 : 0.08;
  const JIRA = 'https://sd.local';
  const ME = { name: 'o.koval', displayName: 'Олена Коваль' };
  const clone = (v) => (v === undefined ? null : JSON.parse(JSON.stringify(v)));
  const now = () => new Date();
  const p2 = (n) => String(n).padStart(2, '0');
  const rand = (a, b) => a + Math.random() * (b - a);
  const pick = (arr) => arr[Math.floor(Math.random() * arr.length)];

  /** RFC3339 with local offset, like Go's time.Format(time.RFC3339). */
  function rfc(d) {
    const off = -d.getTimezoneOffset();
    const sign = off >= 0 ? '+' : '-';
    const a = Math.abs(off);
    return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())}T${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}${sign}${p2(Math.floor(a / 60))}:${p2(a % 60)}`;
  }
  const isoDay = (d) => `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())}`;
  const minutesAgo = (m) => rfc(new Date(Date.now() - m * 60000));
  const atTime = (day, hhmm) => { const [h, m] = hhmm.split(':').map(Number); const d = new Date(day); d.setHours(h, m, 0, 0); return d; };

  /* ---------- workflow ---------- */
  const ST = {
    backlog:    { id: '10000', name: 'Backlog', category: 'new' },
    todo:       { id: '10001', name: 'ToDo', category: 'new' },
    inprog:     { id: '3', name: 'В работе', category: 'indeterminate' },
    forReview:  { id: '10100', name: 'ForReview', category: 'indeterminate' },
    inReview:   { id: '10101', name: 'InReview', category: 'indeterminate' },
    forTesting: { id: '10102', name: 'ForTesting', category: 'indeterminate' },
    solved:     { id: '10200', name: 'Предоставлено решение', category: 'done' },
    done:       { id: '10201', name: 'Готово', category: 'done' },
    reject:     { id: '10202', name: 'Reject', category: 'done' },
  };
  const ORDER = ['backlog', 'todo', 'inprog', 'forReview', 'inReview', 'forTesting', 'solved', 'done', 'reject'];
  // [transitionId, name, target status key]
  const FLOW = {
    backlog: [['11', 'Запланировать', 'todo'], ['21', 'Взять в работу', 'inprog']],
    todo: [['21', 'Взять в работу', 'inprog'], ['12', 'Вернуть в бэклог', 'backlog'], ['91', 'Отклонить', 'reject']],
    inprog: [['31', 'На ревью', 'forReview'], ['32', 'Срочное ревью', 'forReview'], ['41', 'Приостановить', 'todo'], ['51', 'Предоставить решение', 'solved']],
    forReview: [['61', 'Начать ревью', 'inReview'], ['22', 'Вернуть в работу', 'inprog']],
    inReview: [['71', 'Ревью пройдено', 'forTesting'], ['23', 'На доработку', 'inprog']],
    forTesting: [['81', 'Тестирование пройдено', 'done'], ['24', 'Найдены ошибки', 'inprog']],
    solved: [['25', 'Переоткрыть', 'inprog']],
    done: [['26', 'Переоткрыть', 'inprog']],
    reject: [['27', 'Переоткрыть', 'todo']],
  };
  const statusKey = (st) => Object.keys(ST).find((k) => ST[k].id === st.id);

  /* ---------- data ---------- */
  const SEED = [
    ['RBI-1028', 'Відділити frontend від backend', 'inprog', 'Задача', 'High', 12],
    ['RBI-1031', 'Перенести авторизацію на OAuth2 (Keycloak)', 'todo', 'История', 'Medium', 95],
    ['RBI-1019', 'Помилка 500 при експорті звіту в Excel, якщо в періоді більше 10 000 рядків', 'forReview', 'Ошибка', 'Highest', 38],
    ['RBI-997', 'Оновити залежності Spring Boot до 3.3', 'backlog', 'Задача', 'Low', 60 * 26],
    ['CMA-412', 'Добавить фильтр по дате в журнал операций', 'inprog', 'История', 'Medium', 47],
    ['CMA-415', 'Не обновляется счётчик непрочитанных сообщений после перехода по push', 'inReview', 'Ошибка', 'High', 180],
    ['CMA-399', 'Рефакторинг модуля уведомлений', 'backlog', 'Задача', 'Medium', 60 * 24 * 6],
    ['MB-77', 'Push-сповіщення для iOS', 'forTesting', 'История', 'High', 300],
    ['MB-81', 'Краш при повороті екрана на Android 14', 'todo', 'Ошибка', 'Highest', 22],
    ['EDS-230', 'Интеграция с сервисом электронного документооборота: подписание пакета', 'inprog', 'История', 'Medium', 5],
    ['EDS-233', 'Описать API загрузки вложений в Swagger', 'forReview', 'Задача', 'Low', 60 * 3],
    ['EDS-219', 'Таймаут при генерации PDF больше 50 страниц', 'todo', 'Ошибка', 'Medium', 60 * 50],
  ];
  const issues = flags.has('empty') ? [] : SEED.map(([key, summary, st, type, prio, ago]) => ({
    key, summary, url: `${JIRA}/browse/${key}`, project: key.split('-')[0],
    status: ST[st], type: { name: type }, priority: { name: prio }, updated: minutesAgo(ago),
    assignee: { displayName: ME.displayName },
  }));

  const settings = {
    jiraUrl: flags.has('unconfigured') ? '' : JIRA,
    language: 'ru',
    autostart: true,
    worklogEnabled: true,
    worklogTime: '18:00',
    workdayStart: '09:00', workdayEnd: '18:00',
    lunchStart: '13:00', lunchEnd: '14:00',
    dailyCapHours: 8,
    hoursPerDay: 9,
    workDays: [1, 2, 3, 4, 5],
    vacations: [{ from: '2026-12-24', to: '2027-01-02', note: 'Новогодние' }],
    holidays: ['2026-12-25', '2027-01-01'],
    inProgressStatuses: ['In Progress', 'В работе', 'В роботі'],
    notifyComments: true,
    commentPollMinutes: 5,
    boardJql: 'assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC',
  };
  let token = flags.has('unconfigured') ? '' : 'mock-token';
  const configured = () => !!settings.jiraUrl && !!token;

  const AUTHORS = ['Андрій Мельник', 'Ірина Шевченко', 'Дмитрий Орлов', 'Наталья Белова', 'Максим Гончаренко', 'QA Bot'];
  const BODIES = [
    'Подивись, будь ласка, коментар у MR — там питання щодо міграції.',
    'Проверил на стенде, воспроизводится только в Chrome. Логи приложил.',
    'Можемо випустити це в п\'ятницю? Клієнт питає про терміни.',
    'Ревью пройдено, есть пара замечаний по неймингу, некритично.',
    'Тести впали на кроці підписання, див. скриншот у вкладенні.',
    'Добавил описание API в Confluence, ссылка в задаче.',
    'Не забудь оновити changelog перед релізом.',
  ];
  let notifSeq = 100;
  const fresh = flags.has('empty') || flags.has('unconfigured'); // no history / notifications yet
  const notifications = fresh ? [] : [
    [12, 'Андрій Мельник', 'RBI-1028', false, 'Глянь, будь ласка: API-шлюз тепер віддає CORS-заголовки, можна прибирати проксі з фронта.'],
    [55, 'Наталья Белова', 'CMA-415', false, 'Воспроизвела на iOS 17.4: счётчик сбрасывается только после перезапуска приложения. Видео приложила.'],
    [160, 'QA Bot', 'MB-77', true, 'Автотести: 42 passed, 1 failed (PushPayloadTest.testSilentPush). Деталі в Jenkins #1287.'],
    [60 * 20, 'Дмитрий Орлов', 'EDS-233', true, 'Swagger посмотрел, ок. Только добавь, пожалуйста, пример ответа с ошибкой 413.'],
    [60 * 30, 'Ірина Шевченко', 'RBI-1019', true, 'Підтверджую, після фіксу експорт на 25 000 рядків проходить за 12 секунд.'],
  ].map(([ago, author, key, read, body]) => {
    const is = issues.find((i) => i.key === key) || { summary: '' };
    return { id: String(notifSeq++), issueKey: key, issueSummary: is.summary, author, body, created: minutesAgo(ago), url: `${JIRA}/browse/${key}?focusedCommentId=${notifSeq}`, read };
  });

  /* ---------- worklog ---------- */
  const loggedToday = new Map([['CMA-412', 3600]]); // already logged by me today, seconds
  const SHARES = [['RBI-1028', 2 * 3600 + 56 * 60, '09:00'], ['CMA-412', 3 * 3600 + 4 * 60, '09:00'], ['EDS-230', 3600, '14:00']];
  // Logged today on issues that are not rows of the run (e.g. a meeting ticket). The cap covers it too:
  // 1h other + 1h already on CMA-412 + 6h to log = 8h. Omitted under ?mock=empty (and on seeded "old" history runs).
  const OTHER_LOGGED = flags.has('empty') ? undefined : 3600;

  function dayReason(d) {
    if (!settings.worklogEnabled) return 'disabled';
    if (!configured()) return 'not-configured';
    const iso = isoDay(d);
    if (settings.holidays.includes(iso)) return 'holiday';
    if (settings.vacations.some((v) => v.from <= iso && iso <= v.to)) return 'vacation';
    const wd = d.getDay() === 0 ? 7 : d.getDay();
    if (!settings.workDays.includes(wd)) return 'weekend';
    return null;
  }

  function buildRun(trigger) {
    const d = now();
    const run = { at: rfc(d), date: isoDay(d), trigger, items: [], totalSeconds: 0, total: '' };
    const reason = dayReason(d);
    if (reason) { run.skippedReason = reason; return run; }
    if (OTHER_LOGGED !== undefined) run.otherLoggedSeconds = OTHER_LOGGED;
    const capLeft = Math.max(0, settings.dailyCapHours * 3600 - (OTHER_LOGGED || 0) - [...loggedToday.values()].reduce((a, b) => a + b, 0));
    const items = SHARES.map(([key, share, start]) => {
      const is = issues.find((i) => i.key === key) || {};
      const logged = loggedToday.get(key) || 0;
      return { key, summary: is.summary || key, status: (is.status || ST.inprog).name, shareSeconds: share, loggedSeconds: logged, todoSeconds: Math.max(0, share - logged), started: rfc(atTime(d, start)) };
    });
    const sum = items.reduce((a, i) => a + i.todoSeconds, 0);
    if (sum > capLeft) items.forEach((i) => { i.todoSeconds = Math.floor((i.todoSeconds * capLeft) / sum / 60) * 60; });
    items.forEach((i) => { i.duration = fmt(i.todoSeconds); });
    run.items = items;
    run.totalSeconds = items.reduce((a, i) => a + i.todoSeconds, 0);
    run.total = fmt(run.totalSeconds);
    return run;
  }
  function fmt(sec) {
    const m = Math.floor(sec / 60);
    const h = Math.floor(m / 60);
    return [h ? h + 'h' : '', m % 60 ? (m % 60) + 'm' : ''].filter(Boolean).join(' ') || '0m';
  }

  const history = [];
  (function seedHistory() {
    if (fresh) return;
    const d = now();
    for (let back = 1; history.length < 9; back++) {
      const day = new Date(d); day.setDate(d.getDate() - back); day.setHours(18, 0, 3, 0);
      const wd = day.getDay();
      const run = { at: rfc(day), date: isoDay(day), trigger: 'schedule', items: [], totalSeconds: 0, total: '' };
      if (wd === 0 || wd === 6) { if (wd === 6) { run.skippedReason = 'weekend'; history.push(run); } continue; }
      const variants = [
        [['RBI-1028', 5 * 3600], ['CMA-412', 3 * 3600]],
        [['RBI-1019', 2 * 3600 + 30 * 60], ['RBI-1028', 4 * 3600], ['MB-77', 3600 + 30 * 60]],
        [['EDS-233', 8 * 3600]],
        [['CMA-415', 4 * 3600], ['EDS-230', 4 * 3600]],
      ];
      const v = variants[back % variants.length];
      run.items = v.map(([key, sec], i) => {
        const is = issues.find((x) => x.key === key) || { summary: key };
        const item = { key, summary: is.summary, status: 'В работе', shareSeconds: sec, loggedSeconds: 0, todoSeconds: sec, duration: fmt(sec), started: rfc(atTime(day, i ? '13:00' : '09:00')), result: 'logged' };
        if (back === 3 && i === 1) { item.result = 'error'; item.error = 'Jira: 400 Bad Request — Issue is in a status that does not allow logging work'; }
        return item;
      });
      run.totalSeconds = run.items.filter((i) => i.result === 'logged').reduce((a, i) => a + i.todoSeconds, 0);
      run.total = fmt(run.totalSeconds);
      history.push(run);
    }
  })();

  function nextWorklogAt() {
    if (!settings.worklogEnabled || !configured()) return undefined;
    const d = now();
    const ranToday = history.some((r) => r.date === isoDay(d) && r.trigger !== 'preview');
    for (let i = 0; i < 30; i++) {
      const day = new Date(d); day.setDate(d.getDate() + i);
      const at = atTime(day, settings.worklogTime);
      if (i === 0 && (ranToday || at <= d)) continue;
      if (!dayReason(day)) return rfc(at);
    }
    return undefined;
  }

  function state() {
    return {
      configured: configured(),
      user: configured() ? clone(ME) : undefined,
      version: '0.1.0-mock',
      language: settings.language,
      unread: notifications.filter((n) => !n.read).length,
      nextWorklogAt: nextWorklogAt(),
      lastWorklog: history[0] ? clone(history[0]) : undefined,
    };
  }

  /* ---------- validation (messages in the configured language, like the Go side would) ---------- */
  const MSG = {
    ru: { required: 'Обязательное поле', url: 'Введите адрес вида https://…', time: 'Время в формате ЧЧ:ММ', order: 'Начало должно быть раньше конца', within: 'Обед должен быть внутри рабочего дня', range: 'Допустимо от {min} до {max}', int: 'Нужно целое число', days: 'Выберите хотя бы один день', date: 'Некорректная дата', dateOrder: 'Дата окончания раньше даты начала', statuses: 'Нужен хотя бы один статус', token: 'Укажите токен доступа' },
    uk: { required: 'Обов’язкове поле', url: 'Введіть адресу виду https://…', time: 'Час у форматі ГГ:ХХ', order: 'Початок має бути раніше за кінець', within: 'Обід має бути в межах робочого дня', range: 'Допустимо від {min} до {max}', int: 'Потрібне ціле число', days: 'Оберіть хоча б один день', date: 'Некоректна дата', dateOrder: 'Дата завершення раніше за дату початку', statuses: 'Потрібен хоча б один статус', token: 'Вкажіть токен доступу' },
    en: { required: 'Required', url: 'Enter a URL like https://…', time: 'Use HH:MM', order: 'Start must be before end', within: 'Lunch must be within the working day', range: 'Allowed range {min}–{max}', int: 'Must be a whole number', days: 'Select at least one day', date: 'Invalid date', dateOrder: 'End date is before start date', statuses: 'At least one status is required', token: 'Enter an access token' },
  };
  function validate(s, newToken) {
    const m = MSG[s.language] || MSG.ru;
    const errs = [];
    const add = (field, key, p) => errs.push({ field, message: m[key].replace(/\{(\w+)\}/g, (_, k) => p[k]) });
    const isTime = (v) => /^([01]\d|2[0-3]):[0-5]\d$/.test(v || '');
    const isDate = (v) => /^\d{4}-\d{2}-\d{2}$/.test(v || '') && !isNaN(new Date(v).getTime());
    if (!s.jiraUrl) add('jiraUrl', 'required', {});
    else if (!/^https?:\/\/[^\s/]+/i.test(s.jiraUrl)) add('jiraUrl', 'url', {});
    if (!newToken && !token) add('token', 'token', {});
    ['worklogTime', 'workdayStart', 'workdayEnd'].forEach((f) => { if (!isTime(s[f])) add(f, 'time', {}); });
    if (isTime(s.workdayStart) && isTime(s.workdayEnd) && s.workdayStart >= s.workdayEnd) add('workdayEnd', 'order', {});
    if (s.lunchStart || s.lunchEnd) {
      if (!isTime(s.lunchStart)) add('lunchStart', 'time', {});
      if (!isTime(s.lunchEnd)) add('lunchEnd', 'time', {});
      if (isTime(s.lunchStart) && isTime(s.lunchEnd)) {
        if (s.lunchStart >= s.lunchEnd) add('lunchEnd', 'order', {});
        else if (s.lunchStart < s.workdayStart || s.lunchEnd > s.workdayEnd) add('lunchStart', 'within', {});
      }
    }
    if (!(s.dailyCapHours > 0 && s.dailyCapHours <= 24)) add('dailyCapHours', 'range', { min: 0.5, max: 24 });
    if (!(s.hoursPerDay >= 1 && s.hoursPerDay <= 24)) add('hoursPerDay', 'range', { min: 1, max: 24 });
    if (!Number.isInteger(s.commentPollMinutes)) add('commentPollMinutes', 'int', {});
    else if (s.commentPollMinutes < 1 || s.commentPollMinutes > 60) add('commentPollMinutes', 'range', { min: 1, max: 60 });
    if (!Array.isArray(s.workDays) || !s.workDays.length) add('workDays', 'days', {});
    (s.vacations || []).forEach((v, i) => {
      if (!isDate(v.from)) add(`vacations[${i}].from`, 'date', {});
      else if (!isDate(v.to)) add(`vacations[${i}].to`, 'date', {});
      else if (v.from > v.to) add(`vacations[${i}].to`, 'dateOrder', {});
    });
    (s.holidays || []).forEach((h) => { if (!isDate(h)) add('holidays', 'date', {}); });
    if (!Array.isArray(s.inProgressStatuses) || !s.inProgressStatuses.length) add('inProgressStatuses', 'statuses', {});
    if (!String(s.boardJql || '').trim()) add('boardJql', 'required', {});
    return errs;
  }

  const WARN_AUTOSTART = {
    ru: 'Настройки сохранены, но автозапуск изменить не удалось: доступ к реестру запрещён.',
    uk: 'Налаштування збережено, але змінити автозапуск не вдалося: доступ до реєстру заборонено.',
    en: 'Settings saved, but autostart could not be changed: registry access denied.',
  };

  /* ---------- methods ---------- */
  class MockError { constructor(code, message, fields) { this.code = code; this.message = message; if (fields) this.fields = fields; } }
  let boardCalls = 0;
  const maybeFail = (rate, code, message) => { if (Math.random() < rate) throw new MockError(code, message); };
  const requireConfigured = () => { if (!configured()) throw new MockError('not_configured', 'Jira is not configured'); };

  function board() {
    const cols = [];
    ORDER.forEach((k) => {
      const st = ST[k];
      if (st.category === 'done') return; // boardJql: statusCategory != Done
      const list = issues.filter((i) => i.status.id === st.id).sort((a, b) => b.updated.localeCompare(a.updated));
      if (list.length) cols.push({ status: st, issues: list });
    });
    return { columns: clone(cols), fetchedAt: rfc(now()) };
  }

  const METHODS = {
    getState: () => state(),
    getSettings: () => Object.assign(clone(settings), { hasToken: !!token }),
    testConnection(p) {
      const url = (p && p.jiraUrl) || '';
      if (!/^https?:\/\/[^\s/]+/i.test(url)) return { ok: false, error: 'invalid URL' };
      const tk = (p && p.token) || token;
      if (!tk) return { ok: false, error: '401 Unauthorized' };
      if (/bad|wrong/i.test(tk)) return { ok: false, error: '401 Unauthorized: token is invalid or expired' };
      if (!/sd\.local/i.test(url)) return { ok: false, error: `WinHTTP 12007: the server name could not be resolved (${url.replace(/^https?:\/\//, '')})` };
      return { ok: true, displayName: ME.displayName };
    },
    saveSettings(p) {
      const next = Object.assign(clone(settings), p || {});
      delete next.token; delete next.hasToken;
      const errs = validate(next, p && p.token);
      if (errs.length) throw new MockError('validation', 'validation failed', errs);
      const prevAutostart = settings.autostart;
      // Demo (?mock=warnings or ?mock=errors): changing autostart "fails" — the registry keeps the old value.
      const autostartFails = next.autostart !== prevAutostart && (flags.has('warnings') || flags.has('errors'));
      if (autostartFails) next.autostart = prevAutostart;
      Object.keys(settings).forEach((k) => { settings[k] = next[k]; });
      if (p && p.token) token = p.token;
      setTimeout(() => emit('state', state()), 50);
      const result = METHODS.getSettings();
      if (autostartFails) {
        result.autostartEffective = prevAutostart; // effective state; the UI prefers it over `autostart`
        result.warnings = [WARN_AUTOSTART[settings.language] || WARN_AUTOSTART.ru]; // already localized, like Go
      }
      return result;
    },
    getBoard() {
      requireConfigured();
      if (boardCalls++ > 0) maybeFail(ERROR_RATE, 'network', 'WinHTTP 12002: the operation timed out');
      return board();
    },
    getTransitions(p) {
      requireConfigured();
      const is = issues.find((i) => i.key === p.key);
      if (!is) throw new MockError('not_found', `issue ${p.key} not found`);
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12029: cannot connect');
      return (FLOW[statusKey(is.status)] || []).map(([id, name, to]) => ({ id, name, to: clone(ST[to]) }));
    },
    transition(p) {
      requireConfigured();
      const is = issues.find((i) => i.key === p.key);
      if (!is) throw new MockError('not_found', `issue ${p.key} not found`);
      const tr = (FLOW[statusKey(is.status)] || []).find(([id]) => id === String(p.transitionId));
      if (!tr) throw new MockError('jira', `Jira 400: transition ${p.transitionId} is not valid for ${p.key}`);
      maybeFail(ERROR_RATE * 1.8, 'jira', 'Jira 400: Field "Resolution" is required by the workflow validator');
      is.status = ST[tr[2]];
      is.updated = rfc(now());
      return clone(is);
    },
    openUrl(p) {
      const url = (p && p.url) || '';
      if (!/^https?:\/\//i.test(url) || !settings.jiraUrl || !url.startsWith(settings.jiraUrl)) throw new MockError('forbidden_url', 'URL is not under jiraUrl');
      console.info('[jiraffe mock] openUrl →', url); // eslint-disable-line no-console
      return null;
    },
    worklogPreview() {
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12002: the operation timed out');
      return buildRun('preview');
    },
    worklogRun() {
      requireConfigured();
      const run = buildRun('manual');
      run.items.forEach((i) => {
        if (i.todoSeconds < 60) { i.result = 'skipped'; return; }
        if (Math.random() < ERROR_RATE) { i.result = 'error'; i.error = 'Jira 403: You do not have the permission to log work on this issue'; return; }
        i.result = 'logged';
        loggedToday.set(i.key, (loggedToday.get(i.key) || 0) + i.todoSeconds);
      });
      run.totalSeconds = run.items.filter((i) => i.result === 'logged').reduce((a, i) => a + i.todoSeconds, 0);
      run.total = fmt(run.totalSeconds);
      history.unshift(clone(run));
      if (history.length > 60) history.length = 60;
      setTimeout(() => emit('state', state()), 50);
      return run;
    },
    getWorklogHistory: () => clone(history),
    getNotifications() {
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12029: cannot connect');
      return clone(notifications);
    },
    markNotificationsRead(p) {
      const ids = p && Array.isArray(p.ids) ? p.ids.map(String) : null;
      notifications.forEach((n) => { if (!ids || ids.includes(n.id)) n.read = true; });
      return { unread: notifications.filter((n) => !n.read).length };
    },
  };

  /* ---------- binding emulation (same shape as go-webview2 Bind + Dispatch/Eval) ---------- */
  function emit(type, payload) {
    window.dispatchEvent(new CustomEvent('jiraffe', { detail: { type, payload: clone(payload) } }));
  }

  // Real Go inlines a per-run nonce first in <head>; the mock accepts and ignores it.
  if (window.__jiraffeNonce == null) window.__jiraffeNonce = 'mock';
  window.__jiraffe = function (id, method, paramsJSON /* , nonce — ignored by the mock */) {
    const latency = flags.has('slow') ? rand(1500, 3000) : method === 'testConnection' ? rand(700, 1200) : rand(200, 650);
    setTimeout(() => {
      let ok = true;
      let payload;
      try {
        const fn = METHODS[method];
        if (!fn) throw new MockError('unknown_method', `unknown method ${method}`);
        payload = fn(paramsJSON ? JSON.parse(paramsJSON) : {});
      } catch (e) {
        ok = false;
        payload = e instanceof MockError ? e : { code: 'internal', message: String(e && e.message || e) };
      }
      if (typeof window.__jiraffeResolve === 'function') window.__jiraffeResolve(id, ok, JSON.stringify(payload === undefined ? null : payload));
    }, latency);
    return Promise.resolve(null);
  };
  window.__jiraffeMock = { flags: [...flags], emit };

  /* ---------- periodic comment notifications ---------- */
  setInterval(() => {
    if (!settings.notifyComments || !configured() || !issues.length) return;
    const is = pick(issues);
    const n = { id: String(notifSeq++), issueKey: is.key, issueSummary: is.summary, author: pick(AUTHORS), body: pick(BODIES), created: rfc(now()), url: `${is.url}?focusedCommentId=${notifSeq}`, read: false };
    notifications.unshift(n);
    if (notifications.length > 200) notifications.length = 200;
    emit('notification', n);
    emit('state', state());
  }, flags.has('fast') ? 10000 : 45000);

  console.info('[jiraffe mock] fake backend installed. Flags: ?mock=unconfigured,empty,errors,noerrors,slow,fast,warnings'); // eslint-disable-line no-console
})();

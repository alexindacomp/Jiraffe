/* Jiraffe UI: fake backend for development / review in a normal browser.
   Installed only when the Go binding window.__jiraffe is absent. Implements every contract method
   (SPEC v0.1 – v0.3) with latency, occasional errors and periodic `notification` events.
   v0.3 data: projects with different status sets (MB has "Reject", CMA "ForTesting", both "Закрыт"), agile boards
   with column configurations (multi-status columns; MB and CMA have several boards; 880 is shared by MB and CMA;
   HR's default board has none of its statuses ⇒ workflow columns with a board picker; EDS/OPS/SEC/DWH have no boards
   ⇒ workflow columns), sprints across projects, backlog, carried-over issues.
   v0.4 data: a JEditor-style HTML description (EDS-231: panels, list, code, image, table, links, plus a <script>,
   an onerror and a javascript: link that must stay inert) and an HTML comment; epic time (logged / estimate) in RBI
   (10 epics ⇒ "Other", RBI-909 without time), CMA, EDS and MB (estimate only ⇒ "No logged time"); Dev / QA story
   points (RBI-1040 both, CMA-412 Dev only, MB-77 QA only, the rest none).
   URL flags (?mock=a,b): unconfigured | empty | errors (frequent failures) | noerrors | slow | fast (notification every 10 s)
   | warnings (toggling autostart "fails": warning + autostartEffective = previous value)
   | noagile (agile API missing: no boards, workflow columns, sprints unavailable) | truncated (board/epics/sprints report truncated)
   | untracked (trackedProjects = null) | closedfail (getSprintGroups: closed sprints fail ⇒ closedUnavailable)
   | limit (tracking more than 6 projects fails with a validation error, like the Go limit of 200) | noprefs (no getUiPrefs: older backend)
   | timepartial (getEpics reports timePartial: some issues' time could not be read)
   | (v0.5 create / edit: see the "v0.5 create / edit issues" block)
   | update | update-oncheck | update-fail | update-nowrite | update-badsig | update-checkfail | noupdate | devbuild | updated
   (auto-update, see the "v0.4 auto-update" block below). */
(function () {
  'use strict';
  if (typeof window.__jiraffe === 'function') return;

  const flags = new Set((new URLSearchParams(location.search).get('mock') || '').split(',').map((s) => s.trim()).filter(Boolean));
  const ERROR_RATE = flags.has('noerrors') ? 0 : flags.has('errors') ? 0.5 : 0.06;
  const JIRA = 'https://sd.local';
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
  const daysFromNow = (n, h = 10) => { const d = new Date(); d.setDate(d.getDate() + n); d.setHours(h, 0, 0, 0); return rfc(d); };
  const atTime = (day, hhmm) => { const [h, m] = hhmm.split(':').map(Number); const d = new Date(day); d.setHours(h, m, 0, 0); return d; };

  /* ---------- people ---------- */
  const U = {
    me: { name: 'o.koval', displayName: 'Олена Коваль' },
    am: { name: 'a.melnyk', displayName: 'Андрій Мельник' },
    is: { name: 'i.shevchenko', displayName: 'Ірина Шевченко' },
    do: { name: 'd.orlov', displayName: 'Дмитрий Орлов' },
    nb: { name: 'n.belova', displayName: 'Наталья Белова' },
    mh: { name: 'm.honcharenko', displayName: 'Максим Гончаренко' },
    sk: { name: 's.kim', displayName: 'Сергей Ким' },
  };
  const ME = U.me;

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
    closed:     { id: '6', name: 'Закрыт', category: 'done' },
    reject:     { id: '10202', name: 'Reject', category: 'done' },
  };
  const ORDER = ['backlog', 'todo', 'inprog', 'forReview', 'inReview', 'forTesting', 'solved', 'done', 'closed', 'reject'];
  const CAT_RANK = { new: 0, indeterminate: 1, done: 2 };
  // [transitionId, name, target status key]; only targets in the issue's project workflow are offered.
  const FLOW = {
    backlog: [['11', 'Запланировать', 'todo'], ['21', 'Взять в работу', 'inprog']],
    todo: [['21', 'Взять в работу', 'inprog'], ['12', 'Вернуть в бэклог', 'backlog'], ['91', 'Отклонить', 'reject']],
    inprog: [['31', 'На ревью', 'forReview'], ['32', 'Срочное ревью', 'forReview'], ['33', 'На ревью', 'inReview'], ['41', 'Приостановить', 'todo'], ['51', 'Предоставить решение', 'solved'], ['52', 'Готово', 'done']],
    forReview: [['61', 'Начать ревью', 'inReview'], ['22', 'Вернуть в работу', 'inprog']],
    inReview: [['71', 'Ревью пройдено', 'forTesting'], ['72', 'Ревью пройдено', 'done'], ['73', 'Закрыть', 'closed'], ['23', 'На доработку', 'inprog'], ['92', 'Отклонить', 'reject']],
    forTesting: [['81', 'Тестирование пройдено', 'closed'], ['24', 'Найдены ошибки', 'inprog']],
    solved: [['25', 'Переоткрыть', 'inprog'], ['82', 'Закрыть', 'closed']],
    done: [['26', 'Переоткрыть', 'inprog']],
    closed: [['28', 'Переоткрыть', 'inprog']],
    reject: [['27', 'Переоткрыть', 'todo']],
  };
  // Statuses per project workflow (GET /project/{key}/statuses, union over issue types), in workflow order.
  const PSTAT = {
    RBI: ['backlog', 'todo', 'inprog', 'forReview', 'inReview', 'done'],
    CMA: ['backlog', 'todo', 'inprog', 'forReview', 'inReview', 'forTesting', 'solved', 'closed'],
    MB: ['backlog', 'todo', 'inprog', 'inReview', 'reject', 'closed'],
    EDS: ['todo', 'inprog', 'forReview', 'done'],
  };
  const PSTAT_DEFAULT = ['todo', 'inprog', 'done'];
  const projOf = (r) => r.key.split('-')[0];
  const statusesOf = (project) => PSTAT[project] || PSTAT_DEFAULT;
  const statusKey = (st) => Object.keys(ST).find((k) => ST[k].id === st.id);
  const flowOf = (r) => (FLOW[statusKey(r.status)] || []).filter(([, , to]) => statusesOf(projOf(r)).includes(to));
  const isDone = (rec) => rec.status.category === 'done';

  /* ---------- projects, types ---------- */
  const PROJECTS = [
    { key: 'RBI', name: 'Reporting & BI', category: 'Внутренние системы', lead: U.am.displayName },
    { key: 'CMA', name: 'Кабинет клиента', category: 'Клиентские сервисы', lead: U.nb.displayName },
    { key: 'MB', name: 'Мобильный банкинг', category: 'Клиентские сервисы', lead: U.sk.displayName },
    { key: 'EDS', name: 'Электронный документооборот', category: 'Интеграции', lead: U.do.displayName },
    { key: 'HR', name: 'HR-портал', category: 'Внутренние системы', lead: U.is.displayName },
    { key: 'OPS', name: 'Инфраструктура и эксплуатация', category: 'Платформа', lead: U.mh.displayName },
    { key: 'SEC', name: 'Информационная безопасность', category: 'Платформа', lead: U.do.displayName },
    { key: 'DWH', name: 'Хранилище данных', category: 'Внутренние системы', lead: U.is.displayName },
  ];
  // 'LEGACY' is a stale tracked key (project deleted / no access): getProjects returns it with available: false.
  let tracked = flags.has('untracked') ? null : ['RBI', 'CMA', 'MB', 'EDS', 'HR', 'LEGACY'];
  const initTracked = () => { tracked = [...new Set(all().filter((r) => mine(r) && !isDone(r)).map((r) => r.key.split('-')[0]))]; };
  const isTracked = (key) => !!tracked && tracked.includes(String(key).split('-')[0]);

  const TYPES = {
    epic: { name: 'Epic', kind: 'epic' },
    story: { name: 'История', kind: 'story' },
    task: { name: 'Задача', kind: 'task' },
    bug: { name: 'Ошибка', kind: 'task' },
    sub: { name: 'Подзадача', kind: 'subtask' },
  };

  /* ---------- issues ----------
     [key, summary, status, type, priority, assignee, updatedMinutesAgo, extras]
     extras: epic (key), name/color (for epics), sp, dsp/qsp (Dev / QA story points), parent, sprints [ids], labels, comps,
     versions, est [orig, rem, spent] h (also the epic dashboard's estimate / logged time) */
  const H = 3600;
  const RAW = [
    // Epics
    ['RBI-900', 'Новый движок отчётов: построение, экспорт, фильтры', 'inprog', 'epic', 'High', 'am', 60 * 30, { name: 'Движок отчётов', color: 'ghx-label-4' }],
    ['RBI-950', 'Единый вход через корпоративный SSO', 'todo', 'epic', 'Medium', 'am', 60 * 50, { name: 'SSO', color: 'ghx-label-9' }],
    // v0.4: more than 8 RBI epics (dashboard "Other"); time logged on the epics themselves; RBI-909 has none.
    ['RBI-901', 'Мобильная версия портала отчётов', 'inprog', 'epic', 'Medium', 'is', 60 * 30, { name: 'Мобильные отчёты', color: 'ghx-label-10', est: [30, 12, 21] }],
    ['RBI-902', 'Планировщик рассылок отчётов по расписанию', 'inprog', 'epic', 'Medium', 'do', 60 * 31, { name: 'Планировщик рассылок', color: 'ghx-label-2', est: [24, 8, 16] }],
    ['RBI-903', 'Каталог метрик и их владельцев', 'inprog', 'epic', 'Low', 'is', 60 * 32, { name: 'Каталог метрик', color: 'ghx-label-11', est: [16, 6, 11] }],
    ['RBI-904', 'Дашборды для руководителей подразделений', 'todo', 'epic', 'Medium', 'am', 60 * 33, { name: 'Дашборды руководителя', color: 'ghx-label-12', est: [20, 14, 7] }],
    ['RBI-905', 'Миграция хранилища отчётов на PostgreSQL 16', 'inprog', 'epic', 'High', 'mh', 60 * 34, { name: 'Миграция на PostgreSQL 16', color: 'ghx-label-13', est: [12, 8, 4.5] }],
    ['RBI-906', 'Аудит доступа к отчётам', 'todo', 'epic', 'Medium', 'do', 60 * 35, { name: 'Аудит доступа', color: 'ghx-label-5', est: [8, 4, 3] }],
    ['RBI-907', 'Контроль качества данных в витринах', 'todo', 'epic', 'Low', 'is', 60 * 36, { name: 'Data quality', color: 'ghx-label-14', est: [6, 5, 1.5] }],
    ['RBI-908', 'Экспорт отчётов в PDF', 'todo', 'epic', 'Low', 'sk', 60 * 37, { name: 'Экспорт в PDF', color: 'ghx-label-7', est: [4, 3.5, 0.5] }],
    ['RBI-909', 'Архивация устаревших отчётов', 'backlog', 'epic', 'Lowest', null, 60 * 38, { name: 'Архивация отчётов', color: 'ghx-label-3' }],
    ['CMA-300', 'Журнал операций 2.0', 'inprog', 'epic', 'Medium', 'nb', 60 * 70, { name: 'Журнал операций 2.0', color: 'ghx-label-6' }],
    ['CMA-310', 'Уведомления в кабинете клиента', 'todo', 'epic', 'Medium', 'nb', 60 * 72, { name: 'Уведомления', color: 'ghx-label-1', est: [12, 9, 3.5] }],
    ['MB-50', 'Push-сповіщення для мобільних застосунків', 'inprog', 'epic', 'High', 'sk', 60 * 20, { name: 'Push-сповіщення', color: 'ghx-label-2' }],
    ['EDS-200', 'Интеграция с провайдером ЭДО', 'inprog', 'epic', 'High', 'do', 60 * 40, { name: 'Интеграция с ЭДО', color: 'ghx-label-8' }],
    ['EDS-150', 'Миграция архива документов', 'done', 'epic', 'Low', 'do', 60 * 24 * 20, { name: 'Миграция архива', color: 'ghx-label-11' }],
    // Stories
    ['RBI-1031', 'Перенести авторизацию на OAuth2 (Keycloak)', 'todo', 'story', 'Medium', 'me', 95, { epic: 'RBI-950', sp: 5, est: [20, 20, 0], sprints: [6338], labels: ['security', 'backend'], comps: ['Auth'], versions: ['4.2.0'] }],
    ['RBI-1040', 'Экспорт отчётов в Excel без ограничения по строкам', 'inprog', 'story', 'High', 'is', 180, { epic: 'RBI-900', sp: 8, dsp: 5, qsp: 3, sprints: [6337, 6338], comps: ['Export'], versions: ['4.2.0'], est: [24, 10, 14] }],
    ['RBI-1041', 'Конструктор фильтров отчёта', 'todo', 'story', 'Medium', 'do', 60 * 8, { epic: 'RBI-900', sp: 5, sprints: [6338] }],
    ['CMA-412', 'Добавить фильтр по дате в журнал операций', 'inprog', 'story', 'Medium', 'me', 47, { epic: 'CMA-300', sp: 3, dsp: 2, labels: ['ux'], sprints: [7101], est: [8, 3, 6] }],
    ['CMA-420', 'Выгрузка журнала операций в CSV', 'closed', 'story', 'Low', 'mh', 60 * 24 * 4, { epic: 'CMA-300', sp: 2, sprints: [7200], est: [4, 0, 4.5] }],
    ['MB-77', 'Push-сповіщення для iOS', 'inReview', 'story', 'High', 'me', 300, { epic: 'MB-50', sp: 8, qsp: 3, versions: ['3.14'], sprints: [7100, 7101], est: [16, 16, 0] }],
    ['MB-90', 'Налаштування сповіщень у профілі', 'todo', 'story', 'Medium', 'sk', 60 * 26, { epic: 'MB-50', sp: 3, sprints: [7102], est: [6, 6, 0] }],
    ['EDS-230', 'Интеграция с сервисом электронного документооборота: подписание пакета', 'inprog', 'story', 'Medium', 'me', 5, { epic: 'EDS-200', sp: 8, comps: ['Signing'], est: [36, 12, 20] }],
    ['EDS-240', 'Статусы подписания в карточке документа', 'inprog', 'story', 'Medium', 'nb', 60 * 5, { epic: 'EDS-200', sp: 5 }],
    ['RBI-1050', 'Тёмная тема для портала отчётов', 'backlog', 'story', 'Low', 'mh', 60 * 24 * 9, { sp: 3, sprints: [6339] }],
    ['CMA-430', 'Онбординг новых клиентов в кабинете', 'todo', 'story', 'Medium', null, 60 * 24 * 2, { sp: 5, sprints: [7202] }],
    // Tasks, bugs, sub-tasks
    ['RBI-1028', 'Відділити frontend від backend', 'inprog', 'task', 'High', 'me', 12, { epic: 'RBI-950', sprints: [6338], labels: ['architecture'], est: [16, 6, 9.5] }],
    ['RBI-1019', 'Помилка 500 при експорті звіту в Excel, якщо в періоді більше 10 000 рядків', 'forReview', 'bug', 'Highest', 'me', 38, { epic: 'RBI-900', sprints: [6338], comps: ['Export'], due: 3, est: [6, 2, 5] }],
    ['RBI-997', 'Оновити залежності Spring Boot до 3.3', 'backlog', 'task', 'Low', 'me', 60 * 26, { epic: 'RBI-900', sprints: [6339], sp: 2 }],
    ['RBI-1042', 'Индексы для таблицы фактов отчётов', 'inprog', 'task', 'Medium', 'is', 60 * 3, { epic: 'RBI-900', sprints: [6338], sp: 3, est: [8, 2, 7.5] }],
    ['RBI-1043', 'Покрыть экспорт интеграционными тестами', 'todo', 'sub', 'Medium', 'do', 60 * 6, { parent: 'RBI-1040', sprints: [6338] }],
    ['RBI-1044', 'Нагрузочное тестирование движка', 'todo', 'task', 'Medium', 'sk', 60 * 30, { epic: 'RBI-900', sprints: [6336, 6337, 6338], sp: 3 }],
    ['RBI-1045', 'Обновить документацию API отчётов', 'done', 'task', 'Low', 'is', 60 * 24, { epic: 'RBI-900', sprints: [6338], sp: 1, est: [4, 0, 3.5] }],
    ['RBI-1046', 'Метрики времени построения отчёта в Grafana', 'done', 'task', 'Medium', 'me', 60 * 24 * 2, { epic: 'RBI-900', sprints: [6338], sp: 2, est: [6, 0, 6.5] }],
    ['RBI-1029', 'Кэширование справочников', 'done', 'task', 'Medium', 'me', 60 * 24 * 16, { epic: 'RBI-900', sprints: [6337], sp: 3, est: [10, 0, 12] }],
    ['RBI-1030', 'Логирование запросов к отчётам', 'done', 'task', 'Low', 'is', 60 * 24 * 15, { sprints: [6337], sp: 2 }],
    ['CMA-415', 'Не обновляется счётчик непрочитанных сообщений после перехода по push', 'inReview', 'bug', 'High', 'me', 180, { sprints: [7201], sp: 2 }],
    ['CMA-399', 'Рефакторинг модуля уведомлений', 'backlog', 'task', 'Medium', 'me', 60 * 24 * 6, {}],
    ['CMA-421', 'API фильтрации журнала по дате', 'closed', 'task', 'Medium', 'nb', 60 * 24, { epic: 'CMA-300', sprints: [7201], sp: 3, est: [6, 0, 7] }],
    ['CMA-422', 'UI-компонент выбора периода', 'inprog', 'task', 'Medium', 'mh', 60 * 2, { epic: 'CMA-300', sprints: [7201], sp: 5, est: [10, 4, 7.5] }],
    ['CMA-433', 'Падает сборка на CI после обновления Gradle', 'forReview', 'bug', 'High', 'do', 60 * 4, { sprints: [7201] }],
    ['CMA-441', 'Регресс журнала операций перед релизом', 'forTesting', 'task', 'Medium', 'mh', 60 * 6, { epic: 'CMA-300', sprints: [7201], sp: 2, est: [5, 3, 2] }],
    ['CMA-442', 'Консультация: выгрузка выписки за 2023 год', 'solved', 'task', 'Low', 'me', 60 * 24 * 2, { sprints: [7201] }],
    ['MB-81', 'Краш при повороті екрана на Android 14', 'todo', 'bug', 'Highest', 'me', 22, { epic: 'MB-50', sprints: [7101] }],
    ['MB-78', 'Сертифікати APNs для продакшну', 'closed', 'task', 'High', 'sk', 60 * 24 * 3, { epic: 'MB-50', sprints: [7100], sp: 2 }],
    ['MB-79', 'Обробка silent push', 'inprog', 'sub', 'Medium', 'me', 60 * 7, { parent: 'MB-77', sprints: [7101] }],
    ['MB-95', 'Локалізація екранів оплати', 'inReview', 'task', 'Low', 'nb', 60 * 9, { sprints: [7101], sp: 1 }],
    ['MB-96', 'Темна тема для екрана входу', 'reject', 'task', 'Low', 'sk', 60 * 24 * 3, { sprints: [7100] }],
    ['MB-97', 'Оновити SDK аналітики', 'backlog', 'task', 'Low', null, 60 * 24 * 8, {}],
    ['EDS-233', 'Описать API загрузки вложений в Swagger', 'forReview', 'task', 'Low', 'me', 60 * 3, { epic: 'EDS-200' }],
    ['EDS-219', 'Таймаут при генерации PDF больше 50 страниц', 'todo', 'bug', 'Medium', 'me', 60 * 50, { epic: 'EDS-200' }],
    ['EDS-231', 'Подписание пакета: бэкенд и очередь', 'inprog', 'task', 'High', 'am', 60, { epic: 'EDS-200', est: [24, 10, 15] }],
    ['EDS-241', 'Вебхук статусов от провайдера ЭДО', 'todo', 'task', 'Medium', 'is', 60 * 11, { epic: 'EDS-200' }],
    ['EDS-151', 'Перенос архива за 2019–2022', 'done', 'task', 'Medium', 'do', 60 * 24 * 21, { epic: 'EDS-150', est: [20, 0, 18] }],
    ['EDS-152', 'Проверка целостности после миграции', 'done', 'task', 'Low', 'nb', 60 * 24 * 20, { epic: 'EDS-150', est: [6, 0, 5] }],
    ['HR-5', 'Шаблон заявки на отпуск', 'todo', 'task', 'Low', 'am', 60 * 24 * 3, {}],
    ['HR-7', 'Синхронизация оргструктуры', 'inprog', 'task', 'Medium', 'is', 60 * 20, {}],
    ['OPS-12', 'Продлить сертификат sd.local', 'todo', 'task', 'High', 'me', 60 * 15, {}],
    ['SEC-31', 'Аудит прав сервисных учётных записей', 'todo', 'task', 'Medium', 'do', 60 * 24 * 5, {}],
    ['OPS-14', 'Обновить базовый образ CI-агентов', 'inprog', 'task', 'Medium', 'me', 60 * 9, {}],
    ['DWH-8', 'Выгрузка витрины платежей для отчётов', 'todo', 'task', 'Low', 'me', 60 * 24 * 2, { sp: 2 }],
    ['RBI-1060', 'Пагинация в списке отчётов', 'backlog', 'task', 'Low', null, 60 * 24 * 7, { sp: 2 }],
    ['RBI-1061', 'Аудит медленных запросов к отчётам', 'todo', 'task', 'Medium', 'me', 60 * 24, { sp: 3 }],
  ];
  // [from, link type, to]; type: [name, outward, inward]
  const LT = { rel: ['Relates', 'relates to', 'relates to'], blocks: ['Blocks', 'blocks', 'is blocked by'], dup: ['Duplicate', 'duplicates', 'is duplicated by'], clone: ['Cloners', 'clones', 'is cloned by'] };
  const LT_IDS = { blocks: '10000', dup: '10002', rel: '10003', clone: '10100' }; // v0.6 getLinkTypes ids
  const LINKS = [
    ['RBI-1028', 'rel', 'RBI-1031'], ['RBI-1019', 'blocks', 'RBI-1040'], ['RBI-1042', 'rel', 'RBI-1040'],
    ['CMA-421', 'rel', 'CMA-412'], ['CMA-422', 'rel', 'CMA-412'], ['CMA-422', 'rel', 'CMA-420'],
    ['MB-78', 'blocks', 'MB-77'], ['MB-81', 'rel', 'MB-95'], ['EDS-233', 'rel', 'EDS-230'], ['EDS-231', 'rel', 'EDS-230'],
    ['EDS-241', 'blocks', 'EDS-240'], ['CMA-415', 'dup', 'CMA-399'], ['OPS-12', 'rel', 'RBI-1031'],
  ];
  let linkSeq = 70000;
  LINKS.forEach((l) => l.push(String(linkSeq++))); // [from, type, to, id]

  const recs = new Map();
  if (!flags.has('empty')) {
    RAW.forEach(([key, summary, st, type, prio, who, ago, x], i) => {
      const created = minutesAgo(ago + 60 * 24 * (5 + (i % 9)));
      recs.set(key, {
        id: String(20000 + i), rank: i, key, summary, status: ST[st], type: TYPES[type], priority: { name: prio },
        assignee: who ? U[who] : null, updated: minutesAgo(ago), created,
        reporter: who === 'me' ? U.am : U.me, epic: x.epic || null, epicName: x.name, epicColor: x.color,
        storyPoints: x.sp, devStoryPoints: x.dsp, qaStoryPoints: x.qsp, parent: x.parent || null, sprints: x.sprints || [], labels: x.labels || [], components: x.comps || [],
        fixVersions: x.versions || [], est: x.est || null, dueDate: x.due != null ? isoDay(new Date(Date.now() + x.due * 86400000)) : undefined,
        watchers: 1 + (i % 5), comments: [], changelog: [],
      });
    });
  }
  const rec = (key) => recs.get(key);
  const all = () => [...recs.values()];
  const url = (key) => `${JIRA}/browse/${key}`;
  const mine = (r) => !!r.assignee && r.assignee.name === ME.name;

  /* descriptions, comments (wiki markup) and changelogs */
  const DESC = {
    'RBI-1028': `h2. Цель
Разделить монолит на *frontend* и _backend_, чтобы выпускать их независимо.

h3. Шаги
# Вынести статику в отдельный образ, см. {{frontend/Dockerfile}}
# Настроить CORS на API-шлюзе
## разрешить только {{https://sd.local}}
## проверить preflight
# Обновить пайплайн
* сборка фронта
* деплой в k8s

{code:yaml}
gateway:
  cors:
    origins: [ "https://sd.local" ]
{code}

Подробнее в [вики|https://sd.local/wiki/RBI/arch] и в [RBI-1031]. Вопросы — к [~a.melnyk].`,
    'RBI-1019': `При экспорте периода больше *10 000* строк сервер отвечает {{500 Internal Server Error}}.

{noformat}
java.lang.OutOfMemoryError: Java heap space
    at org.apache.poi.xssf.usermodel.XSSFSheet.createRow(XSSFSheet.java:712)
{noformat}

bq. Воспроизводится на стенде _preprod_ стабильно.

Проверка экранирования: {{<img src=x onerror=alert(1)>}} и <script>alert('xss')</script> должны отображаться как текст (это wiki-разметка: HTML внутри {{…}} не включает HTML-режим). Ссылка на чужой сайт: [не наш Jira|https://example.com/evil].`,
    'EDS-230': `h3. Подписание пакета документов
Пакет подписывается одной операцией у провайдера ЭДО.
* до 50 документов в пакете
* статусы приходят вебхуком ([EDS-241])
----
{quote}
Провайдер ограничивает 10 запросов в секунду — нужна очередь.
{quote}`,
    'MB-77': 'Підтримка push-сповіщень для iOS через APNs. Див. [EDS-230] для схеми підпису payload — _жартую_, це інший проєкт 🙂',
    'CMA-412': 'Фильтр по дате «с — по» в журнале операций. Макет в Figma: [макет|https://sd.local/wiki/CMA/filters].',
    // v0.4: JEditor HTML. The <script>, onerror / onclick and the javascript: link must never run; inline styles are dropped.
    'EDS-231': `<div class="userTemplate" style="font-family: Arial; background-color: #ffffff">
<div class="jePanel_blue" style="border: 1px solid #9cc3e6; background-color: #deebf7; padding: 8px">
<div class="jePanelHeader" style="font-weight: bold; color: #1f4e79">BRIEF:</div>
<div class="jePanelContent">
<p dir="auto">Бэкенд подписания пакета: принимает до <b>50</b> документов, ставит задание в очередь и сразу отдаёт <code>jobId</code>.</p>
<p dir="auto">&nbsp;</p>
<p dir="auto">Связано с <a href="https://sd.local/browse/EDS-230">EDS-230</a>, схема в <a href="https://sd.local/wiki/EDS/signing" style="color: red">вики</a>.</p>
</div>
</div>
<p dir="auto">&nbsp;</p>
<p dir="auto">&nbsp;</p>
<div class="jePanel_yell" style="background-color: #fff2cc">
<div class="jePanelHeader_red" style="color: #c00000">ВАЖНО:</div>
<div class="jePanelContent"><ul><li>Лимит провайдера — <i>10 запросов в секунду</i></li><li>Повтор при <code>429</code> с экспоненциальной задержкой<ul><li>не больше 5 попыток</li></ul></li></ul></div>
</div>
<!-- JEditor: служебный комментарий, не должен отображаться -->
<h1 style="font-size: 32px">Формат задания</h1>
<pre style="background: #f5f5f5">POST /api/v1/sign-jobs
{ "documents": ["doc-1", "doc-2"], "callback": "https://sd.local/hooks/eds" }</pre>
<ol start="3"><li>Шаг 3: проверить подпись</li><li>Шаг 4: <u>сохранить</u> квитанцию</li></ol>
<table border="1" style="border-collapse: collapse; width: 900px" onclick="alert('XSS via onclick')">
<thead><tr><th style="background: #ddd">Статус</th><th>Код</th><th>Описание</th></tr></thead>
<tbody>
<tr><td>queued</td><td><code>100</code></td><td>Задание принято и ждёт отправки провайдеру</td></tr>
<tr><td>signing</td><td><code>200</code></td><td rowspan="2" style="background: #fffbe6">Провайдер обрабатывает пакет; статус приходит вебхуком ([EDS-241])</td></tr>
<tr><td>partially_signed_with_a_very_long_status_name_that_does_not_wrap</td><td><code>206</code></td></tr>
<tr><td colspan="3"><strike>failed_legacy</strike> — больше не используется</td></tr>
</tbody></table>
<p dir="auto">Схема очереди:</p>
<p dir="auto"><span class="image-wrap" style=""><img src="https://sd.local/plugins/servlet/jeditor_file_provider?imgId=42&amp;fileName=queue.png" alt="queue.png" style="border: 0"></span></p>
<p dir="auto"><img src="x" onerror="alert('XSS via onerror')"><script>alert('XSS via script')</script><font color="red">Проверка безопасности: скрипт и onerror выше не должны выполниться.</font></p>
<p dir="auto">Ответственный: <span style="color: #c00">Андрій Мельник</span><br>Ревью: Ірина Шевченко<br><br><br><br>Конец списка.</p>
<blockquote>Цитата из ТЗ: «пакет подписывается одной операцией».</blockquote>
<!-- CR-1: a <br> at the edge of an inline element in the middle of a line must stay; CR-2: no <style> text in a link -->
<p><b>Bold line<br></b>Next line</p>
<p>first<i><br>second</i></p>
<p><code>a<br></code>b</p>
<p>Ссылка без CSS в тексте: <a href="https://sd.local/wiki/EDS/queue">очередь<style>.x{color:red}</style></a></p>
<hr>
<p dir="auto"><a href="javascript:alert('XSS via javascript: link')">ссылка javascript:</a> должна остаться текстом. H<sub>2</sub>O, x<sup>2</sup>, <del>удалено</del>, <ins>добавлено</ins>.</p>
<iframe src="https://example.com"></iframe><form action="https://example.com"><input value="x"><button>Отправить</button></form>
</div>`,
  };
  DESC['RBI-1028'] = DESC['RBI-1028'].replace(/\n/g, '\r\n'); // Jira Server sends CRLF
  const COMMENTS = {
    'RBI-1028': [
      ['am', 60 * 26, 'Пару моментов по *архитектуре*:\n# шлюз уже отдаёт CORS-заголовки\n# фронт можно собирать отдельно\n\nГлянь, пожалуйста, [~o.koval].'],
      ['me', 60 * 25, 'Ок, тогда прокси из фронта убираю. Конфиг:\n{code}\nproxy: false\n{code}'],
      ['is', 12, 'Глянь, будь ласка: API-шлюз тепер віддає CORS-заголовки, можна прибирати проксі з фронта.'],
    ],
    'RBI-1019': [
      ['do', 60 * 30, 'Подтверждаю, падает на _Apache POI_. Предлагаю {{SXSSFWorkbook}} со стримингом.'],
      ['is', 60 * 20, 'Підтверджую, після фіксу експорт на 25 000 рядків проходить за 12 секунд.'],
    ],
    'CMA-415': [['nb', 55, 'Воспроизвела на iOS 17.4: счётчик сбрасывается только после перезапуска приложения. Видео приложила.']],
    'EDS-233': [['do', 60 * 20, 'Swagger посмотрел, ок. Только добавь, пожалуйста, пример ответа с ошибкой *413*.']],
    'MB-77': [['sk', 160, 'Автотести: 42 passed, 1 failed ({{PushPayloadTest.testSilentPush}}). Деталі в [Jenkins|https://sd.local/jenkins/1287].']],
    'EDS-230': [['am', 60 * 3, 'Очередь подняли, см. [EDS-231].']],
    'EDS-231': [
      ['is', 60 * 4, '<p dir="auto">Очередь подняли на <b>RabbitMQ</b>, см. <a href="https://sd.local/browse/EDS-230">EDS-230</a>.</p><p dir="auto">&nbsp;</p><ul><li>prefetch = <code>10</code></li><li>DLQ включена</li></ul><table><tr><th>Стенд</th><th>Очередь</th><th>prefetch</th><th>DLQ</th><th>TTL</th><th>Повторы</th><th>Задержка</th><th>Мониторинг</th><th>Владелец</th></tr><tr><td>dev</td><td>eds.sign.dev</td><td>5</td><td>да</td><td>10 мин</td><td>3</td><td>exp</td><td>Grafana</td><td>Андрій Мельник</td></tr><tr><td>prod</td><td>eds.sign</td><td>10</td><td>да</td><td>30 мин</td><td>5</td><td>exp</td><td>Grafana + PagerDuty</td><td>Ірина Шевченко</td></tr></table><p dir="auto"><img src="https://sd.local/plugins/servlet/jeditor_file_provider?imgId=43" onerror="alert(\'XSS in comment\')"></p>'],
      ['me', 60 * 2, 'Спасибо! Обычный wiki-комментарий: *жирный* и {{mono}}.'],
    ],
  };
  let commentSeq = 50000;
  recs.forEach((r) => {
    (COMMENTS[r.key] || []).forEach(([who, ago, body]) => {
      const c = rfc(new Date(Date.now() - ago * 60000));
      r.comments.push({ id: String(commentSeq++), author: U[who], created: c, updated: who === 'me' && ago > 60 * 24 ? rfc(new Date(Date.now() - (ago - 30) * 60000)) : c, body });
    });
    // changelog: created → (todo) → current status, newest first
    const path = ORDER.slice(0, Math.max(1, ORDER.indexOf(statusKey(r.status)) + 1)).filter((k, i, arr) => i === 0 || k === 'todo' || k === 'inprog' || i === arr.length - 1);
    let t0 = new Date(r.created).getTime();
    const step = Math.max(60000, (new Date(r.updated).getTime() - t0) / Math.max(1, path.length));
    for (let i = 1; i < path.length; i++) {
      t0 += step;
      r.changelog.unshift({ id: String(commentSeq++), author: r.assignee || U.am, created: rfc(new Date(t0)), items: [{ field: 'status', from: ST[path[i - 1]].name, to: ST[path[i]].name }] });
    }
    if (r.assignee) r.changelog.push({ id: String(commentSeq++), author: U.am, created: r.created, items: [{ field: 'assignee', from: '', to: r.assignee.displayName }] });
  });

  /* ---------- shapes ---------- */
  const epicRefOf = (key) => { const e = rec(key); return e ? { key: e.key, name: e.epicName || e.summary, color: e.epicColor } : undefined; };
  function toIssue(r) {
    const out = {
      id: r.id, key: r.key, summary: r.summary, url: url(r.key), project: r.key.split('-')[0], status: r.status, type: r.type,
      priority: r.priority, updated: r.updated, assignee: r.assignee, mine: mine(r),
    };
    if (r.epic) out.epic = epicRefOf(r.epic);
    if (r.storyPoints != null) out.storyPoints = r.storyPoints;
    return out;
  }
  function toRef(r, withAssignee = true) {
    const out = { key: r.key, summary: r.summary, url: url(r.key), status: r.status, type: r.type, mine: withAssignee && mine(r) };
    if (withAssignee) out.assignee = r.assignee;
    if (r.type.kind === 'epic') { out.name = r.epicName || r.summary; out.color = r.epicColor; }
    return out;
  }
  const linksOf = (key) => LINKS.filter(([a, , b]) => a === key || b === key).map(([a, t, b, id]) => ({
    id, relation: a === key ? LT[t][1] : LT[t][2], other: a === key ? b : a,
  })).filter((l) => recs.has(l.other));

  /* ---------- sprints / boards ---------- */
  // location = board.location.projectKey; projects = projects whose boards list contains it;
  // columns = board configuration [name, status keys] (statuses not on the board go to the "other" column).
  const AGILE = [
    { id: 12, name: 'RBI Scrum', type: 'scrum', location: 'RBI', projects: ['RBI'],
      columns: [['To Do', ['backlog', 'todo']], ['In Progress', ['inprog']], ['Review', ['forReview', 'inReview']], ['Done', ['done']]] },
    { id: 673, name: 'ETL board', type: 'kanban', location: 'MB', projects: ['MB'],
      columns: [['Backlog', ['backlog']], ['To Do', ['todo']], ['In Progress', ['inprog']], ['Review', ['inReview']], ['Done', ['closed']], ['Rejected', ['reject']]] },
    { id: 880, name: 'BI DEV Agile board', type: 'scrum', location: 'MB', projects: ['MB', 'CMA'],
      columns: [['Open', ['backlog', 'todo']], ['Dev', ['inprog']], ['Review', ['forReview', 'inReview']], ['QA', ['forTesting']], ['Closed', ['closed', 'reject', 'solved']]] },
    { id: 937, name: 'CMA dev board', type: 'kanban', location: 'CMA', projects: ['CMA'],
      columns: [['Selected', ['todo']], ['In progress', ['inprog']], ['Review', ['forReview', 'inReview']], ['Testing', ['forTesting']], ['Done', ['solved', 'closed']]] },
    { id: 951, name: 'CMA general', type: 'scrum', location: 'CMA', projects: ['CMA'],
      columns: [['To Do', ['backlog', 'todo']], ['In Progress', ['inprog', 'forReview', 'inReview', 'forTesting']], ['Done', ['solved', 'closed']]] },
    { id: 990, name: 'HR legacy board', type: 'kanban', location: 'HR', projects: ['HR'], // none of HR's statuses ⇒ workflow layout
      columns: [['Review', ['forReview']], ['Testing', ['forTesting']], ['Solved', ['solved']]] },
    { id: 991, name: 'HR team', type: 'kanban', location: 'HR', projects: ['HR'],
      columns: [['Backlog', []], ['To Do', ['todo']], ['Doing', ['inprog']], ['Done', ['done']]] },
  ];
  const agileShape = (b) => ({ id: b.id, name: b.name, type: b.type, projectKey: b.location });
  const SPRINTS = [
    { id: 6335, name: 'RBI Sprint 41', state: 'closed', startDate: daysFromNow(-48), endDate: daysFromNow(-34), completeDate: daysFromNow(-34, 17), boardId: 12 },
    { id: 6336, name: 'RBI Sprint 42', state: 'closed', startDate: daysFromNow(-34), endDate: daysFromNow(-20), completeDate: daysFromNow(-20, 17), boardId: 12 },
    { id: 6337, name: 'RBI Sprint 43', state: 'closed', startDate: daysFromNow(-20), endDate: daysFromNow(-6), completeDate: daysFromNow(-6, 17), goal: 'Кэш справочников и логирование', boardId: 12 },
    { id: 6338, name: 'RBI Sprint 44', state: 'active', startDate: daysFromNow(-6), endDate: daysFromNow(8, 18), goal: 'Выпустить экспорт без ограничений и подготовить переход на SSO', boardId: 12 },
    { id: 6339, name: 'RBI Sprint 45', state: 'future', startDate: daysFromNow(8), endDate: daysFromNow(22, 18), boardId: 12 },
    { id: 7100, name: 'BI DEV Sprint 17', state: 'closed', startDate: daysFromNow(-30), endDate: daysFromNow(-16), completeDate: daysFromNow(-16, 17), boardId: 880 },
    { id: 7101, name: 'BI DEV Sprint 18', state: 'active', startDate: daysFromNow(-9), endDate: daysFromNow(5, 18), goal: 'Push-сповіщення iOS у прод; фільтр журналу операцій за датою', boardId: 880 },
    { id: 7102, name: 'BI DEV Sprint 19', state: 'future', startDate: daysFromNow(5), endDate: daysFromNow(19, 18), boardId: 880 },
    { id: 7200, name: 'CMA Sprint 5', state: 'closed', startDate: daysFromNow(-24), endDate: daysFromNow(-10), completeDate: daysFromNow(-10, 17), goal: 'Выгрузка журнала в CSV', boardId: 951 },
    { id: 7201, name: 'CMA Sprint 6', state: 'active', startDate: daysFromNow(-10), endDate: daysFromNow(-1, 18), goal: 'Стабилизировать журнал операций 2.0 и закрыть баги CI перед релизом 2.0. Регресс — до пятницы, потом заморозка веток и выкладка на прод.', boardId: 951 },
    { id: 7202, name: 'CMA Sprint 7', state: 'future', boardId: 951 },
  ];

  function progressOf(list) {
    const p = { done: 0, inProgress: 0, todo: 0, total: list.length };
    list.forEach((r) => { if (r.status.category === 'done') p.done++; else if (r.status.category === 'indeterminate') p.inProgress++; else p.todo++; });
    return p;
  }
  const byRank = (a, b) => a.rank - b.rank;
  const statusColumn = (st, issues) => ({ id: st.id, name: st.name, category: st.category, statuses: [st], issues, status: st });
  /** v0.2 status columns (All projects): statuses that have issues, ordered by category, newest first. */
  function columnsOf(list) {
    return ORDER.map((k) => ST[k]).filter((st) => st.category !== 'done')
      .map((st) => statusColumn(st, list.filter((r) => r.status.id === st.id).sort((a, b) => b.updated.localeCompare(a.updated)).map(toIssue)))
      .filter((c) => c.issues.length);
  }
  /** Category of a column holding several statuses (SPEC v0.3): shared one; else done if last with any done status. */
  function columnCategory(statuses, isLast) {
    const cats = [...new Set(statuses.map((st) => st.category))];
    if (cats.length === 1) return cats[0];
    return isLast && statuses.some((st) => st.category === 'done') ? 'done' : 'indeterminate';
  }
  const boardsOf = (project) => (flags.has('noagile') ? [] : AGILE.filter((b) => b.projects.includes(project)).sort((a, b) => a.id - b.id));
  const DAY = 86400000;
  /** getBoard with a project: every column of its agile board (or workflow statuses), recent done issues, Rank order. */
  function projectBoard(scope, project, boardId) {
    if (!PROJECTS.some((x) => x.key === project)) throw new MockError('project_unavailable', `project ${project} is not accessible`);
    let list = all().filter((r) => projOf(r) === project && r.type.kind !== 'epic' && (!isDone(r) || Date.now() - new Date(r.updated).getTime() <= 14 * DAY));
    if (scope === 'mine') list = list.filter(mine);
    if (scope === 'reported') list = list.filter(reportedByMe);
    list.sort(byRank);
    const boards = boardsOf(project);
    const chosen = boards.find((b) => String(b.id) === String(boardId)) || boards.find((b) => b.location === project) || boards[0] || null;
    let cols = [];
    if (chosen) {
      // Only this project's statuses (shared boards carry other projects' too); empty columns are dropped,
      // ids keep the position in the board configuration (gaps possible).
      const own = statusesOf(project);
      cols = chosen.columns.map(([name, keys], i, arr) => {
        const statuses = keys.filter((k) => own.includes(k)).map((k) => ST[k]);
        return { id: 'col-' + i, name, category: statuses.length ? columnCategory(statuses, i === arr.length - 1) : 'new', statuses, issues: [], status: statuses[0] };
      }).filter((c) => c.statuses.length);
    }
    const agile = cols.length > 0;
    if (!agile) {
      cols = statusesOf(project).map((k) => ST[k]).sort((a, b) => CAT_RANK[a.category] - CAT_RANK[b.category]).map((st) => statusColumn(st, []));
    }
    const other = [];
    list.forEach((r) => {
      const col = cols.find((c) => c.statuses.some((st) => st.id === r.status.id));
      if (col) col.issues.push(toIssue(r)); else other.push(r);
    });
    if (other.length) {
      const sts = [...new Map(other.map((r) => [r.status.id, r.status])).values()];
      cols.push({ id: 'other', name: '', category: new Set(sts.map((st) => st.category)).size === 1 ? sts[0].category : 'indeterminate', statuses: sts, issues: other.map(toIssue), status: sts[0] });
    }
    const layout = { source: agile ? 'agile' : 'workflow', project, boards: boards.map(agileShape) };
    if (chosen) { layout.boardId = chosen.id; layout.boardName = chosen.name; }
    return { columns: clone(cols), fetchedAt: rfc(now()), scope, truncated: false, layout };
  }

  /* ---------- sprint groups (SPEC v0.3) ---------- */
  function sprintGroup(sprint, list) {
    const pts = list.reduce((a, r) => { const v = Number(r.storyPoints) || 0; a.total += v; if (isDone(r)) a.done += v; return a; }, { done: 0, total: 0 });
    const g = { issues: list.map(toIssue), progress: progressOf(list), storyPoints: pts, truncated: false };
    if (sprint) g.sprint = sprint;
    return g;
  }
  function sprintGroups(p) {
    const fetchedAt = rfc(now());
    if (flags.has('noagile')) return { groups: [], backlog: null, fetchedAt, truncated: false, available: false };
    if (p.project && !PROJECTS.some((x) => x.key === p.project)) throw new MockError('project_unavailable', `project ${p.project} is not accessible`);
    const projects = p.project ? [p.project] : PROJECTS.map((x) => x.key).filter((k) => isTracked(k + '-1'));
    const scope = scopeOf(p, 'all');
    const sel = (r) => projects.includes(projOf(r)) && r.type.kind !== 'epic' && (scope !== 'mine' || mine(r)) && (scope !== 'reported' || reportedByMe(r));
    const open = SPRINTS.filter((sp) => sp.state !== 'closed');
    const openIds = new Set(open.map((sp) => sp.id));
    const inOpen = all().filter((r) => sel(r) && r.sprints.some((id) => openIds.has(id))).sort(byRank);
    const ordered = [
      ...open.filter((sp) => sp.state === 'active').sort((a, b) => a.startDate.localeCompare(b.startDate) || a.id - b.id),
      ...open.filter((sp) => sp.state === 'future').sort((a, b) => a.id - b.id),
    ];
    const groups = ordered.map((sp) => sprintGroup(sp, inOpen.filter((r) => r.sprints.includes(sp.id)))).filter((g) => g.issues.length);
    const closedUnavailable = !!p.includeClosed && flags.has('closedfail');
    if (p.includeClosed && !closedUnavailable) {
      const boardIds = new Set(projects.flatMap((k) => boardsOf(k).map((b) => b.id)));
      const closed = SPRINTS.filter((sp) => sp.state === 'closed' && boardIds.has(sp.boardId)
        && all().some((r) => projects.includes(projOf(r)) && r.sprints.includes(sp.id)))
        .sort((a, b) => b.completeDate.localeCompare(a.completeDate)).slice(0, 3);
      closed.forEach((sp) => {
        const g = sprintGroup(sp, all().filter((r) => sel(r) && r.sprints.includes(sp.id)).sort(byRank));
        if (g.issues.length) groups.push(g);
      });
    }
    const back = all().filter((r) => sel(r) && !isDone(r) && !r.sprints.some((id) => openIds.has(id))).sort(byRank);
    const truncated = flags.has('truncated');
    const backlog = back.length ? Object.assign(sprintGroup(null, back), { truncated }) : null;
    const out = { groups: clone(groups), backlog: clone(backlog), fetchedAt, truncated, available: true };
    if (closedUnavailable) out.closedUnavailable = true;
    return out;
  }

  /* ---------- epic tree (SPEC v0.2 rules) ---------- */
  const treeSort = (a, b) => (isDone(a) - isDone(b)) || ((CAT_RANK[a.status.category] === 1 ? 0 : CAT_RANK[a.status.category] === 0 ? 1 : 2) - (CAT_RANK[b.status.category] === 1 ? 0 : CAT_RANK[b.status.category] === 0 ? 1 : 2)) || b.key.localeCompare(a.key, undefined, { numeric: true });
  function storyNode(s) {
    const linked = linksOf(s.key).map((l) => rec(l.other)).filter((r) => r.type.kind !== 'epic' && r.type.kind !== 'story');
    const subs = all().filter((r) => r.parent === s.key);
    const tasks = [...new Map([...subs, ...linked].map((r) => [r.key, r])).values()].sort(treeSort);
    const node = { story: toRef(s), tasks: tasks.map((r) => toRef(r)), progress: progressOf(tasks), _all: [s, ...tasks] };
    if (s.storyPoints != null) node.storyPoints = s.storyPoints;
    if (s.epic) node.epic = toRef(rec(s.epic)); // IssueRef + {name, color}, like EpicNode.epic
    return node;
  }
  function epicNode(e) {
    const stories = all().filter((r) => r.epic === e.key && r.type.kind === 'story').sort(treeSort).map(storyNode);
    const linkedToStories = new Set(stories.flatMap((s) => s.tasks.map((t) => t.key)));
    const tasks = all().filter((r) => r.epic === e.key && r.type.kind !== 'story' && r.type.kind !== 'epic' && !linkedToStories.has(r.key)).sort(treeSort);
    const desc = new Map();
    stories.forEach((s) => s._all.forEach((r) => desc.set(r.key, r)));
    tasks.forEach((r) => desc.set(r.key, r));
    const counted = [e, ...desc.values()];
    // v0.4: own timespent / timeoriginalestimate of the epic and every distinct issue of its tree
    const time = counted.reduce((a, r) => {
      if (r.est) { a.estimateSeconds += Math.round(r.est[0] * H); a.spentSeconds += Math.round(r.est[2] * H); }
      return a;
    }, { spentSeconds: 0, estimateSeconds: 0, issues: counted.length });
    return { epic: toRef(e), stories, tasks: tasks.map((r) => toRef(r)), progress: progressOf([...desc.values()]), time, _all: counted };
  }
  const strip = (n) => { const o = Object.assign({}, n); delete o._all; if (o.stories) o.stories = o.stories.map(strip); return o; };
  const keepScope = (node, scope) => scope !== 'mine' || node._all.some(mine);

  /* ---------- settings / state ---------- */
  const settings = {
    jiraUrl: flags.has('unconfigured') ? '' : JIRA,
    language: 'ru',
    autostart: true,
    worklogEnabled: true,
    worklogTime: '18:00',
    workdayStart: '09:00', workdayEnd: '18:00',
    lunchStart: '13:00', lunchEnd: '14:00',
    dailyCapHours: 8,
    loadFactor: 0.75,
    hoursPerDay: 9,
    workDays: [1, 2, 3, 4, 5],
    vacations: [{ from: '2026-12-24', to: '2027-01-02', note: 'Новогодние' }],
    holidays: ['2026-12-25', '2027-01-01'],
    inProgressStatuses: ['In Progress', 'В работе', 'В роботі'],
    notifyComments: true,
    commentPollMinutes: 5,
    boardJql: 'assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC',
    autoCheckUpdates: true,
  };
  let token = flags.has('unconfigured') ? '' : 'mock-token';
  const configured = () => !!settings.jiraUrl && !!token;
  const capSec = () => Math.round(settings.dailyCapHours * settings.loadFactor * 3600);

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
    const is = rec(key) || { summary: '' };
    return { id: String(notifSeq++), issueKey: key, issueSummary: is.summary, author, body, created: minutesAgo(ago), url: `${url(key)}?focusedCommentId=${notifSeq}`, read };
  });

  /* ---------- worklog ---------- */
  const loggedToday = new Map([['CMA-412', 3600]]); // already logged by me today, seconds
  const SHARES = [['RBI-1028', 2 * 3600 + 56 * 60, '09:00'], ['CMA-412', 3 * 3600 + 4 * 60, '09:00'], ['EDS-230', 3600, '14:00']];
  // Logged today on issues that are not rows of the run (e.g. a meeting ticket). The cap covers it too.
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

  /* v0.7.1 past days: D's in-progress shares; logged time per day; missed days (?mock=nomissed: none, missed2: two). */
  const PAST_SHARES = [['RBI-1028', 3 * 3600 + 36 * 60, '09:00'], ['CMA-412', 2 * 3600, '14:00']]; // 5h 36m: the missed day
  const loggedPast = new Map(); // date → Map(key → seconds), runs made for past days
  const skippedDays = new Set();
  const missedDays = new Set(); // set by seedHistory: working days without a run that still have time to add
  const isoOk = (v) => /^\d{4}-\d{2}-\d{2}$/.test(String(v || ''));
  function checkDay(p) {
    const date = p && p.date;
    if (date == null || date === '') return isoDay(now()); // omitted, "" or today: today exactly as before
    if (typeof date !== 'string' || !isoOk(date)) throw new MockError('bad_params', 'date must be YYYY-MM-DD');
    const back = Math.round((new Date(isoDay(now()) + 'T12:00:00') - new Date(date + 'T12:00:00')) / 86400000);
    if (back < 0) throw new MockError('bad_params', 'date is in the future');
    if (back > 30) throw new MockError('bad_params', 'date is more than 30 days back');
    return date;
  }
  function buildPastRun(trigger, date) {
    const d = new Date(date + 'T12:00:00');
    const run = { at: rfc(now()), date, trigger, items: [], totalSeconds: 0, total: '', capSeconds: capSec() };
    const reason = dayReason(d);
    if (reason === 'weekend' || reason === 'holiday' || reason === 'vacation') {
      run.nonWorkingDay = reason;
      if (d.getDay() !== 6 || history.some((r) => r.date === date && r.trigger === 'manual')) return run; // Saturday: 1 h on RBI-1028
      const is = rec('RBI-1028') || {};
      run.items = [{ key: 'RBI-1028', summary: is.summary || 'RBI-1028', status: (is.status || ST.inprog).name, shareSeconds: 3600, loggedSeconds: 0, todoSeconds: 3600, duration: fmt(3600), started: rfc(atTime(d, '11:00')) }];
      run.totalSeconds = 3600; run.total = fmt(3600);
      return run;
    }
    if (reason) { run.skippedReason = reason; return run; }
    const done = history.find((r) => r.date === date && r.trigger !== 'preview' && r.trigger !== 'catchup');
    const quiet = history.some((r) => r.date === date && r.trigger === 'catchup'); // the "nothing to add" day
    if (quiet && !done) return run;
    const logged = loggedPast.get(date) || new Map();
    run.items = PAST_SHARES.map(([key, share, start]) => {
      const is = rec(key) || {};
      const prior = done ? share : logged.get(key) || 0;
      return { key, summary: is.summary || key, status: (is.status || ST.inprog).name, shareSeconds: share, loggedSeconds: prior, todoSeconds: Math.max(0, share - prior), started: rfc(atTime(d, start)) };
    });
    run.items.forEach((i) => { i.duration = fmt(i.todoSeconds); });
    run.totalSeconds = run.items.reduce((a, i) => a + i.todoSeconds, 0);
    run.total = fmt(run.totalSeconds);
    return run;
  }

  function buildRun(trigger, date) {
    if (date && date !== isoDay(now())) return buildPastRun(trigger, date);
    const d = now();
    const run = { at: rfc(d), date: isoDay(d), trigger, items: [], totalSeconds: 0, total: '', capSeconds: capSec() };
    const reason = dayReason(d);
    if (reason) { run.skippedReason = reason; return run; }
    if (OTHER_LOGGED !== undefined) run.otherLoggedSeconds = OTHER_LOGGED;
    const capLeft = Math.max(0, capSec() - (OTHER_LOGGED || 0) - [...loggedToday.values()].reduce((a, b) => a + b, 0));
    const items = SHARES.map(([key, share, start]) => {
      const is = rec(key) || {};
      const logged = loggedToday.get(key) || 0;
      return { key, summary: is.summary || key, status: (is.status || ST.inprog).name, shareSeconds: share, loggedSeconds: logged, todoSeconds: Math.max(0, share - logged), started: rfc(atTime(d, start)) };
    });
    const sum = items.reduce((a, i) => a + i.todoSeconds, 0);
    if (sum > capLeft) items.forEach((i) => { i.todoSeconds = sum ? Math.floor((i.todoSeconds * capLeft) / sum / 60) * 60 : 0; });
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
    let workIdx = 0;
    for (let back = 1; history.length < 9; back++) {
      const day = new Date(d); day.setDate(d.getDate() - back); day.setHours(18, 0, 3, 0);
      const wd = day.getDay();
      const run = { at: rfc(day), date: isoDay(day), trigger: 'schedule', items: [], totalSeconds: 0, total: '', capSeconds: 8 * 3600 };
      if (wd === 0 || wd === 6) { if (wd === 6) { run.skippedReason = 'weekend'; history.push(run); } continue; }
      const variants = [
        [['RBI-1028', 5 * 3600], ['CMA-412', 3 * 3600]],
        [['RBI-1019', 2 * 3600 + 30 * 60], ['RBI-1028', 4 * 3600], ['MB-77', 3600 + 30 * 60]],
        [['EDS-233', 8 * 3600]],
        [['CMA-415', 4 * 3600], ['EDS-230', 4 * 3600]],
      ];
      const v = variants[back % variants.length];
      workIdx += 1;
      if (!flags.has('nomissed') && back <= 7 && (workIdx === 1 || (workIdx === 4 && flags.has('missed2')))) { missedDays.add(run.date); continue; }
      if (back <= 7 && workIdx === 3) { // the catch-up found nothing to add (logged by hand / no in-progress time) and stored it
        run.trigger = 'catchup'; run.at = rfc(atTime(new Date(), '09:05'));
        run.items = v.map(([key, sec], i) => {
          const is = rec(key) || { summary: key };
          return { key, summary: is.summary, status: 'В работе', shareSeconds: sec, loggedSeconds: sec, todoSeconds: 0, duration: fmt(0), started: rfc(atTime(day, i ? '13:00' : '09:00')), result: 'skipped' };
        });
        history.push(run);
        continue;
      }
      run.items = v.map(([key, sec], i) => {
        const is = rec(key) || { summary: key };
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
      version: (updSupported() ? upd.current : 'dev') + '-mock',
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
    if (typeof s.loadFactor !== 'number' || !(s.loadFactor >= 0 && s.loadFactor <= 1)) add('loadFactor', 'range', { min: 0, max: 1 });
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

  /* ---------- v0.4 auto-update (SPEC §5) ----------
     Default: up to date (no prompt). update: 0.5.0 available, prompt at startup | update-fail: the first download fails
     (network), a retry works | update-nowrite: install fails with no_write_access | update-badsig: verify fails with
     bad_signature | update-checkfail: the last check failed | noupdate: up to date | devbuild: supported = false |
     updated: justUpdated toast at startup | update-oncheck: 0.5.0 is found only by "Check for updates" (combine with
     update-fail / -nowrite / -badsig). A successful install "restarts" by reloading the page into the new version. */
  const UPD_KEY = 'jiraffe.mock.update';
  const updSaved = (() => { try { const v = JSON.parse(sessionStorage.getItem(UPD_KEY) || '{}'); return v && typeof v === 'object' ? v : {}; } catch { return {}; } })();
  const saveUpd = (patch) => { Object.assign(updSaved, patch); try { sessionStorage.setItem(UPD_KEY, JSON.stringify(updSaved)); } catch { /* memory only */ } };
  const UPD_NEWER = ['update', 'update-fail', 'update-nowrite', 'update-badsig', 'update-oncheck'].some((f) => flags.has(f)) && !flags.has('noupdate');
  const RELEASE_NOTES = `## Что нового
- Дашборд «Время по эпикам»: доля каждого эпика в списанном времени или оценке по проектам
- HTML-описания и комментарии (JEditor) отображаются как форматированный текст
- Тёмный заголовок окна в тёмной теме Windows
- Dev / QA story points в карточке задачи
- Автоматические обновления с проверкой подписи

## Исправления
- Карточки на доске больше не «прыгают» после смены статуса
- Списание времени учитывает праздники из настроек
- Ссылки на задачи в комментариях открываются в панели

<img src=x onerror=alert('XSS in release notes')> — заметки выпуска показываются только как текст.

Полный список изменений: https://github.com/alexindacomp/Jiraffe/compare/v0.4.0...v0.5.0`;
  const upd = {
    current: updSaved.current || '0.4.0',
    skipped: updSaved.skipped || '',
    installing: false,
    failOnce: flags.has('update-fail') && !updSaved.failedOnce,
    justUpdated: updSaved.justUpdated || (flags.has('updated') ? { from: '0.3.2', to: updSaved.current || '0.4.0' } : null),
    found: !flags.has('update-oncheck'), // update-oncheck: the newer release is only found by a manual check
    lastCheck: flags.has('update-checkfail')
      ? { at: minutesAgo(40), ok: false, error: { ru: 'Не удалось связаться с GitHub: превышено время ожидания (20 с)', uk: 'Не вдалося зв’язатися з GitHub: перевищено час очікування (20 с)', en: 'Could not reach GitHub: timed out (20 s)' } }
      : { at: minutesAgo(12), ok: true },
  };
  if (updSaved.justUpdated) saveUpd({ justUpdated: null }); // shown once per "restart"
  const upd050 = { version: '0.5.0', notes: RELEASE_NOTES, publishedAt: daysFromNow(-2, 14), size: 4823449, url: 'https://github.com/alexindacomp/Jiraffe/releases/tag/v0.5.0' };
  const updLatest = () => ((UPD_NEWER && upd.found) || upd.current === '0.5.0' ? upd050
    : { version: upd.current, notes: 'Исправления и улучшения.', publishedAt: daysFromNow(-20, 12), size: 4700000, url: `https://github.com/alexindacomp/Jiraffe/releases/tag/v${upd.current}` });
  const semverGt = (a, b) => {
    const pa = String(a).replace(/^v/, '').split('.').map(Number); const pb = String(b).replace(/^v/, '').split('.').map(Number);
    for (let i = 0; i < 3; i++) if ((pa[i] || 0) !== (pb[i] || 0)) return (pa[i] || 0) > (pb[i] || 0);
    return false;
  };
  const SEMVER = /^v?\d+\.\d+\.\d+$/;
  const updSupported = () => !flags.has('devbuild');
  function updStatus() {
    const supported = updSupported();
    const latest = updLatest();
    const available = supported && semverGt(latest.version, upd.current);
    const out = { currentVersion: supported ? upd.current : 'dev', supported, available, prompt: available && latest.version !== upd.skipped, installing: upd.installing };
    if (supported) out.latest = clone(latest);
    if (upd.skipped) out.skippedVersion = upd.skipped;
    if (supported && upd.lastCheck) {
      out.lastCheck = { at: upd.lastCheck.at, ok: upd.lastCheck.ok };
      if (!upd.lastCheck.ok) out.lastCheck.error = upd.lastCheck.error[settings.language] || upd.lastCheck.error.ru;
    }
    if (upd.justUpdated) out.justUpdated = clone(upd.justUpdated);
    return out;
  }
  const UPD_MSG = {
    network: { ru: 'Не удалось скачать обновление: соединение прервано', uk: 'Не вдалося завантажити оновлення: з’єднання перервано', en: 'Could not download the update: the connection was interrupted' },
    no_write_access: { ru: 'Нет прав на запись в папку C:\\Program Files\\Jiraffe', uk: 'Немає прав на запис у папку C:\\Program Files\\Jiraffe', en: 'No write access to C:\\Program Files\\Jiraffe' },
    bad_signature: { ru: 'Подпись выпуска не прошла проверку — ничего не установлено', uk: 'Підпис випуску не пройшов перевірку — нічого не встановлено', en: 'The release signature is invalid — nothing was installed' },
  };
  function updFail(code) {
    upd.installing = false;
    emit('updateFailed', { code, message: UPD_MSG[code][settings.language] || UPD_MSG[code].ru });
  }
  /** Download in ~16 steps over ~3 s, then verify / install / restart; failures per flag. */
  function runInstall(latest) {
    const total = latest.size;
    const steps = 16;
    let i = 0;
    emit('updateProgress', { phase: 'download', received: 0, total });
    const tick = setInterval(() => {
      i++;
      const received = Math.min(total, Math.round((total * i) / steps));
      emit('updateProgress', { phase: 'download', received, total });
      if (upd.failOnce && i === 7) { clearInterval(tick); upd.failOnce = false; saveUpd({ failedOnce: true }); updFail('network'); return; }
      if (i < steps) return;
      clearInterval(tick);
      setTimeout(() => {
        emit('updateProgress', { phase: 'verify' });
        setTimeout(() => {
          if (flags.has('update-badsig')) { updFail('bad_signature'); return; }
          emit('updateProgress', { phase: 'install' });
          setTimeout(() => {
            if (flags.has('update-nowrite')) { updFail('no_write_access'); return; }
            emit('updateProgress', { phase: 'restart' });
            setTimeout(() => {
              saveUpd({ current: latest.version, justUpdated: { from: upd.current, to: latest.version }, skipped: '' });
              location.reload(); // the "new version" starts
            }, 1800);
          }, 900);
        }, 800);
      }, 250);
    }, 190);
  }

  /* ---------- v0.5 create / edit issues ----------
     HTML (JEditor) projects: RBI (Dev/QA story point selects + a required custom select "Среда"), EDS.
     Wiki projects: CMA (Bug has a required cascading field ⇒ unsupported, and requires "Шаги воспроизведения"),
     MB and the rest. RBI bugs have required number / datetime extras (RBI-1019: 1234, 0.25, a datetime with seconds);
     RBI-1028's wiki description and CMA bug steps come with CRLF line ends (an untouched edit must send nothing).
     Edit: HR-7 has no edit permission; EDS-230 is changed "in Jira" while you edit it (the first save
     gets edit_conflict, Reload works). "Jira" validation: a due date in the past, or "#jira" in the summary
     (field error + form-level errorMessages). */
  const HTML_PROJECTS = ['RBI', 'EDS'];
  const NO_EDIT = ['HR-7'];
  /* v0.6 issue panel: Developer / Reviewer / QA Engineer (multi-user, "неназначено" placeholder when empty) and
     Merge_requests / Branches (textarea, CRLF, the "Не заполнено" default) in RBI, EDS, MB and CMA (not epics).
     EDS: Reviewer is not on the edit screen (read-only in the panel). RBI-1028: several MRs / branches, a URL token
     with a trailing comma (linked without it, DR-37) and a "prefix:https://…" token (plain text). HR-7: no comment permission (forbidden_comment).
     EDS-230: the first save (form or inline) gets edit_conflict. */
  const DEV_PROJECTS = ['RBI', 'EDS', 'MB', 'CMA'];
  const DEV_IDS = { developer: 'customfield_10410', qa: 'customfield_10411', reviewer: 'customfield_10412', mr: 'customfield_13619', branches: 'customfield_13702' };
  const NO_COMMENT = ['HR-7'];
  const PLACEHOLDER = { name: 'jira.unassigned', displayName: 'неназначено' };
  const isPlaceholder = (u) => !!u && ['неназначено', 'не назначено', 'unassigned'].includes(String(u.displayName || u.name || '').trim().toLowerCase());
  const realUsers = (list) => (list || []).filter((u) => u && u.name && !isPlaceholder(u)).map((u) => ({ name: u.name, displayName: u.displayName || u.name }));
  const fieldText = (v) => { const x = String(v || '').replace(/\r\n?/g, '\n').trim(); return x.toLowerCase() === 'не заполнено' ? '' : x; };
  const hasDev = (r) => DEV_PROJECTS.includes(r.key.split('-')[0]) && r.type.kind !== 'epic';
  const DEV_DATA = {
    'RBI-1028': {
      developer: ['me', 'do'], reviewer: ['am'], qa: [],
      mr: 'https://gitlab.rozetka.company/marketplace/front/-/merge_requests/6224\r\nhttps://gitlab.rozetka.company/marketplace/api/-/merge_requests/311 — ревью у Андрея\r\nсм. https://gitlab.rozetka.company/warranty/warranty-service-api/-/merge_requests/57, там миграции\r\nprefix:https://gitlab.rozetka.company/x/y/-/merge_requests/1',
      branches: 'front: MF-8026/moderation-stages-chips-filters\r\nwarranty-service-api:WS-131\r\nhotfix/RBI-1028-cors',
    },
    'EDS-230': { developer: ['me'], reviewer: ['do'], qa: ['nb'], mr: 'https://gitlab.rozetka.company/eds/signing/-/merge_requests/88', branches: 'signing: EDS-230/sign-package\r\n' },
    'MB-77': { developer: ['me'], reviewer: ['sk'], qa: ['nb', 'is'], mr: 'Не заполнено', branches: 'mobile-ios: MB-77/push' },
  };
  function commentFormatOf(r) {
    const last = r.comments.slice(-10).filter((c) => String(c.body || '').trim());
    if (!last.length) return HTML_PROJECTS.includes(r.key.split('-')[0]) ? 'html' : 'wiki';
    return last.filter((c) => looksHtml(c.body)).length * 2 > last.length ? 'html' : 'wiki';
  }
  const CONFLICT_KEY = 'EDS-230';
  let conflictDone = false;
  let createBusy = false; // a duplicate createIssue while one is in flight ⇒ busy
  const PRIOS = [{ id: '1', name: 'Highest' }, { id: '2', name: 'High' }, { id: '3', name: 'Medium' }, { id: '4', name: 'Low' }, { id: '5', name: 'Lowest' }];
  const SP_OPTS = ['1', '2', '3', '5', '8', '13'].map((v, i) => ({ id: String(10400 + i), name: v }));
  const ENV_OPTS = [['20001', 'dev'], ['20002', 'test'], ['20003', 'preprod'], ['20004', 'prod']].map(([id, name]) => ({ id, name }));
  const COMPONENTS = { RBI: ['Export', 'Auth', 'Reports'], CMA: ['UI', 'API', 'Journal'], EDS: ['Signing', 'Archive'] };
  const VERSIONS = { RBI: ['4.2.0', '4.3.0'], CMA: ['2.0', '2.1'], MB: ['3.14', '3.15'] };
  const optList = (names, base) => (names || ['Core']).map((name, i) => ({ id: String(base + i), name }));
  const TYPE_IDS = { epic: '10000', story: '10001', task: '10002', subtask: '10003', bug: '10004' };
  const NAMES = {
    summary: 'Тема', description: 'Описание', assignee: 'Исполнитель', priority: 'Приоритет', epic: 'Epic Link', epicName: 'Epic Name',
    parent: 'Родительская задача', sprint: 'Спринт', sp: 'Story Points', devSp: 'Dev Story Points', qaSp: 'QA Story Points', labels: 'Метки',
    components: 'Компоненты', fixVersions: 'Исправить в версиях', duedate: 'Срок исполнения', timetracking: 'Учёт времени',
    env: 'Среда', region: 'Регион и филиал', steps: 'Шаги воспроизведения', reporter: 'Автор',
    budget: 'Бюджет, ч', risk: 'Коэффициент риска', start: 'Начало работ',
    developer: 'Developer', reviewer: 'Reviewer', qa: 'QA Engineer', mr: 'Merge_requests', branches: 'Branches',
  };
  /** v0.6 FieldMeta.role (create and edit meta; other fields have none). */
  const ROLES = { description: 'description', assignee: 'assignee', reporter: 'reporter', sp: 'storyPoints', devSp: 'devStoryPoints', qaSp: 'qaStoryPoints',
    developer: 'developer', reviewer: 'reviewer', qa: 'qa', mr: 'mergeRequests', branches: 'branches' };
  const looksHtml = (s) => /<\/?(?:p|div|br|span|b|strong|i|em|u|s|code|pre|ul|ol|li|a|img|table|tr|td|th|h[1-6]|hr|blockquote|font)\b[^>]*>/i
    .test(String(s || '').replace(/\{(code|noformat)(?::[^}\n]*)?\}[\s\S]*?\{\1\}|\{\{[^\n]*?\}\}/g, ''));
  const projectTypes = (project) => {
    const list = [['10002', 'Задача', 'task'], ['10001', 'История', 'story'], ['10004', 'Ошибка', 'task'], ['10000', 'Epic', 'epic'], ['10003', 'Подзадача', 'subtask']];
    return (project === 'CMA' ? list.filter((x) => x[0] !== '10000') : list).map(([id, name, kind]) => ({ id, name, kind, subtask: kind === 'subtask' }));
  };
  function sprintOpts(project) {
    const boards = new Set(boardsOf(project).map((b) => b.id));
    return SPRINTS.filter((sp) => sp.state !== 'closed' && boards.has(sp.boardId)).sort((a, b) => (a.state === 'active' ? 0 : 1) - (b.state === 'active' ? 0 : 1) || a.id - b.id)
      .map((sp) => ({ id: String(sp.id), name: sp.name, state: sp.state }));
  }
  /** FieldMeta list for a project / type (main fields in SPEC order, then the required extras). */
  function metaFields(project, typeId, edit) {
    const ty = projectTypes(project).find((x) => x.id === typeId) || projectTypes(project)[0];
    const kind = ty.kind;
    const html = HTML_PROJECTS.includes(project);
    const f = (key, id, k, extra) => Object.assign({ id, name: NAMES[key], kind: k, required: false, main: true }, ROLES[key] ? { role: ROLES[key] } : {}, extra || {});
    const out = [f('summary', 'summary', 'summary', { required: true, max: 255 }), f('description', 'description', 'description'),
      f('assignee', 'assignee', 'user'), f('priority', 'priority', 'priority', { options: PRIOS, default: { id: '3' } })];
    if (kind !== 'epic' && kind !== 'subtask') out.push(f('epic', 'customfield_10103', 'epic'));
    if (kind === 'epic') out.push(f('epicName', 'customfield_10105', 'epicName', { required: true, max: 255 }));
    if (kind === 'subtask') out.push(f('parent', 'parent', 'parent', { required: true }));
    const sprints = sprintOpts(project);
    if (kind !== 'epic' && kind !== 'subtask' && sprints.length) out.push(f('sprint', 'customfield_10107', 'sprint', { options: sprints })); // no Sprint for sub-tasks
    if (kind === 'story' || (kind === 'task' && project !== 'CMA')) out.push(f('sp', 'customfield_10130', 'storyPoints'));
    if (project === 'RBI' && kind !== 'epic') out.push(f('devSp', 'customfield_10204', 'storyPoints', { options: SP_OPTS }), f('qaSp', 'customfield_10205', 'storyPoints', { options: SP_OPTS }));
    out.push(f('labels', 'labels', 'labels'));
    if (kind !== 'epic') out.push(f('components', 'components', 'components', { options: optList(COMPONENTS[project], 11000) }));
    if (kind !== 'epic' && project !== 'EDS') out.push(f('fixVersions', 'fixVersions', 'versions', { options: optList(VERSIONS[project], 12000) }));
    out.push(f('duedate', 'duedate', 'date'));
    if (kind !== 'epic') out.push(f('timetracking', 'timetracking', 'estimate'));
    if (project === 'RBI' && kind !== 'epic') out.push(f('env', 'customfield_10300', 'select', { required: true, main: false, options: ENV_OPTS }));
    // RBI bugs: required number / datetime extras (edit RBI-1019 → Save without changes must send nothing: 1234, 0.25, seconds)
    if (project === 'RBI' && ty.id === '10004') out.push(f('budget', 'customfield_900', 'number', { required: true, main: false }), f('risk', 'customfield_902', 'number', { required: true, main: false }), f('start', 'customfield_903', 'datetime', { required: true, main: false }));
    if (project === 'CMA' && ty.id === '10004') out.push(f('region', 'customfield_10400', 'unsupported', { required: true, main: false }));
    if (project === 'CMA' && ty.id === '10004') out.push(f('steps', 'customfield_10500', 'textarea', { required: true, main: false }));
    if (project === 'CMA' && ty.id === '10001') out.push(f('reporter', 'reporter', 'user', { required: true, main: false, default: { name: ME.name, displayName: ME.displayName } }));
    if (edit) { // v0.6: the edit screen also has the reporter and, in DEV_PROJECTS, the development fields
      const extra = [];
      const rep0 = out.findIndex((x) => x.id === 'reporter');
      extra.push(rep0 >= 0 ? out.splice(rep0, 1)[0] : f('reporter', 'reporter', 'user', { required: true }));
      if (DEV_PROJECTS.includes(project) && kind !== 'epic') {
        extra.push(f('developer', DEV_IDS.developer, 'users'));
        if (project !== 'EDS') extra.push(f('reviewer', DEV_IDS.reviewer, 'users')); // EDS: read-only in the panel
        extra.push(f('qa', DEV_IDS.qa, 'users'), f('mr', DEV_IDS.mr, 'textarea', { max: 32000 }), f('branches', DEV_IDS.branches, 'textarea', { max: 32000 }));
      }
      const at = out.findIndex((x) => x.main === false);
      out.splice(at < 0 ? out.length : at, 0, ...extra);
    }
    return { type: ty, fields: out, descriptionFormat: html ? 'html' : 'wiki' };
  }
  const hpd = () => settings.hoursPerDay || 8;
  function durText(sec) {
    let m = Math.round(sec / 60); const d = Math.floor(m / (hpd() * 60)); m -= d * hpd() * 60; const h = Math.floor(m / 60); m -= h * 60;
    return [d ? d + 'd' : '', h ? h + 'h' : '', m ? m + 'm' : ''].filter(Boolean).join(' ') || '0m';
  }
  function durSeconds(text) {
    let sec = 0; const re = /(\d+(?:[.,]\d+)?)\s*([wdhm])/gi; let m;
    while ((m = re.exec(String(text || '')))) { const n = Number(m[1].replace(',', '.')); sec += n * { w: 5 * hpd() * 3600, d: hpd() * 3600, h: 3600, m: 60 }[m[2].toLowerCase()]; }
    return Math.round(sec);
  }
  const typeIdOf = (r) => (r.type === TYPES.epic ? '10000' : r.type === TYPES.story ? '10001' : r.type === TYPES.sub ? '10003' : r.type === TYPES.bug ? '10004' : '10002');
  const descOf = (r) => (Object.prototype.hasOwnProperty.call(DESC, r.key) ? DESC[r.key] : r.type.kind === 'epic' ? '' : `Задача *${r.key}*: ${r.summary}.`);
  function editValues(r, fields) {
    const v = {};
    const comps = optList(COMPONENTS[projOf(r)], 11000); const vers = optList(VERSIONS[projOf(r)], 12000);
    fields.forEach((f) => {
      switch (f.id) {
        case 'summary': v[f.id] = r.summary; break;
        case 'description': v[f.id] = descOf(r); break;
        case 'assignee': v[f.id] = r.assignee ? { name: r.assignee.name, displayName: r.assignee.displayName } : null; break;
        case 'priority': { const p = PRIOS.find((x) => x.name === r.priority.name); v[f.id] = p ? { id: p.id } : null; break; }
        case 'customfield_10103': v[f.id] = r.epic ? { key: r.epic, summary: (rec(r.epic) || {}).summary || '' } : null; break;
        case 'customfield_10105': v[f.id] = r.epicName || null; break;
        case 'parent': v[f.id] = r.parent ? { key: r.parent, summary: (rec(r.parent) || {}).summary || '' } : null; break;
        case 'customfield_10107': { const open = SPRINTS.find((sp) => sp.state !== 'closed' && r.sprints.includes(sp.id)); v[f.id] = open ? open.id : null; break; }
        case 'customfield_10130': v[f.id] = r.storyPoints != null ? r.storyPoints : null; break;
        case 'customfield_10204': { const o = SP_OPTS.find((x) => Number(x.name) === r.devStoryPoints); v[f.id] = o ? { id: o.id } : null; break; }
        case 'customfield_10205': { const o = SP_OPTS.find((x) => Number(x.name) === r.qaStoryPoints); v[f.id] = o ? { id: o.id } : null; break; }
        case 'labels': v[f.id] = r.labels.slice(); break;
        case 'components': v[f.id] = r.components.map((n) => (comps.find((c) => c.name === n) || {}).id).filter(Boolean); break;
        case 'fixVersions': v[f.id] = r.fixVersions.map((n) => (vers.find((c) => c.name === n) || {}).id).filter(Boolean); break;
        case 'duedate': v[f.id] = r.dueDate || null; break;
        case 'timetracking': v[f.id] = r.est ? { originalEstimate: durText(r.est[0] * H), remainingEstimate: durText(r.est[1] * H) } : null; break;
        case 'customfield_10300': v[f.id] = { id: r.env || '20001' }; break; // a required field: Jira has a value
        case 'customfield_10500': v[f.id] = r.steps != null ? r.steps : 'Открыть журнал\r\nНажать «Экспорт»\r\nОшибка 500'; break; // Jira Server: CRLF
        case 'customfield_900': v[f.id] = r.budget != null ? r.budget : 1234; break;
        case 'customfield_902': v[f.id] = r.risk != null ? r.risk : 0.25; break;
        case 'customfield_903': v[f.id] = r.start || '2026-11-20T09:30:15+02:00'; break;
        case 'reporter': v[f.id] = r.reporter ? { name: r.reporter.name, displayName: r.reporter.displayName } : null; break;
        case DEV_IDS.developer: v[f.id] = realUsers(r.developer); break;
        case DEV_IDS.reviewer: v[f.id] = realUsers(r.reviewer); break;
        case DEV_IDS.qa: v[f.id] = realUsers(r.qa); break;
        case DEV_IDS.mr: v[f.id] = fieldText(r.mr); break;
        case DEV_IDS.branches: v[f.id] = fieldText(r.branches); break;
        default:
      }
    });
    return v;
  }
  const msg = (ru, uk, en) => ({ ru, uk, en }[settings.language] || ru);
  /** Our checks + "Jira's" 400 errors; returns [{field, message}]. */
  function checkValues(fields, values, creating) {
    const errs = [];
    const extra = []; // Jira's errorMessages: appended to the message, not field errors
    Object.keys(values).forEach((id) => { if (!fields.some((f) => f.id === id)) throw new MockError('bad_params', `unknown field ${id}`); });
    fields.forEach((f) => {
      const v = values[f.id];
      const present = Object.prototype.hasOwnProperty.call(values, f.id);
      const empty = v == null || v === '' || (Array.isArray(v) && !v.length);
      if (f.required && f.kind !== 'unsupported' && empty && (creating || present)) errs.push({ field: f.id, message: msg(`Поле «${f.name}» обязательно.`, `Поле «${f.name}» обовʼязкове.`, `${f.name} is required.`) });
      if (f.kind === 'labels' && Array.isArray(v) && v.some((x) => /\s/.test(x))) errs.push({ field: f.id, message: msg('Метки не могут содержать пробелы.', 'Мітки не можуть містити пробіли.', 'Labels cannot contain spaces.') });
      if (f.kind === 'date' && v && v < isoDay(new Date())) errs.push({ field: f.id, message: msg('Jira: срок исполнения не может быть в прошлом.', 'Jira: термін виконання не може бути в минулому.', 'Jira: the due date cannot be in the past.') });
      if (f.kind === 'summary' && typeof v === 'string' && v.includes('#jira')) {
        errs.push({ field: 'summary', message: msg('Jira: тема содержит недопустимый символ «#».', 'Jira: тема містить недопустимий символ «#».', 'Jira: the summary contains an invalid character "#".') });
        extra.push(msg('Jira: валидатор бизнес-процесса отклонил задачу.', 'Jira: валідатор бізнес-процесу відхилив задачу.', 'Jira: the workflow validator rejected the issue.'));
      }
      if (f.kind === 'unsupported' && creating) errs.push({ field: f.id, message: msg('Это поле можно заполнить только в Jira.', 'Це поле можна заповнити лише в Jira.', 'This field can only be filled in Jira.') });
    });
    if (errs.length || extra.length) {
      const base = msg('Некоторые поля заполнены неверно', 'Деякі поля заповнено неправильно', 'Some fields are invalid');
      throw new MockError('validation', extra.length ? `${base}: ${extra.join('; ')}` : base, errs);
    }
  }
  /** Writes FieldValues into a mock record (the backend maps them to Jira JSON). */
  function applyValues(r, fields, values) {
    const project = projOf(r);
    const comps = optList(COMPONENTS[project], 11000); const vers = optList(VERSIONS[project], 12000);
    const items = [];
    Object.entries(values).forEach(([id, v]) => {
      const f = fields.find((x) => x.id === id);
      switch (id) {
        case 'summary': r.summary = String(v || ''); break;
        case 'description': DESC[r.key] = String(v || ''); break;
        case 'assignee': r.assignee = v ? Object.values(U).find((u) => u.name === v.name) || { name: v.name, displayName: v.displayName || v.name } : null; break;
        case 'priority': r.priority = { name: (PRIOS.find((p) => p.id === (v && v.id)) || PRIOS[2]).name }; break;
        case 'customfield_10103': r.epic = v ? v.key : null; break;
        case 'customfield_10105': r.epicName = v || r.summary; break;
        case 'parent': r.parent = v ? v.key : null; break;
        case 'customfield_10107': r.sprints = r.sprints.filter((sid) => !SPRINTS.some((sp) => sp.id === sid && sp.state !== 'closed')).concat(v != null ? [Number(v)] : []); break;
        case 'customfield_10130': r.storyPoints = v == null ? undefined : Number(v); break;
        case 'customfield_10204': r.devStoryPoints = v ? Number((SP_OPTS.find((o) => o.id === v.id) || {}).name) : undefined; break;
        case 'customfield_10205': r.qaStoryPoints = v ? Number((SP_OPTS.find((o) => o.id === v.id) || {}).name) : undefined; break;
        case 'labels': r.labels = (v || []).slice(); break;
        case 'components': r.components = (v || []).map((x) => (comps.find((c) => c.id === String(x)) || {}).name).filter(Boolean); break;
        case 'fixVersions': r.fixVersions = (v || []).map((x) => (vers.find((c) => c.id === String(x)) || {}).name).filter(Boolean); break;
        case 'duedate': r.dueDate = v || undefined; break;
        case 'timetracking': {
          const spent = r.est ? r.est[2] : 0;
          r.est = v ? [durSeconds(v.originalEstimate) / H, v.remainingEstimate != null ? durSeconds(v.remainingEstimate) / H : durSeconds(v.originalEstimate) / H, spent] : null;
          break;
        }
        case 'customfield_10300': r.env = v ? v.id : null; break;
        case 'customfield_10500': r.steps = v || ''; break;
        case 'customfield_900': r.budget = v == null ? null : Number(v); break;
        case 'customfield_902': r.risk = v == null ? null : Number(v); break;
        case 'customfield_903': r.start = v || null; break;
        case 'reporter': r.reporter = v ? Object.values(U).find((u) => u.name === (v.name || v)) || r.reporter : r.reporter; break;
        // v0.6: an empty people field gets Jira's placeholder back; an empty text field its default
        case DEV_IDS.developer: case DEV_IDS.reviewer: case DEV_IDS.qa: {
          const list = (v || []).map((u) => Object.values(U).find((x) => x.name === u.name) || { name: u.name, displayName: u.displayName || u.name });
          r[{ [DEV_IDS.developer]: 'developer', [DEV_IDS.reviewer]: 'reviewer', [DEV_IDS.qa]: 'qa' }[id]] = list.length ? list : [PLACEHOLDER];
          break;
        }
        case DEV_IDS.mr: r.mr = v ? String(v).replace(/\n/g, '\r\n') : 'Не заполнено'; break;
        case DEV_IDS.branches: r.branches = v ? String(v).replace(/\n/g, '\r\n') : 'Не заполнено'; break;
        default:
      }
      items.push({ field: f ? f.name : id, from: '', to: typeof v === 'string' ? v.slice(0, 60) : Array.isArray(v) ? v.map((x) => (typeof x === 'object' ? x.name || x.key || x.id : x)).join(', ') : v && typeof v === 'object' ? v.displayName || v.key || v.name || (PRIOS.find((p) => p.id === v.id) || {}).name || v.id || '' : String(v == null ? '' : v) });
    });
    return items;
  }

  /* ---------- methods ---------- */
  class MockError { constructor(code, message, fields) { this.code = code; this.message = message; if (fields) this.fields = fields; } }
  let boardCalls = 0;
  const maybeFail = (rate, code, message) => { if (Math.random() < rate) throw new MockError(code, message); };
  const requireConfigured = () => { if (!configured()) throw new MockError('not_configured', 'Jira is not configured'); };
  const scopeOf = (p, def) => (p && (p.scope === 'mine' || p.scope === 'all' || p.scope === 'reported') ? p.scope : def);
  const reportedByMe = (r) => !!r.reporter && r.reporter.name === ME.name; // scope "reported"
  /** v0.3 `project` param of getEpics / getStories: that project instead of the tracked ones (children are not filtered). */
  function projectFilter(p) {
    if (!p || !p.project) return (r) => isTracked(r.key);
    if (!PROJECTS.some((x) => x.key === p.project)) throw new MockError('project_unavailable', `project ${p.project} is not accessible`);
    return (r) => projOf(r) === p.project;
  }

  function board(scope) {
    const open = all().filter((r) => !isDone(r) && r.type.kind !== 'epic');
    let list = open.filter((r) => isTracked(r.key));
    if (scope === 'mine') list = list.filter(mine);
    if (scope === 'reported') list = list.filter(reportedByMe);
    const truncated = flags.has('truncated') && scope === 'all';
    if (truncated) list = list.slice(0, 20);
    const out = { columns: clone(columnsOf(list)), fetchedAt: rfc(now()), scope, truncated };
    if (scope === 'mine') { // my issues dropped by the tracked-projects filter (OPS-12/14, DWH-8 by default)
      const hidden = open.filter((r) => mine(r) && !isTracked(r.key));
      out.hidden = { count: hidden.length, projects: [...new Set(hidden.map((r) => r.key.split('-')[0]))].sort() };
    }
    return out;
  }

  function getProjectsList() {
    const myOpen = (k) => all().filter((r) => r.key.startsWith(k + '-') && mine(r) && !isDone(r)).length;
    const known = PROJECTS.map((p) => ({ key: p.key, name: p.name, category: p.category, lead: p.lead, tracked: isTracked(p.key + '-1'), myOpen: myOpen(p.key), available: true }));
    const stale = (tracked || []).filter((k) => !PROJECTS.some((x) => x.key === k)).map((k) => ({ key: k, name: k, tracked: true, myOpen: 0, available: false }));
    return [...known, ...stale].sort((a, b) => (b.tracked - a.tracked) || a.name.localeCompare(b.name));
  }

  function issueDetail(key) {
    const r = rec(key);
    if (!r) throw new MockError('not_found', `issue ${key} not found`);
    const kind = r.type.kind;
    let children = [];
    if (kind === 'epic') children = all().filter((x) => x.epic === key).sort((a, b) => a.key.localeCompare(b.key)).map((x) => toRef(x));
    else if (kind === 'story') children = storyNode(r).tasks;
    else children = all().filter((x) => x.parent === key).map((x) => toRef(x, false));
    const stories = kind === 'task' || kind === 'subtask' ? linksOf(key).map((l) => rec(l.other)).filter((x) => x.type.kind === 'story').map((x) => toRef(x, false)) : [];
    const tt = {};
    if (r.est) {
      const [o, rem, sp] = r.est.map((h) => Math.round(h * H));
      Object.assign(tt, { originalSeconds: o, remainingSeconds: rem, spentSeconds: sp, original: fmt(o), remaining: fmt(rem), spent: fmt(sp) });
    }
    const det = {
      issue: toIssue(r), description: descOf(r),
      reporter: r.reporter, creator: r.reporter, created: r.created, updated: r.updated,
      labels: r.labels, components: r.components, fixVersions: r.fixVersions, priority: r.priority,
      timeTracking: tt, sprints: SPRINTS.filter((s) => r.sprints.includes(s.id)),
      stories, children, links: linksOf(key).map((l) => ({ id: l.id, relation: l.relation, issue: toRef(rec(l.other), false) })),
      comments: r.comments, changelog: r.changelog, watchers: r.watchers,
    };
    if (r.dueDate) det.dueDate = r.dueDate;
    if (isDone(r)) det.resolution = 'Готово';
    if (r.storyPoints != null) det.storyPoints = r.storyPoints;
    if (r.devStoryPoints != null) det.devStoryPoints = r.devStoryPoints;
    if (r.qaStoryPoints != null) det.qaStoryPoints = r.qaStoryPoints;
    if (r.epic) det.epic = toRef(rec(r.epic));
    if (r.parent) det.parent = toRef(rec(r.parent), false);
    if (hasDev(r)) {
      devInit(r);
      Object.assign(det, { developer: realUsers(r.developer), reviewer: realUsers(r.reviewer), qa: realUsers(r.qa), mergeRequests: fieldText(r.mr), branches: fieldText(r.branches) });
    }
    det.commentFormat = commentFormatOf(r);
    return det;
  }

  /** v0.6 development fields of a record, filled on first use (placeholders and defaults like Jira's). */
  function devInit(r) {
    if (r.mr !== undefined) return;
    const x = DEV_DATA[r.key] || {};
    const users = (list, def) => (list ? list.map((k) => U[k]) : def);
    r.developer = users(x.developer, r.assignee ? [r.assignee] : [PLACEHOLDER]);
    if (!r.developer.length) r.developer = [PLACEHOLDER];
    r.reviewer = users(x.reviewer, [PLACEHOLDER]);
    if (!r.reviewer.length) r.reviewer = [PLACEHOLDER];
    r.qa = users(x.qa, [PLACEHOLDER]);
    if (!r.qa.length) r.qa = [PLACEHOLDER];
    r.mr = x.mr != null ? x.mr : 'Не заполнено';
    r.branches = x.branches != null ? x.branches : 'Не заполнено';
  }

  const METHODS = {
    getState: () => state(),
    getSettings: () => Object.assign(clone(settings), { hasToken: !!token, trackedProjects: clone(tracked) }),
    testConnection(p) {
      const u = (p && p.jiraUrl) || '';
      if (!/^https?:\/\/[^\s/]+/i.test(u)) return { ok: false, error: 'invalid URL' };
      const tk = (p && p.token) || token;
      if (!tk) return { ok: false, error: '401 Unauthorized' };
      if (/bad|wrong/i.test(tk)) return { ok: false, error: '401 Unauthorized: token is invalid or expired' };
      if (!/sd\.local/i.test(u)) return { ok: false, error: `WinHTTP 12007: the server name could not be resolved (${u.replace(/^https?:\/\//, '')})` };
      return { ok: true, displayName: ME.displayName };
    },
    saveSettings(p) {
      const next = Object.assign(clone(settings), p || {});
      delete next.token; delete next.hasToken; delete next.trackedProjects; // ignored, like the Go side
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
    getBoard(p) {
      requireConfigured();
      if (boardCalls++ > 0) maybeFail(ERROR_RATE, 'network', 'WinHTTP 12002: the operation timed out');
      const scope = scopeOf(p, 'mine');
      if (tracked === null && scope === 'mine') initTracked(); // "on first need", like the Go side
      if (p && p.boardId != null && !(Number(p.boardId) >= 0)) throw new MockError('bad_params', 'boardId must be ≥ 0');
      return p && p.project ? projectBoard(scope, String(p.project), p.boardId) : board(scope);
    },
    getTransitions(p) {
      requireConfigured();
      const is = rec(p.key);
      if (!is) throw new MockError('not_found', `issue ${p.key} not found`);
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12029: cannot connect');
      return flowOf(is).map(([id, name, to]) => ({ id, name, to: clone(ST[to]) }));
    },
    transition(p) {
      requireConfigured();
      const is = rec(p.key);
      if (!is) throw new MockError('not_found', `issue ${p.key} not found`);
      const tr = flowOf(is).find(([id]) => id === String(p.transitionId));
      if (!tr) throw new MockError('jira', `Jira 400: transition ${p.transitionId} is not valid for ${p.key}`);
      maybeFail(ERROR_RATE * 1.8, 'jira', 'Jira 400: Field "Resolution" is required by the workflow validator');
      is.changelog.unshift({ id: String(commentSeq++), author: ME, created: rfc(now()), items: [{ field: 'status', from: is.status.name, to: ST[tr[2]].name }] });
      is.status = ST[tr[2]];
      is.updated = rfc(now());
      return clone(toIssue(is));
    },
    getIssue(p) {
      requireConfigured();
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12002: the operation timed out');
      return clone(issueDetail(p && p.key));
    },
    getProjects() {
      requireConfigured();
      if (tracked === null) initTracked();
      return clone(getProjectsList());
    },
    setProjectTracked(p) {
      requireConfigured();
      const key = p && p.key;
      const knownKey = PROJECTS.some((x) => x.key === key);
      if (typeof p.tracked !== 'boolean' || (p.tracked && !knownKey) || (!p.tracked && !knownKey && !(tracked || []).includes(key))) throw new MockError('bad_params', `unknown project ${key}`);
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12029: cannot connect');
      const set = new Set(tracked || []);
      if (p.tracked) set.add(key); else set.delete(key);
      if (flags.has('limit') && set.size > 6) {
        const lim = { ru: 'Можно отслеживать не больше 6 проектов', uk: 'Можна відстежувати не більше 6 проєктів', en: 'You can track at most 6 projects' };
        throw new MockError('validation', lim[settings.language] || lim.ru, [{ field: 'trackedProjects', message: lim[settings.language] || lim.ru }]);
      }
      tracked = [...PROJECTS.map((x) => x.key).filter((k) => set.has(k)), ...[...set].filter((k) => !PROJECTS.some((x) => x.key === k))];
      setTimeout(() => emit('board', {}), 30);
      return clone(getProjectsList());
    },
    getAgileBoards() { // not used by the v0.3 UI; kept as in the contract
      requireConfigured();
      if (flags.has('noagile')) return [];
      return clone(AGILE.filter((b) => b.projects.some((k) => isTracked(k + '-1'))).map(agileShape));
    },
    getSprints(p) { // p.force is accepted (the Go side caches sprints for 10 minutes)
      requireConfigured();
      const b = AGILE.find((x) => String(x.id) === String(p && p.boardId));
      if (!b) throw new MockError('not_found', 'board not found');
      if (b.type === 'kanban') return [];
      const list = SPRINTS.filter((s) => s.boardId === b.id);
      const closed = list.filter((s) => s.state === 'closed').reverse().slice(0, 5);
      return clone([...list.filter((s) => s.state === 'active'), ...list.filter((s) => s.state === 'future'), ...closed]);
    },
    getSprintGroups(p) {
      requireConfigured();
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12002: the operation timed out');
      if (tracked === null) initTracked();
      return sprintGroups(p || {});
    },
    getEpics(p) {
      requireConfigured();
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12002: the operation timed out');
      const scope = scopeOf(p, 'all');
      const inProject = projectFilter(p);
      const epics = all().filter((r) => r.type.kind === 'epic' && inProject(r) && (p.includeDone || !isDone(r))).sort(treeSort)
        .map(epicNode).filter((n) => keepScope(n, scope)).map(strip);
      const orphanStories = all().filter((r) => r.type.kind === 'story' && !r.epic && inProject(r) && (p.includeDone || !isDone(r))).sort(treeSort)
        .map(storyNode).filter((n) => keepScope(n, scope)).map(strip);
      const out = { epics, orphanStories, fetchedAt: rfc(now()), truncated: flags.has('truncated') };
      if (flags.has('timepartial')) out.timePartial = true;
      return clone(out);
    },
    getStories(p) {
      requireConfigured();
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12002: the operation timed out');
      const scope = scopeOf(p, 'all');
      const inProject = projectFilter(p);
      const stories = all().filter((r) => r.type.kind === 'story' && inProject(r) && (p.includeDone || !isDone(r))).sort(treeSort)
        .map(storyNode).filter((n) => keepScope(n, scope)).map(strip);
      return clone({ stories, fetchedAt: rfc(now()), truncated: flags.has('truncated') });
    },
    getUpdateStatus: () => updStatus(),
    checkForUpdates() {
      if (!updSupported()) throw new MockError('update_unsupported', 'updates are not supported in this build');
      upd.lastCheck = flags.has('update-checkfail') ? Object.assign({}, upd.lastCheck, { at: rfc(now()) }) : { at: rfc(now()), ok: true };
      upd.found = true;
      const st = updStatus();
      if (st.prompt) emit('updateAvailable', st.latest); // like Go: the event comes before the method returns
      return st;
    },
    installUpdate(p) {
      const v = String((p && p.version) || '').replace(/^v/, '');
      if (!SEMVER.test(v)) throw new MockError('bad_params', 'version must be X.Y.Z');
      if (!updSupported()) throw new MockError('update_unsupported', 'updates are not supported in this build');
      const latest = updLatest();
      if (v !== latest.version || !semverGt(v, upd.current)) throw new MockError('update_stale', 'not the latest checked version');
      if (upd.installing) throw new MockError('update_busy', 'an update is already being installed');
      upd.installing = true;
      setTimeout(() => runInstall(latest), 60);
      return null;
    },
    skipUpdate(p) {
      const v = String((p && p.version) || '').replace(/^v/, '');
      if (!SEMVER.test(v)) throw new MockError('bad_params', 'version must be X.Y.Z');
      upd.skipped = v;
      saveUpd({ skipped: v });
      return updStatus();
    },
    getCreateMeta(p) {
      requireConfigured();
      const project = p && p.project;
      const pr = PROJECTS.find((x) => x.key === project);
      if (!pr) throw new MockError('project_unavailable', `project ${project} is not accessible`);
      const types = projectTypes(project);
      const typeId = types.some((x) => x.id === String(p.issueTypeId)) ? String(p.issueTypeId) : types.find((x) => !x.subtask).id;
      const m = metaFields(project, typeId);
      return clone({ project: { key: pr.key, name: pr.name }, issueTypes: types, issueTypeId: typeId, fields: m.fields, descriptionFormat: m.descriptionFormat });
    },
    getEditMeta(p) {
      requireConfigured();
      const r = rec(p && p.key);
      if (!r) throw new MockError('not_found', `issue ${p && p.key} not found`);
      const project = projOf(r);
      const pr = PROJECTS.find((x) => x.key === project) || { key: project, name: project };
      const m = metaFields(project, typeIdOf(r), true);
      if (hasDev(r)) devInit(r);
      const fields = NO_EDIT.includes(r.key) ? [] : m.fields;
      const desc = descOf(r);
      const format = looksHtml(desc) ? 'html' : desc.trim() ? 'wiki' : m.descriptionFormat;
      return clone({ key: r.key, issueTypeId: typeIdOf(r), project: { key: pr.key, name: pr.name }, updated: r.updated, fields, descriptionFormat: format, values: fields.length ? editValues(r, fields) : {} });
    },
    searchAssignable(p) {
      requireConfigured();
      const q = String((p && p.query) || '').trim().toLowerCase();
      return clone(Object.values(U).filter((u) => !q || u.name.toLowerCase().includes(q) || u.displayName.toLowerCase().includes(q)).slice(0, 20));
    },
    searchIssuesFor(p) {
      requireConfigured();
      const purpose = p && p.purpose;
      if (purpose !== 'epic' && purpose !== 'parent' && purpose !== 'link') throw new MockError('bad_params', 'purpose must be epic, parent or link');
      const q = String((p && p.query) || '').trim().toLowerCase();
      if (purpose === 'link') {
        return clone(all().filter((r) => !q || r.key.toLowerCase() === q || r.key.toLowerCase().startsWith(q) || r.summary.toLowerCase().includes(q))
          .sort((a, b) => b.updated.localeCompare(a.updated)).slice(0, 20).map((r) => toRef(r)));
      }
      return clone(all().filter((r) => projOf(r) === p.project && !isDone(r) && (purpose === 'epic' ? r.type.kind === 'epic' : r.type.kind !== 'subtask'))
        .filter((r) => !q || r.key.toLowerCase() === q || r.key.toLowerCase().startsWith(q) || r.summary.toLowerCase().includes(q) || String(r.epicName || '').toLowerCase().includes(q))
        .sort((a, b) => b.key.localeCompare(a.key, undefined, { numeric: true })).slice(0, 20).map((r) => toRef(r)));
    },
    createIssue(p) {
      requireConfigured();
      const project = p && p.project;
      if (!PROJECTS.some((x) => x.key === project)) throw new MockError('project_unavailable', `project ${project} is not accessible`);
      const type = projectTypes(project).find((x) => x.id === String(p.issueTypeId));
      if (!type) throw new MockError('bad_params', 'unknown issue type');
      const m = metaFields(project, type.id);
      const values = (p && p.values) || {};
      if (createBusy) throw new MockError('busy', msg('Задача уже создаётся — подождите.', 'Задача вже створюється — зачекайте.', 'An issue is already being created — please wait.'));
      createBusy = true;
      setTimeout(() => { createBusy = false; }, 400);
      checkValues(m.fields, values, true);
      const n = Math.max(0, ...all().filter((r) => projOf(r) === project).map((r) => Number(r.key.split('-')[1]) || 0)) + 1;
      const key = `${project}-${n}`;
      const kindKey = { 10000: 'epic', 10001: 'story', 10003: 'sub', 10004: 'bug' }[type.id] || 'task';
      const st = statusesOf(project).includes('todo') ? 'todo' : statusesOf(project)[0];
      const r = { id: String(30000 + n), rank: recs.size, key, summary: '', status: ST[st], type: TYPES[kindKey], priority: { name: 'Medium' }, assignee: null,
        updated: rfc(now()), created: rfc(now()), reporter: ME, epic: null, epicName: undefined, epicColor: kindKey === 'epic' ? 'ghx-label-12' : undefined,
        storyPoints: undefined, devStoryPoints: undefined, qaStoryPoints: undefined, parent: null, sprints: [], labels: [], components: [], fixVersions: [],
        est: null, dueDate: undefined, watchers: 1, comments: [], changelog: [] };
      DESC[key] = '';
      applyValues(r, m.fields, values);
      if (kindKey === 'epic' && !r.epicName) r.epicName = r.summary;
      recs.set(key, r);
      setTimeout(() => emit('board', {}), 30);
      return { key, url: url(key) };
    },
    updateIssue(p) {
      requireConfigured();
      const r = rec(p && p.key);
      if (!r) throw new MockError('not_found', `issue ${p && p.key} not found`);
      if (NO_EDIT.includes(r.key)) throw new MockError('forbidden_edit', msg('Нет прав на редактирование этой задачи.', 'Немає прав на редагування цієї задачі.', 'You do not have permission to edit this issue.'));
      if (r.key === CONFLICT_KEY && !conflictDone) {
        // someone else edits the issue while the form is open
        conflictDone = true;
        r.summary += ' (уточнено)';
        r.priority = { name: 'Highest' };
        r.storyPoints = 5; // also changed in Jira: a number the user may have typed over (conflict Reload keeps the typed value)
        r.updated = rfc(new Date(Date.now() + 1000));
        r.changelog.unshift({ id: String(commentSeq++), author: U.am, created: r.updated, items: [{ field: 'summary', from: '', to: r.summary }, { field: 'priority', from: 'High', to: 'Highest' }] });
      }
      if (String((p && p.expectedUpdated) || '') !== r.updated) throw new MockError('edit_conflict', msg('Задачу изменили в Jira после того, как вы её открыли.', 'Задачу змінили в Jira після того, як ви її відкрили.', 'The issue was changed in Jira after you opened it.'));
      const m = metaFields(projOf(r), typeIdOf(r), true);
      const values = (p && p.values) || {};
      if (hasDev(r)) devInit(r);
      checkValues(m.fields, values, false);
      const items = applyValues(r, m.fields, values);
      const t0 = new Date(Math.max(Date.now(), new Date(r.updated).getTime() + 1000));
      r.updated = rfc(t0);
      if (items.length) r.changelog.unshift({ id: String(commentSeq++), author: ME, created: r.updated, items: items.map((i) => ({ field: i.field, from: '', to: i.to })) });
      setTimeout(() => emit('board', {}), 30);
      const out = { key: r.key, updated: r.updated };
      // MB: the fields are saved, but the agile sprint move "fails" (contract: warning, still a success)
      if (projOf(r) === 'MB' && Object.prototype.hasOwnProperty.call(values, 'customfield_10107')) out.warning = msg('Спринт не изменён: доска BI DEV Agile board недоступна.', 'Спринт не змінено: дошка BI DEV Agile board недоступна.', 'Sprint not changed: the BI DEV Agile board is not available.');
      return out;
    },
    /* --- v0.6 issue panel --- */
    searchUsers(p) {
      requireConfigured();
      const q = String((p && p.query) || '').trim().toLowerCase();
      if (!q) return [];
      return clone(Object.values(U).filter((u) => u.name.toLowerCase().includes(q) || u.displayName.toLowerCase().includes(q)).slice(0, 20));
    },
    openFieldLink(p) {
      requireConfigured();
      const r = rec(p && p.key);
      if (!r) throw new MockError('not_found', `issue ${p && p.key} not found`);
      if (hasDev(r)) devInit(r);
      const field = p.field === 'mergeRequests' ? r.mr : p.field === 'branches' ? r.branches : null;
      const u = String((p && p.url) || '');
      // a whole token, or a token without trailing .,;:) (DR-37)
      const ok = field != null && /^https?:\/\/[^\s/@]+(\/\S*)?$/i.test(u) && String(field).split(/\s+/).some((tok) => tok === u || tok.replace(/[.,;:)]+$/, '') === u);
      if (!ok) throw new MockError('bad_params', 'the URL is not in the field');
      console.info('[jiraffe mock] openFieldLink →', p.key, p.field, u); // eslint-disable-line no-console
      return null;
    },
    getLinkTypes() {
      requireConfigured();
      return Object.entries(LT).map(([k, [name, outward, inward]]) => ({ id: LT_IDS[k], name, inward, outward }));
    },
    addIssueLink(p) {
      requireConfigured();
      const r = rec(p && p.key);
      if (!r) throw new MockError('not_found', `issue ${p && p.key} not found`);
      const type = Object.keys(LT_IDS).find((k) => LT_IDS[k] === String(p.typeId));
      if (!type || (p.direction !== 'outward' && p.direction !== 'inward')) throw new MockError('bad_params', 'typeId / direction');
      if (String(p.target || '').toUpperCase() === r.key) throw new MockError('bad_params', 'target must differ from key');
      const target = rec(String(p.target || '').toUpperCase());
      if (!target) throw new MockError('not_found', `issue ${p.target} not found`);
      if (NO_EDIT.includes(r.key)) throw new MockError('forbidden_edit', msg('Нет прав на связывание этой задачи.', 'Немає прав на зв’язування цієї задачі.', 'You do not have permission to link this issue.'));
      LINKS.push(p.direction === 'outward' ? [r.key, type, target.key, String(linkSeq++)] : [target.key, type, r.key, String(linkSeq++)]);
      r.updated = rfc(new Date(Math.max(Date.now(), new Date(r.updated).getTime() + 1000)));
      setTimeout(() => emit('board', {}), 30);
      return null;
    },
    deleteIssueLink(p) {
      requireConfigured();
      const r = rec(p && p.key);
      if (!r) throw new MockError('not_found', `issue ${p && p.key} not found`);
      const i = LINKS.findIndex((l) => l[3] === String(p.linkId) && (l[0] === r.key || l[2] === r.key));
      if (i < 0) throw new MockError('bad_params', 'the link does not belong to the issue');
      if (NO_EDIT.includes(r.key)) throw new MockError('forbidden_edit', msg('Нет прав на изменение связей этой задачи.', 'Немає прав на зміну зв’язків цієї задачі.', 'You do not have permission to change links of this issue.'));
      LINKS.splice(i, 1);
      r.updated = rfc(new Date(Math.max(Date.now(), new Date(r.updated).getTime() + 1000)));
      setTimeout(() => emit('board', {}), 30);
      return null;
    },
    addComment(p) {
      requireConfigured();
      const r = rec(p && p.key);
      if (!r) throw new MockError('not_found', `issue ${p && p.key} not found`);
      if (p.format !== commentFormatOf(r)) throw new MockError('bad_params', `format must be ${commentFormatOf(r)}`);
      const body = String(p.body || '').trim();
      if (!body) throw new MockError('validation', msg('Комментарий пуст.', 'Коментар порожній.', 'The comment is empty.'), [{ field: 'body', message: msg('Напишите текст комментария.', 'Напишіть текст коментаря.', 'Write the comment text.') }]);
      if (body.length > 32000) throw new MockError('validation', msg('Комментарий слишком длинный.', 'Коментар задовгий.', 'The comment is too long.'), []);
      if (NO_COMMENT.includes(r.key)) throw new MockError('forbidden_comment', msg('Нет прав на комментирование этой задачи.', 'Немає прав на коментування цієї задачі.', 'You do not have permission to comment on this issue.'));
      const at = rfc(now());
      const c = { id: String(commentSeq++), author: ME, created: at, updated: at, body };
      r.comments.push(c);
      r.updated = rfc(new Date(Math.max(Date.now(), new Date(r.updated).getTime() + 1000)));
      return clone(c); // no event (SPEC v0.6)
    },
    /* --- v0.7 window chrome: the browser has no frameless window; state is simulated, controls are logged --- */
    getWindowInfo: () => clone(win),
    windowControl(p) {
      const action = p && p.action;
      if (!['minimize', 'toggleMaximize', 'close', 'drag', 'resize'].includes(action)) throw new MockError('bad_params', 'action must be minimize | toggleMaximize | close | drag | resize');
      if (action === 'resize' && !['n', 's', 'e', 'w', 'ne', 'nw', 'se', 'sw'].includes(p.edge)) throw new MockError('bad_params', 'edge must be n s e w ne nw se sw');
      if (action !== 'resize' && p.edge != null) throw new MockError('bad_params', 'edge is only for resize');
      console.info('[jiraffe mock] windowControl →', action, p.edge || ''); // eslint-disable-line no-console
      if (action === 'toggleMaximize') { win.maximized = !win.maximized; setTimeout(() => emit('windowState', clone(win)), 30); }
      return null;
    },
    setWindowTheme(p) { // v0.4: the native title bar; nothing to paint in a browser
      if (!p || typeof p.dark !== 'boolean' || (p.caption != null && !/^#[0-9a-f]{6}$/i.test(String(p.caption)))) throw new MockError('bad_params', 'dark must be a boolean, caption "#rrggbb"');
      console.info('[jiraffe mock] setWindowTheme →', p.dark ? 'dark' : 'light', p.caption || ''); // eslint-disable-line no-console
      return null;
    },
    openUrl(p) {
      const u = (p && p.url) || '';
      if (!/^https?:\/\//i.test(u) || !settings.jiraUrl || !u.startsWith(settings.jiraUrl)) throw new MockError('forbidden_url', 'URL is not under jiraUrl');
      console.info('[jiraffe mock] openUrl →', u); // eslint-disable-line no-console
      return null;
    },
    worklogPreview(p) {
      const date = checkDay(p);
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12002: the operation timed out');
      return buildRun('preview', date);
    },
    worklogRun(p) {
      requireConfigured();
      const date = checkDay(p);
      const past = date !== isoDay(now());
      const run = buildRun('manual', date);
      const logged = past ? (loggedPast.get(date) || new Map()) : loggedToday;
      run.items.forEach((i) => {
        if (i.todoSeconds < 60) { i.result = 'skipped'; return; }
        if (Math.random() < ERROR_RATE) { i.result = 'error'; i.error = 'Jira 403: You do not have the permission to log work on this issue'; return; }
        i.result = 'logged';
        logged.set(i.key, (logged.get(i.key) || 0) + i.todoSeconds);
      });
      if (past) {
        loggedPast.set(date, logged);
        if (missedDays.delete(date)) setTimeout(() => emit('worklogMissed', METHODS.getMissedWorklogDays()), 30); // a day left the list
      }
      run.totalSeconds = run.items.filter((i) => i.result === 'logged').reduce((a, i) => a + i.todoSeconds, 0);
      run.total = fmt(run.totalSeconds);
      history.unshift(clone(run));
      if (history.length > 60) history.length = 60;
      setTimeout(() => emit('state', state()), 50);
      return run;
    },
    getWorklogHistory: () => clone(history),
    /* --- v0.7.1 missed days --- */
    getMissedWorklogDays() { // → MissedDays { days }, re-filtered (logged / skipped meanwhile are dropped)
      requireConfigured();
      return { days: clone([...missedDays].filter((d) => !skippedDays.has(d)).sort().map((d) => buildRun('preview', d)).filter((r) => r.totalSeconds > 0)) };
    },
    skipWorklogDay(p) { // → the remaining MissedDays
      const date = checkDay(p);
      skippedDays.add(date);
      console.info('[jiraffe mock] skipWorklogDay →', date); // eslint-disable-line no-console
      const left = METHODS.getMissedWorklogDays();
      setTimeout(() => emit('worklogMissed', left), 30); // a day left the list
      return left;
    },
    getUiPrefs() {
      if (flags.has('noprefs')) throw new MockError('unknown_method', 'unknown method getUiPrefs');
      try { const v = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}'); return v && typeof v === 'object' && !Array.isArray(v) ? v : {}; } catch { return {}; }
    },
    setUiPrefs(p) {
      if (flags.has('noprefs')) throw new MockError('unknown_method', 'unknown method setUiPrefs');
      const v = p && p.prefs;
      if (!v || typeof v !== 'object' || Array.isArray(v)) throw new MockError('bad_params', 'prefs must be an object');
      const raw = JSON.stringify(v);
      if (raw.length > 32 * 1024) throw new MockError('validation', 'prefs are larger than 32 KB');
      try { localStorage.setItem(PREFS_KEY, raw); } catch { /* storage unavailable: accepted, not persisted */ }
      return null;
    },
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

  const PREFS_KEY = 'jiraffe.mock.uiPrefs';

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
  // v0.7.1: the missed-day check runs after start (the UI also asks getMissedWorklogDays)
  setTimeout(() => {
    if (!configured()) return;
    emit('worklogMissed', METHODS.getMissedWorklogDays()); // the first check of a session always sends, also { days: [] }
  }, 2500);

  /* v0.7: a stand-in for the desktop seen through the window (?mock=noglass: the opaque fallback). The real backdrop is
     an untinted blur measured as ≈ 6 + 0.6 × desktop; here: soft colour fields under that dimming. Worst cases:
     ?mock=desk-white (≈ #9f9f9f behind the page: dark theme) and ?mock=desk-black (≈ #060606: light theme).
     Browser only (mock.js is never embedded). */
  const win = { glass: !flags.has('noglass'), maximized: false, active: true };
  if (win.glass) {
    const desk = document.createElement('style');
    const plain = flags.has('desk-white') ? '#9f9f9f' : flags.has('desk-black') ? '#060606' : '';
    desk.textContent = plain ? `html.is-glass { background: ${plain}; }` : `html.is-glass {
  --mock-tint: rgb(15 15 15 / .4);
  background:
    linear-gradient(var(--mock-tint), var(--mock-tint)),
    radial-gradient(42% 55% at 14% 24%, #3f7fd9, transparent 72%),
    radial-gradient(38% 48% at 86% 18%, #e0874f, transparent 70%),
    radial-gradient(55% 50% at 62% 92%, #2fa98a, transparent 72%),
    radial-gradient(30% 40% at 40% 50%, #c9c2b4, transparent 70%),
    linear-gradient(135deg, #1f2c3d, #4d3d60 48%, #24463a);
}`;
    document.head.append(desk);
  }

  /* ---------- periodic comment notifications (tracked projects only, like the Go post-filter) ---------- */
  setInterval(() => {
    const mineOpen = all().filter((r) => mine(r) && isTracked(r.key));
    if (!settings.notifyComments || !configured() || !mineOpen.length) return;
    const is = pick(mineOpen);
    const n = { id: String(notifSeq++), issueKey: is.key, issueSummary: is.summary, author: pick(AUTHORS), body: pick(BODIES), created: rfc(now()), url: `${url(is.key)}?focusedCommentId=${notifSeq}`, read: false };
    notifications.unshift(n);
    if (notifications.length > 200) notifications.length = 200;
    emit('notification', n);
    emit('state', state());
  }, flags.has('fast') ? 10000 : 45000);

  console.info('[jiraffe mock] fake backend installed. Flags: ?mock=unconfigured,empty,errors,noerrors,slow,fast,warnings,noagile,truncated,untracked,limit,noprefs,closedfail,timepartial,update,update-fail,update-nowrite,update-badsig,update-checkfail,noupdate,devbuild,updated,noglass,desk-white,desk-black,nomissed,missed2'); // eslint-disable-line no-console
})();

/* Jiraffe UI: fake backend for development / review in a normal browser.
   Installed only when the Go binding window.__jiraffe is absent. Implements every contract method
   (SPEC v0.1 + v0.2) with latency, occasional errors and periodic `notification` events.
   URL flags (?mock=a,b): unconfigured | empty | errors (frequent failures) | noerrors | slow | fast (notification every 10 s)
   | warnings (toggling autostart "fails": warning + autostartEffective = previous value)
   | noagile (agile API missing: no boards) | truncated (board/epics report truncated) | untracked (trackedProjects = null)
   | limit (tracking more than 6 projects fails with a validation error, like the Go limit of 200) | noprefs (no getUiPrefs: older backend). */
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
    reject:     { id: '10202', name: 'Reject', category: 'done' },
  };
  const ORDER = ['backlog', 'todo', 'inprog', 'forReview', 'inReview', 'forTesting', 'solved', 'done', 'reject'];
  const CAT_RANK = { new: 0, indeterminate: 1, done: 2 };
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
     extras: epic (key), name/color (for epics), sp, parent, sprints [ids], labels, comps, versions, est [orig, rem, spent] h */
  const H = 3600;
  const RAW = [
    // Epics
    ['RBI-900', 'Новый движок отчётов: построение, экспорт, фильтры', 'inprog', 'epic', 'High', 'am', 60 * 30, { name: 'Движок отчётов', color: 'ghx-label-4' }],
    ['RBI-950', 'Единый вход через корпоративный SSO', 'todo', 'epic', 'Medium', 'am', 60 * 50, { name: 'SSO', color: 'ghx-label-9' }],
    ['CMA-300', 'Журнал операций 2.0', 'inprog', 'epic', 'Medium', 'nb', 60 * 70, { name: 'Журнал операций 2.0', color: 'ghx-label-6' }],
    ['MB-50', 'Push-сповіщення для мобільних застосунків', 'inprog', 'epic', 'High', 'sk', 60 * 20, { name: 'Push-сповіщення', color: 'ghx-label-2' }],
    ['EDS-200', 'Интеграция с провайдером ЭДО', 'inprog', 'epic', 'High', 'do', 60 * 40, { name: 'Интеграция с ЭДО', color: 'ghx-label-8' }],
    ['EDS-150', 'Миграция архива документов', 'done', 'epic', 'Low', 'do', 60 * 24 * 20, { name: 'Миграция архива', color: 'ghx-label-11' }],
    // Stories
    ['RBI-1031', 'Перенести авторизацию на OAuth2 (Keycloak)', 'todo', 'story', 'Medium', 'me', 95, { epic: 'RBI-950', sp: 5, sprints: [6338], labels: ['security', 'backend'], comps: ['Auth'], versions: ['4.2.0'] }],
    ['RBI-1040', 'Экспорт отчётов в Excel без ограничения по строкам', 'inprog', 'story', 'High', 'is', 180, { epic: 'RBI-900', sp: 8, sprints: [6338], comps: ['Export'], versions: ['4.2.0'], est: [24, 10, 14] }],
    ['RBI-1041', 'Конструктор фильтров отчёта', 'todo', 'story', 'Medium', 'do', 60 * 8, { epic: 'RBI-900', sp: 5, sprints: [6338] }],
    ['CMA-412', 'Добавить фильтр по дате в журнал операций', 'inprog', 'story', 'Medium', 'me', 47, { epic: 'CMA-300', sp: 3, labels: ['ux'] }],
    ['CMA-420', 'Выгрузка журнала операций в CSV', 'done', 'story', 'Low', 'mh', 60 * 24 * 4, { epic: 'CMA-300', sp: 2 }],
    ['MB-77', 'Push-сповіщення для iOS', 'forTesting', 'story', 'High', 'me', 300, { epic: 'MB-50', sp: 8, versions: ['3.14'] }],
    ['MB-90', 'Налаштування сповіщень у профілі', 'todo', 'story', 'Medium', 'sk', 60 * 26, { epic: 'MB-50', sp: 3 }],
    ['EDS-230', 'Интеграция с сервисом электронного документооборота: подписание пакета', 'inprog', 'story', 'Medium', 'me', 5, { epic: 'EDS-200', sp: 8, comps: ['Signing'], est: [36, 12, 20] }],
    ['EDS-240', 'Статусы подписания в карточке документа', 'inprog', 'story', 'Medium', 'nb', 60 * 5, { epic: 'EDS-200', sp: 5 }],
    ['RBI-1050', 'Тёмная тема для портала отчётов', 'backlog', 'story', 'Low', 'mh', 60 * 24 * 9, { sp: 3, sprints: [6339] }],
    ['CMA-430', 'Онбординг новых клиентов в кабинете', 'todo', 'story', 'Medium', null, 60 * 24 * 2, { sp: 5 }],
    // Tasks, bugs, sub-tasks
    ['RBI-1028', 'Відділити frontend від backend', 'inprog', 'task', 'High', 'me', 12, { epic: 'RBI-950', sprints: [6338], labels: ['architecture'], est: [16, 6, 9.5] }],
    ['RBI-1019', 'Помилка 500 при експорті звіту в Excel, якщо в періоді більше 10 000 рядків', 'forReview', 'bug', 'Highest', 'me', 38, { epic: 'RBI-900', sprints: [6338], comps: ['Export'], due: 3 }],
    ['RBI-997', 'Оновити залежності Spring Boot до 3.3', 'backlog', 'task', 'Low', 'me', 60 * 26, { epic: 'RBI-900', sprints: [6339], sp: 2 }],
    ['RBI-1042', 'Индексы для таблицы фактов отчётов', 'inprog', 'task', 'Medium', 'is', 60 * 3, { epic: 'RBI-900', sprints: [6338], sp: 3 }],
    ['RBI-1043', 'Покрыть экспорт интеграционными тестами', 'todo', 'sub', 'Medium', 'do', 60 * 6, { parent: 'RBI-1040', sprints: [6338] }],
    ['RBI-1044', 'Нагрузочное тестирование движка', 'todo', 'task', 'Medium', 'sk', 60 * 30, { epic: 'RBI-900', sprints: [6338], sp: 3 }],
    ['RBI-1045', 'Обновить документацию API отчётов', 'done', 'task', 'Low', 'is', 60 * 24, { epic: 'RBI-900', sprints: [6338], sp: 1 }],
    ['RBI-1046', 'Метрики времени построения отчёта в Grafana', 'done', 'task', 'Medium', 'me', 60 * 24 * 2, { epic: 'RBI-900', sprints: [6338], sp: 2 }],
    ['RBI-1029', 'Кэширование справочников', 'done', 'task', 'Medium', 'me', 60 * 24 * 16, { epic: 'RBI-900', sprints: [6337], sp: 3 }],
    ['RBI-1030', 'Логирование запросов к отчётам', 'done', 'task', 'Low', 'is', 60 * 24 * 15, { sprints: [6337], sp: 2 }],
    ['CMA-415', 'Не обновляется счётчик непрочитанных сообщений после перехода по push', 'inReview', 'bug', 'High', 'me', 180, {}],
    ['CMA-399', 'Рефакторинг модуля уведомлений', 'backlog', 'task', 'Medium', 'me', 60 * 24 * 6, {}],
    ['CMA-421', 'API фильтрации журнала по дате', 'done', 'task', 'Medium', 'nb', 60 * 24, { epic: 'CMA-300' }],
    ['CMA-422', 'UI-компонент выбора периода', 'inprog', 'task', 'Medium', 'mh', 60 * 2, { epic: 'CMA-300' }],
    ['CMA-433', 'Падает сборка на CI после обновления Gradle', 'forReview', 'bug', 'High', 'do', 60 * 4, {}],
    ['MB-81', 'Краш при повороті екрана на Android 14', 'todo', 'bug', 'Highest', 'me', 22, { epic: 'MB-50' }],
    ['MB-78', 'Сертифікати APNs для продакшну', 'done', 'task', 'High', 'sk', 60 * 24 * 3, { epic: 'MB-50' }],
    ['MB-79', 'Обробка silent push', 'inprog', 'sub', 'Medium', 'me', 60 * 7, { parent: 'MB-77' }],
    ['MB-95', 'Локалізація екранів оплати', 'inReview', 'task', 'Low', 'nb', 60 * 9, {}],
    ['EDS-233', 'Описать API загрузки вложений в Swagger', 'forReview', 'task', 'Low', 'me', 60 * 3, { epic: 'EDS-200' }],
    ['EDS-219', 'Таймаут при генерации PDF больше 50 страниц', 'todo', 'bug', 'Medium', 'me', 60 * 50, { epic: 'EDS-200' }],
    ['EDS-231', 'Подписание пакета: бэкенд и очередь', 'inprog', 'task', 'High', 'am', 60, { epic: 'EDS-200' }],
    ['EDS-241', 'Вебхук статусов от провайдера ЭДО', 'todo', 'task', 'Medium', 'is', 60 * 11, { epic: 'EDS-200' }],
    ['EDS-151', 'Перенос архива за 2019–2022', 'done', 'task', 'Medium', 'do', 60 * 24 * 21, { epic: 'EDS-150' }],
    ['EDS-152', 'Проверка целостности после миграции', 'done', 'task', 'Low', 'nb', 60 * 24 * 20, { epic: 'EDS-150' }],
    ['HR-5', 'Шаблон заявки на отпуск', 'todo', 'task', 'Low', 'am', 60 * 24 * 3, {}],
    ['HR-7', 'Синхронизация оргструктуры', 'inprog', 'task', 'Medium', 'is', 60 * 20, {}],
    ['OPS-12', 'Продлить сертификат sd.local', 'todo', 'task', 'High', 'me', 60 * 15, {}],
    ['SEC-31', 'Аудит прав сервисных учётных записей', 'todo', 'task', 'Medium', 'do', 60 * 24 * 5, {}],
    ['OPS-14', 'Обновить базовый образ CI-агентов', 'inprog', 'task', 'Medium', 'me', 60 * 9, {}],
    ['DWH-8', 'Выгрузка витрины платежей для отчётов', 'todo', 'task', 'Low', 'me', 60 * 24 * 2, { sp: 2 }],
  ];
  // [from, link type, to]; type: [name, outward, inward]
  const LT = { rel: ['Relates', 'relates to', 'relates to'], blocks: ['Blocks', 'blocks', 'is blocked by'], dup: ['Duplicate', 'duplicates', 'is duplicated by'] };
  const LINKS = [
    ['RBI-1028', 'rel', 'RBI-1031'], ['RBI-1019', 'blocks', 'RBI-1040'], ['RBI-1042', 'rel', 'RBI-1040'],
    ['CMA-421', 'rel', 'CMA-412'], ['CMA-422', 'rel', 'CMA-412'], ['CMA-422', 'rel', 'CMA-420'],
    ['MB-78', 'blocks', 'MB-77'], ['MB-81', 'rel', 'MB-95'], ['EDS-233', 'rel', 'EDS-230'], ['EDS-231', 'rel', 'EDS-230'],
    ['EDS-241', 'blocks', 'EDS-240'], ['CMA-415', 'dup', 'CMA-399'], ['OPS-12', 'rel', 'RBI-1031'],
  ];

  const recs = new Map();
  if (!flags.has('empty')) {
    RAW.forEach(([key, summary, st, type, prio, who, ago, x], i) => {
      const created = minutesAgo(ago + 60 * 24 * (5 + (i % 9)));
      recs.set(key, {
        id: String(20000 + i), key, summary, status: ST[st], type: TYPES[type], priority: { name: prio },
        assignee: who ? U[who] : null, updated: minutesAgo(ago), created,
        reporter: who === 'me' ? U.am : U.me, epic: x.epic || null, epicName: x.name, epicColor: x.color,
        storyPoints: x.sp, parent: x.parent || null, sprints: x.sprints || [], labels: x.labels || [], components: x.comps || [],
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

Проверка экранирования: <img src=x onerror=alert(1)> и <script>alert('xss')</script> должны отображаться как текст. Ссылка на чужой сайт: [не наш Jira|https://example.com/evil].`,
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
  };
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
  const linksOf = (key) => LINKS.filter(([a, , b]) => a === key || b === key).map(([a, t, b]) => ({
    relation: a === key ? LT[t][1] : LT[t][2], other: a === key ? b : a,
  })).filter((l) => recs.has(l.other));

  /* ---------- sprints / boards ---------- */
  const AGILE = [
    { id: 12, name: 'RBI Scrum', type: 'scrum', projectKey: 'RBI' },
    { id: 15, name: 'Кабинет клиента — Kanban', type: 'kanban', projectKey: 'CMA' },
  ];
  const SPRINTS = [
    { id: 6335, name: 'RBI Sprint 41', state: 'closed', startDate: daysFromNow(-48), endDate: daysFromNow(-34), completeDate: daysFromNow(-34, 17), boardId: 12 },
    { id: 6336, name: 'RBI Sprint 42', state: 'closed', startDate: daysFromNow(-34), endDate: daysFromNow(-20), completeDate: daysFromNow(-20, 17), boardId: 12 },
    { id: 6337, name: 'RBI Sprint 43', state: 'closed', startDate: daysFromNow(-20), endDate: daysFromNow(-6), completeDate: daysFromNow(-6, 17), goal: 'Кэш справочников и логирование', boardId: 12 },
    { id: 6338, name: 'RBI Sprint 44', state: 'active', startDate: daysFromNow(-6), endDate: daysFromNow(8, 18), goal: 'Выпустить экспорт без ограничений и подготовить переход на SSO', boardId: 12 },
    { id: 6339, name: 'RBI Sprint 45', state: 'future', startDate: daysFromNow(8), endDate: daysFromNow(22, 18), boardId: 12 },
  ];

  function progressOf(list) {
    const p = { done: 0, inProgress: 0, todo: 0, total: list.length };
    list.forEach((r) => { if (r.status.category === 'done') p.done++; else if (r.status.category === 'indeterminate') p.inProgress++; else p.todo++; });
    return p;
  }
  function columnsOf(list, withDone) {
    return ORDER.map((k) => ST[k]).filter((st) => withDone || st.category !== 'done')
      .map((st) => ({ status: st, issues: list.filter((r) => r.status.id === st.id).sort((a, b) => b.updated.localeCompare(a.updated)).map(toIssue) }))
      .filter((c) => c.issues.length);
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
    return { epic: toRef(e), stories, tasks: tasks.map((r) => toRef(r)), progress: progressOf([...desc.values()]), _all: [e, ...desc.values()] };
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

  function buildRun(trigger) {
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
      version: '0.2.0-mock',
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

  /* ---------- methods ---------- */
  class MockError { constructor(code, message, fields) { this.code = code; this.message = message; if (fields) this.fields = fields; } }
  let boardCalls = 0;
  const maybeFail = (rate, code, message) => { if (Math.random() < rate) throw new MockError(code, message); };
  const requireConfigured = () => { if (!configured()) throw new MockError('not_configured', 'Jira is not configured'); };
  const scopeOf = (p, def) => (p && (p.scope === 'mine' || p.scope === 'all') ? p.scope : def);

  function board(scope) {
    const open = all().filter((r) => !isDone(r) && r.type.kind !== 'epic');
    let list = open.filter((r) => isTracked(r.key));
    if (scope === 'mine') list = list.filter(mine);
    const truncated = flags.has('truncated') && scope === 'all';
    if (truncated) list = list.slice(0, 20);
    const out = { columns: clone(columnsOf(list, false)), fetchedAt: rfc(now()), scope, truncated };
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
      issue: toIssue(r), description: DESC[key] || (r.type.kind === 'epic' ? '' : `Задача *${r.key}*: ${r.summary}.`),
      reporter: r.reporter, creator: r.reporter, created: r.created, updated: r.updated,
      labels: r.labels, components: r.components, fixVersions: r.fixVersions, priority: r.priority,
      timeTracking: tt, sprints: SPRINTS.filter((s) => r.sprints.includes(s.id)),
      stories, children, links: linksOf(key).map((l) => ({ relation: l.relation, issue: toRef(rec(l.other), false) })),
      comments: r.comments, changelog: r.changelog, watchers: r.watchers,
    };
    if (r.dueDate) det.dueDate = r.dueDate;
    if (isDone(r)) det.resolution = 'Готово';
    if (r.storyPoints != null) det.storyPoints = r.storyPoints;
    if (r.epic) det.epic = toRef(rec(r.epic));
    if (r.parent) det.parent = toRef(rec(r.parent), false);
    return det;
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
      return board(scope);
    },
    getTransitions(p) {
      requireConfigured();
      const is = rec(p.key);
      if (!is) throw new MockError('not_found', `issue ${p.key} not found`);
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12029: cannot connect');
      return (FLOW[statusKey(is.status)] || []).map(([id, name, to]) => ({ id, name, to: clone(ST[to]) }));
    },
    transition(p) {
      requireConfigured();
      const is = rec(p.key);
      if (!is) throw new MockError('not_found', `issue ${p.key} not found`);
      const tr = (FLOW[statusKey(is.status)] || []).find(([id]) => id === String(p.transitionId));
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
    getAgileBoards() {
      requireConfigured();
      if (flags.has('noagile')) return [];
      return clone(AGILE.filter((b) => isTracked(b.projectKey + '-1')));
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
    getSprint(p) {
      requireConfigured();
      const sp = SPRINTS.find((s) => String(s.id) === String(p && p.sprintId));
      if (!sp) throw new MockError('not_found', 'sprint not found');
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12002: the operation timed out');
      let list = all().filter((r) => r.sprints.includes(sp.id));
      if (scopeOf(p, 'all') === 'mine') list = list.filter(mine);
      const pts = list.reduce((a, r) => { const v = Number(r.storyPoints) || 0; a.total += v; if (isDone(r)) a.done += v; return a; }, { done: 0, total: 0 });
      return clone({ sprint: sp, columns: columnsOf(list, true), progress: progressOf(list), storyPoints: pts, truncated: false });
    },
    getEpics(p) {
      requireConfigured();
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12002: the operation timed out');
      const scope = scopeOf(p, 'all');
      const epics = all().filter((r) => r.type.kind === 'epic' && isTracked(r.key) && (p.includeDone || !isDone(r))).sort(treeSort)
        .map(epicNode).filter((n) => keepScope(n, scope)).map(strip);
      const orphanStories = all().filter((r) => r.type.kind === 'story' && !r.epic && isTracked(r.key) && (p.includeDone || !isDone(r))).sort(treeSort)
        .map(storyNode).filter((n) => keepScope(n, scope)).map(strip);
      return clone({ epics, orphanStories, fetchedAt: rfc(now()), truncated: flags.has('truncated') });
    },
    getStories(p) {
      requireConfigured();
      maybeFail(ERROR_RATE / 2, 'network', 'WinHTTP 12002: the operation timed out');
      const scope = scopeOf(p, 'all');
      const stories = all().filter((r) => r.type.kind === 'story' && isTracked(r.key) && (p.includeDone || !isDone(r))).sort(treeSort)
        .map(storyNode).filter((n) => keepScope(n, scope)).map(strip);
      return clone({ stories, fetchedAt: rfc(now()), truncated: flags.has('truncated') });
    },
    openUrl(p) {
      const u = (p && p.url) || '';
      if (!/^https?:\/\//i.test(u) || !settings.jiraUrl || !u.startsWith(settings.jiraUrl)) throw new MockError('forbidden_url', 'URL is not under jiraUrl');
      console.info('[jiraffe mock] openUrl →', u); // eslint-disable-line no-console
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

  console.info('[jiraffe mock] fake backend installed. Flags: ?mock=unconfigured,empty,errors,noerrors,slow,fast,warnings,noagile,truncated,untracked,limit,noprefs'); // eslint-disable-line no-console
})();

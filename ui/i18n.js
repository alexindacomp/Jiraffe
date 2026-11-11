/* Jiraffe UI: localisation (ru default, uk, en) and locale-aware formatting.
   Exposes window.i18n. Static markup uses data-i18n="key" (textContent) and
   data-i18n-attr="attr:key;attr2:key2". Plural entries are {one, few, many, other}
   objects selected by params.n via Intl.PluralRules. */
(function () {
  'use strict';

  const ru = {
    'nav.label': 'Основная навигация',
    'nav.board': 'Доска',
    'nav.worklog': 'Списание',
    'nav.notifications': 'Уведомления',
    'nav.settings': 'Настройки',
    'title.board': 'Мои задачи',
    'title.worklog': 'Списание времени',
    'title.notifications': 'Уведомления',
    'title.settings': 'Настройки',

    'common.refreshShortcut': 'Обновить (R)',
    'common.ok': 'OK',
    'common.cancel': 'Отмена',
    'common.close': 'Закрыть',
    'common.closeEsc': 'Закрыть (Esc)',
    'common.retry': 'Повторить',
    'common.loading': 'Загрузка…',
    'common.open': 'Открыть',
    'common.openInJira': 'Открыть в Jira',
    'common.openFailed': 'Не удалось открыть ссылку',
    'common.add': 'Добавить',
    'common.remove': 'Удалить',
    'common.removeItem': 'Удалить «{name}»',
    'common.save': 'Сохранить',

    'chrome.connectedAs': 'Подключено: {name}',
    'chrome.notConfigured': 'Jira не подключена',
    'chrome.stateError': 'Нет связи с приложением',
    'chrome.openSettings': 'Открыть настройки',
    'app.stateErrorTitle': 'Не удалось получить состояние приложения',

    'error.generic': 'Что-то пошло не так',
    'error.timeout': 'Сервер не ответил вовремя',
    'error.binding': 'Ошибка связи с приложением',
    'error.no_backend': 'Приложение недоступно',
    'error.network': 'Нет связи с Jira',
    'error.unauthorized': 'Jira отклонила токен (401)',
    'error.forbidden': 'Недостаточно прав в Jira',
    'error.not_configured': 'Jira ещё не подключена',
    'error.not_found': 'Не найдено',
    'error.settings_unavailable': 'Не удалось загрузить настройки',
    'error.forbidden_url': 'Ссылка ведёт не на ваш Jira',
    'error.bad_nonce': 'Внутренняя ошибка приложения — перезапустите Jiraffe',

    'board.searchPlaceholder': 'Поиск задач',
    'board.searchLabel': 'Поиск по ключу или названию (/)',
    'board.projectsLabel': 'Фильтр по проектам',
    'board.allProjects': 'Все',
    'board.updated': 'обновлено {rel}',
    'board.count': { one: '{n} задача', few: '{n} задачи', many: '{n} задач', other: '{n} задачи' },
    'board.errorTitle': 'Не удалось загрузить доску',
    'board.emptyTitle': 'Открытых задач нет',
    'board.emptyText': 'На вас не назначено ни одной незакрытой задачи. Можно выдохнуть.',
    'board.columnEmpty': 'Нет задач',
    'board.noTransitionHint': 'Сюда нет перехода',
    'board.noMatches': 'Ни одна задача не подходит под фильтр.',
    'board.clearFilters': 'Сбросить фильтр',
    'board.refreshFailed': 'Не удалось обновить доску',
    'board.transitionsFailed': 'Не удалось получить переходы для {key}',
    'board.noTransition': 'Для {key} нет перехода из «{from}» в «{to}»',
    'board.moved': '{key} → {status}',
    'board.transitionFailed': '{key}: переход не выполнен',

    'picker.title': 'Выберите переход',
    'picker.text': 'В статус «{status}» для {key} ведёт несколько переходов.',

    'issue.status': 'Статус',
    'issue.type': 'Тип',
    'issue.priority': 'Приоритет',
    'issue.priorityValue': 'Приоритет: {name}',
    'issue.project': 'Проект',
    'issue.assignee': 'Исполнитель',
    'issue.unassigned': 'Не назначен',
    'issue.updated': 'Обновлено',
    'drawer.transitions': 'Перевести в статус',
    'drawer.noTransitions': 'Из текущего статуса нет доступных переходов.',

    'worklog.preview': 'Пересчитать',
    'worklog.logNow': 'Списать сейчас',
    'worklog.history': 'История',
    'worklog.today': 'Сегодня',
    'worklog.workingDay': 'Рабочий день',
    'worklog.dayUnknown': 'Статус дня неизвестен',
    'worklog.reason.weekend': 'Выходной день',
    'worklog.reason.vacation': 'Отпуск',
    'worklog.reason.holiday': 'Праздник',
    'worklog.reason.disabled': 'Автосписание выключено',
    'worklog.reason.not-configured': 'Jira не подключена',
    'worklog.skippedText.weekend': 'Сегодня не рабочий день по вашему графику — время не списывается.',
    'worklog.skippedText.vacation': 'Сегодня отпуск — время не списывается.',
    'worklog.skippedText.holiday': 'Сегодня праздник — время не списывается.',
    'worklog.skippedText.disabled': 'Автоматическое списание выключено в настройках.',
    'worklog.skippedText.not-configured': 'Подключите Jira в настройках, чтобы списывать время.',
    'worklog.nextRun': 'Следующий запуск',
    'worklog.notScheduled': 'Не запланирован',
    'worklog.workHours': 'Рабочие часы',
    'worklog.lunchRange': 'обед {from}–{to}',
    'worklog.noLunch': 'без обеда',
    'worklog.dailyCap': 'Лимит в день',
    'worklog.lastRun': 'Последний запуск',
    'worklog.never': 'Ещё не было',
    'worklog.previewTitle': 'Предпросмотр на сегодня',
    'worklog.resultTitle': 'Результат списания',
    'worklog.calculatedAt': 'Рассчитано в {time} · в Jira ничего не записано',
    'worklog.ranAt': 'Выполнено в {time}',
    'worklog.previewError': 'Не удалось рассчитать списание',
    'worklog.nothingTitle': 'Списывать нечего',
    'worklog.nothingText': 'Сегодня не было задач в статусах «в работе».',
    'worklog.allLogged': 'Всё время за сегодня уже списано.',
    'worklog.col.issue': 'Задача',
    'worklog.col.status': 'Статус',
    'worklog.col.share': 'Доля',
    'worklog.col.logged': 'Уже списано',
    'worklog.col.todo': 'Будет списано',
    'worklog.col.logTotal': 'Списано',
    'worklog.col.attempted': 'Списание',
    'worklog.col.start': 'Начало',
    'worklog.col.result': 'Результат',
    'worklog.total': 'Итого',
    'worklog.legendLogged': 'Уже списано {v}',
    'worklog.legendTodo': 'Будет списано {v}',
    'worklog.legendCap': '{total} из {cap}',
    'worklog.capAria': 'По другим задачам {other}, уже списано {logged}, будет списано {todo}, лимит {cap}, свободно {free}',
    'worklog.legendOther': 'Уже списано по другим задачам {v}',
    'worklog.legendCapFree': '{total} из {cap} · свободно {free}',
    'worklog.otherLoggedNote': 'Сегодня уже списано по другим задачам: {v}',
    'worklog.result.logged': 'Списано',
    'worklog.result.skipped': 'Пропущено',
    'worklog.result.error': 'Ошибка',
    'worklog.issuesCount': { one: '{n} задача', few: '{n} задачи', many: '{n} задач', other: '{n} задачи' },
    'worklog.historyEmpty': 'Истории пока нет',
    'worklog.historyEmptyText': 'Здесь появятся автоматические и ручные запуски.',
    'worklog.skipped': 'Пропущен',
    'worklog.hasErrors': 'С ошибками',
    'worklog.done': 'Готово',
    'worklog.trigger.schedule': 'Авто',
    'worklog.trigger.manual': 'Вручную',
    'worklog.trigger.preview': 'Предпросмотр',
    'worklog.confirmTitle': 'Списать время сейчас?',
    'worklog.confirmBody': {
      one: 'В Jira будет записано {total} по {n} задаче за {date}.',
      few: 'В Jira будет записано {total} по {n} задачам за {date}.',
      many: 'В Jira будет записано {total} по {n} задачам за {date}.',
      other: 'В Jira будет записано {total} по {n} задачам за {date}.',
    },
    'worklog.confirmCalc': 'Расчёт на {time}.',
    'worklog.runStillRunning': 'Списание ещё выполняется — результат появится в истории.',
    'worklog.runDone': 'Списано {total}',
    'worklog.runPartial': 'Списано {total}, ошибок: {n}',
    'worklog.runFailed': 'Списание не выполнено',
    'worklog.autoDone': 'Автосписание выполнено: {total}',
    'worklog.autoSkipped': 'Автосписание пропущено: {reason}',

    'notif.markAll': 'Отметить все прочитанными',
    'notif.unreadCount': { one: '{n} непрочитанное', few: '{n} непрочитанных', many: '{n} непрочитанных', other: '{n} непрочитанного' },
    'notif.allRead': 'Все уведомления прочитаны',
    'notif.loadFailed': 'Не удалось загрузить уведомления',
    'notif.markFailed': 'Не удалось отметить прочитанным',
    'notif.emptyTitle': 'Новых комментариев нет',
    'notif.emptyText': 'Когда кто-то прокомментирует вашу задачу, уведомление появится здесь.',
    'notif.disabledText': 'Уведомления о комментариях выключены в настройках.',
    'notif.itemAria': '{author} прокомментировал(а) {key}',
    'notif.itemAriaUnread': 'Непрочитано: {author} прокомментировал(а) {key}',
    'notif.toast': 'Новый комментарий в {key} · {author}',

    'settings.connection': 'Подключение',
    'settings.jiraUrl': 'Адрес Jira',
    'settings.jiraUrlHint': 'Например, https://sd.local',
    'settings.token': 'Персональный токен',
    'settings.tokenHint': 'Хранится зашифрованным (DPAPI) только на этом компьютере',
    'settings.tokenSaved': 'Сохранён · пусто = не менять',
    'settings.tokenPlaceholder': 'Вставьте токен доступа',
    'settings.showToken': 'Показать токен',
    'settings.hideToken': 'Скрыть токен',
    'settings.test': 'Проверить подключение',
    'settings.testing': 'Проверяем…',
    'settings.testOk': 'Подключено: {name}',
    'settings.testFailed': 'Не удалось подключиться',
    'settings.worklog': 'Списание времени',
    'settings.worklogEnabled': 'Автоматическое списание',
    'settings.worklogEnabledHint': 'Раз в рабочий день по времени, проведённому в статусах «в работе»',
    'settings.worklogTime': 'Время запуска',
    'settings.worklogTimeHint': 'Если приложение было закрыто, запуск произойдёт при старте в тот же день',
    'settings.workday': 'Рабочий день',
    'settings.from': 'С',
    'settings.to': 'По',
    'settings.lunch': 'Обед',
    'settings.lunchHint': 'Оставьте пустым, если обед не нужно вычитать',
    'settings.dailyCap': 'Лимит в день',
    'settings.dailyCapHint': 'Больше этого за день не списывается',
    'settings.hoursPerDay': 'Часов в дне Jira',
    'settings.hoursPerDayHint': 'Только для отображения длительностей (1d)',
    'settings.unitHours': 'ч',
    'settings.unitMinutes': 'мин',
    'settings.statuses': 'Статусы «в работе»',
    'settings.statusesHint': 'Время считается, пока задача находится в одном из этих статусов',
    'settings.statusPlaceholder': 'Название статуса',
    'settings.calendar': 'Календарь',
    'settings.workDays': 'Рабочие дни',
    'settings.vacations': 'Отпуска',
    'settings.vacationsHint': 'В эти дни время не списывается (даты включительно)',
    'settings.noVacations': 'Отпусков нет',
    'settings.addVacation': 'Добавить отпуск',
    'settings.vacationFrom': 'Отпуск {n}: начало',
    'settings.vacationTo': 'Отпуск {n}: конец',
    'settings.vacationNote': 'Комментарий',
    'settings.vacationNoteN': 'Отпуск {n}: комментарий',
    'settings.removeVacation': 'Удалить отпуск {n}',
    'settings.holidays': 'Праздники',
    'settings.holidaysHint': 'Отдельные нерабочие дни',
    'settings.notifications': 'Уведомления',
    'settings.notifyComments': 'Новые комментарии',
    'settings.notifyCommentsHint': 'Показывать уведомление, когда кто-то комментирует мои задачи',
    'settings.pollInterval': 'Интервал проверки',
    'settings.pollIntervalHint': 'От 1 до 60 минут',
    'settings.application': 'Приложение',
    'settings.language': 'Язык',
    'settings.languageHint': 'Применяется сразу',
    'settings.autostart': 'Запускать вместе с Windows',
    'settings.autostartHint': 'Приложение стартует свёрнутым в трей',
    'settings.autostartMismatch': 'Не удалось изменить автозапуск — показано фактическое состояние',
    'settings.advanced': 'Дополнительно',
    'settings.boardJql': 'JQL доски',
    'settings.boardJqlHint': 'Какие задачи показывать на доске',
    'settings.discard': 'Отменить изменения',
    'settings.unsaved': 'Есть несохранённые изменения',
    'settings.allSaved': 'Все изменения сохранены',
    'settings.saving': 'Сохранение…',
    'settings.saveFailed': 'Не удалось сохранить',
    'settings.loadFailed': 'Не удалось загрузить настройки',
    'settings.fixErrors': 'Проверьте отмеченные поля',
    'settings.numberRequired': 'Введите число',
    'settings.integerRequired': 'Введите целое число',
    'settings.timePlaceholder': 'ЧЧ:ММ',
    'settings.datePlaceholder': 'ДД.ММ.ГГГГ',
    'settings.timeInvalid': 'Время в формате ЧЧ:ММ, например 18:00',
    'settings.dateInvalid': 'Дата в формате ДД.ММ.ГГГГ',
    'settings.vacationDateInvalid': 'Отпуск {n}: дата в формате ДД.ММ.ГГГГ',

    'onboarding.viewTitle': 'Подключение к Jira',
    'onboarding.title': 'Добро пожаловать в Jiraffe',
    'onboarding.text': 'Подключите Jira, чтобы видеть свои задачи, списывать время и получать уведомления о комментариях.',
    'onboarding.tokenHint': 'Jira → Профиль → Персональные токены доступа → Создать токен',
    'onboarding.start': 'Подключить',
    'onboarding.done': 'Jira подключена',

    'time.justNow': 'только что',
    'time.todayAt': 'сегодня в {time}',
    'time.tomorrowAt': 'завтра в {time}',
    'time.yesterdayAt': 'вчера в {time}',
    'dur.w': '{n} нед',
    'dur.d': '{n} дн',
    'dur.h': '{n} ч',
    'dur.m': '{n} мин',
  };

  const uk = {
    'nav.label': 'Основна навігація',
    'nav.board': 'Дошка',
    'nav.worklog': 'Облік',
    'nav.notifications': 'Сповіщення',
    'nav.settings': 'Параметри',
    'title.board': 'Мої задачі',
    'title.worklog': 'Облік часу',
    'title.notifications': 'Сповіщення',
    'title.settings': 'Параметри',

    'common.refreshShortcut': 'Оновити (R)',
    'common.ok': 'OK',
    'common.cancel': 'Скасувати',
    'common.close': 'Закрити',
    'common.closeEsc': 'Закрити (Esc)',
    'common.retry': 'Повторити',
    'common.loading': 'Завантаження…',
    'common.open': 'Відкрити',
    'common.openInJira': 'Відкрити в Jira',
    'common.openFailed': 'Не вдалося відкрити посилання',
    'common.add': 'Додати',
    'common.remove': 'Видалити',
    'common.removeItem': 'Видалити «{name}»',
    'common.save': 'Зберегти',

    'chrome.connectedAs': 'Підключено: {name}',
    'chrome.notConfigured': 'Jira не підключено',
    'chrome.stateError': 'Немає зв’язку із застосунком',
    'chrome.openSettings': 'Відкрити налаштування',
    'app.stateErrorTitle': 'Не вдалося отримати стан застосунку',

    'error.generic': 'Щось пішло не так',
    'error.timeout': 'Сервер не відповів вчасно',
    'error.binding': 'Помилка зв’язку із застосунком',
    'error.no_backend': 'Застосунок недоступний',
    'error.network': 'Немає зв’язку з Jira',
    'error.unauthorized': 'Jira відхилила токен (401)',
    'error.forbidden': 'Недостатньо прав у Jira',
    'error.not_configured': 'Jira ще не підключено',
    'error.not_found': 'Не знайдено',
    'error.settings_unavailable': 'Не вдалося завантажити налаштування',
    'error.forbidden_url': 'Посилання веде не на ваш Jira',
    'error.bad_nonce': 'Внутрішня помилка застосунку — перезапустіть Jiraffe',

    'board.searchPlaceholder': 'Пошук задач',
    'board.searchLabel': 'Пошук за ключем або назвою (/)',
    'board.projectsLabel': 'Фільтр за проєктами',
    'board.allProjects': 'Усі',
    'board.updated': 'оновлено {rel}',
    'board.count': { one: '{n} задача', few: '{n} задачі', many: '{n} задач', other: '{n} задачі' },
    'board.errorTitle': 'Не вдалося завантажити дошку',
    'board.emptyTitle': 'Відкритих задач немає',
    'board.emptyText': 'На вас не призначено жодної незакритої задачі. Можна видихнути.',
    'board.columnEmpty': 'Немає задач',
    'board.noTransitionHint': 'Сюди немає переходу',
    'board.noMatches': 'Жодна задача не відповідає фільтру.',
    'board.clearFilters': 'Скинути фільтр',
    'board.refreshFailed': 'Не вдалося оновити дошку',
    'board.transitionsFailed': 'Не вдалося отримати переходи для {key}',
    'board.noTransition': 'Для {key} немає переходу з «{from}» до «{to}»',
    'board.moved': '{key} → {status}',
    'board.transitionFailed': '{key}: перехід не виконано',

    'picker.title': 'Оберіть перехід',
    'picker.text': 'До статусу «{status}» для {key} веде кілька переходів.',

    'issue.status': 'Статус',
    'issue.type': 'Тип',
    'issue.priority': 'Пріоритет',
    'issue.priorityValue': 'Пріоритет: {name}',
    'issue.project': 'Проєкт',
    'issue.assignee': 'Виконавець',
    'issue.unassigned': 'Не призначено',
    'issue.updated': 'Оновлено',
    'drawer.transitions': 'Перевести в статус',
    'drawer.noTransitions': 'З поточного статусу немає доступних переходів.',

    'worklog.preview': 'Перерахувати',
    'worklog.logNow': 'Списати зараз',
    'worklog.history': 'Історія',
    'worklog.today': 'Сьогодні',
    'worklog.workingDay': 'Робочий день',
    'worklog.dayUnknown': 'Статус дня невідомий',
    'worklog.reason.weekend': 'Вихідний день',
    'worklog.reason.vacation': 'Відпустка',
    'worklog.reason.holiday': 'Свято',
    'worklog.reason.disabled': 'Автосписання вимкнено',
    'worklog.reason.not-configured': 'Jira не підключено',
    'worklog.skippedText.weekend': 'Сьогодні неробочий день за вашим графіком — час не списується.',
    'worklog.skippedText.vacation': 'Сьогодні відпустка — час не списується.',
    'worklog.skippedText.holiday': 'Сьогодні свято — час не списується.',
    'worklog.skippedText.disabled': 'Автоматичне списання вимкнено в налаштуваннях.',
    'worklog.skippedText.not-configured': 'Підключіть Jira в налаштуваннях, щоб списувати час.',
    'worklog.nextRun': 'Наступний запуск',
    'worklog.notScheduled': 'Не заплановано',
    'worklog.workHours': 'Робочі години',
    'worklog.lunchRange': 'обід {from}–{to}',
    'worklog.noLunch': 'без обіду',
    'worklog.dailyCap': 'Ліміт на день',
    'worklog.lastRun': 'Останній запуск',
    'worklog.never': 'Ще не було',
    'worklog.previewTitle': 'Попередній перегляд на сьогодні',
    'worklog.resultTitle': 'Результат списання',
    'worklog.calculatedAt': 'Розраховано о {time} · у Jira нічого не записано',
    'worklog.ranAt': 'Виконано о {time}',
    'worklog.previewError': 'Не вдалося розрахувати списання',
    'worklog.nothingTitle': 'Нічого списувати',
    'worklog.nothingText': 'Сьогодні не було задач у статусах «в роботі».',
    'worklog.allLogged': 'Увесь час за сьогодні вже списано.',
    'worklog.col.issue': 'Задача',
    'worklog.col.status': 'Статус',
    'worklog.col.share': 'Частка',
    'worklog.col.logged': 'Вже списано',
    'worklog.col.todo': 'Буде списано',
    'worklog.col.logTotal': 'Списано',
    'worklog.col.attempted': 'Списання',
    'worklog.col.start': 'Початок',
    'worklog.col.result': 'Результат',
    'worklog.total': 'Разом',
    'worklog.legendLogged': 'Вже списано {v}',
    'worklog.legendTodo': 'Буде списано {v}',
    'worklog.legendCap': '{total} з {cap}',
    'worklog.capAria': 'По інших задачах {other}, вже списано {logged}, буде списано {todo}, ліміт {cap}, вільно {free}',
    'worklog.legendOther': 'Вже списано по інших задачах {v}',
    'worklog.legendCapFree': '{total} з {cap} · вільно {free}',
    'worklog.otherLoggedNote': 'Сьогодні вже списано по інших задачах: {v}',
    'worklog.result.logged': 'Списано',
    'worklog.result.skipped': 'Пропущено',
    'worklog.result.error': 'Помилка',
    'worklog.issuesCount': { one: '{n} задача', few: '{n} задачі', many: '{n} задач', other: '{n} задачі' },
    'worklog.historyEmpty': 'Історії поки немає',
    'worklog.historyEmptyText': 'Тут з’являться автоматичні та ручні запуски.',
    'worklog.skipped': 'Пропущено',
    'worklog.hasErrors': 'З помилками',
    'worklog.done': 'Готово',
    'worklog.trigger.schedule': 'Авто',
    'worklog.trigger.manual': 'Вручну',
    'worklog.trigger.preview': 'Перегляд',
    'worklog.confirmTitle': 'Списати час зараз?',
    'worklog.confirmBody': {
      one: 'У Jira буде записано {total} по {n} задачі за {date}.',
      few: 'У Jira буде записано {total} по {n} задачах за {date}.',
      many: 'У Jira буде записано {total} по {n} задачах за {date}.',
      other: 'У Jira буде записано {total} по {n} задачах за {date}.',
    },
    'worklog.confirmCalc': 'Розрахунок на {time}.',
    'worklog.runStillRunning': 'Списання ще триває — результат з’явиться в історії.',
    'worklog.runDone': 'Списано {total}',
    'worklog.runPartial': 'Списано {total}, помилок: {n}',
    'worklog.runFailed': 'Списання не виконано',
    'worklog.autoDone': 'Автосписання виконано: {total}',
    'worklog.autoSkipped': 'Автосписання пропущено: {reason}',

    'notif.markAll': 'Позначити всі прочитаними',
    'notif.unreadCount': { one: '{n} непрочитане', few: '{n} непрочитані', many: '{n} непрочитаних', other: '{n} непрочитаного' },
    'notif.allRead': 'Усі сповіщення прочитано',
    'notif.loadFailed': 'Не вдалося завантажити сповіщення',
    'notif.markFailed': 'Не вдалося позначити прочитаним',
    'notif.emptyTitle': 'Нових коментарів немає',
    'notif.emptyText': 'Коли хтось прокоментує вашу задачу, сповіщення з’явиться тут.',
    'notif.disabledText': 'Сповіщення про коментарі вимкнено в налаштуваннях.',
    'notif.itemAria': '{author} прокоментував(ла) {key}',
    'notif.itemAriaUnread': 'Непрочитане: {author} прокоментував(ла) {key}',
    'notif.toast': 'Новий коментар у {key} · {author}',

    'settings.connection': 'Підключення',
    'settings.jiraUrl': 'Адреса Jira',
    'settings.jiraUrlHint': 'Наприклад, https://sd.local',
    'settings.token': 'Персональний токен',
    'settings.tokenHint': 'Зберігається зашифрованим (DPAPI) лише на цьому комп’ютері',
    'settings.tokenSaved': 'Збережено · порожньо = не змінювати',
    'settings.tokenPlaceholder': 'Вставте токен доступу',
    'settings.showToken': 'Показати токен',
    'settings.hideToken': 'Сховати токен',
    'settings.test': 'Перевірити підключення',
    'settings.testing': 'Перевіряємо…',
    'settings.testOk': 'Підключено: {name}',
    'settings.testFailed': 'Не вдалося підключитися',
    'settings.worklog': 'Облік часу',
    'settings.worklogEnabled': 'Автоматичне списання',
    'settings.worklogEnabledHint': 'Раз на робочий день за часом у статусах «в роботі»',
    'settings.worklogTime': 'Час запуску',
    'settings.worklogTimeHint': 'Якщо застосунок був закритий, запуск відбудеться під час старту того ж дня',
    'settings.workday': 'Робочий день',
    'settings.from': 'З',
    'settings.to': 'До',
    'settings.lunch': 'Обід',
    'settings.lunchHint': 'Залиште порожнім, якщо обід не потрібно віднімати',
    'settings.dailyCap': 'Ліміт на день',
    'settings.dailyCapHint': 'Більше за день не списується',
    'settings.hoursPerDay': 'Годин у дні Jira',
    'settings.hoursPerDayHint': 'Лише для відображення тривалостей (1d)',
    'settings.unitHours': 'год',
    'settings.unitMinutes': 'хв',
    'settings.statuses': 'Статуси «в роботі»',
    'settings.statusesHint': 'Час рахується, поки задача в одному з цих статусів',
    'settings.statusPlaceholder': 'Назва статусу',
    'settings.calendar': 'Календар',
    'settings.workDays': 'Робочі дні',
    'settings.vacations': 'Відпустки',
    'settings.vacationsHint': 'У ці дні час не списується (дати включно)',
    'settings.noVacations': 'Відпусток немає',
    'settings.addVacation': 'Додати відпустку',
    'settings.vacationFrom': 'Відпустка {n}: початок',
    'settings.vacationTo': 'Відпустка {n}: кінець',
    'settings.vacationNote': 'Коментар',
    'settings.vacationNoteN': 'Відпустка {n}: коментар',
    'settings.removeVacation': 'Видалити відпустку {n}',
    'settings.holidays': 'Свята',
    'settings.holidaysHint': 'Окремі неробочі дні',
    'settings.notifications': 'Сповіщення',
    'settings.notifyComments': 'Нові коментарі',
    'settings.notifyCommentsHint': 'Показувати сповіщення, коли хтось коментує мої задачі',
    'settings.pollInterval': 'Інтервал перевірки',
    'settings.pollIntervalHint': 'Від 1 до 60 хвилин',
    'settings.application': 'Застосунок',
    'settings.language': 'Мова',
    'settings.languageHint': 'Застосовується одразу',
    'settings.autostart': 'Запускати разом з Windows',
    'settings.autostartHint': 'Застосунок стартує згорнутим у трей',
    'settings.autostartMismatch': 'Не вдалося змінити автозапуск — показано фактичний стан',
    'settings.advanced': 'Додатково',
    'settings.boardJql': 'JQL дошки',
    'settings.boardJqlHint': 'Які задачі показувати на дошці',
    'settings.discard': 'Скасувати зміни',
    'settings.unsaved': 'Є незбережені зміни',
    'settings.allSaved': 'Усі зміни збережено',
    'settings.saving': 'Збереження…',
    'settings.saveFailed': 'Не вдалося зберегти',
    'settings.loadFailed': 'Не вдалося завантажити налаштування',
    'settings.fixErrors': 'Перевірте позначені поля',
    'settings.numberRequired': 'Введіть число',
    'settings.integerRequired': 'Введіть ціле число',
    'settings.timePlaceholder': 'ГГ:ХХ',
    'settings.datePlaceholder': 'ДД.ММ.РРРР',
    'settings.timeInvalid': 'Час у форматі ГГ:ХХ, наприклад 18:00',
    'settings.dateInvalid': 'Дата у форматі ДД.ММ.РРРР',
    'settings.vacationDateInvalid': 'Відпустка {n}: дата у форматі ДД.ММ.РРРР',

    'onboarding.viewTitle': 'Підключення до Jira',
    'onboarding.title': 'Ласкаво просимо до Jiraffe',
    'onboarding.text': 'Підключіть Jira, щоб бачити свої задачі, списувати час і отримувати сповіщення про коментарі.',
    'onboarding.tokenHint': 'Jira → Профіль → Персональні токени доступу → Створити токен',
    'onboarding.start': 'Підключити',
    'onboarding.done': 'Jira підключено',

    'time.justNow': 'щойно',
    'time.todayAt': 'сьогодні о {time}',
    'time.tomorrowAt': 'завтра о {time}',
    'time.yesterdayAt': 'учора о {time}',
    'dur.w': '{n} тиж',
    'dur.d': '{n} дн',
    'dur.h': '{n} год',
    'dur.m': '{n} хв',
  };

  const en = {
    'nav.label': 'Main navigation',
    'nav.board': 'Board',
    'nav.worklog': 'Worklog',
    'nav.notifications': 'Notifications',
    'nav.settings': 'Settings',
    'title.board': 'My issues',
    'title.worklog': 'Worklog',
    'title.notifications': 'Notifications',
    'title.settings': 'Settings',

    'common.refreshShortcut': 'Refresh (R)',
    'common.ok': 'OK',
    'common.cancel': 'Cancel',
    'common.close': 'Close',
    'common.closeEsc': 'Close (Esc)',
    'common.retry': 'Retry',
    'common.loading': 'Loading…',
    'common.open': 'Open',
    'common.openInJira': 'Open in Jira',
    'common.openFailed': 'Could not open the link',
    'common.add': 'Add',
    'common.remove': 'Remove',
    'common.removeItem': 'Remove “{name}”',
    'common.save': 'Save',

    'chrome.connectedAs': 'Connected as {name}',
    'chrome.notConfigured': 'Jira is not connected',
    'chrome.stateError': 'Cannot reach the app',
    'chrome.openSettings': 'Open settings',
    'app.stateErrorTitle': 'Could not load the app state',

    'error.generic': 'Something went wrong',
    'error.timeout': 'The request timed out',
    'error.binding': 'App bridge error',
    'error.no_backend': 'The app backend is not available',
    'error.network': 'Cannot reach Jira',
    'error.unauthorized': 'Jira rejected the token (401)',
    'error.forbidden': 'Not enough permissions in Jira',
    'error.not_configured': 'Jira is not connected yet',
    'error.not_found': 'Not found',
    'error.settings_unavailable': 'Could not load settings',
    'error.forbidden_url': 'The link does not point to your Jira',
    'error.bad_nonce': 'Internal app error — please restart Jiraffe',

    'board.searchPlaceholder': 'Search issues',
    'board.searchLabel': 'Search by key or summary (/)',
    'board.projectsLabel': 'Filter by project',
    'board.allProjects': 'All',
    'board.updated': 'updated {rel}',
    'board.count': { one: '{n} issue', other: '{n} issues' },
    'board.errorTitle': 'Could not load the board',
    'board.emptyTitle': 'No open issues',
    'board.emptyText': 'Nothing unresolved is assigned to you. Time to breathe.',
    'board.columnEmpty': 'No issues',
    'board.noTransitionHint': 'No transition here',
    'board.noMatches': 'No issues match the filter.',
    'board.clearFilters': 'Clear filter',
    'board.refreshFailed': 'Could not refresh the board',
    'board.transitionsFailed': 'Could not load transitions for {key}',
    'board.noTransition': '{key} has no transition from “{from}” to “{to}”',
    'board.moved': '{key} → {status}',
    'board.transitionFailed': '{key}: transition failed',

    'picker.title': 'Choose a transition',
    'picker.text': 'Several transitions lead {key} to “{status}”.',

    'issue.status': 'Status',
    'issue.type': 'Type',
    'issue.priority': 'Priority',
    'issue.priorityValue': 'Priority: {name}',
    'issue.project': 'Project',
    'issue.assignee': 'Assignee',
    'issue.unassigned': 'Unassigned',
    'issue.updated': 'Updated',
    'drawer.transitions': 'Move to status',
    'drawer.noTransitions': 'No transitions are available from the current status.',

    'worklog.preview': 'Recalculate',
    'worklog.logNow': 'Log now',
    'worklog.history': 'History',
    'worklog.today': 'Today',
    'worklog.workingDay': 'Working day',
    'worklog.dayUnknown': 'Day status unknown',
    'worklog.reason.weekend': 'Day off',
    'worklog.reason.vacation': 'Vacation',
    'worklog.reason.holiday': 'Holiday',
    'worklog.reason.disabled': 'Auto logging is off',
    'worklog.reason.not-configured': 'Jira is not connected',
    'worklog.skippedText.weekend': 'Today is not a working day in your schedule, so no time is logged.',
    'worklog.skippedText.vacation': 'You are on vacation today, so no time is logged.',
    'worklog.skippedText.holiday': 'Today is a holiday, so no time is logged.',
    'worklog.skippedText.disabled': 'Automatic worklog is turned off in Settings.',
    'worklog.skippedText.not-configured': 'Connect Jira in Settings to log time.',
    'worklog.nextRun': 'Next run',
    'worklog.notScheduled': 'Not scheduled',
    'worklog.workHours': 'Working hours',
    'worklog.lunchRange': 'lunch {from}–{to}',
    'worklog.noLunch': 'no lunch break',
    'worklog.dailyCap': 'Daily cap',
    'worklog.lastRun': 'Last run',
    'worklog.never': 'Never',
    'worklog.previewTitle': 'Preview for today',
    'worklog.resultTitle': 'Logging result',
    'worklog.calculatedAt': 'Calculated at {time} · nothing written to Jira',
    'worklog.ranAt': 'Done at {time}',
    'worklog.previewError': 'Could not calculate the worklog',
    'worklog.nothingTitle': 'Nothing to log',
    'worklog.nothingText': 'No issues were in an in-progress status today.',
    'worklog.allLogged': 'All of today’s time is already logged.',
    'worklog.col.issue': 'Issue',
    'worklog.col.status': 'Status',
    'worklog.col.share': 'Share',
    'worklog.col.logged': 'Already logged',
    'worklog.col.todo': 'Will log',
    'worklog.col.logTotal': 'Logged',
    'worklog.col.attempted': 'To log',
    'worklog.col.start': 'Start',
    'worklog.col.result': 'Result',
    'worklog.total': 'Total',
    'worklog.legendLogged': 'Already logged {v}',
    'worklog.legendTodo': 'Will log {v}',
    'worklog.legendCap': '{total} of {cap}',
    'worklog.capAria': 'Other issues {other}, already logged {logged}, will log {todo}, cap {cap}, free {free}',
    'worklog.legendOther': 'Already logged on other issues {v}',
    'worklog.legendCapFree': '{total} of {cap} · {free} free',
    'worklog.otherLoggedNote': 'Already logged on other issues today: {v}',
    'worklog.result.logged': 'Logged',
    'worklog.result.skipped': 'Skipped',
    'worklog.result.error': 'Error',
    'worklog.issuesCount': { one: '{n} issue', other: '{n} issues' },
    'worklog.historyEmpty': 'No history yet',
    'worklog.historyEmptyText': 'Automatic and manual runs will appear here.',
    'worklog.skipped': 'Skipped',
    'worklog.hasErrors': 'With errors',
    'worklog.done': 'Done',
    'worklog.trigger.schedule': 'Auto',
    'worklog.trigger.manual': 'Manual',
    'worklog.trigger.preview': 'Preview',
    'worklog.confirmTitle': 'Log time now?',
    'worklog.confirmBody': { one: '{total} will be written to Jira on {n} issue for {date}.', other: '{total} will be written to Jira across {n} issues for {date}.' },
    'worklog.confirmCalc': 'Calculated at {time}.',
    'worklog.runStillRunning': 'The run is still in progress; the result will appear in history.',
    'worklog.runDone': 'Logged {total}',
    'worklog.runPartial': 'Logged {total}, errors: {n}',
    'worklog.runFailed': 'Worklog failed',
    'worklog.autoDone': 'Automatic worklog done: {total}',
    'worklog.autoSkipped': 'Automatic worklog skipped: {reason}',

    'notif.markAll': 'Mark all as read',
    'notif.unreadCount': { one: '{n} unread', other: '{n} unread' },
    'notif.allRead': 'You’re all caught up',
    'notif.loadFailed': 'Could not load notifications',
    'notif.markFailed': 'Could not mark as read',
    'notif.emptyTitle': 'No new comments',
    'notif.emptyText': 'When someone comments on your issue, it will show up here.',
    'notif.disabledText': 'Comment notifications are turned off in Settings.',
    'notif.itemAria': '{author} commented on {key}',
    'notif.itemAriaUnread': 'Unread: {author} commented on {key}',
    'notif.toast': 'New comment on {key} · {author}',

    'settings.connection': 'Connection',
    'settings.jiraUrl': 'Jira URL',
    'settings.jiraUrlHint': 'For example, https://sd.local',
    'settings.token': 'Personal access token',
    'settings.tokenHint': 'Stored encrypted (DPAPI) on this computer only',
    'settings.tokenSaved': 'Saved · leave empty to keep',
    'settings.tokenPlaceholder': 'Paste your access token',
    'settings.showToken': 'Show token',
    'settings.hideToken': 'Hide token',
    'settings.test': 'Test connection',
    'settings.testing': 'Testing…',
    'settings.testOk': 'Connected as {name}',
    'settings.testFailed': 'Connection failed',
    'settings.worklog': 'Worklog',
    'settings.worklogEnabled': 'Automatic worklog',
    'settings.worklogEnabledHint': 'Once per working day, based on time spent in in-progress statuses',
    'settings.worklogTime': 'Run at',
    'settings.worklogTimeHint': 'If the app was closed, it runs when started later the same day',
    'settings.workday': 'Working day',
    'settings.from': 'From',
    'settings.to': 'To',
    'settings.lunch': 'Lunch',
    'settings.lunchHint': 'Leave empty to not subtract a lunch break',
    'settings.dailyCap': 'Daily cap',
    'settings.dailyCapHint': 'Never log more than this per day',
    'settings.hoursPerDay': 'Hours in a Jira day',
    'settings.hoursPerDayHint': 'Only used to display durations (1d)',
    'settings.unitHours': 'h',
    'settings.unitMinutes': 'min',
    'settings.statuses': 'In-progress statuses',
    'settings.statusesHint': 'Time counts while an issue is in one of these statuses',
    'settings.statusPlaceholder': 'Status name',
    'settings.calendar': 'Calendar',
    'settings.workDays': 'Working days',
    'settings.vacations': 'Vacations',
    'settings.vacationsHint': 'No time is logged on these days (inclusive)',
    'settings.noVacations': 'No vacations',
    'settings.addVacation': 'Add vacation',
    'settings.vacationFrom': 'Vacation {n}: start',
    'settings.vacationTo': 'Vacation {n}: end',
    'settings.vacationNote': 'Note',
    'settings.vacationNoteN': 'Vacation {n}: note',
    'settings.removeVacation': 'Remove vacation {n}',
    'settings.holidays': 'Holidays',
    'settings.holidaysHint': 'Individual days off',
    'settings.notifications': 'Notifications',
    'settings.notifyComments': 'New comments',
    'settings.notifyCommentsHint': 'Notify me when someone comments on my issues',
    'settings.pollInterval': 'Check every',
    'settings.pollIntervalHint': '1 to 60 minutes',
    'settings.application': 'Application',
    'settings.language': 'Language',
    'settings.languageHint': 'Applied immediately',
    'settings.autostart': 'Start with Windows',
    'settings.autostartHint': 'Starts minimized to the tray',
    'settings.autostartMismatch': 'Autostart could not be changed — showing the actual state',
    'settings.advanced': 'Advanced',
    'settings.boardJql': 'Board JQL',
    'settings.boardJqlHint': 'Which issues the board shows',
    'settings.discard': 'Discard changes',
    'settings.unsaved': 'You have unsaved changes',
    'settings.allSaved': 'All changes saved',
    'settings.saving': 'Saving…',
    'settings.saveFailed': 'Could not save',
    'settings.loadFailed': 'Could not load settings',
    'settings.fixErrors': 'Please check the highlighted fields',
    'settings.numberRequired': 'Enter a number',
    'settings.integerRequired': 'Enter a whole number',
    'settings.timePlaceholder': 'HH:MM',
    'settings.datePlaceholder': 'DD/MM/YYYY',
    'settings.timeInvalid': 'Use HH:MM, e.g. 18:00',
    'settings.dateInvalid': 'Use DD/MM/YYYY',
    'settings.vacationDateInvalid': 'Vacation {n}: use DD/MM/YYYY',

    'onboarding.viewTitle': 'Connect to Jira',
    'onboarding.title': 'Welcome to Jiraffe',
    'onboarding.text': 'Connect Jira to see your issues, log time automatically and get notified about comments.',
    'onboarding.tokenHint': 'Jira → Profile → Personal Access Tokens → Create token',
    'onboarding.start': 'Connect',
    'onboarding.done': 'Jira connected',

    'time.justNow': 'just now',
    'time.todayAt': 'today at {time}',
    'time.tomorrowAt': 'tomorrow at {time}',
    'time.yesterdayAt': 'yesterday at {time}',
    'dur.w': '{n}w',
    'dur.d': '{n}d',
    'dur.h': '{n}h',
    'dur.m': '{n}m',
  };

  const dicts = { ru, uk, en };
  const LOCALES = { ru: 'ru-RU', uk: 'uk-UA', en: 'en-GB' };
  const listeners = new Set();
  let lang = 'ru';
  let plural = new Intl.PluralRules(LOCALES.ru);

  function has(key) { return Object.prototype.hasOwnProperty.call(dicts[lang], key) || Object.prototype.hasOwnProperty.call(ru, key); }

  function t(key, params) {
    let v = dicts[lang][key];
    if (v === undefined) v = ru[key];
    if (v === undefined) return key;
    if (typeof v === 'object') {
      const n = params && typeof params.n === 'number' ? params.n : 0;
      v = v[plural.select(n)] ?? v.other;
    }
    if (!params) return v;
    return v.replace(/\{(\w+)\}/g, (m, k) => (params[k] == null ? m : String(params[k])));
  }

  function apply(root) {
    const scope = root || document;
    scope.querySelectorAll('[data-i18n]').forEach((el) => { el.textContent = t(el.dataset.i18n); });
    scope.querySelectorAll('[data-i18n-attr]').forEach((el) => {
      el.dataset.i18nAttr.split(';').forEach((pair) => {
        const [attr, key] = pair.split(':');
        if (attr && key) el.setAttribute(attr.trim(), t(key.trim()));
      });
    });
  }

  function setLang(next) {
    if (!dicts[next]) next = 'ru';
    if (next === lang && document.documentElement.lang === next) return;
    lang = next;
    plural = new Intl.PluralRules(LOCALES[lang]);
    document.documentElement.lang = lang;
    apply();
    listeners.forEach((fn) => fn(lang));
  }

  /* ---------- dates & durations ---------- */

  /** Parses RFC3339 and Jira-style "2026-11-09T14:24:46.807+0200" (offset without colon). */
  function parseDate(v) {
    if (v instanceof Date) return v;
    if (v == null || v === '') return null;
    const s = String(v).replace(/([+-]\d{2})(\d{2})$/, '$1:$2');
    const d = new Date(s);
    return isNaN(d.getTime()) ? null : d;
  }

  const fmtCache = new Map();
  function dtf(opts) {
    const k = lang + JSON.stringify(opts);
    let f = fmtCache.get(k);
    if (!f) { f = new Intl.DateTimeFormat(LOCALES[lang], opts); fmtCache.set(k, f); }
    return f;
  }

  function date(v, opts) {
    const d = parseDate(v);
    return d ? dtf(opts || { day: 'numeric', month: 'short', year: 'numeric' }).format(d) : '';
  }
  function time(v) {
    const d = parseDate(v);
    return d ? dtf({ hour: '2-digit', minute: '2-digit' }).format(d) : '';
  }
  function dateTime(v) {
    const d = parseDate(v);
    return d ? dtf({ day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' }).format(d) : '';
  }
  const startOfDay = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  /** "today at 18:00" / "tomorrow at 18:00" / "Fri, 13 Nov, 18:00". */
  function dayTime(v) {
    const d = parseDate(v);
    if (!d) return '';
    const days = Math.round((startOfDay(d) - startOfDay(new Date())) / 86400000);
    const tm = time(d);
    if (days === 0) return t('time.todayAt', { time: tm });
    if (days === 1) return t('time.tomorrowAt', { time: tm });
    if (days === -1) return t('time.yesterdayAt', { time: tm });
    return dtf({ weekday: 'short', day: 'numeric', month: 'short' }).format(d) + ', ' + tm;
  }
  function relative(v) {
    const d = parseDate(v);
    if (!d) return '';
    const sec = (d.getTime() - Date.now()) / 1000;
    const abs = Math.abs(sec);
    if (abs < 45) return t('time.justNow');
    const rtf = new Intl.RelativeTimeFormat(LOCALES[lang], { numeric: 'auto', style: 'short' });
    if (abs < 2700) return rtf.format(Math.round(sec / 60), 'minute');
    if (abs < 79200) return rtf.format(Math.round(sec / 3600), 'hour');
    if (abs < 26 * 86400) return rtf.format(Math.round(sec / 86400), 'day');
    if (abs < 320 * 86400) return rtf.format(Math.round(sec / (30 * 86400)), 'month');
    return rtf.format(Math.round(sec / (365 * 86400)), 'year');
  }
  /** Jira-style duration with localized units: 1w 2d 3h 15m (1d = hoursPerDay, 1w = 5d). */
  function duration(seconds, hoursPerDay) {
    const hpd = Number(hoursPerDay) > 0 ? Number(hoursPerDay) : 9;
    let m = Math.max(0, Math.floor((Number(seconds) || 0) / 60));
    const perDay = hpd * 60;
    const perWeek = perDay * 5;
    const w = Math.floor(m / perWeek); m -= w * perWeek;
    const d = Math.floor(m / perDay); m -= d * perDay;
    const h = Math.floor(m / 60); m -= h * 60;
    const parts = [];
    if (w) parts.push(t('dur.w', { n: w }));
    if (d) parts.push(t('dur.d', { n: d }));
    if (h) parts.push(t('dur.h', { n: h }));
    if (m || !parts.length) parts.push(t('dur.m', { n: m }));
    return parts.join(' ');
  }
  const cap = (str) => str.charAt(0).toLocaleUpperCase(LOCALES[lang]) + str.slice(1);
  /** "Середа, 11 листопада": weekday formatted alone (nominative), then day + month (genitive). */
  function weekdayDate(v, weekdayStyle) {
    const d = parseDate(v);
    if (!d) return '';
    return cap(dtf({ weekday: weekdayStyle || 'long' }).format(d)) + ', ' + dtf({ day: 'numeric', month: 'long' }).format(d);
  }

  /* Text date inputs (locale-independent of the OS): ru/uk "dd.mm.yyyy", en "dd/mm/yyyy"; values sent as ISO. */
  const dateSep = () => (lang === 'en' ? '/' : '.');
  /** "24.12.2026" | "24/12/2026" | "24-12-2026" | "2026-12-24" → "2026-12-24"; null if not a real date. */
  function parseInputDate(text) {
    const s = String(text || '').trim();
    let y; let m; let d;
    let r = s.match(/^(\d{4})-(\d{1,2})-(\d{1,2})$/);
    if (r) { y = +r[1]; m = +r[2]; d = +r[3]; } else {
      r = s.match(/^(\d{1,2})[./-](\d{1,2})[./-](\d{4})$/);
      if (!r) return null;
      d = +r[1]; m = +r[2]; y = +r[3];
    }
    const dt = new Date(y, m - 1, d);
    if (dt.getFullYear() !== y || dt.getMonth() !== m - 1 || dt.getDate() !== d) return null;
    return isoDate(dt);
  }
  /** "2026-12-24" → "24.12.2026" (en: "24/12/2026"); unparsable input is returned unchanged. */
  function formatInputDate(iso) {
    const r = String(iso || '').match(/^(\d{4})-(\d{2})-(\d{2})$/);
    return r ? [r[3], r[2], r[1]].join(dateSep()) : String(iso || '');
  }

  /** Local date as "YYYY-MM-DD". */
  function isoDate(d) {
    const p = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  }

  window.i18n = {
    t, has, apply, setLang, parseDate, date, time, dateTime, dayTime, relative, duration, isoDate,
    weekdayDate, parseInputDate, formatInputDate,
    get dateSep() { return dateSep(); },
    languages: Object.keys(dicts),
    get lang() { return lang; },
    get locale() { return LOCALES[lang]; },
    onChange(fn) { listeners.add(fn); return () => listeners.delete(fn); },
    _dicts: dicts,
  };
})();

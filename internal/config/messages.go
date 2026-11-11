package config

import "fmt"

// messages holds validation messages per language.
var messages = map[string]map[string]string{
	"en": {
		"url":              "Enter a full http(s) address, e.g. https://jira.example.com",
		"language":         "Choose ru, uk or en",
		"clock":            "Use the HH:MM format",
		"endAfterStart":    "The end must be after the start",
		"lunchBoth":        "Set both lunch start and end, or leave both empty",
		"range":            "Must be between %v and %v",
		"workDaysEmpty":    "Select at least one working day",
		"workDays":         "Days must be 1 (Mon) … 7 (Sun)",
		"vacationDate":     "Vacation #%d: dates must be YYYY-MM-DD",
		"vacationOrder":    "Vacation #%d: the end is before the start",
		"holiday":          "Invalid date %q, expected YYYY-MM-DD",
		"statuses":         "Add at least one status",
		"jqlLong":          "The query is too long",
		"worklogBeforeEnd": "The run time must not be earlier than the end of the workday",
		"projectsMany":     "Too many projects (at most %d)",
		"projectKey":       "Invalid project key %q",
	},
	"ru": {
		"url":              "Укажите полный адрес http(s), например https://jira.example.com",
		"language":         "Выберите ru, uk или en",
		"clock":            "Используйте формат ЧЧ:ММ",
		"endAfterStart":    "Окончание должно быть позже начала",
		"lunchBoth":        "Укажите и начало, и конец обеда или оставьте оба пустыми",
		"range":            "Допустимо от %v до %v",
		"workDaysEmpty":    "Выберите хотя бы один рабочий день",
		"workDays":         "Дни: 1 (Пн) … 7 (Вс)",
		"vacationDate":     "Отпуск №%d: даты в формате ГГГГ-ММ-ДД",
		"vacationOrder":    "Отпуск №%d: окончание раньше начала",
		"holiday":          "Неверная дата %q, нужен формат ГГГГ-ММ-ДД",
		"statuses":         "Добавьте хотя бы один статус",
		"jqlLong":          "Слишком длинный запрос",
		"worklogBeforeEnd": "Время запуска не может быть раньше конца рабочего дня",
		"projectsMany":     "Слишком много проектов (не больше %d)",
		"projectKey":       "Неверный ключ проекта %q",
	},
	"uk": {
		"url":              "Вкажіть повну адресу http(s), наприклад https://jira.example.com",
		"language":         "Оберіть ru, uk або en",
		"clock":            "Використовуйте формат ГГ:ХХ",
		"endAfterStart":    "Кінець має бути пізніше за початок",
		"lunchBoth":        "Вкажіть і початок, і кінець обіду або залиште обидва порожніми",
		"range":            "Допустимо від %v до %v",
		"workDaysEmpty":    "Оберіть хоча б один робочий день",
		"workDays":         "Дні: 1 (Пн) … 7 (Нд)",
		"vacationDate":     "Відпустка №%d: дати у форматі РРРР-ММ-ДД",
		"vacationOrder":    "Відпустка №%d: кінець раніше за початок",
		"holiday":          "Невірна дата %q, потрібен формат РРРР-ММ-ДД",
		"statuses":         "Додайте хоча б один статус",
		"jqlLong":          "Занадто довгий запит",
		"worklogBeforeEnd": "Час запуску не може бути раніше за кінець робочого дня",
		"projectsMany":     "Забагато проєктів (не більше %d)",
		"projectKey":       "Невірний ключ проєкту %q",
	},
}

func msg(lang, key string, args ...any) string {
	m, ok := messages[lang]
	if !ok {
		m = messages["en"]
	}
	f, ok := m[key]
	if !ok {
		f = messages["en"][key]
	}
	if len(args) == 0 {
		return f
	}
	return fmt.Sprintf(f, args...)
}

// RangeMessage is the localized "must be between min and max" field error.
func RangeMessage(lang string, min, max any) string { return msg(lang, "range", min, max) }

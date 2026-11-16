package worklog

// Triggers of a worklog run.
const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
	TriggerPreview  = "preview"
	// TriggerCatchup marks a past day handled by missed-day detection
	// ("nothing to add" found automatically).
	TriggerCatchup = "catchup"
)

// Item results.
const (
	ResultLogged  = "logged"
	ResultSkipped = "skipped"
	ResultError   = "error"
)

// Roles I can hold on an issue (WorklogItem.roles), in contract order.
const (
	RoleDev       = "dev"
	RoleQA        = "qa"
	RoleReviewer  = "reviewer"
	RoleAuthor    = "author"
	RoleCommenter = "commenter"
)

// RoleOrder is the order of roles in WorklogItem.roles / RelatedItem.roles.
var RoleOrder = []string{RoleDev, RoleQA, RoleReviewer, RoleAuthor, RoleCommenter}

// Sources of a fixed-time part (createSource / commentSource).
const (
	SourceDefault = "default"
	SourceGroup   = "group"
	SourceManual  = "manual"
)

// Item is one issue of a worklog run (WorklogItem in the JS contract).
type Item struct {
	Key     string   `json:"key"`
	Summary string   `json:"summary"`
	Status  string   `json:"status"`
	Roles   []string `json:"roles"`
	// Comments is the number of my comments on the issue that day.
	Comments int `json:"comments"`
	// CreateSeconds / CommentSeconds are the effective fixed parts (author /
	// commenter); their sources are set only when the role holds.
	CreateSeconds  int64  `json:"createSeconds"`
	CommentSeconds int64  `json:"commentSeconds"`
	CreateSource   string `json:"createSource,omitempty"`
	CommentSource  string `json:"commentSource,omitempty"`
	FixedSeconds   int64  `json:"fixedSeconds"`
	// ShareSeconds is the status share (time in my status roles' statuses).
	ShareSeconds  int64  `json:"shareSeconds"`
	LoggedSeconds int64  `json:"loggedSeconds"`
	TodoSeconds   int64  `json:"todoSeconds"`
	Duration      string `json:"duration"`
	Started       string `json:"started"`
	// Comment is the worklog description sent to Jira (v0.8.2), e.g.
	// "Разработка; создание задачи; комментарии (2)".
	Comment string `json:"comment,omitempty"`
	Result  string `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Run is the result of a worklog run or preview (WorklogRun in the JS contract).
type Run struct {
	At           string `json:"at"`
	Date         string `json:"date"`
	Trigger      string `json:"trigger"`
	Items        []Item `json:"items"`
	TotalSeconds int64  `json:"totalSeconds"`
	// OtherLoggedSeconds is my time logged today on issues that are not items
	// of this run; it reduces the daily cap.
	OtherLoggedSeconds int64 `json:"otherLoggedSeconds"`
	// CapSeconds is the effective daily cap (dailyCapHours × loadFactor).
	CapSeconds    int64  `json:"capSeconds"`
	Total         string `json:"total"`
	SkippedReason string `json:"skippedReason,omitempty"`
	// NonWorkingDay is set when a past day that is not a working day
	// ("weekend", "holiday", "vacation") was computed on request anyway.
	NonWorkingDay string `json:"nonWorkingDay,omitempty"`
	Error         string `json:"error,omitempty"`
	// Related are my other issues without activity that day: preview only,
	// never stored in the history.
	Related []RelatedItem `json:"related,omitempty"`
}

// RelatedItem is an issue where I hold a role but that has nothing to log
// that day (RelatedItem in the JS contract).
type RelatedItem struct {
	Key     string   `json:"key"`
	Summary string   `json:"summary"`
	Status  string   `json:"status"`
	Roles   []string `json:"roles"`
}

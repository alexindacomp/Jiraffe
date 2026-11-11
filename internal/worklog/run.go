package worklog

// Triggers of a worklog run.
const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
	TriggerPreview  = "preview"
)

// Item results.
const (
	ResultLogged  = "logged"
	ResultSkipped = "skipped"
	ResultError   = "error"
)

// Item is one issue of a worklog run (WorklogItem in the JS contract).
type Item struct {
	Key           string `json:"key"`
	Summary       string `json:"summary"`
	Status        string `json:"status"`
	ShareSeconds  int64  `json:"shareSeconds"`
	LoggedSeconds int64  `json:"loggedSeconds"`
	TodoSeconds   int64  `json:"todoSeconds"`
	Duration      string `json:"duration"`
	Started       string `json:"started"`
	Result        string `json:"result,omitempty"`
	Error         string `json:"error,omitempty"`
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
	OtherLoggedSeconds int64  `json:"otherLoggedSeconds"`
	Total              string `json:"total"`
	SkippedReason      string `json:"skippedReason,omitempty"`
	Error              string `json:"error,omitempty"`
}

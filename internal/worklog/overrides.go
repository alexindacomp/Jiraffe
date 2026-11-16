package worklog

// Limits of DayOverrides (SPEC v0.8).
const (
	MaxOverrideSeconds = 86400
	MaxOverrideIssues  = 200
)

// IssueOverride holds the manual fixed-time parts of one issue for one day;
// nil means "not set" (0 is a valid value).
type IssueOverride struct {
	CreateSeconds  *int64 `json:"createSeconds,omitempty"`
	CommentSeconds *int64 `json:"commentSeconds,omitempty"`
}

// DayOverrides are the user's fixed-time overrides of one day (DayOverrides
// in the JS contract): group totals and per-issue values.
type DayOverrides struct {
	CreateTotalSeconds  *int64                   `json:"createTotalSeconds,omitempty"`
	CommentTotalSeconds *int64                   `json:"commentTotalSeconds,omitempty"`
	Issues              map[string]IssueOverride `json:"issues"`
}

// Normalize drops issues without values and makes Issues non-nil.
func (d *DayOverrides) Normalize() {
	out := make(map[string]IssueOverride, len(d.Issues))
	for k, v := range d.Issues {
		if v.CreateSeconds != nil || v.CommentSeconds != nil {
			out[k] = v
		}
	}
	d.Issues = out
}

// Empty reports whether nothing is set.
func (d DayOverrides) Empty() bool {
	if d.CreateTotalSeconds != nil || d.CommentTotalSeconds != nil {
		return false
	}
	for _, v := range d.Issues {
		if v.CreateSeconds != nil || v.CommentSeconds != nil {
			return false
		}
	}
	return true
}

// Clone returns a deep copy.
func (d DayOverrides) Clone() DayOverrides {
	c := DayOverrides{
		CreateTotalSeconds:  clonePtr(d.CreateTotalSeconds),
		CommentTotalSeconds: clonePtr(d.CommentTotalSeconds),
		Issues:              make(map[string]IssueOverride, len(d.Issues)),
	}
	for k, v := range d.Issues {
		c.Issues[k] = IssueOverride{CreateSeconds: clonePtr(v.CreateSeconds), CommentSeconds: clonePtr(v.CommentSeconds)}
	}
	return c
}

func clonePtr(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// Valid reports whether every value is 0…MaxOverrideSeconds and a whole
// number of minutes, there are at most MaxOverrideIssues issues and every
// key is an issue key (see ValidKey).
func (d DayOverrides) Valid() bool {
	if len(d.Issues) > MaxOverrideIssues {
		return false
	}
	ok := func(p *int64) bool { return p == nil || *p >= 0 && *p <= MaxOverrideSeconds && *p%60 == 0 }
	if !ok(d.CreateTotalSeconds) || !ok(d.CommentTotalSeconds) {
		return false
	}
	for k, v := range d.Issues {
		if !ValidKey(k) || !ok(v.CreateSeconds) || !ok(v.CommentSeconds) {
			return false
		}
	}
	return true
}

// ValidKey checks an issue key against ^[A-Z][A-Z0-9_]*-\d+$ (by hand: the
// regexp package costs ≈250 KB in the exe).
func ValidKey(key string) bool {
	if len(key) == 0 || len(key) > 64 || key[0] < 'A' || key[0] > 'Z' {
		return false
	}
	i := 1
	for i < len(key) && (key[i] >= 'A' && key[i] <= 'Z' || key[i] >= '0' && key[i] <= '9' || key[i] == '_') {
		i++
	}
	if i >= len(key)-1 || key[i] != '-' {
		return false
	}
	for i++; i < len(key); i++ {
		if key[i] < '0' || key[i] > '9' {
			return false
		}
	}
	return true
}

// FixedIssue is an issue with fixed time that day: Created when I created
// it that day (author role on), Comments = my comments on it that day
// (commenter role on).
type FixedIssue struct {
	Key      string
	Created  bool
	Comments int
}

// FixedPart is the effective fixed time of one issue; a source is set only
// when its role holds.
type FixedPart struct {
	CreateSeconds  int64
	CommentSeconds int64
	CreateSource   string
	CommentSource  string
}

// Seconds is the whole fixed time (create + comment part).
func (p FixedPart) Seconds() int64 { return p.CreateSeconds + p.CommentSeconds }

// FixedTime computes the create / comment parts of each issue. Per part the
// precedence is: the issue's manual value; else the day's group total split
// evenly (create: per created issue without a manual value; comment: per
// comment on issues without a manual value), rounded down to whole minutes;
// else the default (createDefault once, commentDefault per comment).
func FixedTime(issues []FixedIssue, ov DayOverrides, createDefault, commentDefault int64) map[string]FixedPart {
	var createShares, commentShares int64
	for _, is := range issues {
		o := ov.Issues[is.Key]
		if is.Created && o.CreateSeconds == nil {
			createShares++
		}
		if is.Comments > 0 && o.CommentSeconds == nil {
			commentShares += int64(is.Comments)
		}
	}
	out := make(map[string]FixedPart, len(issues))
	for _, is := range issues {
		o := ov.Issues[is.Key]
		var p FixedPart
		if is.Created {
			switch {
			case o.CreateSeconds != nil:
				p.CreateSeconds, p.CreateSource = *o.CreateSeconds, SourceManual
			case ov.CreateTotalSeconds != nil:
				p.CreateSeconds, p.CreateSource = wholeMinutes(*ov.CreateTotalSeconds/createShares), SourceGroup
			default:
				p.CreateSeconds, p.CreateSource = createDefault, SourceDefault
			}
		}
		if is.Comments > 0 {
			n := int64(is.Comments)
			switch {
			case o.CommentSeconds != nil:
				p.CommentSeconds, p.CommentSource = *o.CommentSeconds, SourceManual
			case ov.CommentTotalSeconds != nil:
				p.CommentSeconds, p.CommentSource = wholeMinutes(*ov.CommentTotalSeconds*n/commentShares), SourceGroup
			default:
				p.CommentSeconds, p.CommentSource = commentDefault*n, SourceDefault
			}
		}
		out[is.Key] = p
	}
	return out
}

// wholeMinutes rounds seconds down to whole minutes.
func wholeMinutes(s int64) int64 { return s / 60 * 60 }

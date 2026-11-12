package app

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"jiraffe/internal/jira"
)

// Field kinds of FieldMeta.kind (SPEC v0.5).
const (
	FKSummary     = "summary"
	FKDescription = "description"
	FKText        = "text"
	FKTextarea    = "textarea"
	FKNumber      = "number"
	FKDate        = "date"
	FKDateTime    = "datetime"
	FKSelect      = "select"
	FKMultiselect = "multiselect"
	FKPriority    = "priority"
	FKUser        = "user"
	FKUsers       = "users"
	FKLabels      = "labels"
	FKComponents  = "components"
	FKVersions    = "versions"
	FKEpic        = "epic"
	FKEpicName    = "epicName"
	FKSprint      = "sprint"
	FKParent      = "parent"
	FKEstimate    = "estimate"
	FKStoryPoints = "storyPoints"
	FKUnsupported = "unsupported"
)

// Length limits of written strings.
const (
	maxSummary     = 255
	maxText        = 255
	maxDescription = 32000
	maxListValues  = 100
	maxSearchQuery = 100
	maxPicked      = 20
	maxSprintOpts  = 50
)

// Cache keys and lifetimes of the edit metadata.
const (
	cacheCreateMeta = "createmeta"
	cacheDescFormat = "descfmt"
	createMetaTTL   = 10 * time.Minute
	descFormatTTL   = 30 * time.Minute
	descSample      = 10
)

// Description formats.
const (
	DescHTML = "html"
	DescWiki = "wiki"
)

// OptionDTO is an allowed value of a field. For sprint options id is the
// sprint id as a string and state is "active" or "future".
type OptionDTO struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
}

// FieldMetaDTO is the FieldMeta contract type.
type FieldMetaDTO struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Kind     string      `json:"kind"`
	Required bool        `json:"required"`
	Main     bool        `json:"main"`
	Options  []OptionDTO `json:"options,omitempty"`
	Default  any         `json:"default,omitempty"`
	Max      int         `json:"max,omitempty"`
	// agile: the sprint is not in Jira's meta and is set via the agile API.
	agile bool
}

// ProjectRefDTO is {key, name}.
type ProjectRefDTO struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// CreateTypeDTO is an issue type offered by getCreateMeta.
type CreateTypeDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Subtask bool   `json:"subtask"`
}

// CreateMetaDTO is the getCreateMeta result.
type CreateMetaDTO struct {
	Project           ProjectRefDTO   `json:"project"`
	IssueTypes        []CreateTypeDTO `json:"issueTypes"`
	IssueTypeID       string          `json:"issueTypeId"`
	Fields            []FieldMetaDTO  `json:"fields"`
	DescriptionFormat string          `json:"descriptionFormat"`
}

// EditMetaDTO is the getEditMeta result.
type EditMetaDTO struct {
	Key               string         `json:"key"`
	IssueTypeID       string         `json:"issueTypeId"`
	Project           ProjectRefDTO  `json:"project"`
	Updated           string         `json:"updated"` // opaque: pass back as expectedUpdated
	Fields            []FieldMetaDTO `json:"fields"`
	DescriptionFormat string         `json:"descriptionFormat"`
	Values            map[string]any `json:"values"`
}

// kindOf maps a Jira meta field to a FieldMeta kind and its length limit;
// "" means the field is not shown (issue type and project have their own
// controls).
func kindOf(f jira.MetaField, ids jira.FieldIDs) (string, int) {
	switch f.ID {
	case "issuetype", "project":
		return "", 0
	case "summary":
		return FKSummary, maxSummary
	case "description":
		return FKDescription, maxDescription
	case "environment":
		return FKTextarea, maxDescription
	case "assignee", "reporter":
		return FKUser, 0
	case "priority":
		return FKPriority, 0
	case "labels":
		return FKLabels, 0
	case "components":
		return FKComponents, 0
	case "fixVersions", "versions":
		return FKVersions, 0
	case "duedate":
		return FKDate, 0
	case "timetracking":
		return FKEstimate, 0
	case "parent":
		return FKParent, 0
	}
	if f.ID != "" {
		switch f.ID {
		case ids.EpicLink:
			return FKEpic, 0
		case ids.EpicName:
			return FKEpicName, maxText
		case ids.Sprint:
			return FKSprint, 0
		case ids.StoryPoints, ids.DevStoryPoints, ids.QAStoryPoints:
			if f.Schema != nil && (f.Schema.Type == "number" || f.Schema.Type == "option") {
				return FKStoryPoints, 0
			}
			return FKUnsupported, 0
		}
	}
	sc := f.Schema
	if sc == nil {
		return FKUnsupported, 0
	}
	custom := sc.Custom[strings.LastIndexByte(sc.Custom, ':')+1:]
	switch sc.Type {
	case "string":
		switch custom {
		case "textarea":
			return FKTextarea, maxDescription
		case "textfield", "url", "":
			return FKText, maxText
		}
	case "number":
		return FKNumber, 0
	case "date":
		return FKDate, 0
	case "datetime":
		return FKDateTime, 0
	case "option", "version", "securitylevel", "resolution":
		if len(f.AllowedValues) > 0 {
			return FKSelect, 0
		}
	case "priority":
		return FKPriority, 0
	case "user":
		return FKUser, 0
	case "array":
		switch sc.Items {
		case "option":
			if len(f.AllowedValues) > 0 {
				return FKMultiselect, 0
			}
		case "user":
			return FKUsers, 0
		case "string":
			if custom == "labels" || sc.System == "labels" {
				return FKLabels, 0
			}
		case "component":
			return FKComponents, 0
		case "version":
			return FKVersions, 0
		}
	}
	return FKUnsupported, 0
}

// options parses allowedValues ({id, name|value}); disabled and archived
// values are left out.
func options(raw []json.RawMessage) []OptionDTO {
	var out []OptionDTO
	for _, r := range raw {
		var o struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Value    string `json:"value"`
			Disabled bool   `json:"disabled"`
			Archived bool   `json:"archived"`
		}
		if json.Unmarshal(r, &o) != nil || o.ID == "" || o.Disabled || o.Archived {
			continue
		}
		name := o.Name
		if name == "" {
			name = o.Value
		}
		out = append(out, OptionDTO{ID: o.ID, Name: name})
	}
	return out
}

// mainOrder are the main fields in form order (SPEC v0.5); typeKind is the
// kind of the issue type (epic link not for epics and sub-tasks, epic name
// for epics, parent for sub-tasks).
func mainOrder(ids jira.FieldIDs, typeKind string) []string {
	out := []string{"summary", "description", "assignee", "priority"}
	if typeKind != KindEpic && typeKind != KindSubtask {
		out = append(out, ids.EpicLink)
	}
	if typeKind == KindEpic {
		out = append(out, ids.EpicName)
	}
	if typeKind == KindSubtask {
		out = append(out, "parent")
	}
	out = append(out, ids.Sprint, ids.StoryPoints, ids.DevStoryPoints, ids.QAStoryPoints,
		"labels", "components", "fixVersions", "duedate", "timetracking")
	return out
}

// buildFields turns Jira meta fields into the form: the main fields present
// (in their order) and every other required field (in Jira's order);
// unsupported main fields that are not required are left out. A required
// reporter without a default defaults to me (Jira Server fills it in).
func buildFields(meta []jira.MetaField, ids jira.FieldIDs, typeKind string, me *jira.User) []FieldMetaDTO {
	order := map[string]int{}
	for i, id := range mainOrder(ids, typeKind) {
		if id != "" {
			if _, dup := order[id]; !dup {
				order[id] = i
			}
		}
	}
	var mains, extras []FieldMetaDTO
	for _, f := range meta {
		kind, max := kindOf(f, ids)
		if kind == "" || f.ID == "" {
			continue
		}
		_, isMain := order[f.ID]
		d := FieldMetaDTO{ID: f.ID, Name: f.Name, Kind: kind, Required: f.Required, Main: isMain, Max: max}
		if d.Name == "" {
			d.Name = f.ID
		}
		switch kind {
		case FKSelect, FKMultiselect, FKPriority, FKComponents, FKVersions, FKStoryPoints:
			d.Options = options(f.AllowedValues)
		}
		if f.HasDefaultValue && len(f.DefaultValue) > 0 {
			d.Default = readValue(d, f.DefaultValue)
		}
		if f.ID == "reporter" && f.Required && d.Default == nil && me != nil && me.Name != "" {
			d.Default = *userDTO(me)
		}
		switch {
		case isMain && kind != FKUnsupported:
			mains = append(mains, d)
		case f.Required:
			d.Main = false
			extras = append(extras, d)
		}
	}
	sort.SliceStable(mains, func(i, j int) bool { return order[mains[i].ID] < order[mains[j].ID] })
	return append(mains, extras...)
}

// readValue maps a raw Jira value to a FieldValue of the field's kind
// (nil when empty or not understood).
func readValue(f FieldMetaDTO, raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	type idObj struct {
		ID string `json:"id"`
	}
	type userObj struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}
	switch f.Kind {
	case FKSummary, FKDescription, FKText, FKTextarea, FKEpicName:
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	case FKNumber:
		var n float64
		if json.Unmarshal(raw, &n) == nil {
			return n
		}
	case FKStoryPoints:
		if len(f.Options) > 0 {
			var o idObj
			if json.Unmarshal(raw, &o) == nil && o.ID != "" {
				return map[string]string{"id": o.ID}
			}
			return nil
		}
		var n float64
		if json.Unmarshal(raw, &n) == nil {
			return n
		}
	case FKDate:
		var s string
		if json.Unmarshal(raw, &s) == nil && len(s) >= 10 {
			return s[:10]
		}
	case FKDateTime:
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return rfc3339(s)
		}
	case FKSelect, FKPriority:
		var o idObj
		if json.Unmarshal(raw, &o) == nil && o.ID != "" {
			return map[string]string{"id": o.ID}
		}
	case FKMultiselect, FKComponents, FKVersions:
		var os []idObj
		if json.Unmarshal(raw, &os) == nil {
			out := []string{}
			for _, o := range os {
				if o.ID != "" {
					out = append(out, o.ID)
				}
			}
			return out
		}
	case FKLabels:
		var ls []string
		if json.Unmarshal(raw, &ls) == nil {
			return append([]string{}, ls...)
		}
	case FKUser:
		var u userObj
		if json.Unmarshal(raw, &u) == nil && u.Name != "" {
			return UserDTO{Name: u.Name, DisplayName: orElse(u.DisplayName, u.Name)}
		}
	case FKUsers:
		var us []userObj
		if json.Unmarshal(raw, &us) == nil {
			out := []UserDTO{}
			for _, u := range us {
				if u.Name != "" {
					out = append(out, UserDTO{Name: u.Name, DisplayName: orElse(u.DisplayName, u.Name)})
				}
			}
			return out
		}
	case FKEpic:
		var s string
		if json.Unmarshal(raw, &s) == nil && jira.ValidIssueKey(s) {
			return IssuePickDTO{Key: s}
		}
	case FKParent:
		var p struct {
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
			} `json:"fields"`
		}
		if json.Unmarshal(raw, &p) == nil && p.Key != "" {
			return IssuePickDTO{Key: p.Key, Summary: p.Fields.Summary}
		}
	case FKSprint:
		for _, s := range jira.ParseSprints(raw) {
			if s.State == jira.SprintActive || s.State == jira.SprintFuture {
				return s.ID
			}
		}
	case FKEstimate:
		var tt struct {
			OriginalEstimate  string `json:"originalEstimate"`
			RemainingEstimate string `json:"remainingEstimate"`
		}
		if json.Unmarshal(raw, &tt) == nil && (tt.OriginalEstimate != "" || tt.RemainingEstimate != "") {
			return EstimateDTO{OriginalEstimate: tt.OriginalEstimate, RemainingEstimate: tt.RemainingEstimate}
		}
	}
	return nil
}

// IssuePickDTO is the FieldValue of epic and parent fields.
type IssuePickDTO struct {
	Key     string `json:"key"`
	Summary string `json:"summary"`
}

// EstimateDTO is the FieldValue of the estimate field.
type EstimateDTO struct {
	OriginalEstimate  string `json:"originalEstimate,omitempty"`
	RemainingEstimate string `json:"remainingEstimate,omitempty"`
}

// sprintOptions are the active and future sprints of the project's boards
// (active first; shared boards counted once), from the agile caches.
func (a *App) sprintOptions(ctx context.Context, c *jira.Client, identity, project string) []OptionDTO {
	boards, err := a.projectBoards(ctx, c, identity, project, false)
	if err != nil {
		if !permanent(err) {
			log.Printf("sprint options of %s: %v", project, err)
		}
		return nil
	}
	seen := map[int]bool{}
	var active, future []OptionDTO
	for _, b := range boards {
		l, err := a.boardSprints(ctx, c, identity, b.ID, false)
		if err != nil {
			log.Printf("sprint options of board %d: %v", b.ID, err)
			continue
		}
		for _, s := range l.open {
			if seen[s.ID] || s.ID <= 0 {
				continue
			}
			seen[s.ID] = true
			o := OptionDTO{ID: strconv.Itoa(s.ID), Name: s.Name, State: s.State}
			if s.State == jira.SprintActive {
				active = append(active, o)
			} else if s.State == jira.SprintFuture {
				future = append(future, o)
			}
		}
	}
	out := append(active, future...)
	if len(out) > maxSprintOpts {
		out = out[:maxSprintOpts]
	}
	return out
}

// addSprint gives the sprint field its options (open/future sprints of the
// project's boards). When Jira's meta has no sprint field but the project
// has sprints, a sprint field written through the agile API is added —
// except for sub-tasks, which follow their parent's sprint.
func (a *App) addSprint(ctx context.Context, c *jira.Client, identity, project string, v *view, fields []FieldMetaDTO, typeKind string) []FieldMetaDTO {
	sid := v.meta.fields.Sprint
	if sid == "" {
		return fields
	}
	opts := a.sprintOptions(ctx, c, identity, project)
	for i := range fields {
		if fields[i].ID == sid {
			fields[i].Options = opts
			return fields
		}
	}
	if len(opts) == 0 || typeKind == KindSubtask {
		return fields
	}
	sf := FieldMetaDTO{ID: sid, Name: T(a.Language(), "field_sprint"), Kind: FKSprint, Main: true, Options: opts, agile: true}
	// Insert after the main fields that come before Sprint in the order.
	pos := len(fields)
	order := mainOrder(v.meta.fields, "")
	rank := map[string]int{}
	for i, id := range order {
		rank[id] = i
	}
	for i, f := range fields {
		if r, ok := rank[f.ID]; !f.Main || ok && r > rank[sid] {
			pos = i
			break
		}
	}
	return append(fields[:pos], append([]FieldMetaDTO{sf}, fields[pos:]...)...)
}

// descriptionFormat of a raw description: html / wiki, "" when empty.
func descriptionFormat(raw string) string {
	switch {
	case strings.TrimSpace(raw) == "":
		return ""
	case looksLikeHTML(raw):
		return DescHTML
	}
	return DescWiki
}

// projectDescriptionFormat is "html" when most of the latest (≤ 10)
// issues of the project with a description use HTML, else "wiki"; cached
// per project.
func (a *App) projectDescriptionFormat(ctx context.Context, c *jira.Client, identity, project string) string {
	f, _ := a.projectDescriptionFormatKnown(ctx, c, identity, project)
	return f
}

// descFormat is a cached project description format; known: it comes from a
// real sample (not the "wiki" default of a project without described
// issues or after a failed search).
type descFormat struct {
	format string
	known  bool
}

// projectDescriptionFormatKnown is projectDescriptionFormat plus whether
// the format is known from a sample rather than guessed.
func (a *App) projectDescriptionFormatKnown(ctx context.Context, c *jira.Client, identity, project string) (string, bool) {
	key := cacheDescFormat + ":" + project
	if cached, _, ok := a.cacheGet(key, identity, descFormatTTL); ok {
		d := cached.(descFormat)
		return d.format, d.known
	}
	_, gen, _ := a.cacheGet(key, identity, 0)
	jql := "project = " + quoteKeys([]string{project}) + " AND description is not EMPTY ORDER BY updated DESC"
	issues, err := c.Search(ctx, jql, jira.SearchOptions{Fields: []string{"description"}, Limit: descSample})
	if err != nil {
		log.Printf("description format of %s: %v", project, err)
		return DescWiki, false
	}
	html, n := 0, 0
	for _, is := range issues {
		switch descriptionFormat(is.Fields.Description) {
		case DescHTML:
			html++
			n++
		case DescWiki:
			n++
		}
	}
	f := DescWiki
	if n > 0 && html*2 > n {
		f = DescHTML
	}
	a.cachePut(key, identity, gen, a.clock.Now(), descFormat{format: f, known: n > 0})
	return f, n > 0
}

// projectRef returns the key and name of an accessible project.
func (a *App) projectRef(ctx context.Context, c *jira.Client, identity, key string) ProjectRefDTO {
	ref := ProjectRefDTO{Key: key}
	if ps, err := a.accessibleProjects(ctx, c, identity, false); err == nil {
		for _, p := range ps {
			if strings.EqualFold(p.Key, key) {
				ref.Name = p.Name
			}
		}
	}
	return ref
}

// createMeta is a create form: the issue types and the fields of one.
type createMeta struct {
	dto         CreateMetaDTO
	formatKnown bool // DescriptionFormat comes from a real sample
}

// createTypes are the cached issue types of a project's create screen.
type createTypes struct {
	project ProjectRefDTO
	types   []CreateTypeDTO
}

func (a *App) getCreateMeta(ctx context.Context, project, typeID string) (*CreateMetaDTO, error) {
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	m, err := a.createMetaFor(ctx, c, id, project, typeID)
	if err != nil {
		return nil, err
	}
	return &m.dto, nil
}

// createMetaFor builds the create form of project and type ("" or a type
// not offered ⇒ the first non-subtask type). The type list is cached per
// project and the fields per resolved type, so the cache cannot grow with
// arbitrary type ids.
func (a *App) createMetaFor(ctx context.Context, c *jira.Client, id, project, typeID string) (*createMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if strings.TrimSpace(project) == "" {
		return nil, a.badParams("project is required")
	}
	project, err := a.projectParam(ctx, c, id, project)
	if err != nil {
		return nil, err
	}
	if typeID != "" && !isNumeric(typeID) {
		return nil, a.badParams("issueTypeId must be a numeric id")
	}
	v, err := a.newView(ctx, c, id)
	if err != nil {
		return nil, err
	}
	tkey := cacheCreateMeta + ":types:" + project
	var ct *createTypes
	if cached, _, ok := a.cacheGet(tkey, id, createMetaTTL); ok {
		ct = cached.(*createTypes)
	} else {
		_, gen, _ := a.cacheGet(tkey, id, 0)
		types, err := c.CreateMetaIssueTypes(ctx, project)
		if err != nil {
			return nil, a.editErr(err)
		}
		ct = &createTypes{project: a.projectRef(ctx, c, id, project), types: []CreateTypeDTO{}}
		for _, t := range types {
			jt := t
			ct.types = append(ct.types, CreateTypeDTO{ID: t.ID, Name: t.Name, Kind: v.kind(&jt), Subtask: t.Subtask})
		}
		a.cachePut(tkey, id, gen, a.clock.Now(), ct)
	}
	var chosen *CreateTypeDTO
	for i := range ct.types {
		if t := &ct.types[i]; typeID != "" && t.ID == typeID || chosen == nil && !t.Subtask && typeID == "" {
			chosen = t
		}
	}
	if chosen == nil {
		for i := range ct.types {
			if !ct.types[i].Subtask {
				chosen = &ct.types[i]
				break
			}
		}
	}
	if chosen == nil {
		return nil, a.newErr(CodeForbiddenEdit, "err_forbidden_create")
	}
	// The fields and the description format are fixed together: createIssue
	// validates against the format getCreateMeta returned.
	type typeMeta struct {
		fields []FieldMetaDTO
		format string
		known  bool
	}
	fkey := cacheCreateMeta + ":fields:" + project + ":" + chosen.ID
	var tm *typeMeta
	if cached, _, ok := a.cacheGet(fkey, id, createMetaTTL); ok {
		tm = cached.(*typeMeta)
	} else {
		_, gen, _ := a.cacheGet(fkey, id, 0)
		meta, err := c.CreateMetaFields(ctx, project, chosen.ID)
		if err != nil {
			return nil, a.editErr(err)
		}
		tm = &typeMeta{fields: a.addSprint(ctx, c, id, project, v, buildFields(meta, v.meta.fields, chosen.Kind, v.me), chosen.Kind)}
		tm.format, tm.known = a.projectDescriptionFormatKnown(ctx, c, id, project)
		if tm.fields == nil {
			tm.fields = []FieldMetaDTO{}
		}
		a.cachePut(fkey, id, gen, a.clock.Now(), tm)
	}
	return &createMeta{dto: CreateMetaDTO{Project: ct.project, IssueTypes: ct.types, IssueTypeID: chosen.ID, Fields: tm.fields,
		DescriptionFormat: tm.format}, formatKnown: tm.known}, nil
}

func isNumeric(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// editState is what getEditMeta and updateIssue read about an issue.
type editState struct {
	dto  EditMetaDTO
	desc string // the current raw description
}

func (a *App) getEditMeta(ctx context.Context, key string) (*EditMetaDTO, error) {
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	st, err := a.editStateOf(ctx, c, id, key, true)
	if err != nil {
		return nil, err
	}
	return &st.dto, nil
}

// formCandidates are the edit meta fields that can be on the form for any
// issue type (a main field of some type, or required).
func formCandidates(meta []jira.MetaField, ids jira.FieldIDs) []string {
	main := map[string]bool{}
	for _, k := range []string{KindEpic, KindSubtask, KindTask} {
		for _, id := range mainOrder(ids, k) {
			main[id] = true
		}
	}
	var out []string
	for _, f := range meta {
		if kind, _ := kindOf(f, ids); kind != "" && (main[f.ID] || f.Required) {
			out = append(out, f.ID)
		}
	}
	return out
}

// editStateOf reads the edit meta of an issue and its updated, type,
// project and description; withValues also the values of the form fields.
func (a *App) editStateOf(ctx context.Context, c *jira.Client, id, key string, withValues bool) (*editState, error) {
	if !jira.ValidIssueKey(key) {
		return nil, a.newErr(CodeBadParams, "err_invalid_key")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	v, err := a.newView(ctx, c, id)
	if err != nil {
		return nil, err
	}
	meta, err := c.EditMeta(ctx, key)
	if err != nil {
		return nil, a.editErr(err)
	}
	want := []string{"updated", "issuetype", "project", "description"}
	if withValues {
		want = append(want, formCandidates(meta, v.meta.fields)...)
		if v.meta.fields.Sprint != "" {
			want = append(want, v.meta.fields.Sprint)
		}
	}
	raw, err := c.IssueFields(ctx, key, uniqueKeys(want))
	if err != nil {
		return nil, a.editErr(err)
	}
	var it jira.IssueType
	_ = json.Unmarshal(raw["issuetype"], &it)
	var proj jira.Project
	_ = json.Unmarshal(raw["project"], &proj)
	var updated, desc string
	_ = json.Unmarshal(raw["updated"], &updated)
	_ = json.Unmarshal(raw["description"], &desc)
	project := strings.ToUpper(proj.Key)
	if project == "" {
		project = strings.ToUpper(key[:strings.LastIndexByte(key, '-')])
	}
	st := &editState{desc: desc, dto: EditMetaDTO{Key: key, IssueTypeID: it.ID, Project: ProjectRefDTO{Key: project, Name: proj.Name},
		Updated: updated, Fields: []FieldMetaDTO{}, Values: map[string]any{}}}
	typeKind := v.kind(&it)
	if len(meta) > 0 { // none ⇒ no edit permission: fields stay []
		st.dto.Fields = a.addSprint(ctx, c, id, project, v, buildFields(meta, v.meta.fields, typeKind, nil), typeKind)
	}
	if !withValues {
		return st, nil
	}
	if st.dto.DescriptionFormat = descriptionFormat(desc); st.dto.DescriptionFormat == "" {
		st.dto.DescriptionFormat = a.projectDescriptionFormat(ctx, c, id, project)
	}
	var epicKeys []string
	for _, f := range st.dto.Fields {
		val := readValue(f, raw[f.ID])
		if val == nil {
			continue
		}
		if e, ok := val.(IssuePickDTO); ok && f.Kind == FKEpic {
			epicKeys = append(epicKeys, e.Key)
		}
		st.dto.Values[f.ID] = val
	}
	if len(epicKeys) > 0 { // the epic link value is a key: add its summary
		a.loadEpics(ctx, v, epicKeys)
		for id, val := range st.dto.Values {
			if e, ok := val.(IssuePickDTO); ok && e.Summary == "" {
				if info, ok := v.epics[e.Key]; ok {
					e.Summary = info.ref.Summary
					st.dto.Values[id] = e
				}
			}
		}
	}
	return st, nil
}

// --- pickers -------------------------------------------------------------------

func (a *App) searchAssignable(ctx context.Context, project, key, query string) ([]UserDTO, error) {
	c, _, err := a.client()
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	switch {
	case len(query) > maxSearchQuery:
		return nil, a.badParams("query is too long")
	case key != "" && !jira.ValidIssueKey(key):
		return nil, a.newErr(CodeBadParams, "err_invalid_key")
	case key == "" && !jira.ValidProjectKey(project):
		return nil, a.badParams("project or key is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	users, err := c.AssignableUsers(ctx, strings.ToUpper(project), key, query, maxPicked)
	if err != nil {
		return nil, err
	}
	out := make([]UserDTO, 0, len(users))
	for i := range users {
		out = append(out, *userDTO(&users[i]))
	}
	return out, nil
}

// pickerText reduces a search query to lower-case words for a JQL text
// search: letters and digits, and "-", "_", "." only between them;
// everything else (quotes, backslashes, Lucene operators and wildcards)
// becomes a space. Lower case also disables AND / OR / NOT.
func pickerText(q string) string {
	rs := []rune(q)
	word := func(i int) bool { return i >= 0 && i < len(rs) && (unicode.IsLetter(rs[i]) || unicode.IsDigit(rs[i])) }
	var b strings.Builder
	for i, r := range rs {
		switch {
		case word(i):
			b.WriteRune(unicode.ToLower(r))
		case (r == '-' || r == '_' || r == '.') && word(i-1) && word(i+1):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func (a *App) searchIssuesFor(ctx context.Context, purpose, project, query string) ([]IssueRef, error) {
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if len(query) > maxSearchQuery {
		return nil, a.badParams("query is too long")
	}
	if !jira.ValidProjectKey(project) {
		return nil, a.badParams("project is required")
	}
	var jql string
	switch purpose {
	case "epic":
		jql = "project = " + quoteKeys([]string{strings.ToUpper(project)}) + " AND issuetype = Epic AND statusCategory != Done"
	case "parent":
		jql = "project = " + quoteKeys([]string{strings.ToUpper(project)}) + " AND issuetype not in subTaskIssueTypes() AND statusCategory != Done"
	default:
		return nil, a.badParams(`purpose must be "epic" or "parent"`)
	}
	var or []string
	if t := pickerText(query); t != "" {
		or = append(or, `summary ~ "`+t+`*"`)
	}
	if k := strings.ToUpper(query); jira.ValidIssueKey(k) {
		or = append(or, "key = "+quoteKeys([]string{k}))
	}
	if len(or) > 0 {
		jql += " AND (" + strings.Join(or, " OR ") + ")"
	}
	jql += " ORDER BY updated DESC"
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	v, err := a.newView(ctx, c, id)
	if err != nil {
		return nil, err
	}
	// validateQuery=false: a key-shaped query of an issue that does not
	// exist (yet) must not fail the whole search.
	issues, err := c.Search(ctx, jql, jira.SearchOptions{Fields: []string{"summary", "status", "issuetype", "assignee"},
		Limit: maxPicked, NoValidateQuery: true})
	if err != nil {
		return nil, err
	}
	out := make([]IssueRef, 0, len(issues))
	for _, is := range issues {
		out = append(out, v.ref(is))
	}
	return out, nil
}

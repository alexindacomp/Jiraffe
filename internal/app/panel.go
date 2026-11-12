package app

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"jiraffe/internal/jira"
)

// Issue panel (SPEC v0.6): people search, MR / branch links, issue links,
// comments.

// CodeForbiddenComment: the user may not comment on the issue.
const CodeForbiddenComment = "forbidden_comment"

// Limits of the panel methods.
const (
	maxCommentRunes = 32000
	linkTypesTTL    = time.Hour
	cacheLinkTypes  = "linktypes"
)

// LinkTypeDTO is an issue link type.
type LinkTypeDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

func (a *App) searchUsers(ctx context.Context, query string) ([]UserDTO, error) {
	c, _, err := a.client()
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	switch {
	case len(query) > maxSearchQuery:
		return nil, a.badParams("query is too long")
	case query == "":
		return []UserDTO{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	users, err := c.SearchUsers(ctx, query, maxPicked)
	if err != nil {
		return nil, err
	}
	out := []UserDTO{}
	for i := range users {
		if u := userDTO(&users[i]); u.Name != "" && !isPlaceholderUser(*u) {
			out = append(out, *u)
		}
	}
	return out, nil
}

// fieldLinkURL reports whether u may be opened from a field: an absolute
// http(s) URL with a host, no user info, no spaces, control characters or
// invisible format characters (bidi controls, zero-width characters: Cf).
func fieldLinkURL(raw string) bool {
	if raw == "" || len(raw) > 2048 || !utf8.ValidString(raw) {
		return false
	}
	for _, r := range raw {
		if r <= ' ' || r == 0x7f || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.Opaque == ""
}

// openFieldLink opens a URL that is written in the issue's merge requests
// or branches field: re-read now, the URL must be one of its
// whitespace-separated tokens verbatim, or such a token with its trailing
// ".,;:)" characters removed.
func (a *App) openFieldLink(ctx context.Context, key, field, link string) error {
	c, id, err := a.client()
	if err != nil {
		return err
	}
	if !jira.ValidIssueKey(key) {
		return a.newErr(CodeBadParams, "err_invalid_key")
	}
	if !fieldLinkURL(link) {
		return a.badParams("url must be an absolute http(s) URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	m, err := a.metaFor(ctx, c, id)
	if err != nil {
		return err
	}
	var fid string
	switch field {
	case "mergeRequests":
		fid = m.fields.MergeRequests
	case "branches":
		fid = m.fields.Branches
	default:
		return a.badParams(`field must be "mergeRequests" or "branches"`)
	}
	if fid == "" {
		return a.badParams("this Jira has no such field")
	}
	raw, err := c.IssueFields(ctx, key, []string{fid})
	if err != nil {
		return err
	}
	var text string
	_ = json.Unmarshal(raw[fid], &text)
	found := false
	for _, tok := range strings.Fields(fieldText(text)) {
		// A token, or the token without trailing punctuation of the
		// sentence around it ("…/57)," ⇒ "…/57").
		if tok == link || strings.TrimRight(tok, ".,;:)") == link {
			found = true
			break
		}
	}
	if !found {
		return a.badParams("the URL is not in the field")
	}
	if err := a.plat.OpenURL(link); err != nil {
		log.Printf("open field link: %v", err)
		return a.newErr(CodeInternal, "err_open_failed")
	}
	return nil
}

func (a *App) getLinkTypes(ctx context.Context) ([]LinkTypeDTO, error) {
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	return a.linkTypes(ctx, c, id)
}

// linkTypes are the issue link types, cached for an hour.
func (a *App) linkTypes(ctx context.Context, c *jira.Client, identity string) ([]LinkTypeDTO, error) {
	if cached, _, ok := a.cacheGet(cacheLinkTypes, identity, linkTypesTTL); ok {
		return cached.([]LinkTypeDTO), nil
	}
	_, gen, _ := a.cacheGet(cacheLinkTypes, identity, 0)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	types, err := c.LinkTypes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]LinkTypeDTO, 0, len(types))
	for _, t := range types {
		out = append(out, LinkTypeDTO{ID: t.ID, Name: t.Name, Inward: t.Inward, Outward: t.Outward})
	}
	a.cachePut(cacheLinkTypes, identity, gen, a.clock.Now(), out)
	return out, nil
}

// linkErr maps Jira errors of link writes: 403 ⇒ forbidden_edit, 400 ⇒
// validation (Jira's text as the message); 404 and others as they are.
func (a *App) linkErr(err error) error {
	var je *jira.Error
	if errors.As(err, &je) && je.Code == jira.CodeBadRequest && je.Kind != jira.KindInvalidKey {
		return a.jiraValidation(je, nil)
	}
	return a.editErr(err)
}

// addIssueLink makes "key <phrase> target", phrase = the type's outward
// (direction "outward") or inward text. With Jira's POST semantics (see
// jira.CreateIssueLink) "key <outward> target" is the link source=key,
// destination=target, and "key <inward> target" is source=target,
// destination=key.
func (a *App) addIssueLink(ctx context.Context, key, typeID, direction, target string) error {
	c, id, err := a.client()
	if err != nil {
		return err
	}
	target = strings.ToUpper(strings.TrimSpace(target))
	switch {
	case !jira.ValidIssueKey(key) || !jira.ValidIssueKey(target):
		return a.newErr(CodeBadParams, "err_invalid_key")
	case strings.EqualFold(key, target):
		return a.badParams("an issue cannot be linked to itself")
	case direction != "outward" && direction != "inward":
		return a.badParams(`direction must be "outward" or "inward"`)
	}
	types, err := a.linkTypes(ctx, c, id)
	if err != nil {
		return err
	}
	known := false
	for _, t := range types {
		known = known || t.ID == typeID
	}
	if !known {
		return a.badParams("unknown link type")
	}
	source, destination := key, target
	if direction == "inward" {
		source, destination = target, key
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.CreateIssueLink(ctx, typeID, source, destination); err != nil {
		return a.linkErr(err)
	}
	a.afterWrite()
	return nil
}

// deleteIssueLink deletes a link of key (only a link listed on key).
func (a *App) deleteIssueLink(ctx context.Context, key, linkID string) error {
	c, _, err := a.client()
	if err != nil {
		return err
	}
	if !jira.ValidIssueKey(key) {
		return a.newErr(CodeBadParams, "err_invalid_key")
	}
	if !isNumeric(linkID) {
		return a.badParams("linkId must be a numeric id")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	is, err := c.GetIssue(ctx, key, []string{"issuelinks"})
	if err != nil {
		return err
	}
	owned := false
	for _, l := range is.Fields.IssueLinks {
		owned = owned || l.ID == linkID
	}
	if !owned {
		return a.badParams("the link does not belong to the issue")
	}
	if err := c.DeleteIssueLink(ctx, linkID); err != nil {
		return a.linkErr(err)
	}
	a.afterWrite()
	return nil
}

// addComment adds a comment written in the issue's current comment format
// (html: the HTML description contract, images on Jira attachment paths
// only; wiki: the full, unmasked screen).
func (a *App) addComment(ctx context.Context, key, body, format string) (*CommentDTO, error) {
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	if !jira.ValidIssueKey(key) {
		return nil, a.newErr(CodeBadParams, "err_invalid_key")
	}
	if format != DescHTML && format != DescWiki {
		return nil, a.badParams(`format must be "html" or "wiki"`)
	}
	lang := a.Language()
	body = strings.TrimSpace(body)
	errs := &fieldErrs{lang: lang}
	switch n := utf8.RuneCountInString(body); {
	case !utf8.ValidString(body):
		errs.add("body", "fe_invalid")
	case n == 0:
		errs.add("body", "fe_required")
	case n > maxCommentRunes:
		errs.add("body", "fe_too_long", maxCommentRunes)
	}
	if err := errs.err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	latest, err := c.Comments(ctx, key, maxFormatSample)
	if err != nil {
		return nil, err
	}
	current := commentFormat(latest)
	if current == "" {
		current = a.projectDescriptionFormat(ctx, c, id, strings.ToUpper(key[:strings.LastIndexByte(key, '-')]))
	}
	if format != current {
		return nil, a.badParams(`format must be "` + current + `" for this issue`)
	}
	rules := htmlRules{jiraURL: c.BaseURL()}
	if format == DescHTML || needsHTMLCheck(body) {
		if err := checkHTML(body, rules); err != nil {
			errs.add("body", "fe_html", clipText(err.Error(), maxHTMLReason))
			return nil, errs.err()
		}
	}
	cm, err := c.AddComment(ctx, key, body)
	if err != nil {
		var je *jira.Error
		switch {
		case errors.As(err, &je) && je.Code == jira.CodeForbidden:
			return nil, a.newErr(CodeForbiddenComment, "err_forbidden_comment")
		case errors.As(err, &je) && je.Code == jira.CodeBadRequest && je.Kind != jira.KindInvalidKey:
			e := a.jiraValidation(je, []FieldMetaDTO{{ID: "comment"}})
			for i := range e.Fields {
				if e.Fields[i].Field == "comment" {
					e.Fields[i].Field = "body"
				}
			}
			return nil, e
		}
		return nil, err
	}
	// No issue cache holds comments; the comment poller skips my own ones.
	return &CommentDTO{ID: cm.ID, Author: userDTO(cm.Author), Created: rfc3339(cm.Created), Updated: rfc3339(cm.Updated), Body: cm.Body}, nil
}

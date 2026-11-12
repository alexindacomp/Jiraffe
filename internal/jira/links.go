package jira

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// LinkType is an issue link type (GET /rest/api/2/issueLinkType).
type LinkType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

// LinkTypes returns all issue link types.
func (c *Client) LinkTypes(ctx context.Context) ([]LinkType, error) {
	var out struct {
		IssueLinkTypes []LinkType `json:"issueLinkTypes"`
	}
	if err := c.do(ctx, "GET", "/rest/api/2/issueLinkType", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.IssueLinkTypes, nil
}

// CreateIssueLink links two issues so that source reads "<source> <type's
// outward phrase> <destination>" and destination reads "<destination>
// <inward phrase> <source>".
//
// Jira Server's POST /rest/api/2/issueLink names the issues the other way
// round from what one might expect: the link's source (the issue that
// shows the outward phrase) is "inwardIssue" and its destination is
// "outwardIssue" (IssueLinkResource creates the link with
// createIssueLink(inwardIssue, outwardIssue, …) = (source, destination);
// the REST example — type "Duplicate", inwardIssue HSP-1, outwardIssue
// MKY-1 — yields "HSP-1 duplicates MKY-1"). In GET /issue/{key} the source
// then lists the link with "outwardIssue": destination and the destination
// with "inwardIssue": source.
func (c *Client) CreateIssueLink(ctx context.Context, typeID, source, destination string) error {
	if !numericID(typeID) || !ValidIssueKey(source) || !ValidIssueKey(destination) {
		return &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue link"}
	}
	body := map[string]any{
		"type":         map[string]string{"id": typeID},
		"inwardIssue":  map[string]string{"key": source},
		"outwardIssue": map[string]string{"key": destination},
	}
	return c.do(ctx, "POST", "/rest/api/2/issueLink", nil, body, nil)
}

// DeleteIssueLink deletes an issue link by id.
func (c *Client) DeleteIssueLink(ctx context.Context, id string) error {
	if !numericID(id) {
		return &Error{Code: CodeBadRequest, Message: "invalid issue link id " + strconv.Quote(id)}
	}
	return c.do(ctx, "DELETE", "/rest/api/2/issueLink/"+id, nil, nil, nil)
}

// SearchUsers finds active users by user name, display name or e-mail.
func (c *Client) SearchUsers(ctx context.Context, query string, max int) ([]User, error) {
	q := url.Values{}
	q.Set("username", query)
	q.Set("maxResults", strconv.Itoa(max))
	var out []User
	if err := c.do(ctx, "GET", "/rest/api/2/user/search", q, nil, &out); err != nil {
		return nil, err
	}
	if len(out) > max {
		out = out[:max]
	}
	return out, nil
}

// AddComment adds a comment to an issue and returns it.
func (c *Client) AddComment(ctx context.Context, key, body string) (*Comment, error) {
	if !ValidIssueKey(key) {
		return nil, &Error{Code: CodeBadRequest, Kind: KindInvalidKey, Message: "invalid issue key " + strconv.Quote(key)}
	}
	var out Comment
	if err := c.do(ctx, "POST", "/rest/api/2/issue/"+url.PathEscape(key)+"/comment", nil, map[string]string{"body": body}, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.ID) == "" {
		return nil, &Error{Code: CodeBadResponse, Kind: KindJSON, Message: "unexpected response from Jira: no comment id"}
	}
	return &out, nil
}

// Package jira is the v0 implementation of tracker.Tracker backed by Jira
// Cloud REST v3. Auth is basic (email + API token).
package jira

import (
	"context"
	"fmt"
	"iter"
	"net/url"
	"time"

	"github.com/StephanSchmidt/loupe/internal/apiclient"
	"github.com/StephanSchmidt/loupe/internal/tracker"
)

const Provider = "jira-cloud"

// Client implements tracker.Tracker against Jira Cloud.
type Client struct {
	api  *apiclient.Client
	site string
}

// Compile-time assertion.
var _ tracker.Tracker = (*Client)(nil)

// New returns a Client. site is the bare Jira host (e.g.
// "acme.atlassian.net"); the constructor composes the full base URL.
// Tests pass an httptest.Server URL via the baseURL form below.
func New(site, email, apiToken string) (tracker.Tracker, error) {
	if site == "" {
		return nil, fmt.Errorf("jira: site is required")
	}
	if email == "" {
		return nil, fmt.Errorf("jira: email is required")
	}
	if apiToken == "" {
		return nil, fmt.Errorf("jira: api token is required")
	}
	return NewWithBaseURL("https://"+site, email, apiToken)
}

// NewWithBaseURL is the form used by tests against httptest.Server, where
// the base URL is already a full http://127.0.0.1:port. Production
// callers should go through New.
func NewWithBaseURL(baseURL, email, apiToken string) (tracker.Tracker, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("jira: baseURL is required")
	}
	return &Client{
		site: baseURL,
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.BasicAuth(email, apiToken)),
			apiclient.WithProviderName(Provider),
			apiclient.WithHeader("User-Agent", "loupe/dev"),
		),
	}, nil
}

func (c *Client) Name() string { return Provider }

// --- wire structs ---

type projectListWire struct {
	StartAt    int           `json:"startAt"`
	MaxResults int           `json:"maxResults"`
	Total      int           `json:"total"`
	IsLast     bool          `json:"isLast"`
	Values     []projectWire `json:"values"`
}

type projectWire struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type issueSearchWire struct {
	Issues        []issueWire `json:"issues"`
	NextPageToken string      `json:"nextPageToken,omitempty"`
}

type issueWire struct {
	ID     string      `json:"id"`
	Key    string      `json:"key"`
	Fields issueFields `json:"fields"`
}

type issueFields struct {
	Summary   string `json:"summary"`
	IssueType struct {
		Name string `json:"name"`
	} `json:"issuetype"`
	Status struct {
		Name string `json:"name"`
	} `json:"status"`
	Created        jiraTime  `json:"created"`
	ResolutionDate *jiraTime `json:"resolutiondate,omitempty"`
	Assignee       *struct {
		EmailAddress string `json:"emailAddress"`
	} `json:"assignee,omitempty"`
	Project struct {
		Key string `json:"key"`
	} `json:"project"`
	TimeEstimate float64 `json:"timeestimate"`
}

// jiraTime parses Jira's "2026-01-01T10:00:00.000+0000" timestamps, which
// time.RFC3339 doesn't accept (TZ has no colon).
type jiraTime struct {
	time.Time
}

func (t *jiraTime) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" || s == `""` {
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05-0700",
		time.RFC3339,
	} {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed.UTC()
			return nil
		}
	}
	return fmt.Errorf("parse jira time %q", s)
}

// --- ListProjects ---

func (c *Client) ListProjects(ctx context.Context) ([]tracker.Project, error) {
	var out []tracker.Project
	startAt := 0
	for {
		q := url.Values{}
		q.Set("startAt", fmt.Sprintf("%d", startAt))
		q.Set("maxResults", "50")
		var page projectListWire
		resp, err := c.api.Do(ctx, "GET", "/rest/api/3/project/search", q.Encode(), nil)
		if err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		if err := apiclient.DecodeJSON(resp, &page); err != nil {
			return nil, fmt.Errorf("decode projects: %w", err)
		}
		for _, p := range page.Values {
			out = append(out, tracker.Project{Key: p.Key, Name: p.Name})
		}
		if page.IsLast || len(page.Values) == 0 {
			break
		}
		startAt += len(page.Values)
		if startAt >= page.Total && page.Total > 0 {
			break
		}
	}
	return out, nil
}

// --- ListIssues ---

// ListIssues streams issues for projectKey updated after since (zero means
// full history). Uses Jira Cloud's `/rest/api/3/search/jql` endpoint with
// cursor pagination via nextPageToken.
func (c *Client) ListIssues(ctx context.Context, projectKey string, since time.Time) iter.Seq2[tracker.Issue, error] {
	return func(yield func(tracker.Issue, error) bool) {
		jql := fmt.Sprintf("project = %s ORDER BY updated DESC", projectKey)
		if !since.IsZero() {
			jql = fmt.Sprintf(`project = %s AND updated >= "%s" ORDER BY updated DESC`,
				projectKey, since.UTC().Format("2006-01-02 15:04"))
		}

		nextToken := ""
		for {
			q := url.Values{}
			q.Set("jql", jql)
			q.Set("maxResults", "100")
			q.Set("fields", "summary,issuetype,status,created,resolutiondate,assignee,project,timeestimate")
			if nextToken != "" {
				q.Set("nextPageToken", nextToken)
			}
			resp, err := c.api.Do(ctx, "GET", "/rest/api/3/search/jql", q.Encode(), nil)
			if err != nil {
				yield(tracker.Issue{}, fmt.Errorf("list issues %s: %w", projectKey, err))
				return
			}
			var page issueSearchWire
			if err := apiclient.DecodeJSON(resp, &page); err != nil {
				yield(tracker.Issue{}, fmt.Errorf("decode issues %s: %w", projectKey, err))
				return
			}
			for _, raw := range page.Issues {
				if !yield(issueFromWire(raw), nil) {
					return
				}
			}
			if page.NextPageToken == "" {
				return
			}
			nextToken = page.NextPageToken
		}
	}
}

func issueFromWire(raw issueWire) tracker.Issue {
	iss := tracker.Issue{
		ID:         raw.ID,
		Key:        raw.Key,
		ProjectKey: raw.Fields.Project.Key,
		Title:      raw.Fields.Summary,
		Type:       raw.Fields.IssueType.Name,
		Status:     raw.Fields.Status.Name,
		CreatedAt:  raw.Fields.Created.Time,
		Estimate:   raw.Fields.TimeEstimate,
	}
	if raw.Fields.Assignee != nil {
		iss.AssigneeEmail = raw.Fields.Assignee.EmailAddress
	}
	if raw.Fields.ResolutionDate != nil {
		t := raw.Fields.ResolutionDate.Time
		iss.ResolvedAt = &t
		iss.ClosedAt = &t
	}
	return iss
}

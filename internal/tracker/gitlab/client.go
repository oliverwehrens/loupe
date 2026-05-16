// Package gitlab is the v0.4 implementation of tracker.Tracker backed by
// GitLab's REST v4 Issues API. Auth is PRIVATE-TOKEN.
package gitlab

import (
	"context"
	"fmt"
	"iter"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/StephanSchmidt/loupe/internal/apiclient"
	"github.com/StephanSchmidt/loupe/internal/tracker"
)

const (
	Provider       = "gitlab"
	DefaultBaseURL = "https://gitlab.com"
	apiPrefix      = "/api/v4"
)

// Client implements tracker.Tracker against GitLab.
type Client struct {
	api *apiclient.Client
}

var _ tracker.Tracker = (*Client)(nil)

// New returns a Client. baseURL is the GitLab host without /api/v4 (e.g.
// https://gitlab.com). The /api/v4 prefix is appended per-request.
func New(baseURL, token string) (tracker.Tracker, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("gitlab tracker: baseURL is required")
	}
	if token == "" {
		return nil, fmt.Errorf("gitlab tracker: token is required")
	}
	return &Client{
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.HeaderAuth("PRIVATE-TOKEN", token)),
			apiclient.WithHeader("User-Agent", "loupe/dev"),
			apiclient.WithProviderName(Provider),
		),
	}, nil
}

func (c *Client) Name() string { return Provider }

// --- wire structs ---

type wireProject struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
}

type wireIssue struct {
	IID       int        `json:"iid"`
	Title     string     `json:"title"`
	State     string     `json:"state"`
	IssueType string     `json:"issue_type"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at"`
	Labels    []string   `json:"labels"`
	Assignee  *struct {
		Username string `json:"username"`
	} `json:"assignee"`
	TimeStats struct {
		TimeEstimate float64 `json:"time_estimate"`
	} `json:"time_stats"`
}

type wireStateEvent struct {
	CreatedAt time.Time `json:"created_at"`
	State     string    `json:"state"`
}

// --- ListProjects ---

// ListProjects enumerates every project visible to the credential. The
// returned Project.Key is the project's path_with_namespace, matching
// githost.RepoRef.FullName() — useful when both providers point at the same
// GitLab instance.
func (c *Client) ListProjects(ctx context.Context) ([]tracker.Project, error) {
	var out []tracker.Project
	next := apiPrefix + "/projects"
	rawQuery := "per_page=100&membership=true&archived=false&order_by=name&sort=asc&simple=true"
	for next != "" {
		var page []wireProject
		nextURL, err := c.getPage(ctx, next, rawQuery, &page)
		if err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		for _, p := range page {
			out = append(out, tracker.Project{Key: p.PathWithNamespace, Name: p.Name})
		}
		next, rawQuery = splitNextURL(nextURL)
	}
	return out, nil
}

// --- ListIssues ---

// ListIssues streams issues for a project (Key = path_with_namespace)
// updated after since. Transitions are reconstructed from
// /resource_state_events when the issue is closed.
func (c *Client) ListIssues(ctx context.Context, projectKey string, since time.Time) iter.Seq2[tracker.Issue, error] {
	return func(yield func(tracker.Issue, error) bool) {
		next := apiPrefix + "/projects/" + url.PathEscape(projectKey) + "/issues"
		q := url.Values{
			"per_page": {"100"},
			"scope":    {"all"},
			"order_by": {"updated_at"},
			"sort":     {"desc"},
		}
		if !since.IsZero() {
			q.Set("updated_after", since.UTC().Format(time.RFC3339))
		}
		rawQuery := q.Encode()
		for next != "" {
			var page []wireIssue
			nextURL, err := c.getPage(ctx, next, rawQuery, &page)
			if err != nil {
				yield(tracker.Issue{}, fmt.Errorf("list issues %s: %w", projectKey, err))
				return
			}
			for _, raw := range page {
				iss, err := c.issueFromWire(ctx, projectKey, raw)
				if err != nil {
					yield(tracker.Issue{}, err)
					return
				}
				if !yield(iss, nil) {
					return
				}
			}
			next, rawQuery = splitNextURL(nextURL)
		}
	}
}

func (c *Client) issueFromWire(ctx context.Context, projectKey string, raw wireIssue) (tracker.Issue, error) {
	iss := tracker.Issue{
		ID:         strconv.Itoa(raw.IID),
		Key:        fmt.Sprintf("%s#%d", projectKey, raw.IID),
		ProjectKey: projectKey,
		Title:      raw.Title,
		Type:       raw.IssueType,
		Status:     raw.State,
		CreatedAt:  raw.CreatedAt.UTC(),
		Estimate:   raw.TimeStats.TimeEstimate,
	}
	if raw.Assignee != nil {
		iss.AssigneeEmail = raw.Assignee.Username
	}
	if raw.ClosedAt != nil {
		t := raw.ClosedAt.UTC()
		iss.ClosedAt = &t
		iss.ResolvedAt = &t
	}
	if raw.State == "closed" {
		transitions, err := c.listStateEvents(ctx, projectKey, raw.IID)
		if err != nil {
			return tracker.Issue{}, err
		}
		iss.Transitions = transitions
	}
	return iss, nil
}

// listStateEvents fetches the resource_state_events for one issue and maps
// them to oldest-first Transition rows. GitLab's events expose only
// open/close/reopen, so transitions are coarse but enough for cycle-time
// "closed at" timing.
func (c *Client) listStateEvents(ctx context.Context, projectKey string, iid int) ([]tracker.Transition, error) {
	path := apiPrefix + "/projects/" + url.PathEscape(projectKey) +
		"/issues/" + strconv.Itoa(iid) + "/resource_state_events"
	rawQuery := "per_page=100"
	var events []wireStateEvent
	for path != "" {
		var page []wireStateEvent
		nextURL, err := c.getPage(ctx, path, rawQuery, &page)
		if err != nil {
			return nil, fmt.Errorf("list state events %s#%d: %w", projectKey, iid, err)
		}
		events = append(events, page...)
		path, rawQuery = splitNextURL(nextURL)
	}
	out := make([]tracker.Transition, 0, len(events))
	prev := "opened"
	for _, e := range events {
		out = append(out, tracker.Transition{
			At:         e.CreatedAt.UTC(),
			FromStatus: prev,
			ToStatus:   e.State,
		})
		prev = e.State
	}
	return out, nil
}

// --- helpers ---

func (c *Client) getPage(ctx context.Context, path, rawQuery string, dest any) (string, error) {
	resp, err := c.api.Do(ctx, "GET", path, rawQuery, nil)
	if err != nil {
		return "", err
	}
	nextURL := parseLinkHeader(resp.Header.Get("Link"))
	if err := apiclient.DecodeJSON(resp, dest); err != nil {
		return "", err
	}
	return nextURL, nil
}

func parseLinkHeader(h string) string {
	if h == "" {
		return ""
	}
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start := strings.Index(part, "<")
		end := strings.Index(part, ">")
		if start < 0 || end <= start+1 {
			return ""
		}
		return part[start+1 : end]
	}
	return ""
}

func splitNextURL(rawNext string) (string, string) {
	if rawNext == "" {
		return "", ""
	}
	u, err := url.Parse(rawNext)
	if err != nil {
		return "", ""
	}
	return u.Path, u.RawQuery
}

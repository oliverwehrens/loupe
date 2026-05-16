// Package gitlab is the v0.4 implementation of githost.GitHost backed by
// GitLab's REST v4 API. Auth is PRIVATE-TOKEN (PAT or group access token).
// Self-hosted GitLab is supported by passing the base URL (e.g.
// https://gitlab.example.com/api/v4) at construction.
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
	"github.com/StephanSchmidt/loupe/internal/githost"
)

const (
	// Provider is the value persisted in the `provider` column of every row
	// produced by this client.
	Provider = "gitlab"

	// DefaultBaseURL is the gitlab.com host. The /api/v4 prefix is added
	// per-request because apiclient replaces (rather than joins) the base
	// URL path. Self-hosted callers override the host via config.
	DefaultBaseURL = "https://gitlab.com"

	apiPrefix = "/api/v4"
)

// Client implements githost.GitHost against GitLab.
type Client struct {
	api *apiclient.Client
}

var _ githost.GitHost = (*Client)(nil)

// New returns a Client. baseURL is the GitLab host without the /api/v4
// suffix (e.g. https://gitlab.com or https://gitlab.acme.com). The /api/v4
// path is appended internally; tests pass an httptest.Server URL directly.
func New(baseURL, token string) (githost.GitHost, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("gitlab: baseURL is required")
	}
	if token == "" {
		return nil, fmt.Errorf("gitlab: token is required")
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

type wireGroup struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	FullPath string `json:"full_path"`
}

type wireProject struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	Namespace         struct {
		FullPath string `json:"full_path"`
	} `json:"namespace"`
}

type wireCommit struct {
	ID            string    `json:"id"`
	Message       string    `json:"message"`
	AuthorName    string    `json:"author_name"`
	AuthorEmail   string    `json:"author_email"`
	AuthoredDate  time.Time `json:"authored_date"`
	CommittedDate time.Time `json:"committed_date"`
	ParentIDs     []string  `json:"parent_ids"`
}

type wireMR struct {
	IID          int        `json:"iid"`
	Title        string     `json:"title"`
	State        string     `json:"state"`
	SourceBranch string     `json:"source_branch"`
	TargetBranch string     `json:"target_branch"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	MergedAt     *time.Time `json:"merged_at"`
	ClosedAt     *time.Time `json:"closed_at"`
	MergeCommitSHA string   `json:"merge_commit_sha"`
	Author       *struct {
		Username string `json:"username"`
		Bot      bool   `json:"bot"`
	} `json:"author"`
	Labels []string `json:"labels"`
}

// --- ListWorkspaces ---

// ListWorkspaces enumerates every group the credential can see. Personal
// project namespaces are not surfaced — typical baseline use case is an
// employer's GitLab, not personal accounts.
func (c *Client) ListWorkspaces(ctx context.Context) ([]githost.Workspace, error) {
	var out []githost.Workspace
	next := apiPrefix + "/groups"
	rawQuery := "per_page=100&all_available=true&order_by=name&sort=asc"
	for next != "" {
		var page []wireGroup
		nextURL, err := c.getPage(ctx, next, rawQuery, &page)
		if err != nil {
			return nil, fmt.Errorf("list groups: %w", err)
		}
		for _, g := range page {
			out = append(out, githost.Workspace{Slug: g.FullPath, Name: g.Name})
		}
		next, rawQuery = splitNextURL(nextURL)
	}
	return out, nil
}

// --- ListRepos ---

// ListRepos returns every project under a group, including subgroups.
// Workspace slug is the group's full_path; each Repo's Workspace stays the
// top-level group while Slug carries any subgroup path so Workspace+"/"+Slug
// reproduces path_with_namespace.
func (c *Client) ListRepos(ctx context.Context, workspaceSlug string) ([]githost.Repo, error) {
	var out []githost.Repo
	next := apiPrefix + "/groups/" + url.PathEscape(workspaceSlug) + "/projects"
	rawQuery := "per_page=100&include_subgroups=true&archived=false&order_by=name&sort=asc"
	for next != "" {
		var page []wireProject
		nextURL, err := c.getPage(ctx, next, rawQuery, &page)
		if err != nil {
			return nil, fmt.Errorf("list projects in %s: %w", workspaceSlug, err)
		}
		for _, p := range page {
			out = append(out, repoFromWire(workspaceSlug, p))
		}
		next, rawQuery = splitNextURL(nextURL)
	}
	return out, nil
}

// repoFromWire maps a GitLab project to a Loupe Repo. Workspace = the
// queried top-level group; Slug = path_with_namespace minus the workspace
// prefix, so RepoRef.FullName() == path_with_namespace.
func repoFromWire(workspace string, p wireProject) githost.Repo {
	slug := p.Path
	if p.PathWithNamespace != "" {
		// Strip "workspace/" prefix; if absent (shouldn't happen but be
		// defensive), fall back to the bare project path.
		if rest, ok := strings.CutPrefix(p.PathWithNamespace, workspace+"/"); ok {
			slug = rest
		}
	}
	return githost.Repo{
		RepoRef: githost.RepoRef{Workspace: workspace, Slug: slug},
		Name:    p.Name,
	}
}

// projectIDPath URL-encodes a Loupe RepoRef back into GitLab's
// path_with_namespace form (slashes as %2F) for use as a project ID.
func projectIDPath(repo githost.RepoRef) string {
	return url.PathEscape(repo.FullName())
}

// --- ListCommits ---

func (c *Client) ListCommits(ctx context.Context, repo githost.RepoRef, since time.Time) iter.Seq2[githost.Commit, error] {
	return func(yield func(githost.Commit, error) bool) {
		next := apiPrefix + "/projects/" + projectIDPath(repo) + "/repository/commits"
		q := url.Values{"per_page": {"100"}, "all": {"true"}}
		if !since.IsZero() {
			q.Set("since", since.UTC().Format(time.RFC3339))
		}
		rawQuery := q.Encode()
		for next != "" {
			var page []wireCommit
			nextURL, err := c.getPage(ctx, next, rawQuery, &page)
			if err != nil {
				yield(githost.Commit{}, fmt.Errorf("list commits %s: %w", repo.FullName(), err))
				return
			}
			for _, raw := range page {
				if !yield(commitFromWire(raw), nil) {
					return
				}
			}
			next, rawQuery = splitNextURL(nextURL)
		}
	}
}

func commitFromWire(raw wireCommit) githost.Commit {
	committed := raw.CommittedDate
	if committed.IsZero() {
		committed = raw.AuthoredDate
	}
	return githost.Commit{
		SHA:         raw.ID,
		AuthorEmail: raw.AuthorEmail,
		AuthorName:  raw.AuthorName,
		CommittedAt: committed.UTC(),
		Message:     raw.Message,
		ParentCount: len(raw.ParentIDs),
	}
}

// --- ListPullRequests ---

// ListPullRequests streams merge requests newest-updated-first and stops at
// the since watermark. State mapping: opened→OPEN, merged→MERGED, others
// →DECLINED.
func (c *Client) ListPullRequests(ctx context.Context, repo githost.RepoRef, since time.Time) iter.Seq2[githost.PullRequest, error] {
	return func(yield func(githost.PullRequest, error) bool) {
		next := apiPrefix + "/projects/" + projectIDPath(repo) + "/merge_requests"
		q := url.Values{
			"per_page": {"100"},
			"state":    {"all"},
			"order_by": {"updated_at"},
			"sort":     {"desc"},
			"scope":    {"all"},
		}
		if !since.IsZero() {
			q.Set("updated_after", since.UTC().Format(time.RFC3339))
		}
		rawQuery := q.Encode()
		for next != "" {
			var page []wireMR
			nextURL, err := c.getPage(ctx, next, rawQuery, &page)
			if err != nil {
				yield(githost.PullRequest{}, fmt.Errorf("list MRs %s: %w", repo.FullName(), err))
				return
			}
			for _, raw := range page {
				if !since.IsZero() && raw.UpdatedAt.Before(since) {
					return
				}
				if !yield(prFromWire(raw), nil) {
					return
				}
			}
			next, rawQuery = splitNextURL(nextURL)
		}
	}
}

func prFromWire(raw wireMR) githost.PullRequest {
	pr := githost.PullRequest{
		ID:                strconv.Itoa(raw.IID),
		Title:             raw.Title,
		SourceBranch:      raw.SourceBranch,
		DestinationBranch: raw.TargetBranch,
		CreatedAt:         raw.CreatedAt.UTC(),
		MergeCommitSHA:    raw.MergeCommitSHA,
		Labels:            append([]string(nil), raw.Labels...),
	}
	switch raw.State {
	case "opened":
		pr.State = "OPEN"
	case "merged":
		pr.State = "MERGED"
	default:
		pr.State = "DECLINED"
	}
	if raw.MergedAt != nil {
		t := raw.MergedAt.UTC()
		pr.MergedAt = &t
	}
	if raw.ClosedAt != nil {
		t := raw.ClosedAt.UTC()
		pr.ClosedAt = &t
	}
	if raw.Author != nil {
		pr.AuthorLogin = raw.Author.Username
		// GitLab doesn't return author email on MR list; the username is the
		// best identifier we have, mirrored into AuthorEmail so legacy
		// email-substring detectors keep working.
		pr.AuthorEmail = raw.Author.Username
		pr.AuthorIsBot = raw.Author.Bot
	}
	return pr
}

// --- ListPRCommits ---

func (c *Client) ListPRCommits(ctx context.Context, repo githost.RepoRef, prID string) ([]githost.Commit, error) {
	var out []githost.Commit
	next := apiPrefix + "/projects/" + projectIDPath(repo) + "/merge_requests/" + url.PathEscape(prID) + "/commits"
	rawQuery := "per_page=100"
	for next != "" {
		var page []wireCommit
		nextURL, err := c.getPage(ctx, next, rawQuery, &page)
		if err != nil {
			return nil, fmt.Errorf("list MR commits %s!%s: %w", repo.FullName(), prID, err)
		}
		for _, raw := range page {
			out = append(out, commitFromWire(raw))
		}
		next, rawQuery = splitNextURL(nextURL)
	}
	return out, nil
}

// --- helpers ---

// getPage executes a GET, decodes JSON into dest, and returns the URL from
// the Link: rel="next" header. GitLab follows RFC 5988 the same way GitHub
// does, so parsing logic is identical.
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

// Package azuredevops is the v0.4 implementation of githost.GitHost backed
// by Azure DevOps Services / Server REST API (api-version=7.1). Auth is
// PAT-as-basic-password (empty user). Self-hosted Azure DevOps Server is
// supported by passing the appropriate base URL.
package azuredevops

import (
	"context"
	"fmt"
	"iter"
	"net/url"
	"strconv"
	"time"

	"github.com/StephanSchmidt/loupe/internal/apiclient"
	"github.com/StephanSchmidt/loupe/internal/githost"
)

const (
	Provider       = "azuredevops"
	DefaultBaseURL = "https://dev.azure.com"
	apiVersion     = "7.1"
	pageSize       = 100
)

// Client implements githost.GitHost against Azure DevOps. The org maps to
// the Azure organization (the first segment of every URL); each Loupe
// Workspace maps to an Azure Team Project under that organization.
type Client struct {
	api *apiclient.Client
	org string
}

var _ githost.GitHost = (*Client)(nil)

// New returns a Client. baseURL is the Azure host (https://dev.azure.com or
// https://server.acme.com/tfs); org is the Azure organization name. Both
// are required.
func New(baseURL, org, token string) (githost.GitHost, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("azuredevops: baseURL is required")
	}
	if org == "" {
		return nil, fmt.Errorf("azuredevops: organization is required")
	}
	if token == "" {
		return nil, fmt.Errorf("azuredevops: token is required")
	}
	return &Client{
		org: org,
		api: apiclient.New(baseURL,
			// PAT goes in the password slot; empty username is the documented
			// Azure DevOps convention.
			apiclient.WithAuth(apiclient.BasicAuth("", token)),
			apiclient.WithHeader("Accept", "application/json"),
			apiclient.WithHeader("User-Agent", "loupe/dev"),
			apiclient.WithProviderName(Provider),
		),
	}, nil
}

func (c *Client) Name() string { return Provider }

// orgPath returns "/{org}".
func (c *Client) orgPath() string { return "/" + url.PathEscape(c.org) }

// projectPath returns "/{org}/{project}".
func (c *Client) projectPath(project string) string {
	return c.orgPath() + "/" + url.PathEscape(project)
}

// --- wire structs ---

type wireProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type wireRepo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Project struct {
		Name string `json:"name"`
	} `json:"project"`
}

type wireCommit struct {
	CommitID string `json:"commitId"`
	Author   struct {
		Name  string    `json:"name"`
		Email string    `json:"email"`
		Date  time.Time `json:"date"`
	} `json:"author"`
	Committer struct {
		Date time.Time `json:"date"`
	} `json:"committer"`
	Comment string `json:"comment"`
	Parents []string `json:"parents"`
}

type wirePR struct {
	PullRequestID int    `json:"pullRequestId"`
	Title         string `json:"title"`
	Status        string `json:"status"` // active | abandoned | completed | notSet
	SourceRefName string `json:"sourceRefName"`
	TargetRefName string `json:"targetRefName"`
	CreationDate  time.Time  `json:"creationDate"`
	ClosedDate    *time.Time `json:"closedDate"`
	LastMergeCommit *struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeCommit"`
	CreatedBy *struct {
		UniqueName  string `json:"uniqueName"`
		DisplayName string `json:"displayName"`
	} `json:"createdBy"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

type wireValueProjects struct {
	Value []wireProject `json:"value"`
}

type wireValueRepos struct {
	Value []wireRepo `json:"value"`
}

type wireValueCommits struct {
	Value []wireCommit `json:"value"`
}

type wireValuePRs struct {
	Value []wirePR `json:"value"`
}

// --- ListWorkspaces ---

// ListWorkspaces enumerates Team Projects under the configured
// organization. The project name doubles as the workspace slug.
func (c *Client) ListWorkspaces(ctx context.Context) ([]githost.Workspace, error) {
	var out []githost.Workspace
	skip := 0
	for {
		q := url.Values{
			"api-version": {apiVersion},
			"$top":        {strconv.Itoa(pageSize)},
			"$skip":       {strconv.Itoa(skip)},
		}
		var resp wireValueProjects
		if err := c.get(ctx, c.orgPath()+"/_apis/projects", q.Encode(), &resp); err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		for _, p := range resp.Value {
			out = append(out, githost.Workspace{Slug: p.Name, Name: p.Name})
		}
		if len(resp.Value) < pageSize {
			return out, nil
		}
		skip += len(resp.Value)
	}
}

// --- ListRepos ---

func (c *Client) ListRepos(ctx context.Context, workspaceSlug string) ([]githost.Repo, error) {
	q := url.Values{"api-version": {apiVersion}}
	var resp wireValueRepos
	if err := c.get(ctx, c.projectPath(workspaceSlug)+"/_apis/git/repositories", q.Encode(), &resp); err != nil {
		return nil, fmt.Errorf("list repos in %s: %w", workspaceSlug, err)
	}
	out := make([]githost.Repo, 0, len(resp.Value))
	for _, r := range resp.Value {
		out = append(out, githost.Repo{
			RepoRef: githost.RepoRef{Workspace: workspaceSlug, Slug: r.Name},
			Name:    r.Name,
		})
	}
	return out, nil
}

// --- ListCommits ---

// ListCommits streams commits in the given repo. Azure's commits endpoint
// uses searchCriteria.fromDate (ISO 8601, no zone) for filtering.
func (c *Client) ListCommits(ctx context.Context, repo githost.RepoRef, since time.Time) iter.Seq2[githost.Commit, error] {
	return func(yield func(githost.Commit, error) bool) {
		skip := 0
		for {
			q := url.Values{
				"api-version":              {apiVersion},
				"searchCriteria.$top":      {strconv.Itoa(pageSize)},
				"searchCriteria.$skip":     {strconv.Itoa(skip)},
				"searchCriteria.includeLinks": {"false"},
			}
			if !since.IsZero() {
				q.Set("searchCriteria.fromDate", since.UTC().Format("2006-01-02T15:04:05Z"))
			}
			path := c.projectPath(repo.Workspace) + "/_apis/git/repositories/" + url.PathEscape(repo.Slug) + "/commits"
			var resp wireValueCommits
			if err := c.get(ctx, path, q.Encode(), &resp); err != nil {
				yield(githost.Commit{}, fmt.Errorf("list commits %s: %w", repo.FullName(), err))
				return
			}
			for _, raw := range resp.Value {
				if !yield(commitFromWire(raw), nil) {
					return
				}
			}
			if len(resp.Value) < pageSize {
				return
			}
			skip += len(resp.Value)
		}
	}
}

func commitFromWire(raw wireCommit) githost.Commit {
	committed := raw.Committer.Date
	if committed.IsZero() {
		committed = raw.Author.Date
	}
	return githost.Commit{
		SHA:         raw.CommitID,
		AuthorEmail: raw.Author.Email,
		AuthorName:  raw.Author.Name,
		CommittedAt: committed.UTC(),
		Message:     raw.Comment,
		ParentCount: len(raw.Parents),
	}
}

// --- ListPullRequests ---

// ListPullRequests streams PRs (active + completed + abandoned), newest
// first. Azure orders by creationDate desc on its own; the since filter is
// applied client-side.
func (c *Client) ListPullRequests(ctx context.Context, repo githost.RepoRef, since time.Time) iter.Seq2[githost.PullRequest, error] {
	return func(yield func(githost.PullRequest, error) bool) {
		skip := 0
		for {
			q := url.Values{
				"api-version":            {apiVersion},
				"searchCriteria.status":  {"all"},
				"$top":                   {strconv.Itoa(pageSize)},
				"$skip":                  {strconv.Itoa(skip)},
			}
			path := c.projectPath(repo.Workspace) + "/_apis/git/repositories/" + url.PathEscape(repo.Slug) + "/pullrequests"
			var resp wireValuePRs
			if err := c.get(ctx, path, q.Encode(), &resp); err != nil {
				yield(githost.PullRequest{}, fmt.Errorf("list PRs %s: %w", repo.FullName(), err))
				return
			}
			for _, raw := range resp.Value {
				if !since.IsZero() && raw.CreationDate.Before(since) {
					return
				}
				if !yield(prFromWire(raw), nil) {
					return
				}
			}
			if len(resp.Value) < pageSize {
				return
			}
			skip += len(resp.Value)
		}
	}
}

func prFromWire(raw wirePR) githost.PullRequest {
	pr := githost.PullRequest{
		ID:                strconv.Itoa(raw.PullRequestID),
		Title:             raw.Title,
		SourceBranch:      trimRefsHeads(raw.SourceRefName),
		DestinationBranch: trimRefsHeads(raw.TargetRefName),
		CreatedAt:         raw.CreationDate.UTC(),
	}
	switch raw.Status {
	case "active":
		pr.State = "OPEN"
	case "completed":
		pr.State = "MERGED"
		if raw.ClosedDate != nil {
			t := raw.ClosedDate.UTC()
			pr.MergedAt = &t
			pr.ClosedAt = &t
		}
	case "abandoned":
		pr.State = "DECLINED"
		if raw.ClosedDate != nil {
			t := raw.ClosedDate.UTC()
			pr.ClosedAt = &t
		}
	default:
		pr.State = "OPEN"
	}
	if raw.LastMergeCommit != nil {
		pr.MergeCommitSHA = raw.LastMergeCommit.CommitID
	}
	if raw.CreatedBy != nil {
		pr.AuthorLogin = raw.CreatedBy.UniqueName
		pr.AuthorEmail = raw.CreatedBy.UniqueName
	}
	for _, l := range raw.Labels {
		pr.Labels = append(pr.Labels, l.Name)
	}
	return pr
}

// trimRefsHeads strips the "refs/heads/" prefix Azure uses on branch refs.
func trimRefsHeads(ref string) string {
	const prefix = "refs/heads/"
	if len(ref) > len(prefix) && ref[:len(prefix)] == prefix {
		return ref[len(prefix):]
	}
	return ref
}

// --- ListPRCommits ---

func (c *Client) ListPRCommits(ctx context.Context, repo githost.RepoRef, prID string) ([]githost.Commit, error) {
	q := url.Values{"api-version": {apiVersion}, "$top": {"100"}}
	path := c.projectPath(repo.Workspace) + "/_apis/git/repositories/" + url.PathEscape(repo.Slug) +
		"/pullRequests/" + url.PathEscape(prID) + "/commits"
	var resp wireValueCommits
	if err := c.get(ctx, path, q.Encode(), &resp); err != nil {
		return nil, fmt.Errorf("list PR commits %s!%s: %w", repo.FullName(), prID, err)
	}
	out := make([]githost.Commit, 0, len(resp.Value))
	for _, raw := range resp.Value {
		out = append(out, commitFromWire(raw))
	}
	return out, nil
}

// --- helpers ---

func (c *Client) get(ctx context.Context, path, rawQuery string, dest any) error {
	resp, err := c.api.Do(ctx, "GET", path, rawQuery, nil)
	if err != nil {
		return err
	}
	return apiclient.DecodeJSON(resp, dest)
}

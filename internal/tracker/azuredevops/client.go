// Package azuredevops is the v0.4 implementation of tracker.Tracker backed
// by Azure DevOps Services / Server work items. Issues are queried via
// WIQL, batched via the workitems endpoint, and status transitions are
// reconstructed from per-item updates.
package azuredevops

import (
	"bytes"
	"context"
	"encoding/json"
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
	Provider       = "azuredevops"
	DefaultBaseURL = "https://dev.azure.com"
	apiVersion     = "7.1"

	// workitemsBatchSize is the upstream cap on the /workitems batch
	// endpoint. Azure docs allow up to 200 IDs per call.
	workitemsBatchSize = 200

	// updatesPageSize is the page size for per-item /updates pagination.
	updatesPageSize = 100
)

type Client struct {
	api *apiclient.Client
	org string
}

var _ tracker.Tracker = (*Client)(nil)

// New returns a Client. baseURL is the Azure host; org is the Azure
// organization. Both required.
func New(baseURL, org, token string) (tracker.Tracker, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("azuredevops tracker: baseURL is required")
	}
	if org == "" {
		return nil, fmt.Errorf("azuredevops tracker: organization is required")
	}
	if token == "" {
		return nil, fmt.Errorf("azuredevops tracker: token is required")
	}
	return &Client{
		org: org,
		api: apiclient.New(baseURL,
			apiclient.WithAuth(apiclient.BasicAuth("", token)),
			apiclient.WithHeader("Accept", "application/json"),
			apiclient.WithHeader("User-Agent", "loupe/dev"),
			apiclient.WithProviderName(Provider),
		),
	}, nil
}

func (c *Client) Name() string { return Provider }

func (c *Client) projectPath(project string) string {
	return "/" + url.PathEscape(c.org) + "/" + url.PathEscape(project)
}

// --- wire types ---

type wireProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type wireValueProjects struct {
	Value []wireProject `json:"value"`
}

type wiqlResponse struct {
	WorkItems []struct {
		ID int `json:"id"`
	} `json:"workItems"`
}

type workItem struct {
	ID     int `json:"id"`
	Fields struct {
		Title        string     `json:"System.Title"`
		State        string     `json:"System.State"`
		WorkItemType string     `json:"System.WorkItemType"`
		CreatedDate  time.Time  `json:"System.CreatedDate"`
		ClosedDate   *time.Time `json:"Microsoft.VSTS.Common.ClosedDate"`
		ResolvedDate *time.Time `json:"Microsoft.VSTS.Common.ResolvedDate"`
		Estimate     float64    `json:"Microsoft.VSTS.Scheduling.OriginalEstimate"`
		AssignedTo   *struct {
			UniqueName string `json:"uniqueName"`
		} `json:"System.AssignedTo"`
	} `json:"fields"`
}

type workItemsBatch struct {
	Value []workItem `json:"value"`
}

type workItemUpdate struct {
	ID          int       `json:"id"`
	RevisedDate time.Time `json:"revisedDate"`
	Fields      map[string]struct {
		OldValue any `json:"oldValue"`
		NewValue any `json:"newValue"`
	} `json:"fields"`
}

type workItemUpdatesResp struct {
	Value []workItemUpdate `json:"value"`
}

// --- ListProjects ---

func (c *Client) ListProjects(ctx context.Context) ([]tracker.Project, error) {
	var out []tracker.Project
	skip := 0
	for {
		q := url.Values{
			"api-version": {apiVersion},
			"$top":        {"100"},
			"$skip":       {strconv.Itoa(skip)},
		}
		var resp wireValueProjects
		if err := c.get(ctx, "/"+url.PathEscape(c.org)+"/_apis/projects", q.Encode(), &resp); err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		for _, p := range resp.Value {
			out = append(out, tracker.Project{Key: p.Name, Name: p.Name})
		}
		if len(resp.Value) < 100 {
			return out, nil
		}
		skip += len(resp.Value)
	}
}

// --- ListIssues ---

// ListIssues runs a WIQL query for the project, batches the resulting IDs
// through the workitems endpoint, and yields each issue with a populated
// Transitions slice (fetched per-item from /updates). The per-item updates
// call is necessary because Azure DevOps has no inline-history equivalent
// to Jira's expand=changelog.
func (c *Client) ListIssues(ctx context.Context, projectKey string, since time.Time) iter.Seq2[tracker.Issue, error] {
	return func(yield func(tracker.Issue, error) bool) {
		ids, err := c.runWIQL(ctx, projectKey, since)
		if err != nil {
			yield(tracker.Issue{}, fmt.Errorf("list issues %s: %w", projectKey, err))
			return
		}
		for start := 0; start < len(ids); start += workitemsBatchSize {
			end := start + workitemsBatchSize
			if end > len(ids) {
				end = len(ids)
			}
			batch, err := c.fetchBatch(ctx, projectKey, ids[start:end])
			if err != nil {
				yield(tracker.Issue{}, err)
				return
			}
			for _, wi := range batch {
				transitions, err := c.fetchTransitions(ctx, projectKey, wi.ID)
				if err != nil {
					yield(tracker.Issue{}, err)
					return
				}
				if !yield(issueFromWire(projectKey, wi, transitions), nil) {
					return
				}
			}
		}
	}
}

// runWIQL POSTs a WIQL query that scopes to the project and (when set) the
// updated_after watermark. Returns work-item IDs newest-changed-first.
func (c *Client) runWIQL(ctx context.Context, projectKey string, since time.Time) ([]int, error) {
	var clauses []string
	clauses = append(clauses, fmt.Sprintf("[System.TeamProject] = '%s'", strings.ReplaceAll(projectKey, "'", "''")))
	if !since.IsZero() {
		clauses = append(clauses, fmt.Sprintf("[System.ChangedDate] >= '%s'", since.UTC().Format("2006-01-02T15:04:05Z")))
	}
	wiql := "SELECT [System.Id] FROM workitems WHERE " + strings.Join(clauses, " AND ") +
		" ORDER BY [System.ChangedDate] DESC"

	body, err := json.Marshal(map[string]string{"query": wiql})
	if err != nil {
		return nil, fmt.Errorf("marshal WIQL: %w", err)
	}
	resp, err := c.api.Do(ctx, "POST",
		c.projectPath(projectKey)+"/_apis/wit/wiql",
		"api-version="+apiVersion,
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var wiqlResp wiqlResponse
	if err := apiclient.DecodeJSON(resp, &wiqlResp); err != nil {
		return nil, fmt.Errorf("decode WIQL response: %w", err)
	}
	ids := make([]int, len(wiqlResp.WorkItems))
	for i, w := range wiqlResp.WorkItems {
		ids[i] = w.ID
	}
	return ids, nil
}

func (c *Client) fetchBatch(ctx context.Context, projectKey string, ids []int) ([]workItem, error) {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	q := url.Values{
		"ids":         {strings.Join(parts, ",")},
		"api-version": {apiVersion},
	}
	var resp workItemsBatch
	if err := c.get(ctx, c.projectPath(projectKey)+"/_apis/wit/workitems", q.Encode(), &resp); err != nil {
		return nil, fmt.Errorf("fetch workitems batch (%d ids): %w", len(ids), err)
	}
	return resp.Value, nil
}

// fetchTransitions pages through /updates for a work item and returns
// State-only transitions in oldest-first order.
func (c *Client) fetchTransitions(ctx context.Context, projectKey string, workItemID int) ([]tracker.Transition, error) {
	var out []tracker.Transition
	skip := 0
	for {
		q := url.Values{
			"api-version": {apiVersion},
			"$top":        {strconv.Itoa(updatesPageSize)},
			"$skip":       {strconv.Itoa(skip)},
		}
		var resp workItemUpdatesResp
		path := c.projectPath(projectKey) + "/_apis/wit/workItems/" + strconv.Itoa(workItemID) + "/updates"
		if err := c.get(ctx, path, q.Encode(), &resp); err != nil {
			return nil, fmt.Errorf("fetch updates for %s#%d: %w", projectKey, workItemID, err)
		}
		for _, u := range resp.Value {
			f, ok := u.Fields["System.State"]
			if !ok {
				continue
			}
			out = append(out, tracker.Transition{
				At:         u.RevisedDate.UTC(),
				FromStatus: anyToString(f.OldValue),
				ToStatus:   anyToString(f.NewValue),
			})
		}
		if len(resp.Value) < updatesPageSize {
			return out, nil
		}
		skip += len(resp.Value)
	}
}

func anyToString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func issueFromWire(projectKey string, wi workItem, transitions []tracker.Transition) tracker.Issue {
	iss := tracker.Issue{
		ID:          strconv.Itoa(wi.ID),
		Key:         fmt.Sprintf("%s#%d", projectKey, wi.ID),
		ProjectKey:  projectKey,
		Title:       wi.Fields.Title,
		Type:        wi.Fields.WorkItemType,
		Status:      wi.Fields.State,
		CreatedAt:   wi.Fields.CreatedDate.UTC(),
		Estimate:    wi.Fields.Estimate,
		Transitions: transitions,
	}
	if wi.Fields.AssignedTo != nil {
		iss.AssigneeEmail = wi.Fields.AssignedTo.UniqueName
	}
	if wi.Fields.ResolvedDate != nil {
		t := wi.Fields.ResolvedDate.UTC()
		iss.ResolvedAt = &t
	}
	if wi.Fields.ClosedDate != nil {
		t := wi.Fields.ClosedDate.UTC()
		iss.ClosedAt = &t
		if iss.ResolvedAt == nil {
			iss.ResolvedAt = &t
		}
	}
	return iss
}

// --- helpers ---

func (c *Client) get(ctx context.Context, path, rawQuery string, dest any) error {
	resp, err := c.api.Do(ctx, "GET", path, rawQuery, nil)
	if err != nil {
		return err
	}
	return apiclient.DecodeJSON(resp, dest)
}

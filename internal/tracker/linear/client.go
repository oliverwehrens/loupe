// Package linear is the v0.4 implementation of tracker.Tracker backed by
// Linear's GraphQL API. Auth is a raw Authorization header (Linear's API
// keys are not Bearer tokens).
package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"time"

	"github.com/StephanSchmidt/loupe/internal/apiclient"
	"github.com/StephanSchmidt/loupe/internal/tracker"
)

const (
	Provider       = "linear"
	DefaultBaseURL = "https://api.linear.app"
	graphqlPath    = "/graphql"

	// pageSize is Linear's max (250). We pick 100 to mirror the other
	// providers and stay well under any rate-limit-per-document budget.
	pageSize = 100
)

// Client implements tracker.Tracker against Linear.
type Client struct {
	api *apiclient.Client
}

var _ tracker.Tracker = (*Client)(nil)

// New returns a Client. baseURL is normally https://api.linear.app; tests
// pass an httptest.Server URL.
func New(baseURL, token string) (tracker.Tracker, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("linear: baseURL is required")
	}
	if token == "" {
		return nil, fmt.Errorf("linear: token is required")
	}
	return &Client{
		api: apiclient.New(baseURL,
			// Linear's API keys are passed as a raw Authorization header,
			// not "Bearer <key>".
			apiclient.WithAuth(apiclient.HeaderAuth("Authorization", token)),
			apiclient.WithHeader("User-Agent", "loupe/dev"),
			apiclient.WithProviderName(Provider),
		),
	}, nil
}

func (c *Client) Name() string { return Provider }

// --- GraphQL queries ---

const teamsQuery = `query($first: Int!, $after: String) {
  teams(first: $first, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { id key name }
  }
}`

const issuesQuery = `query($teamKey: String!, $first: Int!, $after: String) {
  issues(
    filter: { team: { key: { eq: $teamKey } } },
    first: $first, after: $after,
    orderBy: updatedAt
  ) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id identifier title createdAt completedAt canceledAt estimate
      state { name type }
      assignee { email }
      history(first: 250) {
        nodes { createdAt fromState { name } toState { name } }
      }
    }
  }
}`

const issuesSinceQuery = `query($teamKey: String!, $first: Int!, $after: String, $since: DateTimeOrDuration!) {
  issues(
    filter: { team: { key: { eq: $teamKey } }, updatedAt: { gte: $since } },
    first: $first, after: $after,
    orderBy: updatedAt
  ) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id identifier title createdAt completedAt canceledAt estimate
      state { name type }
      assignee { email }
      history(first: 250) {
        nodes { createdAt fromState { name } toState { name } }
      }
    }
  }
}`

// --- wire types ---

type teamsResp struct {
	Teams struct {
		PageInfo pageInfo   `json:"pageInfo"`
		Nodes    []teamNode `json:"nodes"`
	} `json:"teams"`
}

type teamNode struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type issuesResp struct {
	Issues struct {
		PageInfo pageInfo    `json:"pageInfo"`
		Nodes    []issueNode `json:"nodes"`
	} `json:"issues"`
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type issueNode struct {
	ID          string     `json:"id"`
	Identifier  string     `json:"identifier"`
	Title       string     `json:"title"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt"`
	CanceledAt  *time.Time `json:"canceledAt"`
	Estimate    float64    `json:"estimate"`
	State       *struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Assignee *struct {
		Email string `json:"email"`
	} `json:"assignee"`
	History struct {
		Nodes []historyNode `json:"nodes"`
	} `json:"history"`
}

type historyNode struct {
	CreatedAt time.Time `json:"createdAt"`
	FromState *struct {
		Name string `json:"name"`
	} `json:"fromState"`
	ToState *struct {
		Name string `json:"name"`
	} `json:"toState"`
}

// --- ListProjects ---

// ListProjects enumerates every Linear team. team.key is the human-readable
// prefix on issue identifiers ("ENG" → "ENG-123") and serves as
// tracker.Project.Key.
func (c *Client) ListProjects(ctx context.Context) ([]tracker.Project, error) {
	var out []tracker.Project
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 10000 {
			return nil, fmt.Errorf("list linear teams: pagination exceeded %d pages", pages)
		}
		vars := map[string]any{"first": pageSize}
		if cursor != "" {
			vars["after"] = cursor
		}
		data, err := c.api.DoGraphQL(ctx, graphqlPath, teamsQuery, vars)
		if err != nil {
			return nil, fmt.Errorf("list teams: %w", err)
		}
		var resp teamsResp
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("decode teams: %w", err)
		}
		for _, t := range resp.Teams.Nodes {
			out = append(out, tracker.Project{Key: t.Key, Name: t.Name})
		}
		if !resp.Teams.PageInfo.HasNextPage || resp.Teams.PageInfo.EndCursor == "" {
			return out, nil
		}
		if resp.Teams.PageInfo.EndCursor == cursor {
			return nil, fmt.Errorf("list teams: cursor did not advance: %q", cursor)
		}
		cursor = resp.Teams.PageInfo.EndCursor
	}
}

// --- ListIssues ---

// ListIssues streams issues for a Linear team (projectKey == team key).
// since maps to Linear's updatedAt filter; zero means full history.
func (c *Client) ListIssues(ctx context.Context, projectKey string, since time.Time) iter.Seq2[tracker.Issue, error] {
	return func(yield func(tracker.Issue, error) bool) {
		query := issuesQuery
		baseVars := map[string]any{
			"teamKey": projectKey,
			"first":   pageSize,
		}
		if !since.IsZero() {
			query = issuesSinceQuery
			baseVars["since"] = since.UTC().Format(time.RFC3339)
		}

		cursor := ""
		for pages := 0; ; pages++ {
			if pages > 10000 {
				yield(tracker.Issue{}, fmt.Errorf("list linear issues %s: pagination exceeded %d pages", projectKey, pages))
				return
			}
			vars := map[string]any{}
			for k, v := range baseVars {
				vars[k] = v
			}
			if cursor != "" {
				vars["after"] = cursor
			}
			data, err := c.api.DoGraphQL(ctx, graphqlPath, query, vars)
			if err != nil {
				yield(tracker.Issue{}, fmt.Errorf("list issues %s: %w", projectKey, err))
				return
			}
			var resp issuesResp
			if err := json.Unmarshal(data, &resp); err != nil {
				yield(tracker.Issue{}, fmt.Errorf("decode issues %s: %w", projectKey, err))
				return
			}
			for _, n := range resp.Issues.Nodes {
				if !yield(issueFromWire(projectKey, n), nil) {
					return
				}
			}
			if !resp.Issues.PageInfo.HasNextPage || resp.Issues.PageInfo.EndCursor == "" {
				return
			}
			if resp.Issues.PageInfo.EndCursor == cursor {
				yield(tracker.Issue{}, fmt.Errorf("list issues %s: cursor did not advance", projectKey))
				return
			}
			cursor = resp.Issues.PageInfo.EndCursor
		}
	}
}

func issueFromWire(projectKey string, n issueNode) tracker.Issue {
	iss := tracker.Issue{
		ID:         n.ID,
		Key:        n.Identifier,
		ProjectKey: projectKey,
		Title:      n.Title,
		CreatedAt:  n.CreatedAt.UTC(),
		Estimate:   n.Estimate,
	}
	if n.State != nil {
		// Use the workflow state name (e.g. "In Progress") rather than the
		// type ("started") so cycle-time DevStartedStatuses can match
		// user-defined workflow states.
		iss.Status = n.State.Name
	}
	if n.Assignee != nil {
		iss.AssigneeEmail = n.Assignee.Email
	}
	if n.CompletedAt != nil {
		t := n.CompletedAt.UTC()
		iss.ResolvedAt = &t
		iss.ClosedAt = &t
	} else if n.CanceledAt != nil {
		t := n.CanceledAt.UTC()
		iss.ClosedAt = &t
	}
	iss.Transitions = transitionsFromHistory(n.History.Nodes)
	return iss
}

// transitionsFromHistory flattens Linear IssueHistory into status-change
// transitions, oldest-first. Non-state-change events (assignee, priority,
// …) have nil fromState/toState and are dropped.
func transitionsFromHistory(events []historyNode) []tracker.Transition {
	if len(events) == 0 {
		return nil
	}
	out := make([]tracker.Transition, 0, len(events))
	for _, e := range events {
		if e.ToState == nil && e.FromState == nil {
			continue
		}
		var from, to string
		if e.FromState != nil {
			from = e.FromState.Name
		}
		if e.ToState != nil {
			to = e.ToState.Name
		}
		out = append(out, tracker.Transition{
			At:         e.CreatedAt.UTC(),
			FromStatus: from,
			ToStatus:   to,
		})
	}
	return out
}

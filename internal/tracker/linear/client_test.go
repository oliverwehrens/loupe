package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StephanSchmidt/loupe/internal/tracker"
)

// graphqlServer captures each incoming GraphQL request and replies from a
// FIFO list of canned response bodies. Tests assert on the captured
// requests (query body / variables) and verify the right replies were
// produced.
type graphqlServer struct {
	t        *testing.T
	srv      *httptest.Server
	requests []recordedRequest
	replies  []string
	pos      int
	gotAuth  string
}

type recordedRequest struct {
	Body string
	Vars map[string]any
}

func newGraphQLServer(t *testing.T, replies ...string) *graphqlServer {
	t.Helper()
	g := &graphqlServer{t: t, replies: replies}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		g.gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		g.requests = append(g.requests, recordedRequest{Body: req.Query, Vars: req.Variables})

		if g.pos >= len(g.replies) {
			t.Errorf("no canned reply for request #%d (have %d)", g.pos+1, len(g.replies))
			http.Error(w, "no reply", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(g.replies[g.pos]))
		g.pos++
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func newClient(t *testing.T, baseURL string) tracker.Tracker {
	t.Helper()
	c, err := New(baseURL, "lin_api_xxx")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNew_Validates(t *testing.T) {
	cases := []struct{ base, tok, want string }{
		{"", "t", "baseURL"},
		{"https://api.linear.app", "", "token"},
	}
	for _, c := range cases {
		_, err := New(c.base, c.tok)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("New(%q,%q) = %v, want substring %q", c.base, c.tok, err, c.want)
		}
	}
}

func TestListProjects_Pagination(t *testing.T) {
	g := newGraphQLServer(t,
		`{"data":{"teams":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[{"id":"t1","key":"ENG","name":"Engineering"}]}}}`,
		`{"data":{"teams":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"t2","key":"DSGN","name":"Design"}]}}}`,
	)

	c := newClient(t, g.srv.URL)
	projects, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if g.gotAuth != "lin_api_xxx" {
		t.Errorf("Authorization = %q, want raw API key (no Bearer prefix)", g.gotAuth)
	}
	if len(projects) != 2 || projects[0].Key != "ENG" || projects[1].Key != "DSGN" {
		t.Errorf("projects = %+v", projects)
	}
	if got := g.requests[1].Vars["after"]; got != "c1" {
		t.Errorf("page 2 cursor = %v, want c1", got)
	}
}

func TestListIssues_StatusAndTransitions(t *testing.T) {
	g := newGraphQLServer(t,
		`{"data":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
			{
				"id":"i1","identifier":"ENG-1","title":"Investigate","createdAt":"2026-03-01T10:00:00Z",
				"completedAt":"2026-03-05T15:00:00Z","estimate":3.0,
				"state":{"name":"Done","type":"completed"},
				"assignee":{"email":"alice@example.com"},
				"history":{"nodes":[
					{"createdAt":"2026-03-02T09:00:00Z","fromState":{"name":"Todo"},"toState":{"name":"In Progress"}},
					{"createdAt":"2026-03-05T15:00:00Z","fromState":{"name":"In Progress"},"toState":{"name":"Done"}},
					{"createdAt":"2026-03-06T08:00:00Z","fromState":null,"toState":null}
				]}
			}
		]}}}`,
	)

	c := newClient(t, g.srv.URL)
	var issues []tracker.Issue
	for iss, err := range c.ListIssues(context.Background(), "ENG", time.Time{}) {
		if err != nil {
			t.Fatalf("iter: %v", err)
		}
		issues = append(issues, iss)
	}
	if len(issues) != 1 {
		t.Fatalf("len(issues) = %d", len(issues))
	}
	iss := issues[0]
	if iss.Key != "ENG-1" || iss.Status != "Done" {
		t.Errorf("issue = %+v", iss)
	}
	if iss.AssigneeEmail != "alice@example.com" {
		t.Errorf("assignee = %q", iss.AssigneeEmail)
	}
	if iss.ClosedAt == nil || iss.ResolvedAt == nil {
		t.Errorf("expected ClosedAt+ResolvedAt for completed issue: %+v", iss)
	}
	if iss.Estimate != 3.0 {
		t.Errorf("estimate = %v", iss.Estimate)
	}
	if len(iss.Transitions) != 2 {
		// non-state-change event should be filtered out
		t.Fatalf("len(transitions) = %d, want 2 (assignment event filtered): %+v", len(iss.Transitions), iss.Transitions)
	}
	if iss.Transitions[0].FromStatus != "Todo" || iss.Transitions[0].ToStatus != "In Progress" {
		t.Errorf("transition[0] = %+v", iss.Transitions[0])
	}

	// Query selected should be the non-since variant.
	if !strings.Contains(g.requests[0].Body, "$teamKey") {
		t.Errorf("query missing $teamKey: %s", g.requests[0].Body)
	}
	if strings.Contains(g.requests[0].Body, "$since") {
		t.Errorf("zero since should not use the since-variant query")
	}
}

func TestListIssues_SinceUsesSinceQuery(t *testing.T) {
	g := newGraphQLServer(t,
		`{"data":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}`,
	)
	c := newClient(t, g.srv.URL)
	since, _ := time.Parse(time.RFC3339, "2026-03-01T00:00:00Z")
	for _, err := range c.ListIssues(context.Background(), "ENG", since) {
		if err != nil {
			t.Fatalf("iter: %v", err)
		}
	}
	if !strings.Contains(g.requests[0].Body, "$since") {
		t.Errorf("non-zero since should use the since-variant query, got: %s", g.requests[0].Body)
	}
	if got := g.requests[0].Vars["since"]; got != "2026-03-01T00:00:00Z" {
		t.Errorf("since variable = %v", got)
	}
}

func TestListIssues_GraphQLErrorPropagates(t *testing.T) {
	g := newGraphQLServer(t,
		`{"data":null,"errors":[{"message":"team not found"}]}`,
	)
	c := newClient(t, g.srv.URL)
	var capturedErr error
	for _, err := range c.ListIssues(context.Background(), "MISSING", time.Time{}) {
		if err != nil {
			capturedErr = err
		}
	}
	if capturedErr == nil || !strings.Contains(capturedErr.Error(), "team not found") {
		t.Errorf("expected error containing 'team not found', got %v", capturedErr)
	}
}

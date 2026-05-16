package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StephanSchmidt/loupe/internal/tracker"
)

type fakeServer struct {
	t      *testing.T
	srv    *httptest.Server
	routes map[string][]http.HandlerFunc
	cursor map[string]int
}

func newFake(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{
		t:      t,
		routes: make(map[string][]http.HandlerFunc),
		cursor: make(map[string]int),
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		hs, ok := f.routes[key]
		if !ok {
			t.Errorf("unexpected request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			http.NotFound(w, r)
			return
		}
		i := f.cursor[key]
		if i >= len(hs) {
			i = len(hs) - 1
		}
		f.cursor[key] = i + 1
		hs[i](w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) route(method, path string, hs ...http.HandlerFunc) {
	f.routes[method+" "+path] = hs
}

func newClient(t *testing.T, baseURL string) tracker.Tracker {
	t.Helper()
	c, err := New(baseURL, "glpat-abc")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func mustJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestNew_Validates(t *testing.T) {
	cases := []struct{ base, tok, want string }{
		{"", "t", "baseURL"},
		{"https://gitlab.com", "", "token"},
	}
	for _, c := range cases {
		_, err := New(c.base, c.tok)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("New(%q,%q) = %v, want substring %q", c.base, c.tok, err, c.want)
		}
	}
}

func TestListProjects(t *testing.T) {
	f := newFake(t)
	f.route("GET", "/api/v4/projects", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(t, w, []map[string]any{
			{"id": 1, "name": "Service", "path_with_namespace": "acme/svc"},
			{"id": 2, "name": "Lib", "path_with_namespace": "acme/team/lib"},
		})
	})

	c := newClient(t, f.srv.URL)
	projects, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("len(projects) = %d", len(projects))
	}
	if projects[0].Key != "acme/svc" || projects[1].Key != "acme/team/lib" {
		t.Errorf("project keys: %+v", projects)
	}
}

func TestListIssues_OpenIssueNoStateEvents(t *testing.T) {
	f := newFake(t)
	f.route("GET", "/api/v4/projects/acme%2Fsvc/issues", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(t, w, []map[string]any{
			{
				"iid":        7,
				"title":      "Investigate flakiness",
				"state":      "opened",
				"issue_type": "issue",
				"created_at": "2026-03-01T10:00:00Z",
				"updated_at": "2026-03-05T10:00:00Z",
				"labels":     []string{"triage"},
				"assignee":   map[string]any{"username": "alice"},
				"time_stats": map[string]any{"time_estimate": 3600},
			},
		})
	})

	c := newClient(t, f.srv.URL)
	var issues []tracker.Issue
	for iss, err := range c.ListIssues(context.Background(), "acme/svc", time.Time{}) {
		if err != nil {
			t.Fatalf("iter: %v", err)
		}
		issues = append(issues, iss)
	}
	if len(issues) != 1 {
		t.Fatalf("len(issues) = %d", len(issues))
	}
	iss := issues[0]
	if iss.Key != "acme/svc#7" {
		t.Errorf("key = %q", iss.Key)
	}
	if iss.Status != "opened" {
		t.Errorf("status = %q", iss.Status)
	}
	if iss.ClosedAt != nil || iss.ResolvedAt != nil {
		t.Errorf("open issue has ClosedAt/ResolvedAt set: %+v", iss)
	}
	if len(iss.Transitions) != 0 {
		t.Errorf("open issue shouldn't have transitions: %+v", iss.Transitions)
	}
	if iss.Estimate != 3600 {
		t.Errorf("estimate = %v", iss.Estimate)
	}
	if iss.AssigneeEmail != "alice" {
		t.Errorf("assignee = %q", iss.AssigneeEmail)
	}
}

func TestListIssues_ClosedFetchesStateEvents(t *testing.T) {
	f := newFake(t)
	closed := "2026-03-10T12:00:00Z"
	f.route("GET", "/api/v4/projects/acme%2Fsvc/issues", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("updated_after"); got == "" {
			t.Errorf("missing updated_after with non-zero since")
		}
		mustJSON(t, w, []map[string]any{
			{
				"iid": 9, "title": "Done", "state": "closed", "issue_type": "issue",
				"created_at": "2026-03-08T10:00:00Z",
				"updated_at": "2026-03-10T12:00:00Z",
				"closed_at":  closed,
			},
		})
	})
	f.route("GET", "/api/v4/projects/acme%2Fsvc/issues/9/resource_state_events", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(t, w, []map[string]any{
			{"created_at": "2026-03-10T12:00:00Z", "state": "closed"},
		})
	})

	c := newClient(t, f.srv.URL)
	since, _ := time.Parse(time.RFC3339, "2026-03-01T00:00:00Z")
	var issues []tracker.Issue
	for iss, err := range c.ListIssues(context.Background(), "acme/svc", since) {
		if err != nil {
			t.Fatalf("iter: %v", err)
		}
		issues = append(issues, iss)
	}
	if len(issues) != 1 {
		t.Fatalf("len(issues) = %d", len(issues))
	}
	iss := issues[0]
	if iss.ClosedAt == nil {
		t.Fatal("ClosedAt nil for closed issue")
	}
	if len(iss.Transitions) != 1 || iss.Transitions[0].ToStatus != "closed" {
		t.Errorf("transitions = %+v", iss.Transitions)
	}
}

func TestListProjects_Pagination(t *testing.T) {
	f := newFake(t)
	f.route("GET", "/api/v4/projects",
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Link", `<`+f.srv.URL+`/api/v4/projects?page=2>; rel="next"`)
			mustJSON(t, w, []map[string]any{{"id": 1, "name": "A", "path_with_namespace": "a/a"}})
		},
		func(w http.ResponseWriter, _ *http.Request) {
			mustJSON(t, w, []map[string]any{{"id": 2, "name": "B", "path_with_namespace": "b/b"}})
		},
	)

	c := newClient(t, f.srv.URL)
	projects, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Errorf("len(projects) = %d, want 2", len(projects))
	}
}

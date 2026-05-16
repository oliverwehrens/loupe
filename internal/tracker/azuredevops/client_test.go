package azuredevops

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
	c, err := New(baseURL, "myorg", "pat-xxx")
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
	cases := []struct{ base, org, tok, want string }{
		{"", "o", "t", "baseURL"},
		{"https://x", "", "t", "organization"},
		{"https://x", "o", "", "token"},
	}
	for _, c := range cases {
		_, err := New(c.base, c.org, c.tok)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("New(%q,%q,%q) = %v, want substring %q", c.base, c.org, c.tok, err, c.want)
		}
	}
}

func TestListProjects(t *testing.T) {
	f := newFake(t)
	f.route("GET", "/myorg/_apis/projects", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(t, w, map[string]any{
			"value": []map[string]any{
				{"id": "p1", "name": "ProjA"},
				{"id": "p2", "name": "ProjB"},
			},
		})
	})

	c := newClient(t, f.srv.URL)
	projects, err := c.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 || projects[0].Key != "ProjA" {
		t.Errorf("projects = %+v", projects)
	}
}

func TestListIssues_WIQLPlusBatchPlusTransitions(t *testing.T) {
	f := newFake(t)
	stubWIQLPath(t, f)
	stubWorkItemsBatch(t, f)
	stubItem1Updates(f)
	stubItem2Updates(f)

	c := newClient(t, f.srv.URL)
	since, _ := time.Parse(time.RFC3339, "2026-03-01T00:00:00Z")
	var issues []tracker.Issue
	for iss, err := range c.ListIssues(context.Background(), "ProjA", since) {
		if err != nil {
			t.Fatalf("iter: %v", err)
		}
		issues = append(issues, iss)
	}
	if len(issues) != 2 {
		t.Fatalf("len(issues) = %d", len(issues))
	}
	assertDoneIssue(t, issues[0])
	if len(issues[1].Transitions) != 0 {
		t.Errorf("open issue with no updates should have empty transitions, got %+v", issues[1].Transitions)
	}
}

func stubWIQLPath(t *testing.T, f *fakeServer) {
	t.Helper()
	f.route("POST", "/myorg/ProjA/_apis/wit/wiql", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "System.TeamProject") {
			t.Errorf("WIQL missing project clause: %s", body)
		}
		if !strings.Contains(string(body), "System.ChangedDate") {
			t.Errorf("WIQL missing ChangedDate clause when since is set: %s", body)
		}
		mustJSON(t, w, map[string]any{
			"workItems": []map[string]any{{"id": 1}, {"id": 2}},
		})
	})
}

func stubWorkItemsBatch(t *testing.T, f *fakeServer) {
	t.Helper()
	f.route("GET", "/myorg/ProjA/_apis/wit/workitems", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("ids"); got != "1,2" {
			t.Errorf("ids query = %q, want 1,2", got)
		}
		mustJSON(t, w, map[string]any{
			"value": []map[string]any{
				{
					"id": 1,
					"fields": map[string]any{
						"System.Title":                               "Investigate",
						"System.State":                               "Done",
						"System.WorkItemType":                        "Bug",
						"System.CreatedDate":                         "2026-03-01T10:00:00Z",
						"Microsoft.VSTS.Common.ClosedDate":           "2026-03-05T12:00:00Z",
						"Microsoft.VSTS.Common.ResolvedDate":         "2026-03-05T11:00:00Z",
						"Microsoft.VSTS.Scheduling.OriginalEstimate": 5.0,
						"System.AssignedTo":                          map[string]any{"uniqueName": "alice@x.com"},
					},
				},
				{
					"id": 2,
					"fields": map[string]any{
						"System.Title":        "Open work",
						"System.State":        "Active",
						"System.WorkItemType": "Task",
						"System.CreatedDate":  "2026-03-02T10:00:00Z",
					},
				},
			},
		})
	})
}

func stubItem1Updates(f *fakeServer) {
	f.route("GET", "/myorg/ProjA/_apis/wit/workItems/1/updates", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(f.t, w, map[string]any{
			"value": []map[string]any{
				{
					"id": 1, "revisedDate": "2026-03-02T09:00:00Z",
					"fields": map[string]any{
						"System.State": map[string]any{"oldValue": "New", "newValue": "Active"},
					},
				},
				{
					"id": 2, "revisedDate": "2026-03-05T12:00:00Z",
					"fields": map[string]any{
						"System.State":      map[string]any{"oldValue": "Active", "newValue": "Done"},
						"System.AssignedTo": map[string]any{"oldValue": nil, "newValue": "alice@x.com"},
					},
				},
				{
					"id": 3, "revisedDate": "2026-03-06T09:00:00Z",
					"fields": map[string]any{
						"System.AssignedTo": map[string]any{"oldValue": nil, "newValue": "bob@x.com"},
					},
				},
			},
		})
	})
}

func stubItem2Updates(f *fakeServer) {
	f.route("GET", "/myorg/ProjA/_apis/wit/workItems/2/updates", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(f.t, w, map[string]any{"value": []map[string]any{}})
	})
}

func assertDoneIssue(t *testing.T, iss tracker.Issue) {
	t.Helper()
	if iss.Key != "ProjA#1" || iss.Status != "Done" {
		t.Errorf("issue = %+v", iss)
	}
	if iss.ClosedAt == nil || iss.ResolvedAt == nil {
		t.Errorf("done issue missing ClosedAt/ResolvedAt: %+v", iss)
	}
	if iss.AssigneeEmail != "alice@x.com" {
		t.Errorf("assignee = %q", iss.AssigneeEmail)
	}
	// state transitions only, assignee event filtered out
	if len(iss.Transitions) != 2 {
		t.Fatalf("len(transitions) = %d, want 2: %+v", len(iss.Transitions), iss.Transitions)
	}
	if iss.Transitions[0].FromStatus != "New" || iss.Transitions[0].ToStatus != "Active" {
		t.Errorf("transition[0] = %+v", iss.Transitions[0])
	}
	if iss.Transitions[1].ToStatus != "Done" {
		t.Errorf("transition[1] = %+v", iss.Transitions[1])
	}
	if iss.Estimate != 5.0 {
		t.Errorf("estimate = %v", iss.Estimate)
	}
}

func TestListIssues_EmptyResultNoBatchCall(t *testing.T) {
	f := newFake(t)
	f.route("POST", "/myorg/ProjA/_apis/wit/wiql", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(t, w, map[string]any{"workItems": []map[string]any{}})
	})

	c := newClient(t, f.srv.URL)
	var issues []tracker.Issue
	for iss, err := range c.ListIssues(context.Background(), "ProjA", time.Time{}) {
		if err != nil {
			t.Fatalf("iter: %v", err)
		}
		issues = append(issues, iss)
	}
	if len(issues) != 0 {
		t.Errorf("expected 0 issues, got %d", len(issues))
	}
}

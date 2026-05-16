package azuredevops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/StephanSchmidt/loupe/internal/githost"
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

func newClient(t *testing.T, baseURL string) githost.GitHost {
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

func TestListWorkspaces(t *testing.T) {
	f := newFake(t)
	f.route("GET", "/myorg/_apis/projects", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(t, w, map[string]any{
			"value": []map[string]any{
				{"id": "p1", "name": "ProjA"},
				{"id": "p2", "name": "Proj B"},
			},
		})
	})

	c := newClient(t, f.srv.URL)
	ws, err := c.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(ws) != 2 || ws[0].Slug != "ProjA" || ws[1].Slug != "Proj B" {
		t.Errorf("workspaces = %+v", ws)
	}
}

func TestListRepos(t *testing.T) {
	f := newFake(t)
	f.route("GET", "/myorg/ProjA/_apis/git/repositories", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(t, w, map[string]any{
			"value": []map[string]any{
				{"id": "r1", "name": "svc", "project": map[string]any{"name": "ProjA"}},
			},
		})
	})

	c := newClient(t, f.srv.URL)
	repos, err := c.ListRepos(context.Background(), "ProjA")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].Slug != "svc" || repos[0].Workspace != "ProjA" {
		t.Errorf("repos = %+v", repos)
	}
}

func TestListCommits(t *testing.T) {
	f := newFake(t)
	f.route("GET", "/myorg/ProjA/_apis/git/repositories/svc/commits", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("searchCriteria.fromDate"); got == "" {
			t.Errorf("missing fromDate")
		}
		mustJSON(t, w, map[string]any{
			"value": []map[string]any{
				{
					"commitId": "abc123",
					"author":   map[string]any{"name": "Alice", "email": "a@x", "date": "2026-02-01T10:00:00Z"},
					"committer": map[string]any{"date": "2026-02-01T10:05:00Z"},
					"comment":  "Initial",
					"parents":  []string{"deadbeef"},
				},
			},
		})
	})

	c := newClient(t, f.srv.URL)
	repo := githost.RepoRef{Workspace: "ProjA", Slug: "svc"}
	since, _ := time.Parse(time.RFC3339, "2026-01-01T00:00:00Z")
	var commits []githost.Commit
	for cm, err := range c.ListCommits(context.Background(), repo, since) {
		if err != nil {
			t.Fatalf("iter: %v", err)
		}
		commits = append(commits, cm)
	}
	if len(commits) != 1 || commits[0].SHA != "abc123" || commits[0].ParentCount != 1 {
		t.Errorf("commits = %+v", commits)
	}
}

func TestListPullRequests_StateMapping(t *testing.T) {
	f := newFake(t)
	closed := "2026-02-15T12:00:00Z"
	f.route("GET", "/myorg/ProjA/_apis/git/repositories/svc/pullrequests", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(t, w, map[string]any{
			"value": []map[string]any{
				{
					"pullRequestId":   42,
					"title":           "Add feature",
					"status":          "completed",
					"sourceRefName":   "refs/heads/feat/x",
					"targetRefName":   "refs/heads/main",
					"creationDate":    "2026-02-10T09:00:00Z",
					"closedDate":      closed,
					"lastMergeCommit": map[string]any{"commitId": "feedface"},
					"createdBy":       map[string]any{"uniqueName": "alice@x.com", "displayName": "Alice"},
					"labels":          []map[string]any{{"name": "ai"}},
				},
				{
					"pullRequestId": 43,
					"title":         "Abandoned",
					"status":        "abandoned",
					"sourceRefName": "refs/heads/bad",
					"targetRefName": "refs/heads/main",
					"creationDate":  "2026-02-11T09:00:00Z",
				},
				{
					"pullRequestId": 44,
					"title":         "Open",
					"status":        "active",
					"sourceRefName": "refs/heads/wip",
					"targetRefName": "refs/heads/main",
					"creationDate":  "2026-02-12T09:00:00Z",
				},
			},
		})
	})

	c := newClient(t, f.srv.URL)
	repo := githost.RepoRef{Workspace: "ProjA", Slug: "svc"}
	var prs []githost.PullRequest
	for pr, err := range c.ListPullRequests(context.Background(), repo, time.Time{}) {
		if err != nil {
			t.Fatalf("iter: %v", err)
		}
		prs = append(prs, pr)
	}
	if len(prs) != 3 {
		t.Fatalf("len(prs) = %d", len(prs))
	}
	if prs[0].State != "MERGED" || prs[0].MergedAt == nil || prs[0].MergeCommitSHA != "feedface" {
		t.Errorf("merged = %+v", prs[0])
	}
	if prs[0].SourceBranch != "feat/x" || prs[0].DestinationBranch != "main" {
		t.Errorf("ref names not stripped: %+v", prs[0])
	}
	if prs[1].State != "DECLINED" {
		t.Errorf("abandoned = %+v", prs[1])
	}
	if prs[2].State != "OPEN" {
		t.Errorf("active = %+v", prs[2])
	}
	if len(prs[0].Labels) != 1 || prs[0].Labels[0] != "ai" {
		t.Errorf("labels = %v", prs[0].Labels)
	}
}

func TestListPRCommits(t *testing.T) {
	f := newFake(t)
	f.route("GET", "/myorg/ProjA/_apis/git/repositories/svc/pullRequests/42/commits", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(t, w, map[string]any{
			"value": []map[string]any{
				{
					"commitId":  "sha1",
					"author":    map[string]any{"name": "A", "email": "a@x", "date": "2026-02-01T10:00:00Z"},
					"committer": map[string]any{"date": "2026-02-01T10:00:00Z"},
					"comment":   "first",
				},
			},
		})
	})

	c := newClient(t, f.srv.URL)
	commits, err := c.ListPRCommits(context.Background(), githost.RepoRef{Workspace: "ProjA", Slug: "svc"}, "42")
	if err != nil {
		t.Fatalf("ListPRCommits: %v", err)
	}
	if len(commits) != 1 || commits[0].SHA != "sha1" {
		t.Errorf("commits = %+v", commits)
	}
}

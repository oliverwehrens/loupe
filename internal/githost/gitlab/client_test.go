package gitlab

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
	t        *testing.T
	srv      *httptest.Server
	routes   map[string][]http.HandlerFunc
	cursor   map[string]int
	gotToken string
}

func newFake(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{
		t:      t,
		routes: make(map[string][]http.HandlerFunc),
		cursor: make(map[string]int),
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gotToken = r.Header.Get("PRIVATE-TOKEN")
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

func TestListWorkspaces(t *testing.T) {
	f := newFake(t)
	f.route("GET", "/api/v4/groups", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(t, w, []map[string]any{
			{"id": 1, "name": "Acme", "full_path": "acme"},
			{"id": 2, "name": "Beta Team", "full_path": "acme/beta"},
		})
	})

	c := newClient(t, f.srv.URL)
	ws, err := c.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if f.gotToken != "glpat-abc" {
		t.Errorf("PRIVATE-TOKEN header = %q, want glpat-abc", f.gotToken)
	}
	if len(ws) != 2 || ws[0].Slug != "acme" || ws[1].Slug != "acme/beta" {
		t.Errorf("workspaces = %+v", ws)
	}
}

func TestListWorkspaces_Pagination(t *testing.T) {
	f := newFake(t)
	page1Cb := false
	f.route("GET", "/api/v4/groups",
		func(w http.ResponseWriter, r *http.Request) {
			page1Cb = true
			w.Header().Set("Link", `<`+f.srv.URL+`/api/v4/groups?page=2&per_page=100>; rel="next"`)
			mustJSON(t, w, []map[string]any{{"id": 1, "name": "A", "full_path": "a"}})
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") != "2" {
				t.Errorf("page2 query = %q, want page=2", r.URL.RawQuery)
			}
			mustJSON(t, w, []map[string]any{{"id": 2, "name": "B", "full_path": "b"}})
		},
	)

	c := newClient(t, f.srv.URL)
	ws, err := c.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if !page1Cb {
		t.Fatal("page 1 not fetched")
	}
	if len(ws) != 2 {
		t.Errorf("len(workspaces) = %d, want 2", len(ws))
	}
}

func TestListRepos_SubgroupSlugs(t *testing.T) {
	f := newFake(t)
	f.route("GET", "/api/v4/groups/acme/projects", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "include_subgroups=true") {
			t.Errorf("missing include_subgroups: %q", r.URL.RawQuery)
		}
		mustJSON(t, w, []map[string]any{
			{"id": 10, "name": "Top", "path": "top", "path_with_namespace": "acme/top"},
			{"id": 11, "name": "Sub", "path": "svc", "path_with_namespace": "acme/team/svc"},
		})
	})

	c := newClient(t, f.srv.URL)
	repos, err := c.ListRepos(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("len(repos) = %d, want 2", len(repos))
	}
	if repos[0].Workspace != "acme" || repos[0].Slug != "top" {
		t.Errorf("top-level repo = %+v", repos[0])
	}
	if repos[1].Workspace != "acme" || repos[1].Slug != "team/svc" {
		t.Errorf("subgroup repo = %+v", repos[1])
	}
	if got := repos[1].FullName(); got != "acme/team/svc" {
		t.Errorf("FullName() = %q, want acme/team/svc", got)
	}
}

func TestListCommits_StreamsAndDecodes(t *testing.T) {
	f := newFake(t)
	f.route("GET", "/api/v4/projects/acme%2Fsvc/repository/commits", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("since"); !strings.HasPrefix(got, "2026-01-01") {
			t.Errorf("since = %q", got)
		}
		mustJSON(t, w, []map[string]any{
			{
				"id":             "abc123",
				"author_name":    "Alice",
				"author_email":   "alice@example.com",
				"authored_date":  "2026-02-01T10:00:00Z",
				"committed_date": "2026-02-01T10:05:00Z",
				"message":        "Add thing",
				"parent_ids":     []string{"deadbeef"},
			},
		})
	})

	c := newClient(t, f.srv.URL)
	repo := githost.RepoRef{Workspace: "acme", Slug: "svc"}
	since, _ := time.Parse(time.RFC3339, "2026-01-01T00:00:00Z")
	var got []githost.Commit
	for cm, err := range c.ListCommits(context.Background(), repo, since) {
		if err != nil {
			t.Fatalf("iter: %v", err)
		}
		got = append(got, cm)
	}
	if len(got) != 1 || got[0].SHA != "abc123" || got[0].ParentCount != 1 {
		t.Errorf("commits = %+v", got)
	}
}

func TestListPullRequests_StateAndAuthor(t *testing.T) {
	f := newFake(t)
	merged := "2026-02-15T12:00:00Z"
	f.route("GET", "/api/v4/projects/acme%2Fsvc/merge_requests", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(t, w, []map[string]any{
			{
				"iid":              42,
				"title":            "Refactor",
				"state":            "merged",
				"source_branch":    "feat/x",
				"target_branch":    "main",
				"created_at":       "2026-02-10T09:00:00Z",
				"updated_at":       "2026-02-15T12:00:00Z",
				"merged_at":        merged,
				"merge_commit_sha": "feedface",
				"author":           map[string]any{"username": "alice", "bot": false},
				"labels":           []string{"ai", "backend"},
			},
			{
				"iid":           43,
				"title":         "WIP",
				"state":         "opened",
				"source_branch": "wip/y",
				"target_branch": "main",
				"created_at":    "2026-02-12T09:00:00Z",
				"updated_at":    "2026-02-14T09:00:00Z",
				"author":        map[string]any{"username": "bot-app", "bot": true},
			},
		})
	})

	c := newClient(t, f.srv.URL)
	repo := githost.RepoRef{Workspace: "acme", Slug: "svc"}
	var prs []githost.PullRequest
	for pr, err := range c.ListPullRequests(context.Background(), repo, time.Time{}) {
		if err != nil {
			t.Fatalf("iter: %v", err)
		}
		prs = append(prs, pr)
	}
	if len(prs) != 2 {
		t.Fatalf("len(prs) = %d", len(prs))
	}
	if prs[0].State != "MERGED" || prs[0].MergedAt == nil || prs[0].AuthorLogin != "alice" {
		t.Errorf("merged pr = %+v", prs[0])
	}
	if prs[1].State != "OPEN" || !prs[1].AuthorIsBot {
		t.Errorf("open bot pr = %+v", prs[1])
	}
	if len(prs[0].Labels) != 2 {
		t.Errorf("labels = %v", prs[0].Labels)
	}
}

func TestListPullRequests_StopsAtSince(t *testing.T) {
	f := newFake(t)
	f.route("GET", "/api/v4/projects/acme%2Fsvc/merge_requests", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(t, w, []map[string]any{
			{"iid": 5, "title": "New", "state": "opened", "created_at": "2026-03-10T09:00:00Z", "updated_at": "2026-03-10T09:00:00Z", "author": map[string]any{"username": "x"}},
			{"iid": 4, "title": "Old", "state": "merged", "created_at": "2026-01-01T09:00:00Z", "updated_at": "2026-01-01T09:00:00Z", "author": map[string]any{"username": "x"}},
		})
	})

	c := newClient(t, f.srv.URL)
	repo := githost.RepoRef{Workspace: "acme", Slug: "svc"}
	since, _ := time.Parse(time.RFC3339, "2026-02-01T00:00:00Z")
	var prs []githost.PullRequest
	for pr, err := range c.ListPullRequests(context.Background(), repo, since) {
		if err != nil {
			t.Fatalf("iter: %v", err)
		}
		prs = append(prs, pr)
	}
	if len(prs) != 1 || prs[0].ID != "5" {
		t.Errorf("expected only PR 5 (newer than since), got %+v", prs)
	}
}

func TestListPRCommits(t *testing.T) {
	f := newFake(t)
	f.route("GET", "/api/v4/projects/acme%2Fsvc/merge_requests/42/commits", func(w http.ResponseWriter, _ *http.Request) {
		mustJSON(t, w, []map[string]any{
			{
				"id": "sha1", "author_name": "A", "author_email": "a@x",
				"authored_date": "2026-02-01T10:00:00Z", "committed_date": "2026-02-01T10:00:00Z",
				"message": "first", "parent_ids": []string{},
			},
			{
				"id": "sha2", "author_name": "A", "author_email": "a@x",
				"authored_date": "2026-02-02T10:00:00Z", "committed_date": "2026-02-02T10:00:00Z",
				"message": "second", "parent_ids": []string{"sha1"},
			},
		})
	})

	c := newClient(t, f.srv.URL)
	commits, err := c.ListPRCommits(context.Background(), githost.RepoRef{Workspace: "acme", Slug: "svc"}, "42")
	if err != nil {
		t.Fatalf("ListPRCommits: %v", err)
	}
	if len(commits) != 2 || commits[1].SHA != "sha2" {
		t.Errorf("commits = %+v", commits)
	}
}

package ingest

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/StephanSchmidt/loupe/internal/githost"
	"github.com/StephanSchmidt/loupe/internal/store"
)

// fakeGitHost implements githost.GitHost from in-memory data. No HTTP.
type fakeGitHost struct {
	workspaces []githost.Workspace
	repos      map[string][]githost.Repo        // workspace slug → repos
	commits    map[string][]githost.Commit      // "ws/repo" → commits
	prs        map[string][]githost.PullRequest // "ws/repo" → PRs
	prCommits  map[string]map[string][]githost.Commit
}

func (f *fakeGitHost) Name() string { return "fake-vcs" }

func (f *fakeGitHost) ListWorkspaces(_ context.Context) ([]githost.Workspace, error) {
	return f.workspaces, nil
}

func (f *fakeGitHost) ListRepos(_ context.Context, ws string) ([]githost.Repo, error) {
	return f.repos[ws], nil
}

func (f *fakeGitHost) ListCommits(_ context.Context, repo githost.RepoRef, since time.Time) iter.Seq2[githost.Commit, error] {
	commits := f.commits[repo.FullName()]
	return func(yield func(githost.Commit, error) bool) {
		for _, c := range commits {
			if !since.IsZero() && c.CommittedAt.Before(since) {
				continue
			}
			if !yield(c, nil) {
				return
			}
		}
	}
}

func (f *fakeGitHost) ListPullRequests(_ context.Context, repo githost.RepoRef, since time.Time) iter.Seq2[githost.PullRequest, error] {
	prs := f.prs[repo.FullName()]
	return func(yield func(githost.PullRequest, error) bool) {
		for _, pr := range prs {
			if !since.IsZero() && pr.CreatedAt.Before(since) {
				continue
			}
			if !yield(pr, nil) {
				return
			}
		}
	}
}

func (f *fakeGitHost) ListPRCommits(_ context.Context, repo githost.RepoRef, prID string) ([]githost.Commit, error) {
	return f.prCommits[repo.FullName()][prID], nil
}

func buildFake() *fakeGitHost {
	at := func(n int64) time.Time { return time.Unix(n, 0).UTC() }
	return &fakeGitHost{
		workspaces: []githost.Workspace{
			{Slug: "acme", Name: "Acme"},
			{Slug: "beta", Name: "Beta"},
		},
		repos: map[string][]githost.Repo{
			"acme": {
				{RepoRef: githost.RepoRef{Workspace: "acme", Slug: "backend"}, Name: "backend"},
				{RepoRef: githost.RepoRef{Workspace: "acme", Slug: "frontend"}, Name: "frontend"},
			},
			"beta": {
				{RepoRef: githost.RepoRef{Workspace: "beta", Slug: "agent"}, Name: "agent"},
			},
		},
		commits: map[string][]githost.Commit{
			"acme/backend": {
				{SHA: "a1", AuthorEmail: "alice@a", AuthorName: "Alice", CommittedAt: at(1700000300), Message: "feat"},
				{SHA: "a2", AuthorEmail: "alice@a", AuthorName: "Alice", CommittedAt: at(1700000200),
					Message: "fix\n\nCo-Authored-By: Claude <noreply@anthropic.com>"},
			},
			"acme/frontend": {
				{SHA: "f1", AuthorEmail: "bob@a", AuthorName: "Bob", CommittedAt: at(1700000400), Message: "init"},
			},
			"beta/agent": {
				{SHA: "g1", AuthorEmail: "carol@b", AuthorName: "Carol", CommittedAt: at(1700000500), Message: "init"},
			},
		},
		prs: map[string][]githost.PullRequest{
			"acme/backend": {
				{ID: "1", Title: "Add login", State: "MERGED", AuthorEmail: "alice@a",
					SourceBranch: "feat/login", DestinationBranch: "main",
					CreatedAt: at(1700000100), MergeCommitSHA: "merge-a"},
			},
		},
	}
}

func TestIngestGitHost_EndToEnd(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	stats, err := IngestGitHost(context.Background(), s, buildFake(), nil)
	if err != nil {
		t.Fatalf("IngestGitHost: %v", err)
	}

	t.Run("stats counters", func(t *testing.T) {
		want := GitHostStats{Workspaces: 2, Repos: 3, Commits: 4, PullRequests: 1}
		if stats != want {
			t.Errorf("stats = %+v, want %+v", stats, want)
		}
	})

	t.Run("workspaces table populated", func(t *testing.T) {
		assertCount(t, s, `SELECT COUNT(*) FROM workspaces WHERE provider = 'fake-vcs'`, 2)
	})

	t.Run("repos table populated", func(t *testing.T) {
		assertCount(t, s, `SELECT COUNT(*) FROM repos WHERE provider = 'fake-vcs'`, 3)
	})

	t.Run("commit columns carry provider+workspace", func(t *testing.T) {
		var provider, workspace string
		if err := s.DB().QueryRow(`SELECT provider, workspace FROM commits WHERE sha = 'a1'`).Scan(&provider, &workspace); err != nil {
			t.Fatalf("read commit a1: %v", err)
		}
		if provider != "fake-vcs" || workspace != "acme" {
			t.Errorf("commit a1 columns: provider=%q workspace=%q", provider, workspace)
		}
	})

	t.Run("trailer body preserved", func(t *testing.T) {
		var msg string
		if err := s.DB().QueryRow(`SELECT message FROM commits WHERE sha = 'a2'`).Scan(&msg); err != nil {
			t.Fatalf("read commit a2: %v", err)
		}
		if !contains(msg, "Co-Authored-By: Claude") {
			t.Errorf("a2 message missing trailer: %q", msg)
		}
	})

	t.Run("PR columns carry provider+workspace", func(t *testing.T) {
		var provider, workspace string
		if err := s.DB().QueryRow(`SELECT provider, workspace FROM prs WHERE id = '1'`).Scan(&provider, &workspace); err != nil {
			t.Fatalf("read pr 1: %v", err)
		}
		if provider != "fake-vcs" || workspace != "acme" {
			t.Errorf("pr 1 columns: provider=%q workspace=%q", provider, workspace)
		}
	})

	t.Run("watermarks advanced", func(t *testing.T) {
		var wm int64
		if err := s.DB().QueryRow(`SELECT last_commit_indexed_at FROM repos WHERE full_name = 'acme/backend'`).Scan(&wm); err != nil {
			t.Fatalf("read watermark: %v", err)
		}
		if wm == 0 {
			t.Errorf("watermark not advanced for acme/backend")
		}
	})
}

func assertCount(t *testing.T, s *store.Store, query string, want int) {
	t.Helper()
	var got int
	if err := s.DB().QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	if got != want {
		t.Errorf("count %q = %d, want %d", query, got, want)
	}
}

func TestIngestGitHost_IdempotentReruns(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	fake := buildFake()
	if _, err := IngestGitHost(context.Background(), s, fake, nil); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if _, err := IngestGitHost(context.Background(), s, fake, nil); err != nil {
		t.Fatalf("second ingest: %v", err)
	}

	var nCommits, nPRs, nRepos int
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM commits`).Scan(&nCommits)
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM prs`).Scan(&nPRs)
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM repos`).Scan(&nRepos)
	if nCommits != 4 || nPRs != 1 || nRepos != 3 {
		t.Errorf("after re-ingest: commits=%d prs=%d repos=%d (want 4/1/3 — no duplicates)", nCommits, nPRs, nRepos)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

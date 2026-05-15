package cmdstatus

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/StephanSchmidt/loupe/internal/store"
)

// seedStore plants representative rows so WriteStatus has something to
// summarise. Mirrors what `loupe baseline` would have populated end-to-end.
func seedStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB().Exec(q, args...); err != nil {
			t.Fatalf("seed: %s: %v", q, err)
		}
	}

	exec(`INSERT INTO workspaces (provider, slug, name, discovered_at, last_indexed_at)
        VALUES ('bitbucket-cloud', 'acme', 'Acme', 1700000000, 1700003000)`)
	exec(`INSERT INTO repos (provider, full_name, workspace, slug, name, discovered_at)
        VALUES
            ('bitbucket-cloud', 'acme/backend',  'acme', 'backend',  'backend',  1700000000),
            ('bitbucket-cloud', 'acme/frontend', 'acme', 'frontend', 'frontend', 1700000000),
            ('bitbucket-cloud', 'acme/agent',    'acme', 'agent',    'agent',    1700000000)`)

	exec(`INSERT INTO tracker_projects (provider, key, name, discovered_at, last_issue_indexed_at)
        VALUES
            ('jira-cloud', 'ENG', 'Engineering', 1700000000, 1700003120),
            ('jira-cloud', 'OPS', 'Ops',         1700000000, 1700003120)`)

	exec(`INSERT INTO commits (sha, repo_name, author_email, author_name, committed_at, message, provider, workspace)
        VALUES
            ('c1',   'acme/backend',  'a@a', 'A', 1700000100, 'msg', 'bitbucket-cloud', 'acme'),
            ('c2',   'acme/backend',  'a@a', 'A', 1700000200, 'msg', 'bitbucket-cloud', 'acme'),
            ('c3',   'acme/frontend', 'b@a', 'B', 1700000300, 'msg', 'bitbucket-cloud', 'acme'),
            ('bot1', 'acme/backend',  '49699333+dependabot[bot]@users.noreply.github.com', 'dependabot[bot]', 1700000400, 'bump', 'bitbucket-cloud', 'acme'),
            ('bot2', 'acme/backend',  '29139614+renovate[bot]@users.noreply.github.com',   'renovate[bot]',   1700000500, 'pin',  'bitbucket-cloud', 'acme')`)
	// Bot commit even has an AI signal — exclusion has to drop the bot
	// before the AI-tagged count is computed.
	exec(`INSERT INTO ai_signals (commit_sha, signal_kind, signal_source, confidence)
        VALUES ('c2',   'co_author_trailer', 'claude', 'high'),
               ('bot1', 'co_author_trailer', 'claude', 'high')`)

	exec(`INSERT INTO tickets (id, project_key, title, status, created_at)
        VALUES ('ENG-1', 'ENG', 't', 'Done', 1700000000)`)

	return s
}

func TestWriteStatus_FullySeeded(t *testing.T) {
	s := seedStore(t)
	defer func() { _ = s.Close() }()
	var buf bytes.Buffer
	if err := WriteStatus(context.Background(), s, &buf); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	out := buf.String()
	t.Logf("output:\n%s", out)

	for _, want := range []string{
		"Bitbucket:",
		"1 workspace",
		"3 repos",
		"Jira:",
		"2 projects",
		"Commits:",
		// Bot commits (bot1, bot2) and the bot's AI signal must not count.
		"3  (1 AI-tagged, 33.3%)",
		"Tickets:",
		"1",
		"last commit indexed",
		"last issue indexed",
		"Excluded 2 bot-authored commits across 2 bots (Dependabot, Renovate)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q\nfull:\n%s", want, out)
		}
	}
	// Inflated counts that would only appear if bots leaked in; raw email
	// fragments must be gone now that display names are rendered.
	for _, unwanted := range []string{
		"5  (2 AI-tagged",
		"AI-tagged, 40.0%",
		"49699333+dependabot",
		"29139614+renovate",
		"@users.noreply.github.com",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unexpected %q in status output\nfull:\n%s", unwanted, out)
		}
	}
}

func TestWriteStatus_EmptyStore(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	var buf bytes.Buffer
	if err := WriteStatus(context.Background(), s, &buf); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	if !strings.Contains(buf.String(), "Empty state") {
		t.Errorf("expected 'Empty state' message, got: %q", buf.String())
	}
}

func TestDisplayName(t *testing.T) {
	if displayName("bitbucket-cloud") != "Bitbucket" {
		t.Errorf("bitbucket-cloud → %q", displayName("bitbucket-cloud"))
	}
	if displayName("jira-cloud") != "Jira" {
		t.Errorf("jira-cloud → %q", displayName("jira-cloud"))
	}
	if displayName("unknown") != "unknown" {
		t.Errorf("fallback failed")
	}
}

func TestPluralise(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{{1, "repo"}, {0, "repos"}, {2, "repos"}}
	for _, c := range cases {
		if got := pluralise("repo", c.n); got != c.want {
			t.Errorf("pluralise(repo, %d) = %q, want %q", c.n, got, c.want)
		}
	}
}

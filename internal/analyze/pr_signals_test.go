package analyze

import (
	"context"
	"testing"

	"github.com/StephanSchmidt/loupe/internal/store"
)

func seedPR(t *testing.T, s *store.Store, id, branch, mergeSHA, labelsJSON string) {
	t.Helper()
	_, err := s.DB().Exec(`
        INSERT INTO prs (id, repo_name, title, state, author_email,
            source_branch, destination_branch, created_at, merged_at,
            merge_commit_sha, labels)
        VALUES (?, 'r', 't', 'MERGED', 'a@a', ?, 'main', 1, 2, ?, ?)`,
		id, branch, mergeSHA, labelsJSON)
	if err != nil {
		t.Fatalf("seed pr %s: %v", id, err)
	}
}

func seedPRCommitRow(t *testing.T, s *store.Store, sha string) {
	t.Helper()
	_, err := s.DB().Exec(`
        INSERT INTO commits (sha, repo_name, author_email, author_name, committed_at, message)
        VALUES (?, 'r', 'a@a', 'A', 1700000000, 'msg')`, sha)
	if err != nil {
		t.Fatalf("seed commit %s: %v", sha, err)
	}
}

func seedPRCommit(t *testing.T, s *store.Store, prID, sha string) {
	t.Helper()
	_, err := s.DB().Exec(`INSERT INTO pr_commits (pr_id, commit_sha) VALUES (?, ?)`, prID, sha)
	if err != nil {
		t.Fatalf("seed pr_commit %s/%s: %v", prID, sha, err)
	}
}

func TestDetectPRSignals_LabelAndBranch(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	seedPRCommitRow(t, s, "merge1")
	seedPR(t, s, "pr1", "feat/x", "merge1", `["ai-generated","backend"]`)

	seedPRCommitRow(t, s, "merge2")
	seedPR(t, s, "pr2", "copilot/refactor", "merge2", `[]`)

	seedPRCommitRow(t, s, "merge3")
	seedPR(t, s, "pr3", "feat/y", "merge3", `["chore"]`)

	ctx := context.Background()
	n, err := DetectPRSignals(ctx, s, DefaultPRSignalConfig())
	if err != nil {
		t.Fatalf("DetectPRSignals: %v", err)
	}
	if n != 2 {
		t.Errorf("DetectPRSignals wrote %d signals, want 2 (one label + one branch)", n)
	}

	gotByKind := map[string]string{}
	rows, err := s.DB().Query(`SELECT commit_sha, signal_kind, signal_source FROM ai_signals ORDER BY commit_sha`)
	if err != nil {
		t.Fatalf("read signals: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sha, kind, src string
		if err := rows.Scan(&sha, &kind, &src); err != nil {
			t.Fatalf("scan signal: %v", err)
		}
		gotByKind[sha] = kind + ":" + src
	}

	if got := gotByKind["merge1"]; got != KindPRLabel+":"+SourceAIGeneric {
		t.Errorf("merge1 signal = %q, want %q", got, KindPRLabel+":"+SourceAIGeneric)
	}
	if got := gotByKind["merge2"]; got != KindBranchName+":"+SourceCopilot {
		t.Errorf("merge2 signal = %q, want %q", got, KindBranchName+":"+SourceCopilot)
	}
	if _, ok := gotByKind["merge3"]; ok {
		t.Errorf("merge3 should not have produced a signal")
	}
}

func TestDetectPRSignals_ToolSpecificLabel(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	seedPRCommitRow(t, s, "m1")
	seedPR(t, s, "pr1", "feat/x", "m1", `["claude"]`)

	ctx := context.Background()
	if _, err := DetectPRSignals(ctx, s, DefaultPRSignalConfig()); err != nil {
		t.Fatalf("DetectPRSignals: %v", err)
	}

	var src string
	err = s.DB().QueryRow(`SELECT signal_source FROM ai_signals WHERE commit_sha = 'm1'`).Scan(&src)
	if err != nil {
		t.Fatalf("read signal: %v", err)
	}
	if src != SourceClaude {
		t.Errorf("got source %q, want %q (label should map to specific tool)", src, SourceClaude)
	}
}

func TestDetectPRSignals_AttributesToPRCommits(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	for _, sha := range []string{"merge1", "child1", "child2"} {
		seedPRCommitRow(t, s, sha)
	}
	seedPR(t, s, "pr1", "copilot/feat", "merge1", `[]`)
	seedPRCommit(t, s, "pr1", "child1")
	seedPRCommit(t, s, "pr1", "child2")

	ctx := context.Background()
	n, err := DetectPRSignals(ctx, s, DefaultPRSignalConfig())
	if err != nil {
		t.Fatalf("DetectPRSignals: %v", err)
	}
	// Branch signal on merge1 + child1 + child2 = 3 rows
	if n != 3 {
		t.Errorf("got %d signals, want 3 (merge + 2 PR commits)", n)
	}
}

func TestDetectPRSignals_Idempotent(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	seedPRCommitRow(t, s, "merge1")
	seedPR(t, s, "pr1", "copilot/x", "merge1", `["ai-generated"]`)

	ctx := context.Background()
	if _, err := DetectPRSignals(ctx, s, DefaultPRSignalConfig()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := DetectPRSignals(ctx, s, DefaultPRSignalConfig()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ai_signals WHERE commit_sha='merge1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	// Label + branch = 2 signals, idempotent.
	if n != 2 {
		t.Errorf("ai_signals count = %d, want 2 (idempotent re-run)", n)
	}
}

func TestDetectPRSignals_EmptyConfigSkips(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	seedPRCommitRow(t, s, "m1")
	seedPR(t, s, "pr1", "copilot/x", "m1", `["ai-generated"]`)

	ctx := context.Background()
	n, err := DetectPRSignals(ctx, s, PRSignalConfig{})
	if err != nil {
		t.Fatalf("empty cfg: %v", err)
	}
	if n != 0 {
		t.Errorf("empty cfg should write 0 signals, got %d", n)
	}
}

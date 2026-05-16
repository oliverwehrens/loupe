package analyze

import (
	"context"
	"testing"

	"github.com/StephanSchmidt/loupe/internal/store"
)

func seedPRCommitWithMessage(t *testing.T, s *store.Store, prID, sha, email, name, message string) {
	t.Helper()
	_, err := s.DB().Exec(`
        INSERT INTO pr_commits (pr_id, commit_sha, author_email, author_name, message)
        VALUES (?, ?, ?, ?, ?)`, prID, sha, email, name, message)
	if err != nil {
		t.Fatalf("seed pr_commit %s/%s: %v", prID, sha, err)
	}
}

func TestDetectSquashRecovery_RecoversTrailers(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	// PR squashed into merge1; pre-squash commits had Claude + aider trailers.
	seedPRCommitRow(t, s, "merge1")
	seedPR(t, s, "pr1", "feat/x", "merge1", `[]`)
	seedPRCommitWithMessage(t, s, "pr1", "src1", "alice@example.com", "Alice",
		"fix\n\nCo-Authored-By: Claude <noreply@anthropic.com>")
	seedPRCommitWithMessage(t, s, "pr1", "src2", "alice@example.com", "Alice",
		"refactor\n\nCo-Authored-By: aider <aider@local>")

	ctx := context.Background()
	n, err := DetectSquashRecovery(ctx, s)
	if err != nil {
		t.Fatalf("DetectSquashRecovery: %v", err)
	}
	if n != 2 {
		t.Errorf("recovered %d signals, want 2 (claude + aider)", n)
	}

	rows, err := s.DB().Query(`SELECT signal_kind, signal_source FROM ai_signals WHERE commit_sha='merge1' ORDER BY signal_source`)
	if err != nil {
		t.Fatalf("read signals: %v", err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]string{}
	for rows.Next() {
		var k, src string
		if err := rows.Scan(&k, &src); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[src] = k
	}
	if got[SourceClaude] != KindSquashRecovery {
		t.Errorf("expected squash_recovery signal for claude, got %v", got)
	}
	if got[SourceAider] != KindSquashRecovery {
		t.Errorf("expected squash_recovery signal for aider, got %v", got)
	}
}

func TestDetectSquashRecovery_SkipsUnmergedPRs(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	// PR with empty merge_commit_sha (still open / declined).
	seedPR(t, s, "pr1", "feat/x", "", `[]`)
	seedPRCommitWithMessage(t, s, "pr1", "src1", "a@a", "A",
		"fix\n\nCo-Authored-By: Claude <noreply@anthropic.com>")

	ctx := context.Background()
	n, err := DetectSquashRecovery(ctx, s)
	if err != nil {
		t.Fatalf("DetectSquashRecovery: %v", err)
	}
	if n != 0 {
		t.Errorf("unmerged PR should yield 0 recovered signals, got %d", n)
	}
}

func TestDetectSquashRecovery_Idempotent(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	seedPRCommitRow(t, s, "merge1")
	seedPR(t, s, "pr1", "feat/x", "merge1", `[]`)
	seedPRCommitWithMessage(t, s, "pr1", "src1", "a@a", "A",
		"fix\n\nCo-Authored-By: Claude <noreply@anthropic.com>")

	ctx := context.Background()
	if _, err := DetectSquashRecovery(ctx, s); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := DetectSquashRecovery(ctx, s); err != nil {
		t.Fatalf("second: %v", err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ai_signals WHERE commit_sha='merge1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("count after re-run = %d, want 1", n)
	}
}

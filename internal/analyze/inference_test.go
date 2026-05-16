package analyze

import (
	"context"
	"testing"
	"time"

	"github.com/StephanSchmidt/loupe/internal/store"
)

// seedCommitForInference inserts a commit and (optionally) an AI signal
// for it. The repo argument lets tests isolate per-repo grouping.
func seedCommitForInference(t *testing.T, s *store.Store, sha, email, repo string, at time.Time, withHighSignal bool) {
	t.Helper()
	if _, err := s.DB().Exec(`
        INSERT INTO commits (sha, repo_name, author_email, author_name, committed_at, message)
        VALUES (?, ?, ?, 'A', ?, 'msg')`, sha, repo, email, at.Unix()); err != nil {
		t.Fatalf("seed commit %s: %v", sha, err)
	}
	if withHighSignal {
		if _, err := s.DB().Exec(`
            INSERT INTO ai_signals (commit_sha, signal_kind, signal_source, confidence, detail)
            VALUES (?, ?, ?, ?, '')`, sha, KindCoAuthorTrailer, SourceClaude, ConfidenceHigh); err != nil {
			t.Fatalf("seed signal %s: %v", sha, err)
		}
	}
}

func TestInferFromSeatHolders_PropagatesWithinGroup(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	wk := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC) // Monday
	// Alice, repo r1, week of May 11 — one tagged + three untagged
	seedCommitForInference(t, s, "a1", "alice@x", "r1", wk, true)
	seedCommitForInference(t, s, "a2", "alice@x", "r1", wk.Add(24*time.Hour), false)
	seedCommitForInference(t, s, "a3", "alice@x", "r1", wk.Add(2*24*time.Hour), false)
	seedCommitForInference(t, s, "a4", "alice@x", "r1", wk.Add(3*24*time.Hour), false)

	ctx := context.Background()
	n, err := InferFromSeatHolders(ctx, s)
	if err != nil {
		t.Fatalf("InferFromSeatHolders: %v", err)
	}
	if n != 3 {
		t.Errorf("inferred %d signals, want 3", n)
	}

	var got int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ai_signals WHERE signal_kind=?`, KindSeatInference).Scan(&got); err != nil {
		t.Fatalf("count seat_inference: %v", err)
	}
	if got != 3 {
		t.Errorf("seat_inference rows = %d, want 3", got)
	}
}

func TestInferFromSeatHolders_DoesNotCrossRepo(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	wk := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC)
	// Alice has a tagged commit in r1 but only untagged commits in r2.
	seedCommitForInference(t, s, "a1", "alice@x", "r1", wk, true)
	seedCommitForInference(t, s, "a2", "alice@x", "r2", wk, false)

	ctx := context.Background()
	if _, err := InferFromSeatHolders(ctx, s); err != nil {
		t.Fatalf("InferFromSeatHolders: %v", err)
	}

	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ai_signals WHERE signal_kind=?`, KindSeatInference).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("seat_inference should not cross repos; got %d rows", n)
	}
}

func TestInferFromSeatHolders_DoesNotCrossWeek(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	wk := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC) // ISO week N
	wkNext := wk.AddDate(0, 0, 7)                       // ISO week N+1
	seedCommitForInference(t, s, "a1", "alice@x", "r1", wk, true)
	seedCommitForInference(t, s, "a2", "alice@x", "r1", wkNext, false)

	ctx := context.Background()
	if _, err := InferFromSeatHolders(ctx, s); err != nil {
		t.Fatalf("InferFromSeatHolders: %v", err)
	}

	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ai_signals WHERE signal_kind=?`, KindSeatInference).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("seat_inference should not cross ISO weeks; got %d rows", n)
	}
}

func TestInferFromSeatHolders_OnlyMediumConfidence(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	wk := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC)
	seedCommitForInference(t, s, "a1", "alice@x", "r1", wk, true)
	seedCommitForInference(t, s, "a2", "alice@x", "r1", wk, false)

	ctx := context.Background()
	if _, err := InferFromSeatHolders(ctx, s); err != nil {
		t.Fatalf("InferFromSeatHolders: %v", err)
	}

	var conf, src string
	if err := s.DB().QueryRow(`SELECT confidence, signal_source FROM ai_signals WHERE commit_sha='a2' AND signal_kind=?`,
		KindSeatInference).Scan(&conf, &src); err != nil {
		t.Fatalf("read inferred row: %v", err)
	}
	if conf != ConfidenceMedium {
		t.Errorf("got confidence %q, want %q", conf, ConfidenceMedium)
	}
	if src != SourceAIGeneric {
		t.Errorf("got source %q, want %q", src, SourceAIGeneric)
	}
}

func TestInferFromSeatHolders_Idempotent(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	wk := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC)
	seedCommitForInference(t, s, "a1", "alice@x", "r1", wk, true)
	seedCommitForInference(t, s, "a2", "alice@x", "r1", wk, false)

	ctx := context.Background()
	if _, err := InferFromSeatHolders(ctx, s); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := InferFromSeatHolders(ctx, s); err != nil {
		t.Fatalf("second: %v", err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ai_signals WHERE signal_kind=?`, KindSeatInference).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("count after re-run = %d, want 1", n)
	}
}

package analyze

import (
	"context"
	"testing"
	"time"

	"github.com/StephanSchmidt/loupe/internal/store"
)

func TestRunAllDetectors_OrchestratesEveryPass(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	wk := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC)

	// Commit with a Co-Authored-By trailer → co_author_trailer signal.
	seedCommitForInference(t, s, "c1", "alice@x", "r1", wk, false)
	if _, err := s.DB().Exec(`UPDATE commits SET message = ? WHERE sha = 'c1'`,
		"fix\n\nCo-Authored-By: Claude <noreply@anthropic.com>"); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	// Companion commit by the same author, same week, same repo → seat_inference candidate.
	seedCommitForInference(t, s, "c2", "alice@x", "r1", wk, false)

	// Merge commit + PR with copilot branch prefix → branch_name signal.
	seedCommitForInference(t, s, "merge1", "alice@x", "r1", wk, false)
	if _, err := s.DB().Exec(`
        INSERT INTO prs (id, repo_name, title, state, author_email,
            source_branch, destination_branch, created_at, merged_at,
            merge_commit_sha, labels)
        VALUES ('pr1', 'r1', 't', 'MERGED', 'alice@x', 'copilot/feat',
            'main', 1, 2, 'merge1', '[]')`); err != nil {
		t.Fatalf("seed pr: %v", err)
	}

	// PR commit with a trailer → squash_recovery signal on merge1.
	if _, err := s.DB().Exec(`
        INSERT INTO pr_commits (pr_id, commit_sha, author_email, author_name, message)
        VALUES ('pr1', 'src1', 'alice@x', 'A', ?)`,
		"wip\n\nCo-Authored-By: aider <aider@local>"); err != nil {
		t.Fatalf("seed pr_commit: %v", err)
	}

	ctx := context.Background()
	cfg := DetectionConfig{
		SquashMergeRecovery: true,
		SeatInference:       true,
	}
	if _, err := RunAllDetectors(ctx, s, cfg); err != nil {
		t.Fatalf("RunAllDetectors: %v", err)
	}

	kinds := map[string]int{}
	rows, err := s.DB().Query(`SELECT signal_kind, COUNT(*) FROM ai_signals GROUP BY signal_kind`)
	if err != nil {
		t.Fatalf("read signals: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		kinds[k] = n
	}

	for _, want := range []string{KindCoAuthorTrailer, KindBranchName, KindSquashRecovery, KindSeatInference} {
		if kinds[want] == 0 {
			t.Errorf("expected at least one %s signal, kinds = %v", want, kinds)
		}
	}
}

func TestRunAllDetectors_RespectsToggles(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	wk := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC)
	seedCommitForInference(t, s, "c1", "alice@x", "r1", wk, true) // existing high-conf signal
	seedCommitForInference(t, s, "c2", "alice@x", "r1", wk, false)

	ctx := context.Background()
	cfg := DetectionConfig{
		SquashMergeRecovery: false,
		SeatInference:       false,
	}
	if _, err := RunAllDetectors(ctx, s, cfg); err != nil {
		t.Fatalf("RunAllDetectors: %v", err)
	}

	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ai_signals WHERE signal_kind=?`, KindSeatInference).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("SeatInference disabled but got %d seat_inference rows", n)
	}
}

// AdoptionRatio is the only WeekStats helper not exercised by the cutover
// or weekly tests yet.
func TestWeekStats_AdoptionRatio(t *testing.T) {
	cases := []struct {
		name string
		w    WeekStats
		want float64
	}{
		{"zero authors", WeekStats{}, 0},
		{"half", WeekStats{DistinctAuthors: 4, AIAuthors: 2}, 0.5},
		{"all", WeekStats{DistinctAuthors: 3, AIAuthors: 3}, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.w.AdoptionRatio(); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

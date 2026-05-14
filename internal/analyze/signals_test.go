package analyze

import (
	"context"
	"testing"

	"github.com/StephanSchmidt/loupe/internal/store"
)

func TestDetectFromMessage(t *testing.T) {
	cases := []struct {
		name    string
		msg     string
		wantN   int
		wantSrc []string
	}{
		{
			name: "claude trailer",
			msg: `fix login

Co-Authored-By: Claude <noreply@anthropic.com>`,
			wantN:   1,
			wantSrc: []string{SourceClaude},
		},
		{
			name: "aider trailer",
			msg: `refactor auth

Co-Authored-By: aider <aider@bot.local>`,
			wantN:   1,
			wantSrc: []string{SourceAider},
		},
		{
			name: "two distinct trailers",
			msg: `multi tool

Co-Authored-By: Claude <noreply@anthropic.com>
Co-Authored-By: Copilot <copilot@github.com>`,
			wantN:   2,
			wantSrc: []string{SourceClaude, SourceCopilot},
		},
		{
			name: "duplicate trailer collapses",
			msg: `multi-claude

Co-Authored-By: Claude <noreply@anthropic.com>
Co-Authored-By: Claude Code <noreply@anthropic.com>`,
			wantN:   1,
			wantSrc: []string{SourceClaude},
		},
		{
			name: "human co-author does not count",
			msg: `pair programming

Co-Authored-By: Bob <bob@acme.com>`,
			wantN: 0,
		},
		{
			name:  "no trailer",
			msg:   "regular commit",
			wantN: 0,
		},
		{
			name: "trailer-shaped prose in body should not match",
			msg: `discussion

In the meeting Alice said: Co-Authored-By: us all <none>`,
			wantN: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectFromMessage(tc.msg)
			if len(got) != tc.wantN {
				t.Fatalf("got %d signals (%v), want %d", len(got), got, tc.wantN)
			}
			for i, want := range tc.wantSrc {
				if got[i].Source != want {
					t.Errorf("signals[%d].Source = %q, want %q", i, got[i].Source, want)
				}
				if got[i].Confidence != ConfidenceHigh {
					t.Errorf("signals[%d].Confidence = %q, want high", i, got[i].Confidence)
				}
			}
		})
	}
}

func TestDetectAndStore_Idempotent(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	commits := []struct{ sha, msg string }{
		{"a1", "noop"},
		{"a2", "fix\n\nCo-Authored-By: Claude <noreply@anthropic.com>"},
		{"a3", "feat\n\nCo-Authored-By: aider <aider@local>"},
	}
	for _, c := range commits {
		_, err := s.DB().Exec(
			`INSERT INTO commits (sha, repo_name, author_email, author_name, committed_at, message)
             VALUES (?, 'r', 'a@a', 'A', 1700000000, ?)`,
			c.sha, c.msg)
		if err != nil {
			t.Fatalf("seed commit %s: %v", c.sha, err)
		}
	}

	ctx := context.Background()

	n, err := DetectAndStore(ctx, s)
	if err != nil {
		t.Fatalf("DetectAndStore: %v", err)
	}
	if n != 2 {
		t.Errorf("first DetectAndStore wrote %d signals, want 2", n)
	}

	var rowCount int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ai_signals`).Scan(&rowCount); err != nil {
		t.Fatalf("count signals: %v", err)
	}
	if rowCount != 2 {
		t.Errorf("ai_signals rows = %d, want 2", rowCount)
	}

	// Re-run must not duplicate.
	if _, err := DetectAndStore(ctx, s); err != nil {
		t.Fatalf("second DetectAndStore: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ai_signals`).Scan(&rowCount); err != nil {
		t.Fatalf("count signals after re-run: %v", err)
	}
	if rowCount != 2 {
		t.Errorf("ai_signals rows after re-run = %d, want 2", rowCount)
	}
}

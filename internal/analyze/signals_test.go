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
			name: "aider trailer without email",
			msg: `refactor auth

Co-Authored-By: aider`,
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
		{
			name: "devin trailer",
			msg: `bugfix

Co-Authored-By: devin-ai-integration[bot] <158243894+devin-ai-integration[bot]@users.noreply.github.com>`,
			wantN:   1,
			wantSrc: []string{SourceDevin},
		},
		{
			name: "gemini code assist trailer",
			msg: `chore: bump deps

Co-Authored-By: gemini-code-assist[bot] <176961590+gemini-code-assist[bot]@users.noreply.github.com>`,
			wantN:   1,
			wantSrc: []string{SourceGemini},
		},
		{
			name: "jules trailer",
			msg: `feat: scaffold module

Co-Authored-By: google-labs-jules[bot] <161369871+google-labs-jules[bot]@users.noreply.github.com>`,
			wantN:   1,
			wantSrc: []string{SourceJules},
		},
		{
			name: "opencode trailer",
			msg: `wip

Co-Authored-By: opencode <noreply@opencode.ai>`,
			wantN:   1,
			wantSrc: []string{SourceOpenCode},
		},
		{
			name: "broken trailer with tool substring in name does not match",
			msg: `something

Co-Authored-By: cursor <broken email`,
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
				if got[i].Kind != KindCoAuthorTrailer {
					t.Errorf("signals[%d].Kind = %q, want %q", i, got[i].Kind, KindCoAuthorTrailer)
				}
				if got[i].Confidence != ConfidenceHigh {
					t.Errorf("signals[%d].Confidence = %q, want high", i, got[i].Confidence)
				}
			}
		})
	}
}

func TestDetectFromBodyFooters(t *testing.T) {
	cases := []struct {
		name    string
		msg     string
		wantN   int
		wantSrc []string
	}{
		{
			name: "claude code default footer",
			msg: `fix bug

🤖 Generated with [Claude Code](https://claude.com/claude-code)`,
			wantN:   1,
			wantSrc: []string{SourceClaude},
		},
		{
			name:    "claude code footer without emoji",
			msg:     "Generated with Claude Code",
			wantN:   1,
			wantSrc: []string{SourceClaude},
		},
		{
			name: "opencode footer",
			msg: `wip

🤖 Generated with [opencode]`,
			wantN:   1,
			wantSrc: []string{SourceOpenCode},
		},
		{
			name:  "prose containing 'Generated' is not a footer",
			msg:   "Generated 30 new test files for the renderer.",
			wantN: 0,
		},
		{
			name:  "empty message",
			msg:   "",
			wantN: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectFromBodyFooters(tc.msg)
			if len(got) != tc.wantN {
				t.Fatalf("got %d footer signals (%v), want %d", len(got), got, tc.wantN)
			}
			for i, want := range tc.wantSrc {
				if got[i].Source != want {
					t.Errorf("signals[%d].Source = %q, want %q", i, got[i].Source, want)
				}
				if got[i].Kind != KindBodyFooter {
					t.Errorf("signals[%d].Kind = %q, want %q", i, got[i].Kind, KindBodyFooter)
				}
			}
		})
	}
}

func TestIsAIBot(t *testing.T) {
	cases := []struct {
		desc, email, name string
		wantSrc           string
		wantOK            bool
	}{
		{"copilot swe agent",
			"203108557+copilot-swe-agent[bot]@users.noreply.github.com", "copilot-swe-agent[bot]",
			SourceCopilot, true},
		{"copilot bot",
			"175728472+copilot[bot]@users.noreply.github.com", "copilot[bot]",
			SourceCopilot, true},
		{"devin ai integration",
			"158243894+devin-ai-integration[bot]@users.noreply.github.com", "devin-ai-integration[bot]",
			SourceDevin, true},
		{"gemini code assist",
			"176961590+gemini-code-assist[bot]@users.noreply.github.com", "gemini-code-assist[bot]",
			SourceGemini, true},
		{"jules bot",
			"161369871+google-labs-jules[bot]@users.noreply.github.com", "google-labs-jules[bot]",
			SourceJules, true},

		{"human author", "alice@example.com", "Alice", "", false},
		{"dependabot is not an AI bot",
			"49699333+dependabot[bot]@users.noreply.github.com", "dependabot[bot]", "", false},
		{"copilot pr reviewer is not the coding agent",
			"175728472+copilot-pull-request-reviewer[bot]@users.noreply.github.com", "copilot-pull-request-reviewer[bot]",
			"", false},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			gotSrc, gotOK := IsAIBot(c.email, c.name)
			if gotOK != c.wantOK || gotSrc != c.wantSrc {
				t.Errorf("IsAIBot(%q, %q) = (%q, %v), want (%q, %v)",
					c.email, c.name, gotSrc, gotOK, c.wantSrc, c.wantOK)
			}
		})
	}
}

func TestDetectFromAuthorIdentity(t *testing.T) {
	t.Run("known ai bot returns high confidence signal", func(t *testing.T) {
		got, ok := detectFromAuthorIdentity(
			"203108557+copilot-swe-agent[bot]@users.noreply.github.com",
			"copilot-swe-agent[bot]")
		if !ok {
			t.Fatal("expected signal for copilot-swe-agent author")
		}
		if got.Source != SourceCopilot || got.Confidence != ConfidenceHigh || got.Kind != KindBotAuthor {
			t.Errorf("got %+v, want source=%s confidence=%s kind=%s",
				got, SourceCopilot, ConfidenceHigh, KindBotAuthor)
		}
	})
	t.Run("unknown [bot] returns medium-confidence unknown-ai-bot", func(t *testing.T) {
		got, ok := detectFromAuthorIdentity("999+mystery-bot[bot]@users.noreply.github.com", "mystery-bot[bot]")
		if !ok {
			t.Fatal("expected fallback signal for unknown bot")
		}
		if got.Source != SourceUnknownAIBot || got.Confidence != ConfidenceMedium {
			t.Errorf("got %+v, want source=%s confidence=%s",
				got, SourceUnknownAIBot, ConfidenceMedium)
		}
	})
	t.Run("curated non-AI bot does not fire", func(t *testing.T) {
		_, ok := detectFromAuthorIdentity("49699333+dependabot[bot]@users.noreply.github.com", "dependabot[bot]")
		if ok {
			t.Error("dependabot should not produce an AI bot-author signal")
		}
	})
	t.Run("human author does not fire", func(t *testing.T) {
		_, ok := detectFromAuthorIdentity("alice@example.com", "Alice")
		if ok {
			t.Error("human author should not produce a bot-author signal")
		}
	})
}

func TestDetectAndStore_Idempotent(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	commits := []struct{ sha, email, name, msg string }{
		{"a1", "a@a", "A", "noop"},
		{"a2", "a@a", "A", "fix\n\nCo-Authored-By: Claude <noreply@anthropic.com>"},
		{"a3", "a@a", "A", "feat\n\nCo-Authored-By: aider <aider@local>"},
		{"a4", "203108557+copilot-swe-agent[bot]@users.noreply.github.com", "copilot-swe-agent[bot]",
			"agent commit"},
		{"a5", "a@a", "A", "fix\n\n🤖 Generated with [Claude Code]\nCo-Authored-By: Claude <noreply@anthropic.com>"},
	}
	for _, c := range commits {
		_, err := s.DB().Exec(
			`INSERT INTO commits (sha, repo_name, author_email, author_name, committed_at, message)
             VALUES (?, 'r', ?, ?, 1700000000, ?)`,
			c.sha, c.email, c.name, c.msg)
		if err != nil {
			t.Fatalf("seed commit %s: %v", c.sha, err)
		}
	}

	ctx := context.Background()

	if _, err := DetectAndStore(ctx, s); err != nil {
		t.Fatalf("DetectAndStore: %v", err)
	}

	var rowCount int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ai_signals`).Scan(&rowCount); err != nil {
		t.Fatalf("count signals: %v", err)
	}
	// a2: trailer (claude)
	// a3: trailer (aider)
	// a4: bot_author (copilot)
	// a5: trailer (claude) + body_footer (claude)
	// → 5 rows
	if rowCount != 5 {
		t.Errorf("ai_signals rows = %d, want 5", rowCount)
	}

	// Re-run must not duplicate.
	if _, err := DetectAndStore(ctx, s); err != nil {
		t.Fatalf("second DetectAndStore: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ai_signals`).Scan(&rowCount); err != nil {
		t.Fatalf("count signals after re-run: %v", err)
	}
	if rowCount != 5 {
		t.Errorf("ai_signals rows after re-run = %d, want 5", rowCount)
	}
}

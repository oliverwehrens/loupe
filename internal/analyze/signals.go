package analyze

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/StephanSchmidt/loupe/internal/store"
)

const (
	SourceClaude  = "claude"
	SourceAider   = "aider"
	SourceCopilot = "copilot"
	SourceCursor  = "cursor"

	KindCoAuthorTrailer = "co_author_trailer"

	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// trailerRe matches a single Co-Authored-By trailer line.
//
// The regex is permissive on whitespace and case (modern git is case-insensitive
// for trailer tokens) and anchored to a line so unrelated occurrences of
// "Co-Authored-By:" inside prose don't trigger false positives.
var trailerRe = regexp.MustCompile(`(?im)^\s*Co-Authored-By:\s*([^<]+?)\s*<\s*([^>]+)\s*>\s*$`)

type Signal struct {
	CommitSHA  string
	Kind       string
	Source     string
	Confidence string
	Detail     string
}

// DetectFromMessage extracts AI signals from a single commit message.
// The same (commit, source) pair is reported at most once, even if multiple
// matching trailers exist — duplicates would just collide in the upsert.
func DetectFromMessage(message string) []Signal {
	matches := trailerRe.FindAllStringSubmatch(message, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]Signal, 0, len(matches))
	for _, m := range matches {
		name := strings.TrimSpace(m[1])
		email := strings.ToLower(strings.TrimSpace(m[2]))
		source := classifyTrailer(name, email)
		if source == "" {
			continue
		}
		if _, dup := seen[source]; dup {
			continue
		}
		seen[source] = struct{}{}
		out = append(out, Signal{
			Kind:       KindCoAuthorTrailer,
			Source:     source,
			Confidence: ConfidenceHigh,
			Detail:     fmt.Sprintf("%s <%s>", name, email),
		})
	}
	return out
}

func classifyTrailer(name, email string) string {
	lname := strings.ToLower(name)
	switch {
	case strings.Contains(email, "anthropic.com") || strings.Contains(lname, "claude"):
		return SourceClaude
	case strings.Contains(lname, "aider") || strings.HasPrefix(email, "aider"):
		return SourceAider
	case strings.Contains(lname, "copilot") || strings.Contains(email, "copilot"):
		return SourceCopilot
	case strings.Contains(lname, "cursor") || strings.Contains(email, "cursor"):
		return SourceCursor
	}
	return ""
}

const upsertSignalSQL = `
INSERT INTO ai_signals (commit_sha, signal_kind, signal_source, confidence, detail)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(commit_sha, signal_kind, signal_source) DO UPDATE SET
    confidence = excluded.confidence,
    detail     = excluded.detail
`

// DetectAndStore scans every commit in the store, extracts AI signals from
// each commit message, and upserts them into ai_signals. Idempotent.
// Returns the number of signal rows written.
func DetectAndStore(ctx context.Context, s *store.Store) (int, error) {
	// SetMaxOpenConns(1) means we can't iterate rows AND open a tx on the
	// same connection. Read all commits into memory first, then write.
	rows, err := s.DB().QueryContext(ctx, `SELECT sha, message FROM commits`)
	if err != nil {
		return 0, fmt.Errorf("query commits: %w", err)
	}
	type row struct {
		sha, msg string
	}
	var commits []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.sha, &r.msg); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan commit row: %w", err)
		}
		commits = append(commits, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("iterate commits: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close rows: %w", err)
	}

	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, upsertSignalSQL)
	if err != nil {
		return 0, fmt.Errorf("prepare upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	count := 0
	for _, c := range commits {
		for _, sig := range DetectFromMessage(c.msg) {
			if _, err := stmt.ExecContext(ctx, c.sha, sig.Kind, sig.Source, sig.Confidence, sig.Detail); err != nil {
				return count, fmt.Errorf("upsert signal for %s: %w", c.sha, err)
			}
			count++
		}
	}
	if err := tx.Commit(); err != nil {
		return count, fmt.Errorf("commit tx: %w", err)
	}
	return count, nil
}

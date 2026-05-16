package analyze

import (
	"context"
	"fmt"
	"time"

	"github.com/StephanSchmidt/loupe/internal/store"
)

// InferFromSeatHolders extends the AI-commit picture beyond commits with
// direct evidence: for each (author_email, ISO-week, repo) group that
// contains at least one high-confidence AI signal, every other commit in
// the same group from the same author is marked seat_inference at medium
// confidence.
//
// The intuition is "if Alice wrote a commit with a Claude trailer this
// week in repo X, the four other commits she made in the same repo and
// week are likely also AI-assisted". This is recall-oriented and explicitly
// inferred; the deck distinguishes high vs medium confidence so the
// adoption number can't pretend the inference is direct evidence.
//
// Inference uses SourceAIGeneric for the source — picking a specific
// tool when a seat-holder uses several would be guesswork.
//
// Idempotent — primary key (commit_sha, signal_kind="seat_inference",
// signal_source) prevents duplicates across re-runs.
func InferFromSeatHolders(ctx context.Context, s *store.Store) (int, error) {
	commits, err := loadCommitsForInference(ctx, s)
	if err != nil {
		return 0, err
	}
	highSig, err := loadHighConfidenceSignals(ctx, s)
	if err != nil {
		return 0, err
	}

	type key struct {
		email string
		week  time.Time
		repo  string
	}
	type group struct {
		hasHighSignal bool
		untagged      []string
	}
	groups := map[key]*group{}

	for _, c := range commits {
		if IsBot(c.authorEmail, c.authorName) {
			if _, isAI := IsAIBot(c.authorEmail, c.authorName); !isAI {
				// Filter non-AI bots so dependabot weeks don't propagate
				// seat-inference into Alice's commits.
				continue
			}
		}
		k := key{c.authorEmail, IsoWeekStart(c.committedAt), c.repoName}
		g := groups[k]
		if g == nil {
			g = &group{}
			groups[k] = g
		}
		if _, has := highSig[c.sha]; has {
			g.hasHighSignal = true
		} else {
			g.untagged = append(g.untagged, c.sha)
		}
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
	for _, g := range groups {
		if !g.hasHighSignal {
			continue
		}
		for _, sha := range g.untagged {
			if _, err := stmt.ExecContext(ctx,
				sha, KindSeatInference, SourceAIGeneric, ConfidenceMedium,
				"inferred from same author/week/repo with direct AI signal"); err != nil {
				return count, fmt.Errorf("upsert seat_inference for %s: %w", sha, err)
			}
			count++
		}
	}
	if err := tx.Commit(); err != nil {
		return count, fmt.Errorf("commit tx: %w", err)
	}
	return count, nil
}

type commitForInference struct {
	sha         string
	authorEmail string
	authorName  string
	repoName    string
	committedAt time.Time
}

func loadCommitsForInference(ctx context.Context, s *store.Store) ([]commitForInference, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT sha, author_email, author_name, repo_name, committed_at FROM commits`)
	if err != nil {
		return nil, fmt.Errorf("query commits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []commitForInference
	for rows.Next() {
		var c commitForInference
		var ts int64
		if err := rows.Scan(&c.sha, &c.authorEmail, &c.authorName, &c.repoName, &ts); err != nil {
			return nil, fmt.Errorf("scan commit: %w", err)
		}
		c.committedAt = time.Unix(ts, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// loadHighConfidenceSignals returns the set of commit SHAs that carry at
// least one high-confidence AI signal. Used as the "this commit already
// has direct evidence" gate for seat inference.
func loadHighConfidenceSignals(ctx context.Context, s *store.Store) (map[string]struct{}, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT DISTINCT commit_sha FROM ai_signals WHERE confidence = ?`, ConfidenceHigh)
	if err != nil {
		return nil, fmt.Errorf("query high signals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]struct{}{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, fmt.Errorf("scan high signal: %w", err)
		}
		out[sha] = struct{}{}
	}
	return out, rows.Err()
}

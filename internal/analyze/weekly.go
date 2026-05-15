package analyze

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/StephanSchmidt/loupe/internal/store"
)

// WeekStats holds aggregate commit + adoption counts for a single ISO week.
//
// AdoptionRatio is left for callers to compute on demand — keeping it derived
// avoids the float64 drifting out of sync with the integer counts.
type WeekStats struct {
	WeekStart       time.Time
	TotalCommits    int
	AICommits       int
	DistinctAuthors int
	AIAuthors       int
}

// CommitRatio is AICommits / TotalCommits. Returns 0 if TotalCommits is 0.
func (w WeekStats) CommitRatio() float64 {
	if w.TotalCommits == 0 {
		return 0
	}
	return float64(w.AICommits) / float64(w.TotalCommits)
}

// AdoptionRatio is AIAuthors / DistinctAuthors. Returns 0 if DistinctAuthors is 0.
func (w WeekStats) AdoptionRatio() float64 {
	if w.DistinctAuthors == 0 {
		return 0
	}
	return float64(w.AIAuthors) / float64(w.DistinctAuthors)
}

// IsoWeekStart returns the start (Monday 00:00 UTC) of the ISO week that
// contains t.
func IsoWeekStart(t time.Time) time.Time {
	t = t.UTC()
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7 // Sunday → end of ISO week
	}
	return time.Date(t.Year(), t.Month(), t.Day()-(wd-1), 0, 0, 0, 0, time.UTC)
}

// WeeklyStats returns one WeekStats row per ISO week with at least one
// commit, ordered chronologically.
func WeeklyStats(ctx context.Context, s *store.Store) ([]WeekStats, error) {
	rows, err := s.DB().QueryContext(ctx, `
        SELECT c.committed_at, c.author_email, c.author_name,
               CASE WHEN sig.commit_sha IS NOT NULL THEN 1 ELSE 0 END AS is_ai
        FROM commits c
        LEFT JOIN (SELECT DISTINCT commit_sha FROM ai_signals) sig
            ON sig.commit_sha = c.sha
        ORDER BY c.committed_at
    `)
	if err != nil {
		return nil, fmt.Errorf("query weekly: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type weekAgg struct {
		WeekStats
		authors   map[string]struct{}
		aiAuthors map[string]struct{}
	}
	buckets := make(map[time.Time]*weekAgg)

	for rows.Next() {
		var ts int64
		var email, name string
		var isAI int
		if err := rows.Scan(&ts, &email, &name, &isAI); err != nil {
			return nil, fmt.Errorf("scan weekly row: %w", err)
		}
		if IsBot(email, name) {
			continue
		}
		wk := IsoWeekStart(time.Unix(ts, 0))
		b, ok := buckets[wk]
		if !ok {
			b = &weekAgg{
				WeekStats: WeekStats{WeekStart: wk},
				authors:   make(map[string]struct{}),
				aiAuthors: make(map[string]struct{}),
			}
			buckets[wk] = b
		}
		b.TotalCommits++
		b.authors[email] = struct{}{}
		if isAI == 1 {
			b.AICommits++
			b.aiAuthors[email] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate weekly: %w", err)
	}

	out := make([]WeekStats, 0, len(buckets))
	for _, b := range buckets {
		b.DistinctAuthors = len(b.authors)
		b.AIAuthors = len(b.aiAuthors)
		out = append(out, b.WeekStats)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WeekStart.Before(out[j].WeekStart) })
	return out, nil
}

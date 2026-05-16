package analyze

import (
	"context"
	"fmt"
	"sort"

	"github.com/montanaflynn/stats"

	"github.com/StephanSchmidt/loupe/internal/store"
)

// Summary holds the six descriptive numbers — mean, median, p10, p90, min,
// max — that summarise any weekly series. Returned from Summarise.
type Summary struct {
	Mean   float64
	Median float64
	P10    float64
	P90    float64
	Min    float64
	Max    float64
}

// Summarise computes a Summary over xs. Returns an error when xs is empty.
func Summarise(xs []float64) (Summary, error) {
	d := stats.Float64Data(xs)
	mean, err := d.Mean()
	if err != nil {
		return Summary{}, fmt.Errorf("mean: %w", err)
	}
	median, err := d.Median()
	if err != nil {
		return Summary{}, fmt.Errorf("median: %w", err)
	}
	p10, err := d.Percentile(10)
	if err != nil {
		return Summary{}, fmt.Errorf("p10: %w", err)
	}
	p90, err := d.Percentile(90)
	if err != nil {
		return Summary{}, fmt.Errorf("p90: %w", err)
	}
	mn, err := d.Min()
	if err != nil {
		return Summary{}, fmt.Errorf("min: %w", err)
	}
	mx, err := d.Max()
	if err != nil {
		return Summary{}, fmt.Errorf("max: %w", err)
	}
	return Summary{Mean: mean, Median: median, P10: p10, P90: p90, Min: mn, Max: mx}, nil
}

// WeeklySeries decomposes weeks into three aligned float series: total
// commits, AI commits, and the AI ratio per week.
func WeeklySeries(weeks []WeekStats) (commits, ai, ratio []float64) {
	commits = make([]float64, len(weeks))
	ai = make([]float64, len(weeks))
	ratio = make([]float64, len(weeks))
	for i, w := range weeks {
		commits[i] = float64(w.TotalCommits)
		ai[i] = float64(w.AICommits)
		ratio[i] = w.CommitRatio()
	}
	return commits, ai, ratio
}

// TrendDirection is the human-readable label produced by RatioTrend.
type TrendDirection string

const (
	TrendRising  TrendDirection = "rising"
	TrendFalling TrendDirection = "falling"
	TrendFlat    TrendDirection = "flat"
)

// RatioTrend fits a line to the AI-ratio series and returns the slope in
// percentage-points-per-week along with a direction label. ok=false when
// the series is too short or the regression fails.
func RatioTrend(ratio []float64) (slopePPPerWeek float64, direction TrendDirection, ok bool) {
	if len(ratio) < 2 {
		return 0, TrendFlat, false
	}
	series := make(stats.Series, len(ratio))
	for i, v := range ratio {
		series[i] = stats.Coordinate{X: float64(i), Y: v}
	}
	fitted, err := stats.LinearRegression(series)
	if err != nil || len(fitted) < 2 {
		return 0, TrendFlat, false
	}
	dx := fitted[len(fitted)-1].X - fitted[0].X
	if dx == 0 {
		return 0, TrendFlat, false
	}
	slope := (fitted[len(fitted)-1].Y - fitted[0].Y) / dx
	direction = TrendFlat
	switch {
	case slope > 0.005:
		direction = TrendRising
	case slope < -0.005:
		direction = TrendFalling
	}
	return slope * 100, direction, true
}

// SplitByCutover partitions weeks into pre- and post-cutover cohorts.
// Weeks whose WeekStart is before cutover.Date go into before; the rest
// into after.
func SplitByCutover(weeks []WeekStats, cutover Cutover) (before, after []WeekStats) {
	for _, w := range weeks {
		if w.WeekStart.Before(cutover.Date) {
			before = append(before, w)
		} else {
			after = append(after, w)
		}
	}
	return before, after
}

// CohortMeans returns the mean weekly commit count and mean AI ratio for a
// cohort of weeks. Empty cohorts produce zeros.
func CohortMeans(weeks []WeekStats) (commitsMean, ratioMean float64) {
	if len(weeks) == 0 {
		return 0, 0
	}
	commits, _, ratio := WeeklySeries(weeks)
	commitsMean, _ = stats.Float64Data(commits).Mean()
	ratioMean, _ = stats.Float64Data(ratio).Mean()
	return commitsMean, ratioMean
}

// ToolSignal is a per-tool attribution row from ToolBreakdown.
type ToolSignal struct {
	// Source is the detector-tagged tool key (e.g. "claude", "copilot").
	Source string
	// Commits is the distinct-commit count attributed to this tool — a
	// commit with multiple signals from the same source counts once.
	Commits int
	// Pct is Commits / sum(Commits) × 100 across the returned rows.
	Pct float64
}

// ToolBreakdownStats summarises tool attribution across all AI-tagged
// commits in the store.
type ToolBreakdownStats struct {
	Tools           []ToolSignal // sorted by Commits desc, then Source asc
	TotalSignals    int          // sum of per-tool commit counts (multi-tool commits double-count)
	DistinctCommits int          // distinct commit SHAs across all tools (excludes bot-authored)
}

// ToolBreakdown queries the ai_signals table joined to commits and
// produces a per-tool distinct-commit count, filtering bot authors so
// they don't drown out human-authored attribution.
func ToolBreakdown(ctx context.Context, s *store.Store) (ToolBreakdownStats, error) {
	rows, err := s.DB().QueryContext(ctx, `
        SELECT sig.signal_source, sig.commit_sha, c.author_email, c.author_name
        FROM ai_signals sig
        JOIN commits c ON c.sha = sig.commit_sha
    `)
	if err != nil {
		return ToolBreakdownStats{}, fmt.Errorf("query ai tool breakdown: %w", err)
	}
	defer func() { _ = rows.Close() }()

	perSource := make(map[string]map[string]struct{})
	distinct := make(map[string]struct{})
	for rows.Next() {
		var source, sha, email, name string
		if err := rows.Scan(&source, &sha, &email, &name); err != nil {
			return ToolBreakdownStats{}, fmt.Errorf("scan tool row: %w", err)
		}
		if IsBot(email, name) {
			continue
		}
		distinct[sha] = struct{}{}
		if _, ok := perSource[source]; !ok {
			perSource[source] = make(map[string]struct{})
		}
		perSource[source][sha] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return ToolBreakdownStats{}, err
	}

	out := ToolBreakdownStats{DistinctCommits: len(distinct)}
	if len(perSource) == 0 {
		return out, nil
	}
	for src, shas := range perSource {
		out.Tools = append(out.Tools, ToolSignal{Source: src, Commits: len(shas)})
		out.TotalSignals += len(shas)
	}
	sort.Slice(out.Tools, func(i, j int) bool {
		if out.Tools[i].Commits != out.Tools[j].Commits {
			return out.Tools[i].Commits > out.Tools[j].Commits
		}
		return out.Tools[i].Source < out.Tools[j].Source
	})
	for i := range out.Tools {
		out.Tools[i].Pct = float64(out.Tools[i].Commits) / float64(out.TotalSignals) * 100
	}
	return out, nil
}

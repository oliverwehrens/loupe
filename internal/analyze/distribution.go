package analyze

import (
	"fmt"

	"github.com/montanaflynn/stats"
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

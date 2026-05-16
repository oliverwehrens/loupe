package analyze

import (
	"math"
	"testing"
	"time"
)

func TestSummarise(t *testing.T) {
	s, err := Summarise([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	if err != nil {
		t.Fatalf("Summarise: %v", err)
	}
	if s.Mean != 5.5 || s.Median != 5.5 {
		t.Errorf("mean=%v median=%v want 5.5/5.5", s.Mean, s.Median)
	}
	if s.Min != 1 || s.Max != 10 {
		t.Errorf("min/max = %v/%v want 1/10", s.Min, s.Max)
	}
}

func TestSummarise_Empty(t *testing.T) {
	if _, err := Summarise(nil); err == nil {
		t.Error("expected error on empty input")
	}
}

func TestWeeklySeries(t *testing.T) {
	weeks := []WeekStats{
		{WeekStart: time.Now(), TotalCommits: 10, AICommits: 2},
		{WeekStart: time.Now(), TotalCommits: 20, AICommits: 10},
	}
	commits, ai, ratio := WeeklySeries(weeks)
	if commits[0] != 10 || commits[1] != 20 {
		t.Errorf("commits = %v", commits)
	}
	if ai[0] != 2 || ai[1] != 10 {
		t.Errorf("ai = %v", ai)
	}
	if math.Abs(ratio[0]-0.2) > 1e-9 || math.Abs(ratio[1]-0.5) > 1e-9 {
		t.Errorf("ratio = %v want [0.2, 0.5]", ratio)
	}
}

func TestRatioTrend_Rising(t *testing.T) {
	slope, dir, ok := RatioTrend([]float64{0.0, 0.1, 0.2, 0.3, 0.4})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if dir != TrendRising {
		t.Errorf("direction = %q want rising", dir)
	}
	if slope < 9 || slope > 11 {
		t.Errorf("slope = %v, want ≈10 pp/week", slope)
	}
}

func TestRatioTrend_Falling(t *testing.T) {
	_, dir, ok := RatioTrend([]float64{0.4, 0.3, 0.2, 0.1, 0.0})
	if !ok || dir != TrendFalling {
		t.Errorf("direction = %q ok=%v want falling/true", dir, ok)
	}
}

func TestRatioTrend_Flat(t *testing.T) {
	_, dir, ok := RatioTrend([]float64{0.2, 0.2, 0.2, 0.2})
	if !ok || dir != TrendFlat {
		t.Errorf("direction = %q ok=%v want flat/true", dir, ok)
	}
}

func TestRatioTrend_TooShort(t *testing.T) {
	if _, _, ok := RatioTrend([]float64{0.3}); ok {
		t.Error("expected ok=false for single-element series")
	}
}

func TestSplitByCutover(t *testing.T) {
	cutoverDate := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	weeks := []WeekStats{
		{WeekStart: time.Date(2026, 2, 23, 0, 0, 0, 0, time.UTC)},
		{WeekStart: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)},
		{WeekStart: time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)},
	}
	before, after := SplitByCutover(weeks, Cutover{Date: cutoverDate})
	if len(before) != 1 || len(after) != 2 {
		t.Errorf("split = %d before / %d after, want 1/2", len(before), len(after))
	}
}

func TestCohortMeans(t *testing.T) {
	weeks := []WeekStats{
		{TotalCommits: 10, AICommits: 5},
		{TotalCommits: 20, AICommits: 10},
	}
	cMean, rMean := CohortMeans(weeks)
	if cMean != 15 {
		t.Errorf("commitsMean = %v, want 15", cMean)
	}
	if math.Abs(rMean-0.5) > 1e-9 {
		t.Errorf("ratioMean = %v, want 0.5", rMean)
	}
}

func TestCohortMeans_Empty(t *testing.T) {
	c, r := CohortMeans(nil)
	if c != 0 || r != 0 {
		t.Errorf("empty cohort = (%v, %v), want (0, 0)", c, r)
	}
}

package deck

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-analyze/charts"

	"github.com/StephanSchmidt/loupe/internal/analyze"
)

const (
	chartWidth  = 1200
	chartHeight = 480
)

// RenderThroughputChart writes a PNG of weekly commit totals stacked as
// human-authored vs AI-tagged. If the cutover is detected, its week label is
// flagged with a leading "▼" and the chart subtext names the cutover date.
func RenderThroughputChart(weeks []analyze.WeekStats, cutover analyze.Cutover, outPath string) error {
	if len(weeks) == 0 {
		return fmt.Errorf("RenderThroughputChart: no weekly data")
	}

	labels, cutoverIdx := buildLabels(weeks, cutover)
	human := make([]float64, len(weeks))
	ai := make([]float64, len(weeks))
	for i, w := range weeks {
		human[i] = float64(w.TotalCommits - w.AICommits)
		ai[i] = float64(w.AICommits)
	}

	opt := charts.NewBarChartOptionWithData([][]float64{human, ai})
	opt.StackSeries = charts.Ptr(true)
	opt.Title = charts.TitleOption{Text: "Weekly commits"}
	if cutover.Detected {
		opt.Title.Subtext = fmt.Sprintf("AI adoption cutover: %s (%s)",
			cutover.Date.Format("Jan 2, 2006"), cutover.Reason)
	}
	opt.CategoryAxis = charts.CategoryAxisOption{
		Labels:        labels,
		LabelRotation: charts.DegreesToRadians(45),
	}
	opt.Legend = charts.LegendOption{
		SeriesNames: []string{"Human", "AI-tagged"},
	}
	_ = cutoverIdx // currently only consumed via the label prefix

	p := charts.NewPainter(charts.PainterOptions{Width: chartWidth, Height: chartHeight})
	if err := p.BarChart(opt); err != nil {
		return fmt.Errorf("render throughput chart: %w", err)
	}
	return writePNG(p, outPath)
}

// RenderAdoptionChart writes a PNG line chart of AI-author adoption % per week.
func RenderAdoptionChart(weeks []analyze.WeekStats, cutover analyze.Cutover, outPath string) error {
	if len(weeks) == 0 {
		return fmt.Errorf("RenderAdoptionChart: no weekly data")
	}

	labels, _ := buildLabels(weeks, cutover)
	pct := make([]float64, len(weeks))
	for i, w := range weeks {
		pct[i] = w.AdoptionRatio() * 100
	}

	opt := charts.NewLineChartOptionWithData([][]float64{pct})
	opt.Title = charts.TitleOption{Text: "AI adoption — % of weekly active devs"}
	if cutover.Detected {
		opt.Title.Subtext = fmt.Sprintf("Cutover: %s (%s)",
			cutover.Date.Format("Jan 2, 2006"), cutover.Reason)
	}
	opt.XAxis = charts.XAxisOption{
		Labels:        labels,
		LabelRotation: charts.DegreesToRadians(45),
	}
	opt.Legend = charts.LegendOption{
		SeriesNames: []string{"AI-using devs %"},
	}

	p := charts.NewPainter(charts.PainterOptions{Width: chartWidth, Height: chartHeight})
	if err := p.LineChart(opt); err != nil {
		return fmt.Errorf("render adoption chart: %w", err)
	}
	return writePNG(p, outPath)
}

// buildLabels returns one axis label per week and the index of the cutover
// week (or -1 if no cutover is detected). The cutover label is prefixed with
// "▼ " so it stands out in lieu of a true vertical-line marker, which
// go-analyze/charts doesn't currently support at arbitrary category indices.
func buildLabels(weeks []analyze.WeekStats, cutover analyze.Cutover) ([]string, int) {
	labels := make([]string, len(weeks))
	cutoverIdx := -1
	for i, w := range weeks {
		labels[i] = w.WeekStart.Format("Jan 02")
		if cutover.Detected && w.WeekStart.Equal(cutover.Date) {
			cutoverIdx = i
		}
	}
	if cutoverIdx >= 0 {
		labels[cutoverIdx] = "▼ " + labels[cutoverIdx]
	}
	return labels, cutoverIdx
}

func writePNG(p *charts.Painter, outPath string) error {
	buf, err := p.Bytes()
	if err != nil {
		return fmt.Errorf("painter.Bytes: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o750); err != nil {
		return fmt.Errorf("create chart dir: %w", err)
	}
	if err := os.WriteFile(outPath, buf, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}

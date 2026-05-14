package deck

import (
	"encoding/json"
	"fmt"
	"html/template"

	"github.com/StephanSchmidt/loupe/internal/analyze"
)

// ChartPayload holds JSON-encoded Apache ECharts option objects for the
// charts on the deck. Each field is marked template.JS so the HTML
// template can embed it verbatim inside a <script> block — JSON object
// literals are valid JS expressions.
type ChartPayload struct {
	ThroughputJSON template.JS
	AdoptionJSON   template.JS
}

// BuildChartPayload prepares the ECharts option payloads for the deck. The
// rendered template hands these to echarts.init().setOption() in the
// browser. The Go side does no PNG rasterisation.
func BuildChartPayload(weeks []analyze.WeekStats, cutover analyze.Cutover) (ChartPayload, error) {
	if len(weeks) == 0 {
		return ChartPayload{}, fmt.Errorf("BuildChartPayload: no weekly data")
	}
	thru, err := marshalOption(buildThroughputOption(weeks, cutover))
	if err != nil {
		return ChartPayload{}, fmt.Errorf("marshal throughput option: %w", err)
	}
	adopt, err := marshalOption(buildAdoptionOption(weeks, cutover))
	if err != nil {
		return ChartPayload{}, fmt.Errorf("marshal adoption option: %w", err)
	}
	// json.Marshal escapes <, >, & as \u-sequences, so the payload cannot
	// break out of the enclosing <script>. No untrusted JS lives in either
	// option map (only numbers, ECharts keywords, and time-formatted
	// strings), so the template.JS conversion is safe here.
	return ChartPayload{
		ThroughputJSON: template.JS(thru), // #nosec G203 -- JSON-encoded payload, see comment above
		AdoptionJSON:   template.JS(adopt), // #nosec G203 -- JSON-encoded payload, see comment above
	}, nil
}

func marshalOption(opt map[string]any) ([]byte, error) {
	// json.Marshal escapes <, >, & by default, which keeps embedded strings
	// from breaking out of the surrounding <script> block.
	return json.Marshal(opt)
}

// buildThroughputOption produces the ECharts option for the stacked weekly
// commit bar chart (human vs AI-tagged). When the cutover is detected the
// chart carries a vertical markLine at the cutover week.
func buildThroughputOption(weeks []analyze.WeekStats, cutover analyze.Cutover) map[string]any {
	labels := make([]string, len(weeks))
	human := make([]int, len(weeks))
	ai := make([]int, len(weeks))
	cutoverIdx := -1
	for i, w := range weeks {
		labels[i] = w.WeekStart.Format("Jan 02")
		human[i] = w.TotalCommits - w.AICommits
		ai[i] = w.AICommits
		if cutover.Detected && w.WeekStart.Equal(cutover.Date) {
			cutoverIdx = i
		}
	}

	humanSeries := map[string]any{
		"name": "Human", "type": "bar", "stack": "total", "data": human,
	}
	aiSeries := map[string]any{
		"name": "AI-tagged", "type": "bar", "stack": "total", "data": ai,
	}
	if cutoverIdx >= 0 {
		aiSeries["markLine"] = cutoverMarkLine(cutoverIdx)
	}

	return map[string]any{
		"title":   titleWithCutover("Weekly commits", cutover),
		"tooltip": map[string]any{"trigger": "axis", "axisPointer": map[string]any{"type": "shadow"}},
		"legend":  map[string]any{"data": []string{"Human", "AI-tagged"}},
		"grid":    map[string]any{"left": 56, "right": 24, "top": 80, "bottom": 64},
		"xAxis": map[string]any{
			"type":      "category",
			"data":      labels,
			"axisLabel": map[string]any{"rotate": 45, "interval": 0},
		},
		"yAxis":  map[string]any{"type": "value"},
		"series": []map[string]any{humanSeries, aiSeries},
	}
}

// buildAdoptionOption produces the ECharts option for the AI-author
// adoption line chart (percentage of weekly active devs with at least one
// AI-tagged commit).
func buildAdoptionOption(weeks []analyze.WeekStats, cutover analyze.Cutover) map[string]any {
	labels := make([]string, len(weeks))
	pct := make([]float64, len(weeks))
	cutoverIdx := -1
	for i, w := range weeks {
		labels[i] = w.WeekStart.Format("Jan 02")
		pct[i] = w.AdoptionRatio() * 100
		if cutover.Detected && w.WeekStart.Equal(cutover.Date) {
			cutoverIdx = i
		}
	}

	series := map[string]any{
		"name":   "AI-using devs %",
		"type":   "line",
		"smooth": true,
		"data":   pct,
		"areaStyle": map[string]any{
			"opacity": 0.15,
		},
	}
	if cutoverIdx >= 0 {
		series["markLine"] = cutoverMarkLine(cutoverIdx)
	}

	return map[string]any{
		"title":   titleWithCutover("AI adoption — % of weekly active devs", cutover),
		"tooltip": map[string]any{"trigger": "axis"},
		"legend":  map[string]any{"data": []string{"AI-using devs %"}},
		"grid":    map[string]any{"left": 56, "right": 24, "top": 80, "bottom": 64},
		"xAxis": map[string]any{
			"type":      "category",
			"data":      labels,
			"axisLabel": map[string]any{"rotate": 45, "interval": 0},
		},
		"yAxis": map[string]any{
			"type":      "value",
			"min":       0,
			"max":       100,
			"axisLabel": map[string]any{"formatter": "{value}%"},
		},
		"series": []map[string]any{series},
	}
}

func titleWithCutover(text string, cutover analyze.Cutover) map[string]any {
	title := map[string]any{"text": text, "left": "center"}
	if cutover.Detected {
		title["subtext"] = fmt.Sprintf("AI adoption cutover: %s (%s)",
			cutover.Date.Format("Jan 2, 2006"), cutover.Reason)
	}
	return title
}

func cutoverMarkLine(xIndex int) map[string]any {
	return map[string]any{
		"silent": true,
		"symbol": "none",
		"lineStyle": map[string]any{
			"color": "#cc0000",
			"type":  "dashed",
			"width": 2,
		},
		"label": map[string]any{
			"formatter": "AI cutover",
			"position":  "end",
		},
		"data": []map[string]any{{"xAxis": xIndex}},
	}
}

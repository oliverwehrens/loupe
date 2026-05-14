package deck

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/StephanSchmidt/loupe/internal/analyze"
)

func sampleWeeks() []analyze.WeekStats {
	base := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	out := make([]analyze.WeekStats, 12)
	for i := range out {
		out[i] = analyze.WeekStats{
			WeekStart:       base.AddDate(0, 0, 7*i),
			TotalCommits:    20 + i*2,
			AICommits:       i,
			DistinctAuthors: 5,
			AIAuthors:       (i + 1) / 2,
		}
	}
	return out
}

func sampleCutover(weeks []analyze.WeekStats) analyze.Cutover {
	return analyze.Cutover{
		Detected:  true,
		Date:      weeks[6].WeekStart,
		Reason:    analyze.CutoverReasonAuto,
		Threshold: 0.05,
	}
}

// decodeOption parses the option blob back into a generic map for shape
// assertions. Tests inspect a few load-bearing fields rather than the full
// ECharts schema, which is too broad and changes upstream.
func decodeOption(t *testing.T, blob string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(blob), &out); err != nil {
		t.Fatalf("unmarshal option: %v\nblob: %s", err, blob)
	}
	return out
}

func TestBuildChartPayload_ThroughputShape(t *testing.T) {
	weeks := sampleWeeks()
	cutover := sampleCutover(weeks)

	got, err := BuildChartPayload(weeks, cutover)
	if err != nil {
		t.Fatalf("BuildChartPayload: %v", err)
	}

	opt := decodeOption(t, string(got.ThroughputJSON))

	series, _ := opt["series"].([]any)
	if len(series) != 2 {
		t.Fatalf("throughput series len = %d, want 2", len(series))
	}

	human, _ := series[0].(map[string]any)
	if human["name"] != "Human" || human["stack"] != "total" {
		t.Errorf("human series misconfigured: %+v", human)
	}
	humanData, _ := human["data"].([]any)
	if len(humanData) != len(weeks) {
		t.Errorf("human series data len = %d, want %d", len(humanData), len(weeks))
	}

	ai, _ := series[1].(map[string]any)
	if ai["name"] != "AI-tagged" || ai["stack"] != "total" {
		t.Errorf("ai series misconfigured: %+v", ai)
	}
	if _, hasMark := ai["markLine"]; !hasMark {
		t.Errorf("expected markLine on AI series when cutover detected; got: %+v", ai)
	}

	title, _ := opt["title"].(map[string]any)
	if title["text"] != "Weekly commits" {
		t.Errorf("title.text = %v, want %q", title["text"], "Weekly commits")
	}
	subtext, _ := title["subtext"].(string)
	if !strings.Contains(subtext, cutover.Date.Format("Jan 2, 2006")) {
		t.Errorf("title.subtext = %q, want it to reference cutover date", subtext)
	}
}

func TestBuildChartPayload_AdoptionShape(t *testing.T) {
	weeks := sampleWeeks()
	cutover := sampleCutover(weeks)

	got, err := BuildChartPayload(weeks, cutover)
	if err != nil {
		t.Fatalf("BuildChartPayload: %v", err)
	}

	opt := decodeOption(t, string(got.AdoptionJSON))

	series, _ := opt["series"].([]any)
	if len(series) != 1 {
		t.Fatalf("adoption series len = %d, want 1", len(series))
	}
	s0, _ := series[0].(map[string]any)
	if s0["type"] != "line" {
		t.Errorf("adoption series type = %v, want line", s0["type"])
	}
	if _, hasMark := s0["markLine"]; !hasMark {
		t.Errorf("expected markLine on adoption series when cutover detected; got: %+v", s0)
	}

	y, _ := opt["yAxis"].(map[string]any)
	if y["max"].(float64) != 100 {
		t.Errorf("yAxis.max = %v, want 100", y["max"])
	}
}

func TestBuildChartPayload_NoCutoverOmitsMarkLine(t *testing.T) {
	weeks := sampleWeeks()
	got, err := BuildChartPayload(weeks, analyze.Cutover{Detected: false})
	if err != nil {
		t.Fatalf("BuildChartPayload: %v", err)
	}
	opt := decodeOption(t, string(got.ThroughputJSON))
	series, _ := opt["series"].([]any)
	for i, s := range series {
		sm, _ := s.(map[string]any)
		if _, hasMark := sm["markLine"]; hasMark {
			t.Errorf("series[%d] has markLine but cutover not detected", i)
		}
	}
	title, _ := opt["title"].(map[string]any)
	if _, hasSubtext := title["subtext"]; hasSubtext {
		t.Errorf("title.subtext should be absent when cutover not detected; got: %+v", title)
	}
}

func TestBuildChartPayload_NoData(t *testing.T) {
	if _, err := BuildChartPayload(nil, analyze.Cutover{}); err == nil {
		t.Errorf("expected error for empty weeks, got nil")
	}
}

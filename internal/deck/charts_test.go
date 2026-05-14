package deck

import (
	"bytes"
	"os"
	"path/filepath"
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

// pngMagic is the 8-byte PNG signature.
var pngMagic = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

func assertPNG(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(data) < 1000 {
		t.Errorf("%s suspiciously small (%d bytes)", path, len(data))
	}
	if !bytes.HasPrefix(data, pngMagic) {
		t.Errorf("%s is not a PNG: header = %x", path, data[:8])
	}
}

func TestRenderThroughputChart(t *testing.T) {
	weeks := sampleWeeks()
	cutover := sampleCutover(weeks)
	out := filepath.Join(t.TempDir(), "throughput.png")
	if err := RenderThroughputChart(weeks, cutover, out); err != nil {
		t.Fatalf("RenderThroughputChart: %v", err)
	}
	assertPNG(t, out)
}

func TestRenderAdoptionChart(t *testing.T) {
	weeks := sampleWeeks()
	cutover := sampleCutover(weeks)
	out := filepath.Join(t.TempDir(), "adoption.png")
	if err := RenderAdoptionChart(weeks, cutover, out); err != nil {
		t.Fatalf("RenderAdoptionChart: %v", err)
	}
	assertPNG(t, out)
}

func TestRenderThroughputChart_NoData(t *testing.T) {
	out := filepath.Join(t.TempDir(), "throughput.png")
	if err := RenderThroughputChart(nil, analyze.Cutover{}, out); err == nil {
		t.Errorf("expected error for empty weeks, got nil")
	}
}

func TestRenderThroughputChart_NoCutover(t *testing.T) {
	weeks := sampleWeeks()
	out := filepath.Join(t.TempDir(), "throughput.png")
	if err := RenderThroughputChart(weeks, analyze.Cutover{Detected: false}, out); err != nil {
		t.Fatalf("RenderThroughputChart without cutover: %v", err)
	}
	assertPNG(t, out)
}

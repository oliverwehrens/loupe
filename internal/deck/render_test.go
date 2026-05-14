package deck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/StephanSchmidt/loupe/internal/analyze"
	"github.com/StephanSchmidt/loupe/internal/config"
)

func TestRenderDeck_ProducesAllArtifacts(t *testing.T) {
	cfg := &config.Config{
		Org:     "acme-eng",
		GitHost: config.GitHostConfig{Provider: config.ProviderBitbucketCloud},
	}
	weeks := sampleWeeks()
	cutover := sampleCutover(weeks)
	reportDate := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)

	dir := t.TempDir()
	if err := RenderDeck(dir, cfg, weeks, cutover, reportDate); err != nil {
		t.Fatalf("RenderDeck: %v", err)
	}

	wantFiles := []string{
		"index.html",
		"assets/reveal.js",
		"assets/reveal.css",
		"assets/theme/white.css",
		"charts/throughput.png",
		"charts/adoption.png",
	}
	for _, f := range wantFiles {
		p := filepath.Join(dir, f)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("missing artifact %s: %v", f, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("artifact %s is empty", f)
		}
	}

	// Smoke-test the rendered HTML: verifies template fields were substituted
	// and the references point at the assets we just produced.
	html, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	body := string(html)
	for _, want := range []string{
		"acme-eng",
		"AI Impact Report",
		"charts/throughput.png",
		"charts/adoption.png",
		"assets/reveal.js",
		"Co-Authored-By",
		"Auto-detected",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}

func TestRenderDeck_NoCutoverDetected(t *testing.T) {
	cfg := &config.Config{
		Org:     "acme-eng",
		GitHost: config.GitHostConfig{Provider: config.ProviderBitbucketCloud},
	}
	weeks := sampleWeeks()
	cutover := analyze.Cutover{Detected: false, Reason: analyze.CutoverReasonNotDetected, Threshold: 0.05}
	reportDate := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)

	dir := t.TempDir()
	if err := RenderDeck(dir, cfg, weeks, cutover, reportDate); err != nil {
		t.Fatalf("RenderDeck: %v", err)
	}
	html, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(html), "No AI cutover detected") {
		t.Errorf("expected not-detected cutover text in slide; got: %s", html)
	}
}

package analyze

import (
	"context"
	"testing"
	"time"

	"github.com/StephanSchmidt/loupe/internal/store"
)

// seedCommit inserts a commit and, if isAI, a high-confidence claude signal.
func seedCommit(t *testing.T, s *store.Store, sha, email string, at time.Time, isAI bool) {
	t.Helper()
	if _, err := s.DB().Exec(
		`INSERT INTO commits (sha, repo_name, author_email, author_name, committed_at, message)
         VALUES (?, 'r', ?, ?, ?, '')`,
		sha, email, email, at.UTC().Unix(),
	); err != nil {
		t.Fatalf("seed commit %s: %v", sha, err)
	}
	if isAI {
		if _, err := s.DB().Exec(
			`INSERT INTO ai_signals (commit_sha, signal_kind, signal_source, confidence)
             VALUES (?, 'co_author_trailer', 'claude', 'high')`,
			sha,
		); err != nil {
			t.Fatalf("seed signal %s: %v", sha, err)
		}
	}
}

func TestIsoWeekStart(t *testing.T) {
	// 2026-05-13 is a Wednesday → Monday is 2026-05-11
	got := IsoWeekStart(time.Date(2026, 5, 13, 14, 30, 0, 0, time.UTC))
	want := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("IsoWeekStart(Wed) = %v, want %v", got, want)
	}
	// Sunday should map back to the Monday before it (ISO week ends Sunday).
	gotSun := IsoWeekStart(time.Date(2026, 5, 17, 23, 0, 0, 0, time.UTC))
	if !gotSun.Equal(want) {
		t.Errorf("IsoWeekStart(Sun) = %v, want %v", gotSun, want)
	}
}

func TestWeeklyStats_Aggregation(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	wk1Mon := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC) // Monday week 2
	wk2Mon := time.Date(2026, 1, 12, 10, 0, 0, 0, time.UTC)

	seedCommit(t, s, "a1", "alice@a", wk1Mon, false)
	seedCommit(t, s, "a2", "alice@a", wk1Mon.Add(2*time.Hour), false)
	seedCommit(t, s, "b1", "bob@a", wk2Mon, true)
	seedCommit(t, s, "b2", "alice@a", wk2Mon.Add(time.Hour), true)
	seedCommit(t, s, "b3", "alice@a", wk2Mon.Add(2*time.Hour), false)

	weeks, err := WeeklyStats(context.Background(), s)
	if err != nil {
		t.Fatalf("WeeklyStats: %v", err)
	}
	if len(weeks) != 2 {
		t.Fatalf("len(weeks) = %d, want 2", len(weeks))
	}

	if weeks[0].TotalCommits != 2 || weeks[0].AICommits != 0 {
		t.Errorf("week1 = %+v, want 2 total / 0 ai", weeks[0])
	}
	if weeks[0].DistinctAuthors != 1 {
		t.Errorf("week1 DistinctAuthors = %d, want 1", weeks[0].DistinctAuthors)
	}

	if weeks[1].TotalCommits != 3 || weeks[1].AICommits != 2 {
		t.Errorf("week2 = %+v, want 3 total / 2 ai", weeks[1])
	}
	if weeks[1].DistinctAuthors != 2 || weeks[1].AIAuthors != 2 {
		t.Errorf("week2 authors: distinct=%d ai=%d, want 2/2", weeks[1].DistinctAuthors, weeks[1].AIAuthors)
	}
	if r := weeks[1].CommitRatio(); r < 0.66 || r > 0.67 {
		t.Errorf("week2 CommitRatio = %v, want ~0.667", r)
	}
}

func TestDetectCutover_AutoFindsFirstQualifyingWeek(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	// 3 weeks. Week 1: 0% AI. Week 2: 4% AI (below 5% threshold).
	// Week 3: 33% AI (clears threshold).
	wkStart := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 50; i++ {
		seedCommit(t, s, week1Sha(i), "alice@a", wkStart.Add(time.Hour*time.Duration(i)), false)
	}
	wk2 := wkStart.AddDate(0, 0, 7)
	for i := 0; i < 25; i++ { // 25 total, 1 AI = 4%
		seedCommit(t, s, week2Sha(i), "alice@a", wk2.Add(time.Hour*time.Duration(i)), i == 0)
	}
	wk3 := wkStart.AddDate(0, 0, 14)
	for i := 0; i < 9; i++ { // 9 total, 3 AI = 33%
		seedCommit(t, s, week3Sha(i), "alice@a", wk3.Add(time.Hour*time.Duration(i)), i < 3)
	}

	c, err := DetectCutover(context.Background(), s, 0.05, time.Time{})
	if err != nil {
		t.Fatalf("DetectCutover: %v", err)
	}
	if !c.Detected {
		t.Fatalf("Detected = false, want true")
	}
	if c.Reason != CutoverReasonAuto {
		t.Errorf("Reason = %q, want auto", c.Reason)
	}
	if !c.Date.Equal(wk3) {
		t.Errorf("Date = %v, want %v (week 3)", c.Date, wk3)
	}
}

func TestDetectCutover_OverrideTakesPrecedence(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	override := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC) // Tue
	c, err := DetectCutover(context.Background(), s, 0.05, override)
	if err != nil {
		t.Fatalf("DetectCutover: %v", err)
	}
	if c.Reason != CutoverReasonOverride {
		t.Errorf("Reason = %q, want override", c.Reason)
	}
	wantMon := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	if !c.Date.Equal(wantMon) {
		t.Errorf("Date = %v, want Monday %v", c.Date, wantMon)
	}
}

func TestDetectCutover_NotDetected(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	// All-human commits — no AI signals seeded.
	wkStart := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		seedCommit(t, s, week1Sha(i), "alice@a", wkStart.Add(time.Hour*time.Duration(i)), false)
	}

	c, err := DetectCutover(context.Background(), s, 0.05, time.Time{})
	if err != nil {
		t.Fatalf("DetectCutover: %v", err)
	}
	if c.Detected {
		t.Errorf("Detected = true, want false when no AI signals")
	}
	if c.Reason != CutoverReasonNotDetected {
		t.Errorf("Reason = %q, want not-detected", c.Reason)
	}
}

// helpers — distinct sha generators so seed inserts don't collide across weeks
func week1Sha(i int) string { return shaN("w1", i) }
func week2Sha(i int) string { return shaN("w2", i) }
func week3Sha(i int) string { return shaN("w3", i) }

func shaN(prefix string, i int) string {
	return prefix + "-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [10]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

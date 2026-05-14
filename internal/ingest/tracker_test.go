package ingest

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/StephanSchmidt/loupe/internal/store"
	"github.com/StephanSchmidt/loupe/internal/tracker"
)

type fakeTracker struct {
	projects []tracker.Project
	issues   map[string][]tracker.Issue
}

func (f *fakeTracker) Name() string { return "fake-tracker" }

func (f *fakeTracker) ListProjects(_ context.Context) ([]tracker.Project, error) {
	return f.projects, nil
}

func (f *fakeTracker) ListIssues(_ context.Context, key string, since time.Time) iter.Seq2[tracker.Issue, error] {
	issues := f.issues[key]
	return func(yield func(tracker.Issue, error) bool) {
		for _, iss := range issues {
			if !since.IsZero() && iss.CreatedAt.Before(since) {
				continue
			}
			if !yield(iss, nil) {
				return
			}
		}
	}
}

func buildFakeTracker() *fakeTracker {
	at := func(n int64) time.Time { return time.Unix(n, 0).UTC() }
	return &fakeTracker{
		projects: []tracker.Project{
			{Key: "ENG", Name: "Engineering"},
			{Key: "OPS", Name: "Ops"},
		},
		issues: map[string][]tracker.Issue{
			"ENG": {
				{ID: "10001", Key: "ENG-1", ProjectKey: "ENG", Title: "Fix login", Type: "Bug",
					Status: "Done", CreatedAt: at(1700000000), AssigneeEmail: "alice@a"},
				{ID: "10002", Key: "ENG-2", ProjectKey: "ENG", Title: "Add OAuth", Type: "Story",
					Status: "In Progress", CreatedAt: at(1700001000)},
			},
			"OPS": {
				{ID: "20001", Key: "OPS-1", ProjectKey: "OPS", Title: "Cluster drift", Type: "Incident",
					Status: "Done", CreatedAt: at(1700002000)},
			},
		},
	}
}

func TestIngestTracker_EndToEnd(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	stats, err := IngestTracker(context.Background(), s, buildFakeTracker(), nil, TrackerFilter{})
	if err != nil {
		t.Fatalf("IngestTracker: %v", err)
	}
	if stats.Projects != 2 || stats.Issues != 3 {
		t.Errorf("stats = %+v, want 2 projects / 3 issues", stats)
	}

	var n int
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM tracker_projects`).Scan(&n)
	if n != 2 {
		t.Errorf("tracker_projects rows = %d, want 2", n)
	}
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM tickets`).Scan(&n)
	if n != 3 {
		t.Errorf("tickets rows = %d, want 3", n)
	}

	// Ticket primary key uses the human-readable key, not the numeric ID.
	var title string
	if err := s.DB().QueryRow(`SELECT title FROM tickets WHERE id = 'ENG-1'`).Scan(&title); err != nil {
		t.Fatalf("read ENG-1: %v", err)
	}
	if title != "Fix login" {
		t.Errorf("ENG-1 title = %q", title)
	}

	// Watermarks advanced.
	var wm int64
	if err := s.DB().QueryRow(`SELECT last_issue_indexed_at FROM tracker_projects WHERE key = 'ENG'`).Scan(&wm); err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	if wm == 0 {
		t.Errorf("watermark not advanced for ENG")
	}
}

func TestIngestTracker_ProjectFilterSkipsOthers(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	stats, err := IngestTracker(context.Background(), s, buildFakeTracker(), nil, TrackerFilter{Project: "ENG"})
	if err != nil {
		t.Fatalf("IngestTracker: %v", err)
	}
	if stats.Projects != 1 || stats.Issues != 2 {
		t.Errorf("stats = %+v, want 1 project / 2 issues", stats)
	}
	var n int
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM tickets WHERE project_key = 'OPS'`).Scan(&n)
	if n != 0 {
		t.Errorf("OPS tickets count = %d, want 0", n)
	}
}

func TestIngestTracker_Idempotent(t *testing.T) {
	s, err := store.Open(store.MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	fake := buildFakeTracker()
	if _, err := IngestTracker(context.Background(), s, fake, nil, TrackerFilter{}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := IngestTracker(context.Background(), s, fake, nil, TrackerFilter{}); err != nil {
		t.Fatalf("second: %v", err)
	}
	var n int
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM tickets`).Scan(&n)
	if n != 3 {
		t.Errorf("tickets after re-ingest = %d, want 3", n)
	}
}

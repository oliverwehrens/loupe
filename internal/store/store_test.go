package store

import (
	"path/filepath"
	"sort"
	"testing"
)

var expectedTables = []string{
	"ai_signals",
	"commits",
	"pr_commits",
	"prs",
	"repos",
	"runs",
	"ticket_commits",
	"ticket_transitions",
	"tickets",
	"tracker_projects",
	"workspaces",
}

func listTables(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.DB().Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		// skip sqlite-internal autoincrement table
		if n == "sqlite_sequence" {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func TestOpen_InMemory_CreatesSchema(t *testing.T) {
	s, err := Open(MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	got := listTables(t, s)
	if !equalStrings(got, expectedTables) {
		t.Errorf("tables = %v, want %v", got, expectedTables)
	}
}

func TestOpen_OnDisk_CreatesParentDir(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "state.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	got := listTables(t, s)
	if !equalStrings(got, expectedTables) {
		t.Errorf("tables = %v, want %v", got, expectedTables)
	}
	if s.Path() != dbPath {
		t.Errorf("Path = %q, want %q", s.Path(), dbPath)
	}
}

func TestEnsureSchema_Idempotent(t *testing.T) {
	s, err := Open(MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	// Apply twice; must not error.
	if err := s.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema second call: %v", err)
	}
	got := listTables(t, s)
	if !equalStrings(got, expectedTables) {
		t.Errorf("tables after re-apply = %v, want %v", got, expectedTables)
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	s, err := Open(MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	var v int
	if err := s.DB().QueryRow("PRAGMA foreign_keys").Scan(&v); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if v != 1 {
		t.Errorf("foreign_keys = %d, want 1", v)
	}
}

func TestAdditiveColumnsPresent(t *testing.T) {
	s, err := Open(MemoryPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	cases := []struct {
		table, column string
	}{
		{"commits", "provider"},
		{"commits", "workspace"},
		{"prs", "provider"},
		{"prs", "workspace"},
		{"tickets", "provider"},
	}
	for _, c := range cases {
		has, err := s.hasColumn(c.table, c.column)
		if err != nil {
			t.Errorf("hasColumn(%s,%s): %v", c.table, c.column, err)
			continue
		}
		if !has {
			t.Errorf("expected column %s.%s after Open", c.table, c.column)
		}
	}
}

func TestAdditiveColumns_IdempotentOnReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")

	// Open and close once; columns should be added.
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = s1.Close()

	// Re-open: applyAdditiveColumns must be a no-op (no duplicate-column error).
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = s2.Close() }()

	// Sanity: the column is still there.
	has, err := s2.hasColumn("commits", "provider")
	if err != nil {
		t.Fatalf("hasColumn after reopen: %v", err)
	}
	if !has {
		t.Errorf("commits.provider missing after re-Open")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

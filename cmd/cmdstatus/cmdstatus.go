package cmdstatus

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/StephanSchmidt/loupe/internal/analyze"
	"github.com/StephanSchmidt/loupe/internal/store"
)

const stateDBPath = ".loupe/state.db"

func BuildStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Summarise what's indexed locally — provider counts, last-import timestamps",
		SilenceUsage: true,
		RunE:         runStatus,
	}
	return cmd
}

func runStatus(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()

	if _, err := os.Stat(stateDBPath); err != nil {
		_, _ = fmt.Fprintf(out, "No state yet at %s — run `loupe baseline` first.\n", stateDBPath)
		return nil
	}

	s, err := store.Open(stateDBPath)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	return WriteStatus(cmd.Context(), s, out)
}

// WriteStatus is the format-and-print path, separated from runStatus so
// tests can drive it against a seeded in-memory store.
func WriteStatus(ctx context.Context, s *store.Store, out io.Writer) error {
	hosts, err := perGitHostStats(ctx, s.DB())
	if err != nil {
		return err
	}
	trackers, err := perTrackerStats(ctx, s.DB())
	if err != nil {
		return err
	}
	counts, err := commitCounts(ctx, s.DB())
	if err != nil {
		return err
	}
	tickets, err := count(ctx, s.DB(), "SELECT COUNT(*) FROM tickets")
	if err != nil {
		return err
	}

	if len(hosts) == 0 && len(trackers) == 0 && counts.total == 0 && tickets == 0 {
		_, _ = fmt.Fprintln(out, "Empty state — run `loupe baseline` to index providers.")
		return nil
	}

	for _, h := range hosts {
		_, _ = fmt.Fprintf(out, "%-10s %d %s (%d %s)%s\n",
			displayName(h.provider)+":",
			h.workspaces, pluralise("workspace", h.workspaces),
			h.repos, pluralise("repo", h.repos),
			formatLastIndexed("last commit indexed", h.lastIndexed),
		)
	}
	for _, t := range trackers {
		_, _ = fmt.Fprintf(out, "%-10s %d %s%s\n",
			displayName(t.provider)+":",
			t.projects, pluralise("project", t.projects),
			formatLastIndexed("last issue indexed", t.lastIndexed),
		)
	}

	aiPct := 0.0
	if counts.total > 0 {
		aiPct = float64(counts.ai) / float64(counts.total) * 100
	}
	_, _ = fmt.Fprintf(out, "%-10s %d  (%d AI-tagged, %.1f%%)\n", "Commits:", counts.total, counts.ai, aiPct)
	_, _ = fmt.Fprintf(out, "%-10s %d\n", "Tickets:", tickets)

	if counts.botExcluded > 0 {
		_, _ = fmt.Fprintf(out, "Excluded %d bot-authored commits across %d bots (%s)\n",
			counts.botExcluded, len(counts.botDisplayNames), formatBotList(counts.botDisplayNames))
	}

	return nil
}

type hostStats struct {
	provider    string
	workspaces  int
	repos       int
	lastIndexed sql.NullInt64
}

type trackerStats struct {
	provider    string
	projects    int
	lastIndexed sql.NullInt64
}

func perGitHostStats(ctx context.Context, db *sql.DB) ([]hostStats, error) {
	rows, err := db.QueryContext(ctx, `
        SELECT w.provider,
               COUNT(DISTINCT w.slug)                   AS workspaces,
               COALESCE((SELECT COUNT(*) FROM repos r WHERE r.provider = w.provider), 0) AS repos,
               MAX(w.last_indexed_at)                   AS last_indexed
        FROM workspaces w
        GROUP BY w.provider
        ORDER BY w.provider
    `)
	if err != nil {
		return nil, fmt.Errorf("status: query workspaces: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []hostStats
	for rows.Next() {
		var h hostStats
		if err := rows.Scan(&h.provider, &h.workspaces, &h.repos, &h.lastIndexed); err != nil {
			return nil, fmt.Errorf("status: scan workspace row: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func perTrackerStats(ctx context.Context, db *sql.DB) ([]trackerStats, error) {
	rows, err := db.QueryContext(ctx, `
        SELECT provider, COUNT(*) AS projects, MAX(last_issue_indexed_at) AS last_indexed
        FROM tracker_projects
        GROUP BY provider
        ORDER BY provider
    `)
	if err != nil {
		return nil, fmt.Errorf("status: query tracker_projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []trackerStats
	for rows.Next() {
		var t trackerStats
		if err := rows.Scan(&t.provider, &t.projects, &t.lastIndexed); err != nil {
			return nil, fmt.Errorf("status: scan tracker row: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type commitSummary struct {
	total       int
	ai          int
	botExcluded int
	// botDisplayNames collapses excluded bots to canonical labels
	// (Dependabot, Renovate, …) so multiple raw email forms for the
	// same automation appear once in the footer.
	botDisplayNames []string
}

// commitCounts walks the commits table once, partitioning rows into the
// human-visible numerator/denominator and the bot rollup. Both totals
// exclude bots — those land in botExcluded / botDisplayNames so callers
// can surface what was dropped.
func commitCounts(ctx context.Context, db *sql.DB) (commitSummary, error) {
	rows, err := db.QueryContext(ctx, `
        SELECT c.author_email, c.author_name,
               CASE WHEN sig.commit_sha IS NOT NULL THEN 1 ELSE 0 END AS is_ai
        FROM commits c
        LEFT JOIN (SELECT DISTINCT commit_sha FROM ai_signals) sig
            ON sig.commit_sha = c.sha
    `)
	if err != nil {
		return commitSummary{}, fmt.Errorf("status: scan commits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out commitSummary
	displayNames := make(map[string]struct{})
	for rows.Next() {
		var email, name string
		var ai int
		if err := rows.Scan(&email, &name, &ai); err != nil {
			return commitSummary{}, fmt.Errorf("status: scan commit row: %w", err)
		}
		if analyze.IsBot(email, name) {
			out.botExcluded++
			displayNames[analyze.BotDisplayName(email, name)] = struct{}{}
			continue
		}
		out.total++
		if ai == 1 {
			out.ai++
		}
	}
	if err := rows.Err(); err != nil {
		return commitSummary{}, fmt.Errorf("status: iterate commit rows: %w", err)
	}
	out.botDisplayNames = make([]string, 0, len(displayNames))
	for d := range displayNames {
		out.botDisplayNames = append(out.botDisplayNames, d)
	}
	sort.Strings(out.botDisplayNames)
	return out, nil
}

// formatBotList renders the bot-name list for the exclusion footer.
// Truncates to keep the line readable when many bots are present.
func formatBotList(names []string) string {
	const maxShown = 5
	if len(names) <= maxShown {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:maxShown], ", ") + ", ..."
}

func count(ctx context.Context, db *sql.DB, q string) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("status: %s: %w", q, err)
	}
	return n, nil
}

func displayName(provider string) string {
	switch provider {
	case "bitbucket-cloud":
		return "Bitbucket"
	case "jira-cloud":
		return "Jira"
	case "github":
		return "GitHub"
	default:
		return provider
	}
}

func pluralise(noun string, n int) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

func formatLastIndexed(label string, ts sql.NullInt64) string {
	if !ts.Valid || ts.Int64 == 0 {
		return ""
	}
	t := time.Unix(ts.Int64, 0).UTC()
	return fmt.Sprintf(" — %s %s UTC", label, t.Format("2006-01-02 15:04"))
}

package analyze

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/StephanSchmidt/loupe/internal/store"
)

// PRSignalConfig controls which PR-level patterns count as AI signals.
// An empty Labels or BranchPrefixes disables the corresponding detector.
type PRSignalConfig struct {
	// Labels are matched case-insensitively against each PR's labels JSON
	// array. A label string that contains a known tool name (e.g.
	// "claude", "copilot") is attributed to that source; everything else
	// is attributed to SourceAIGeneric.
	Labels []string
	// BranchPrefixes are matched case-insensitively against the PR's
	// source_branch. Prefixes whose first path segment names a known tool
	// (e.g. "copilot/") get attributed to that source.
	BranchPrefixes []string
}

// DefaultPRSignalConfig is the out-of-the-box label/prefix list. Users can
// override via loupe.yaml. Kept small on purpose — these defaults need to
// be uncontroversial for the methodology slide.
func DefaultPRSignalConfig() PRSignalConfig {
	return PRSignalConfig{
		Labels: []string{
			"ai-generated", "ai-assisted", "ai-assist", "ai",
			"copilot", "claude", "cursor", "aider", "devin", "jules", "gemini",
		},
		BranchPrefixes: []string{
			"copilot/", "cursor/", "claude/", "aider/",
			"devin/", "jules/", "bolt/", "windsurf/",
		},
	}
}

// DetectPRSignals walks every PR in the store, matches its labels and
// source branch against cfg, and upserts pr_label / branch_name signals
// for every commit known to belong to the PR.
//
// Attribution covers (a) the PR's merge_commit_sha (the squashed/merge
// commit that landed on the destination branch) and (b) every row in
// pr_commits for that PR. Until squash-recovery (task 6) populates
// pr_commits, only (a) will fire — which is exactly the case where the
// pre-squash trailers would have been lost anyway, so PR-level signals
// are the load-bearing detection path for those merges.
//
// Idempotent — upserts on the same primary key as DetectAndStore.
func DetectPRSignals(ctx context.Context, s *store.Store, cfg PRSignalConfig) (int, error) {
	if len(cfg.Labels) == 0 && len(cfg.BranchPrefixes) == 0 {
		return 0, nil
	}

	prs, err := loadPRDescriptors(ctx, s.DB())
	if err != nil {
		return 0, err
	}

	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, upsertSignalSQL)
	if err != nil {
		return 0, fmt.Errorf("prepare upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	knownSHAs, err := loadKnownCommitSHAs(ctx, tx)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, pr := range prs {
		sigs := signalsForPR(pr, cfg)
		if len(sigs) == 0 {
			continue
		}
		shas, err := commitsForPR(ctx, tx, pr.id, pr.mergeSHA)
		if err != nil {
			return count, err
		}
		for _, sha := range shas {
			if _, ok := knownSHAs[sha]; !ok {
				// ai_signals.commit_sha has a FK to commits.sha — skip
				// SHAs that aren't in the commits table (typically a PR
				// merge commit on a branch we didn't crawl).
				continue
			}
			for _, sig := range sigs {
				if _, err := stmt.ExecContext(ctx, sha, sig.Kind, sig.Source, sig.Confidence, sig.Detail); err != nil {
					return count, fmt.Errorf("upsert pr signal for %s: %w", sha, err)
				}
				count++
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return count, fmt.Errorf("commit tx: %w", err)
	}
	return count, nil
}

type prDescriptor struct {
	id           string
	sourceBranch string
	mergeSHA     string
	labels       []string
}

func loadPRDescriptors(ctx context.Context, db *sql.DB) ([]prDescriptor, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, source_branch, merge_commit_sha, labels FROM prs`)
	if err != nil {
		return nil, fmt.Errorf("query prs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []prDescriptor
	for rows.Next() {
		var (
			id, branch  string
			merge, labs sql.NullString
		)
		if err := rows.Scan(&id, &branch, &merge, &labs); err != nil {
			return nil, fmt.Errorf("scan pr row: %w", err)
		}
		d := prDescriptor{id: id, sourceBranch: branch, mergeSHA: merge.String}
		if labs.Valid && labs.String != "" {
			if err := json.Unmarshal([]byte(labs.String), &d.labels); err != nil {
				// Stored as something other than a JSON array — skip
				// label detection for this PR rather than erroring.
				d.labels = nil
			}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// commitsForPR returns every commit SHA known to belong to the PR — the
// merge commit (if any) plus any rows in pr_commits. Deduplicates so
// upserts don't write the same (commit, signal) twice.
//
// Uses a queryer so it works inside either a *sql.Tx or *sql.DB.
func commitsForPR(ctx context.Context, q queryer, prID, mergeSHA string) ([]string, error) {
	seen := make(map[string]struct{})
	if mergeSHA != "" {
		seen[mergeSHA] = struct{}{}
	}
	rows, err := q.QueryContext(ctx, `SELECT commit_sha FROM pr_commits WHERE pr_id = ?`, prID)
	if err != nil {
		return nil, fmt.Errorf("query pr_commits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, fmt.Errorf("scan pr_commits: %w", err)
		}
		seen[sha] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for sha := range seen {
		out = append(out, sha)
	}
	return out, nil
}

// queryer is the minimal interface DetectPRSignals uses against the
// store. Both *sql.DB and *sql.Tx implement it, so the function can run
// inside an outer transaction without changing call sites.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// loadKnownCommitSHAs returns the set of SHAs present in the commits
// table. Used to filter PR-derived signals down to SHAs that exist (the
// ai_signals.commit_sha FK forbids inserts for missing parents).
func loadKnownCommitSHAs(ctx context.Context, q queryer) (map[string]struct{}, error) {
	rows, err := q.QueryContext(ctx, `SELECT sha FROM commits`)
	if err != nil {
		return nil, fmt.Errorf("query commits sha: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]struct{}{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, fmt.Errorf("scan sha: %w", err)
		}
		out[sha] = struct{}{}
	}
	return out, rows.Err()
}

// signalsForPR returns one Signal per matching label or branch prefix.
// The Signal's CommitSHA is left blank — callers fill it in per commit.
func signalsForPR(pr prDescriptor, cfg PRSignalConfig) []Signal {
	var out []Signal
	for _, raw := range pr.labels {
		want := strings.ToLower(strings.TrimSpace(raw))
		if want == "" {
			continue
		}
		for _, allowed := range cfg.Labels {
			if want == strings.ToLower(allowed) {
				out = append(out, Signal{
					Kind:       KindPRLabel,
					Source:     sourceFromLabel(want),
					Confidence: ConfidenceHigh,
					Detail:     "label: " + raw,
				})
				break
			}
		}
	}
	if branch := strings.ToLower(strings.TrimSpace(pr.sourceBranch)); branch != "" {
		for _, prefix := range cfg.BranchPrefixes {
			p := strings.ToLower(prefix)
			if strings.HasPrefix(branch, p) {
				out = append(out, Signal{
					Kind:       KindBranchName,
					Source:     sourceFromBranchPrefix(p),
					Confidence: ConfidenceHigh,
					Detail:     "branch: " + pr.sourceBranch,
				})
				break
			}
		}
	}
	return out
}

// sourceFromLabel maps a matched label to a specific AI source when the
// label contains a known tool name, otherwise SourceAIGeneric. Always
// case-insensitive substring match against the label string.
func sourceFromLabel(label string) string {
	l := strings.ToLower(label)
	for _, m := range labelBranchSourceMap {
		if strings.Contains(l, m.token) {
			return m.source
		}
	}
	return SourceAIGeneric
}

// sourceFromBranchPrefix is the branch-prefix companion of sourceFromLabel.
// Matches the prefix's leading segment (everything before the first `/`).
func sourceFromBranchPrefix(prefix string) string {
	head := strings.TrimSuffix(strings.ToLower(prefix), "/")
	for _, m := range labelBranchSourceMap {
		if head == m.token || strings.Contains(head, m.token) {
			return m.source
		}
	}
	return SourceAIGeneric
}

// labelBranchSourceMap shares the tool-name → Source mapping between label
// and branch detection. Ordered most-specific first so e.g. "gemini-code-
// assist" wins over a bare "gemini" entry if both were ever added.
var labelBranchSourceMap = []struct {
	token, source string
}{
	{"gemini-code-assist", SourceGemini},
	{"copilot", SourceCopilot},
	{"cursor", SourceCursor},
	{"claude", SourceClaude},
	{"aider", SourceAider},
	{"devin", SourceDevin},
	{"jules", SourceJules},
	{"gemini", SourceGemini},
	{"opencode", SourceOpenCode},
}

// DetectSquashRecovery runs the commit-message + body-footer + bot-author
// detectors against the pre-squash commits stored in pr_commits and emits
// signals keyed to each PR's merge_commit_sha with kind=squash_recovery.
//
// This recovers AI signals that get dropped when a PR is squash-merged
// and the original Co-Authored-By trailers don't survive on the merge
// commit. PRs without a merge_commit_sha (open / declined / superseded)
// are skipped — there's no commit on the destination branch to attribute
// the signal to.
//
// Idempotent — same primary key as the other detectors.
func DetectSquashRecovery(ctx context.Context, s *store.Store) (int, error) {
	rows, err := s.DB().QueryContext(ctx, `
        SELECT p.merge_commit_sha, pc.author_email, pc.author_name, pc.message
        FROM pr_commits pc
        JOIN prs p ON p.id = pc.pr_id
        WHERE p.merge_commit_sha IS NOT NULL AND p.merge_commit_sha <> ''
          AND p.merge_commit_sha IN (SELECT sha FROM commits)
    `)
	if err != nil {
		return 0, fmt.Errorf("query pr_commits join: %w", err)
	}

	type prCommit struct {
		mergeSHA, email, name, message string
	}
	var prCommits []prCommit
	for rows.Next() {
		var c prCommit
		if err := rows.Scan(&c.mergeSHA, &c.email, &c.name, &c.message); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan pr_commit: %w", err)
		}
		prCommits = append(prCommits, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, upsertSignalSQL)
	if err != nil {
		return 0, fmt.Errorf("prepare upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	// Deduplicate so multiple PR commits with the same trailer don't
	// rewrite the same (merge_sha, source) row repeatedly within one run.
	seen := make(map[string]struct{})

	count := 0
	for _, pc := range prCommits {
		var sources []Signal
		sources = append(sources, DetectFromMessage(pc.message)...)
		sources = append(sources, detectFromBodyFooters(pc.message)...)
		if sig, ok := detectFromAuthorIdentity(pc.email, pc.name); ok {
			sources = append(sources, sig)
		}
		for _, sig := range sources {
			key := pc.mergeSHA + "|" + sig.Source
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			if _, err := stmt.ExecContext(ctx, pc.mergeSHA, KindSquashRecovery, sig.Source, ConfidenceHigh,
				"recovered from PR commit ("+sig.Kind+")"); err != nil {
				return count, fmt.Errorf("upsert squash_recovery for %s: %w", pc.mergeSHA, err)
			}
			count++
		}
	}
	if err := tx.Commit(); err != nil {
		return count, fmt.Errorf("commit tx: %w", err)
	}
	return count, nil
}

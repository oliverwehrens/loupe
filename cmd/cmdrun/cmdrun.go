package cmdrun

import (
	"errors"

	"github.com/spf13/cobra"
)

func BuildRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Weekly incremental run — fetch new data and render an updated deck",
		Long: `Incremental update against the existing sqlite state. Fetches only
git commits, PRs, and Jira tickets newer than the last successful run, then
renders a fresh reveal.js deck with "what changed this week" deltas vs. the
baseline.

Target runtime: under 1 minute. Designed to be CI-friendly — exit code 0 on
success, non-zero on failure, plus a summary line on stdout.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("not yet implemented")
		},
	}

	cmd.Flags().String("since", "", "override window start (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().Bool("offline", false, "skip Jira and Bitbucket API calls (git-only mode)")
	cmd.Flags().Bool("dry-run", false, "validate config without writing state")

	return cmd
}

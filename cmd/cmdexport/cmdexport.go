package cmdexport

import (
	"errors"

	"github.com/spf13/cobra"
)

func BuildExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the most recent deck as a static HTML folder or PDF",
		Long: `Bundles the reveal.js deck into a self-contained directory you can
zip, email, or commit to a reports/ branch. With --pdf, renders the deck to
a single PDF file via headless Chromium.`,
		Hidden:       true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("not yet implemented")
		},
	}

	cmd.Flags().String("run", "", "specific run directory to export (defaults to latest)")
	cmd.Flags().String("out", "./loupe-report", "output directory or file")
	cmd.Flags().Bool("pdf", false, "render the deck to PDF (requires Chromium)")

	return cmd
}

package cmdpresent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/StephanSchmidt/loupe/internal/browser"
	"github.com/StephanSchmidt/loupe/internal/config"
	"github.com/StephanSchmidt/loupe/internal/deck"
)

const defaultConfigPath = "loupe.yaml"

func BuildPresentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "present",
		Short: "Open the most recent reveal.js deck in your default browser",
		Long: `Serves the most recent run under <output.path>/ on a local
ephemeral port and opens it in your default browser. Ctrl+C to stop.`,
		SilenceUsage: true,
		RunE:         runPresent,
	}

	cmd.Flags().String("run", "", "specific run directory under reports/ (defaults to latest)")
	cmd.Flags().Int("port", 0, "port to bind (0 = pick a free one)")
	cmd.Flags().String("config", defaultConfigPath, "path to loupe.yaml")

	return cmd
}

func runPresent(cmd *cobra.Command, args []string) error {
	configPath, _ := cmd.Flags().GetString("config")
	runArg, _ := cmd.Flags().GetString("run")
	port, _ := cmd.Flags().GetInt("port")

	if port < 0 || port > 65535 {
		return fmt.Errorf("--port %d is out of range (0–65535; 0 picks a free port)", port)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	var deckDir string
	switch {
	case runArg != "":
		deckDir = filepath.Join(cfg.Output.Path, runArg)
	default:
		deckDir, err = deck.FindLatestRun(cfg.Output.Path)
		if err != nil {
			return err
		}
	}

	if _, err := os.Stat(filepath.Join(deckDir, "index.html")); err != nil {
		return fmt.Errorf("no rendered deck at %s — run `loupe baseline` first", deckDir)
	}

	return deck.Serve(deckDir, port, cmd.OutOrStdout(), browser.DefaultOpener{})
}

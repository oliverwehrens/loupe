package deck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/StephanSchmidt/loupe/internal/browser"
)

// FindLatestRun returns the lexicographically greatest subdirectory of
// reportsDir that contains an index.html. Run directories are named with
// ISO timestamps, so lex-greatest == newest.
func FindLatestRun(reportsDir string) (string, error) {
	entries, err := os.ReadDir(reportsDir)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", reportsDir, err)
	}
	candidates := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(reportsDir, e.Name(), "index.html")); err == nil {
			candidates = append(candidates, e.Name())
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no rendered runs in %s — run `loupe baseline` first", reportsDir)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(candidates)))
	return filepath.Join(reportsDir, candidates[0]), nil
}

// Serve starts a static file server bound to 127.0.0.1:port serving deckDir,
// hands the URL to opener, and blocks until SIGINT or SIGTERM is received.
// Pass port 0 to bind any free port and browser.NoopOpener{} from tests so
// no real browser pops up.
//
// statusOut receives one log line when the server is ready and one when it
// shuts down. Pass os.Stdout for the CLI; tests can capture it.
func Serve(deckDir string, port int, statusOut io.Writer, opener browser.Opener) error {
	if _, err := os.Stat(filepath.Join(deckDir, "index.html")); err != nil {
		return fmt.Errorf("no index.html under %s: %w", deckDir, err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	url := "http://" + ln.Addr().String() + "/"

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(deckDir)))
	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	if statusOut != nil {
		_, _ = fmt.Fprintf(statusOut, "Serving %s on %s (Ctrl+C to stop)\n", deckDir, url)
	}

	// Open the browser shortly after we start serving so the first request
	// hits a server that's already accepting.
	if opener != nil {
		go func() {
			time.Sleep(150 * time.Millisecond)
			_ = opener.Open(url)
		}()
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	select {
	case <-sigChan:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		if statusOut != nil {
			_, _ = fmt.Fprintln(statusOut, "stopped")
		}
		return nil
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server: %w", err)
	}
}

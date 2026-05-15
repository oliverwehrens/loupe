// Package auth holds shared helpers for prompting the user for secrets at
// command-invocation time. v0 prompts every time — no env vars, no
// keychain. A future iteration can plug in either path here without
// touching call sites.
package auth

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// PromptToken writes `<label>: ` to stderr (so stdout stays pipeable),
// reads a token with terminal echo off, and returns it trimmed.
func PromptToken(label string) (string, error) {
	return promptToken(label, os.Stderr, readPasswordFromStdin)
}

// promptToken is the test-injectable form. read returns the raw token
// bytes (no trailing newline; ReadPassword strips it).
func promptToken(label string, w io.Writer, read func() ([]byte, error)) (string, error) {
	if _, err := fmt.Fprintf(w, "%s: ", label); err != nil {
		return "", fmt.Errorf("write prompt: %w", err)
	}
	raw, err := read()
	// Write the newline regardless of err so the next prompt isn't glued
	// to a partial input.
	_, _ = fmt.Fprintln(w)
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	return tok, nil
}

func readPasswordFromStdin() ([]byte, error) {
	return term.ReadPassword(int(os.Stdin.Fd()))
}

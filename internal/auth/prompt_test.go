package auth

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestPromptToken_TrimsAndReturnsTyped(t *testing.T) {
	var w bytes.Buffer
	got, err := promptToken("Token", &w, func() ([]byte, error) {
		return []byte("  s3cret-token  "), nil
	})
	if err != nil {
		t.Fatalf("promptToken: %v", err)
	}
	if got != "s3cret-token" {
		t.Errorf("got %q, want trimmed %q", got, "s3cret-token")
	}
	if !strings.HasPrefix(w.String(), "Token: ") {
		t.Errorf("prompt written = %q, want prefix \"Token: \"", w.String())
	}
}

func TestPromptToken_RejectsEmpty(t *testing.T) {
	var w bytes.Buffer
	_, err := promptToken("Bitbucket app password", &w, func() ([]byte, error) {
		return []byte("   "), nil
	})
	if err == nil {
		t.Fatal("expected error for empty token, got nil")
	}
	if !strings.Contains(err.Error(), "Bitbucket app password is required") {
		t.Errorf("err = %v, want it to mention the label and 'is required'", err)
	}
}

func TestPromptToken_PropagatesReadError(t *testing.T) {
	var w bytes.Buffer
	want := fmt.Errorf("io timeout")
	_, err := promptToken("Token", &w, func() ([]byte, error) {
		return nil, want
	})
	if err == nil || !strings.Contains(err.Error(), "io timeout") {
		t.Errorf("err = %v, want wrapping %v", err, want)
	}
	// Even on error, the newline must have been emitted so the next prompt
	// renders on its own line.
	if !strings.HasSuffix(w.String(), "\n") {
		t.Errorf("prompt buffer didn't end with newline: %q", w.String())
	}
}

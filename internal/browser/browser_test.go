package browser

import (
	"strings"
	"testing"
)

// Compile-time check that DefaultOpener implements Opener.
var _ Opener = DefaultOpener{}
var _ Opener = NoopOpener{}

func TestValidateURL_Valid(t *testing.T) {
	for _, u := range []string{
		"https://example.com",
		"http://localhost:8080",
		"https://example.com/path?q=1",
		"http://127.0.0.1:50492/",
	} {
		if err := ValidateURL(u); err != nil {
			t.Errorf("ValidateURL(%q) = %v, want nil", u, err)
		}
	}
}

func TestValidateURL_Rejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "must not be empty"},
		{"no scheme", "example.com", "invalid URL"},
		{"ftp scheme", "ftp://example.com", "http or https"},
		{"javascript scheme", "javascript:alert(1)", "http or https"},
		{"data scheme", "data:text/html,<h1>hi</h1>", "http or https"},
		{"file scheme", "file:///etc/passwd", "http or https"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateURL(tc.in)
			if err == nil {
				t.Fatalf("ValidateURL(%q) = nil, want error containing %q", tc.in, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidateURL(%q) = %v, want substring %q", tc.in, err, tc.want)
			}
		})
	}
}

func TestNoopOpener_DoesNothing(t *testing.T) {
	if err := (NoopOpener{}).Open("https://example.com"); err != nil {
		t.Errorf("NoopOpener.Open returned %v, want nil", err)
	}
}

// recordingOpener captures the URL that was passed to Open. Helpful as a
// test fixture for callers that wrap an Opener.
type recordingOpener struct {
	called string
	err    error
}

func (r *recordingOpener) Open(u string) error {
	r.called = u
	return r.err
}

func TestRecordingOpenerInterface(t *testing.T) {
	var o Opener = &recordingOpener{}
	if err := o.Open("https://example.com"); err != nil {
		t.Errorf("Open returned %v", err)
	}
	if r := o.(*recordingOpener); r.called != "https://example.com" {
		t.Errorf("called = %q, want https://example.com", r.called)
	}
}

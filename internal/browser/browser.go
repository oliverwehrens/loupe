package browser

import (
	"fmt"
	"net/url"
)

// Opener opens a URL in the user's default browser.
type Opener interface {
	Open(url string) error
}

// DefaultOpener shells out to the platform-native handler (open / xdg-open /
// rundll32) via the OS-specific startBrowser stub. It does not block — the
// command is launched with Start(), not Run().
type DefaultOpener struct{}

func (DefaultOpener) Open(rawURL string) error {
	if err := ValidateURL(rawURL); err != nil {
		return err
	}
	return startBrowser(rawURL)
}

// ValidateURL rejects anything that isn't an http(s) URL. Loupe only ever
// passes its own localhost URL through here, but validating at the boundary
// keeps the exec call safe even if a future caller forwards an attacker-
// influenced string.
func ValidateURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("URL must not be empty")
	}
	u, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL must use http or https scheme, got %q", u.Scheme)
	}
	return nil
}

// NoopOpener is an Opener that does nothing. Tests use it so Serve doesn't
// actually try to launch a real browser.
type NoopOpener struct{}

func (NoopOpener) Open(string) error { return nil }

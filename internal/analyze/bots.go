package analyze

import "strings"

// IsBot reports whether a commit author is an automated bot rather than
// a human contributor. Used to exclude bot-authored commits from every
// analytics read path — the rows stay in sqlite, they just don't count.
//
// We err on the side of false negatives: an "Alice Bottega" being counted
// as human is far better than her being filtered out. So the detection
// requires either the exact GitHub `[bot]` suffix convention or a match
// against a small list of known automation identities.
func IsBot(email, name string) bool {
	return botDisplayLookup(email, name) != "" || hasBotSuffix(name)
}

// BotDisplayName returns the canonical display label for an automated
// author — "Dependabot" instead of `49699333+dependabot[bot]@...`. Falls
// back to the author name with the trailing `[bot]` stripped (Title-Cased)
// for unrecognised bots, and to the raw email if there's no name at all.
//
// Callers should only invoke this when IsBot reports true; passing a
// human author returns the unmodified name.
func BotDisplayName(email, name string) string {
	if d := botDisplayLookup(email, name); d != "" {
		return d
	}
	// `Some-Service[bot]` → `Some-Service`.
	if i := strings.LastIndex(strings.ToLower(name), "[bot]"); i >= 0 {
		stripped := strings.TrimSpace(name[:i])
		if stripped != "" {
			return stripped
		}
	}
	if name != "" {
		return name
	}
	return email
}

func hasBotSuffix(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), "[bot]")
}

// botDisplayLookup returns the display name for the first matching rule,
// or "" when nothing matches. Order in botRules matters only when two
// substrings could collide — currently they don't, but keep the list
// stable when adding entries.
func botDisplayLookup(email, name string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	if e == "noreply@github.com" {
		return "GitHub"
	}
	for _, r := range botRules {
		if strings.Contains(e, r.emailSubstring) {
			return r.displayName
		}
	}
	return ""
}

// botRules is the curated mapping of automation identities to their
// display labels. Each entry is a commitment to filter every commit whose
// email contains the substring — keep narrow, add only when you've
// confirmed the identity is genuinely automated.
type botRule struct {
	emailSubstring string
	displayName    string
}

var botRules = []botRule{
	{"dependabot", "Dependabot"},
	{"renovate", "Renovate"},
	{"github-actions", "GitHub Actions"},
	{"mergify", "Mergify"},
	{"codecov", "Codecov"},
	{"snyk-bot", "Snyk"},
	{"imgbot", "ImgBot"},
	{"allcontributors", "AllContributors"},
	{"semantic-release-bot", "semantic-release"},
	{"copilot-pull-request-reviewer", "Copilot PR Reviewer"},
}

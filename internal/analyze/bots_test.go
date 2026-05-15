package analyze

import "testing"

func TestBotDisplayName(t *testing.T) {
	cases := []struct {
		desc, email, name, want string
	}{
		{"dependabot maps to Dependabot",
			"49699333+dependabot[bot]@users.noreply.github.com", "dependabot[bot]", "Dependabot"},
		{"renovate maps to Renovate",
			"29139614+renovate[bot]@users.noreply.github.com", "renovate[bot]", "Renovate"},
		{"github-actions maps to GitHub Actions",
			"41898282+github-actions[bot]@users.noreply.github.com", "GitHub Actions", "GitHub Actions"},
		{"noreply@github.com maps to GitHub",
			"noreply@github.com", "GitHub", "GitHub"},
		{"unknown [bot] strips the suffix",
			"unknown@example.com", "Acme-Service[bot]", "Acme-Service"},
		{"unknown bot with email-only falls back to email",
			"some-bot@example.com", "", "some-bot@example.com"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if got := BotDisplayName(c.email, c.name); got != c.want {
				t.Errorf("BotDisplayName(%q, %q) = %q, want %q", c.email, c.name, got, c.want)
			}
		})
	}
}

func TestIsBot(t *testing.T) {
	cases := []struct {
		name, email, author string
		want                bool
	}{
		// Positives — common GitHub bots use the `[bot]` suffix and a
		// users.noreply.github.com email.
		{"dependabot", "49699333+dependabot[bot]@users.noreply.github.com", "dependabot[bot]", true},
		{"renovate", "29139614+renovate[bot]@users.noreply.github.com", "renovate[bot]", true},
		{"github-actions", "41898282+github-actions[bot]@users.noreply.github.com", "GitHub Actions", true},
		{"mergify", "37929162+mergify[bot]@users.noreply.github.com", "mergify[bot]", true},
		{"snyk-bot", "snyk-bot@snyk.io", "Snyk bot", true},
		{"semantic-release", "semantic-release-bot@martynus.net", "semantic-release-bot", true},
		{"github-noreply", "noreply@github.com", "GitHub", true},
		// Suffix check is case-insensitive.
		{"upper-bot-suffix", "anything@example.com", "Some-Service[BOT]", true},

		// Negatives — "bot" substring without the suffix is not a signal.
		{"alice", "alice@example.com", "Alice", false},
		{"bottega-surname", "john@example.com", "John Bottega", false},
		{"hubot-prefix", "hubot@example.com", "Hubot McRobot", false},
		{"empty", "", "", false},
		{"only-email", "test@example.com", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsBot(c.email, c.author); got != c.want {
				t.Errorf("IsBot(%q, %q) = %v, want %v", c.email, c.author, got, c.want)
			}
		})
	}
}

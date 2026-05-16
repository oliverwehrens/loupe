package analyze

import "regexp"

// footerPattern is one body-footer rule. The regex is matched against the
// full message in multiline mode; a hit produces a single high-confidence
// signal of kind body_footer with the named source.
type footerPattern struct {
	source string
	re     *regexp.Regexp
	detail string
}

// footerPatterns are non-trailer markers some tools embed in the message
// body. They survive squash merges and rebase-and-merge the same way
// trailers do, so they're worth a separate pass — and a tool may emit a
// footer without a Co-Authored-By line (Claude Code can, OpenCode does
// by default at the time of writing).
var footerPatterns = []footerPattern{
	{
		source: SourceClaude,
		// Matches "Generated with [Claude Code]" / "Generated with Claude Code"
		// and the dual-line "🤖 Generated with [Claude Code]" Claude Code
		// writes by default. The robot emoji is a corroborating but not
		// required prefix.
		re:     regexp.MustCompile(`(?im)^\s*(?:🤖\s*)?Generated with \[?Claude Code\]?`),
		detail: "Generated with Claude Code",
	},
	{
		source: SourceOpenCode,
		re:     regexp.MustCompile(`(?im)^\s*(?:🤖\s*)?Generated with \[?opencode\]?`),
		detail: "Generated with opencode",
	},
}

// detectFromBodyFooters scans message for non-trailer "Generated with …"
// footers and returns one signal per distinct source matched. Like the
// trailer detector, the same (commit, source) pair is reported at most
// once.
func detectFromBodyFooters(message string) []Signal {
	if message == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var out []Signal
	for _, p := range footerPatterns {
		if !p.re.MatchString(message) {
			continue
		}
		if _, dup := seen[p.source]; dup {
			continue
		}
		seen[p.source] = struct{}{}
		out = append(out, Signal{
			Kind:       KindBodyFooter,
			Source:     p.source,
			Confidence: ConfidenceHigh,
			Detail:     p.detail,
		})
	}
	return out
}

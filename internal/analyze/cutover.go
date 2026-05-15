package analyze

import (
	"context"
	"time"

	"github.com/StephanSchmidt/loupe/internal/store"
)

const (
	CutoverReasonOverride    = "config-override"
	CutoverReasonAuto        = "auto"
	CutoverReasonNotDetected = "not-detected"
)

type Cutover struct {
	// Detected is true when either a config override is set or the
	// auto-detector found a week meeting the threshold.
	Detected bool
	// Date is the start (Monday UTC) of the cutover week. Zero when
	// Detected is false.
	Date time.Time
	// Reason explains how the cutover was set: "config-override", "auto",
	// or "not-detected".
	Reason string
	// Threshold is the AI-commit ratio used for auto detection. Surfaced
	// on the methodology slide so skeptics can poke at it.
	Threshold float64
}

// DetectCutover finds the first ISO week with an AI commit ratio at or above
// threshold. If override is non-zero it's used verbatim and weekly data is not
// consulted.
func DetectCutover(ctx context.Context, s *store.Store, threshold float64, override time.Time) (Cutover, error) {
	if !override.IsZero() {
		return Cutover{
			Detected:  true,
			Date:      IsoWeekStart(override),
			Reason:    CutoverReasonOverride,
			Threshold: threshold,
		}, nil
	}

	weeks, err := WeeklyStats(ctx, s)
	if err != nil {
		return Cutover{}, err
	}
	for _, w := range weeks {
		// `>= threshold` would match every week (including AI-commit=0
		// weeks) when threshold is 0 or negative — the very first week
		// would be tagged as the cutover. Require some real AI activity.
		if w.AICommits == 0 {
			continue
		}
		if w.CommitRatio() >= threshold {
			return Cutover{
				Detected:  true,
				Date:      w.WeekStart,
				Reason:    CutoverReasonAuto,
				Threshold: threshold,
			}, nil
		}
	}
	return Cutover{
		Detected:  false,
		Reason:    CutoverReasonNotDetected,
		Threshold: threshold,
	}, nil
}

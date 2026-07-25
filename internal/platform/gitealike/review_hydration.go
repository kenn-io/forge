package gitealike

import (
	"fmt"

	"go.kenn.io/middleman/internal/platform"
)

const (
	// MaxReviewHydrationPages bounds review identity discovery before any
	// per-review comment fan-out begins.
	MaxReviewHydrationPages = 10
	// MaxReviewHydrationReviews caps the per-review comment fan-out for one
	// merge-request detail refresh.
	MaxReviewHydrationReviews = 100
	// MaxReviewHydrationReviewsPerPass bounds one resumable detail continuation.
	MaxReviewHydrationReviewsPerPass = 8
	// MaxReviewHydrationComments caps the complete inline-comment dataset that
	// one merge-request detail refresh may persist.
	MaxReviewHydrationComments = 1000
)

// ReviewHydrationLimit returns a typed deferral when a complete review dataset
// is too large for one bounded detail refresh.
func ReviewHydrationLimit(field string, observed, limit int) error {
	return &platform.Error{
		Code:  platform.ErrCodePageLimit,
		Field: field,
		Err: fmt.Errorf(
			"review hydration observed %d records, exceeding the limit of %d",
			observed, limit,
		),
	}
}

package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/db"
)

func TestCarryMergeRequestDerivedFields(t *testing.T) {
	fetched := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	existing := &db.MergeRequest{
		PlatformHeadSHA: "head-sha",
		CommentCount:    7,
		ReviewDecision:  "approved",
		CIStatus:        "success",
		CIChecksJSON:    `[{"name":"build"}]`,
		CIHadPending:    true,
		DetailFetchedAt: &fetched,
	}

	tests := []struct {
		name           string
		normalized     db.MergeRequest
		wantCIStatus   string
		wantChecksJSON string
		wantCIPending  bool
		wantDetail     bool
	}{
		{
			name: "same head keeps CI, terminal keeps detail marker",
			normalized: db.MergeRequest{
				State: db.MergeRequestStateClosed, PlatformHeadSHA: "HEAD-SHA",
			},
			wantCIStatus:   "success",
			wantChecksJSON: `[{"name":"build"}]`,
			wantCIPending:  true,
			wantDetail:     true,
		},
		{
			name: "changed head drops head-derived CI state",
			normalized: db.MergeRequest{
				State: db.MergeRequestStateClosed, PlatformHeadSHA: "other-sha",
			},
			wantDetail: true,
		},
		{
			name: "reopen drops the detail marker so sync refetches",
			normalized: db.MergeRequest{
				State: db.MergeRequestStateOpen, PlatformHeadSHA: "head-sha",
			},
			wantCIStatus:   "success",
			wantChecksJSON: `[{"name":"build"}]`,
			wantCIPending:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			normalized := tt.normalized
			CarryMergeRequestDerivedFields(&normalized, existing)
			assert.Equal(7, normalized.CommentCount)
			assert.Equal("approved", normalized.ReviewDecision)
			assert.Equal(tt.wantCIStatus, normalized.CIStatus)
			assert.Equal(tt.wantChecksJSON, normalized.CIChecksJSON)
			assert.Equal(tt.wantCIPending, normalized.CIHadPending)
			if tt.wantDetail {
				assert.Equal(&fetched, normalized.DetailFetchedAt)
			} else {
				assert.Nil(normalized.DetailFetchedAt)
			}
		})
	}

	t.Run("missing stored row is a no-op", func(t *testing.T) {
		normalized := db.MergeRequest{State: db.MergeRequestStateClosed}
		CarryMergeRequestDerivedFields(&normalized, nil)
		assert.Zero(t, normalized.CommentCount)
	})
}

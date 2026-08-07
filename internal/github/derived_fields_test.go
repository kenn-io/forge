package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
)

// TestCarryMergeRequestDerivedFieldsPersistence proves the carry semantics on
// the stored row, not just the helper's output: CI status and checks survive
// a terminal snapshot when the head is unchanged and clear when it moved,
// while ci_had_pending and detail_fetched_at are owned by the snapshot
// upsert itself (stored flag wins; a set marker is never cleared).
func TestCarryMergeRequestDerivedFieldsPersistence(t *testing.T) {
	tests := []struct {
		name           string
		closedHeadSHA  string
		wantCIStatus   string
		wantChecksJSON string
	}{
		{
			name:           "same head keeps CI state",
			closedHeadSHA:  "HEAD-SHA",
			wantCIStatus:   "success",
			wantChecksJSON: `[{"name":"build"}]`,
		},
		{
			name:          "changed head drops head-derived CI state",
			closedHeadSHA: "other-sha",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			database := openTestDB(t)
			ctx := t.Context()
			now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
			repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
				Platform: "github", PlatformHost: "github.com",
				PlatformRepoID: "1", Owner: "owner", Name: "repo",
				RepoPath: "owner/repo",
			})
			require.NoError(t, err)
			seeded := &db.MergeRequest{
				RepoID: repoID, PlatformID: 1, Number: 1, Title: "Synthetic merge request",
				State: db.MergeRequestStateOpen, PlatformHeadSHA: "head-sha",
				CommentCount: 7, ReviewDecision: "approved",
				CIStatus: "success", CIChecksJSON: `[{"name":"build"}]`, CIHadPending: true,
				DetailFetchedAt: &now,
				CreatedAt:       now.Add(-time.Hour), UpdatedAt: now, LastActivityAt: now,
			}
			_, err = database.UpsertMergeRequest(ctx, seeded)
			require.NoError(t, err)

			closed := &db.MergeRequest{
				RepoID: repoID, PlatformID: 1, Number: 1, Title: "Synthetic merge request",
				State: db.MergeRequestStateClosed, PlatformHeadSHA: tt.closedHeadSHA,
				CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(time.Minute),
				LastActivityAt: now.Add(time.Minute),
			}
			CarryMergeRequestDerivedFields(closed, seeded)
			_, _, accepted, err := database.UpsertMergeRequestSnapshotWithLabels(ctx, closed)
			require.NoError(t, err)
			require.True(t, accepted)

			stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
			require.NoError(t, err)
			require.NotNil(t, stored)
			assert.Equal(db.MergeRequestStateClosed, stored.State)
			assert.Equal(7, stored.CommentCount)
			assert.Equal("approved", stored.ReviewDecision)
			assert.Equal(tt.wantCIStatus, stored.CIStatus)
			assert.Equal(tt.wantChecksJSON, stored.CIChecksJSON)
			// Owned by the upsert, not the carry: the stored pending flag
			// always wins and a set detail marker is never cleared.
			assert.True(stored.CIHadPending)
			require.NotNil(t, stored.DetailFetchedAt)
			assert.Equal(now, stored.DetailFetchedAt.UTC())
		})
	}

	t.Run("missing stored row is a no-op", func(t *testing.T) {
		normalized := db.MergeRequest{State: db.MergeRequestStateClosed}
		CarryMergeRequestDerivedFields(&normalized, nil)
		assert.Zero(t, normalized.CommentCount)
	})
}

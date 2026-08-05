package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateMREventMetadata(t *testing.T) {
	tests := []struct {
		name         string
		updateKey    string
		wantMetadata string
	}{
		{
			name:         "matching event",
			updateKey:    "commit-1",
			wantMetadata: `{"commit_order_key":1,"obsolete":true}`,
		},
		{
			name:         "non-matching event",
			updateKey:    "commit-missing",
			wantMetadata: `{"commit_order_key":1}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			database := openTestDB(t)
			ctx := t.Context()
			createdAt := time.Date(2026, 8, 5, 10, 30, 0, 0, time.UTC)
			repoID := insertTestRepo(t, database, "owner", "repo")
			mrID := insertTestMR(t, database, repoID, 1, "Synthetic merge request", createdAt)
			require.NoError(t, database.UpsertMREvents(ctx, []MREvent{{
				MergeRequestID: mrID,
				EventType:      "commit",
				Author:         "developer",
				Summary:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Body:           "Synthetic commit body",
				MetadataJSON:   `{"commit_order_key":1}`,
				CreatedAt:      createdAt,
				DedupeKey:      "commit-1",
			}}))

			require.NoError(t, database.UpdateMREventMetadata(ctx, mrID, map[string]string{
				tt.updateKey: `{"commit_order_key":1,"obsolete":true}`,
			}))
			events, err := database.ListMREvents(ctx, mrID)
			require.NoError(t, err)
			require.Len(t, events, 1)
			event := events[0]
			assert.JSONEq(tt.wantMetadata, event.MetadataJSON)
			assert.Equal("developer", event.Author)
			assert.Equal("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", event.Summary)
			assert.Equal("Synthetic commit body", event.Body)
			assert.Equal(createdAt, event.CreatedAt)
		})
	}
}

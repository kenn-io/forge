package platform

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDBCIChecksComputesDurationFromProviderTimestamps(t *testing.T) {
	assert := assert.New(t)
	started := time.Date(2026, 5, 12, 14, 0, 0, 0, time.UTC)
	completed := started.Add(90 * time.Second)

	checks := DBCIChecks([]CICheck{{
		Name:        "build",
		Status:      "completed",
		Conclusion:  "success",
		StartedAt:   &started,
		CompletedAt: &completed,
	}, {
		Name:        "pending",
		Status:      "in_progress",
		StartedAt:   &started,
		CompletedAt: nil,
	}})

	require.Len(t, checks, 2)
	require.NotNil(t, checks[0].DurationSeconds)
	assert.Equal(int64(90), *checks[0].DurationSeconds)
	assert.Nil(checks[1].DurationSeconds)
}

func TestDBMergeRequestCarriesProviderMergeableState(t *testing.T) {
	mr := DBMergeRequest(42, MergeRequest{
		Number:         7,
		MergeableState: "dirty",
	})

	assert.Equal(t, "dirty", mr.MergeableState)
}

// TestDBMergeRequestCarriesCloneURLUnknownMarker proves the persistence
// conversion threads the unknown-clone-URL marker through, so a degraded
// provider observation reaches the upsert with its preserve semantics intact.
func TestDBMergeRequestCarriesCloneURLUnknownMarker(t *testing.T) {
	mr := DBMergeRequest(42, MergeRequest{
		Number:                  7,
		HeadRepoCloneURLUnknown: true,
	})

	assert.True(t, mr.HeadRepoCloneURLUnknown)
}

package federation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProtocolMigrationPreservesHubEnrollmentAndRevocation(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	path := filepath.Join(t.TempDir(), "enrollments.json")
	now := time.Now().UTC()
	state := persistedEnrollmentStore{Version: 1, Enrollments: []Enrollment{{
		ID: testEnrollmentID, NodeID: testNodeID, HubID: testHubID,
		HubURL: "https://hub.example", SpokeBaseURL: "https://spoke.example",
		ProtocolVersion: 3, State: EnrollmentRevoked, ExpiresAt: now,
		CreatedAt: now, UpdatedAt: now, RevokedAt: now,
	}}}
	raw, err := json.Marshal(state)
	require.NoError(err)
	require.NoError(os.WriteFile(path, raw, 0o600))
	_, err = Open(path, StoreOptions{})
	require.ErrorIs(err, ErrProtocolMismatch)
	prepared := 0
	require.NoError(MigrateProtocol3To4(path, func(local *LocalEnrollment) (string, error) {
		assert.Nil(local)
		prepared++
		return "", nil
	}))
	store, err := Open(path, StoreOptions{})
	require.NoError(err)
	actual, err := store.Get(t.Context(), testEnrollmentID)
	require.NoError(err)
	expected := state.Enrollments[0]
	expected.ProtocolVersion = 4
	assert.Equal(expected, actual)
	require.NoError(MigrateProtocol3To4(path, func(*LocalEnrollment) (string, error) {
		prepared++
		return "", nil
	}))
	assert.Equal(1, prepared)
}

func TestProtocolMigrationRejectsFutureAndDoesNotPublishAfterPreparationFailure(t *testing.T) {
	for _, protocol := range []int{3, 5} {
		t.Run(string(rune('0'+protocol)), func(t *testing.T) {
			require := require.New(t)
			path := filepath.Join(t.TempDir(), "enrollments.json")
			state := persistedEnrollmentStore{Version: 1, Local: &LocalEnrollment{
				EnrollmentID: testEnrollmentID, NodeID: testNodeID,
				SpokeBaseURL: "https://spoke.example", HubURL: "https://hub.example",
				ProtocolVersion: protocol, State: EnrollmentPending,
			}}
			raw, err := json.Marshal(state)
			require.NoError(err)
			require.NoError(os.WriteFile(path, raw, 0o600))
			err = MigrateProtocol3To4(path, func(*LocalEnrollment) (string, error) {
				return "", errors.New("preparation failed")
			})
			require.Error(err)
			actual, err := os.ReadFile(path)
			require.NoError(err)
			assert.Equal(t, raw, actual)
		})
	}
}

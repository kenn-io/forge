package platformdb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"
)

func TestBitbucketUUIDSurvivesRepositoryRename(t *testing.T) {
	d := dbtest.Open(t)
	ref := platform.RepoRef{Platform: platform.KindBitbucket, Host: "bitbucket.org", PlatformExternalID: "{11111111-1111-4111-8111-111111111111}", Owner: "team", Name: "widgets", RepoPath: "team/widgets"}
	first, _, err := d.ReconcileRepositoryObservation(t.Context(), DBRepoIdentity(ref), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	ref.Owner, ref.Name, ref.RepoPath = "new-team", "renamed", "new-team/renamed"
	renamed, _, err := d.ReconcileRepositoryObservation(t.Context(), DBRepoIdentity(ref), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, first.Repository.ID, renamed.Repository.ID)
	stored, err := d.GetRepoByID(t.Context(), first.Repository.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "{11111111-1111-4111-8111-111111111111}", stored.PlatformRepoID)
	assert.Equal(t, "new-team/renamed", stored.RepoPath)
}

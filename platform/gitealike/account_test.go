package gitealike

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func TestPullRequestKeepsAccountIdentityWithoutInferringBots(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mr := NormalizePullRequest(platform.RepoRef{}, PullRequestDTO{
		User:     UserDTO{ID: 21, UserName: "robot[bot]"},
		MergedBy: UserDTO{ID: 22, UserName: "merger-a"},
	})
	require.NotNil(mr.AuthorAccount)
	require.NotNil(mr.MergerAccount)
	assert.Equal("21", mr.AuthorAccount.ID)
	assert.Equal("22", mr.MergerAccount.ID)
	assert.Equal(platform.AccountUnknown, mr.AuthorAccount.Type)
}

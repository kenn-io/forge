package platform

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBitbucketDataCenterRemoteIdentity(t *testing.T) {
	ref := RepoRef{Platform: KindBitbucket, Host: "code.example.test:8443", Owner: "PROJECT", Name: "repo"}
	for _, remote := range []string{"https://code.example.test:8443/scm/PROJECT/repo.git", "ssh://git@code.example.test:7999/PROJECT/repo.git"} {
		require.NoError(t, ValidateRemoteIdentity(ref, remote))
		require.Equal(t, "PROJECT/repo", RemoteRepoPath(ref.Platform, ref.Host, remote))
	}
	for _, remote := range []string{"https://code.example.test:9443/scm/PROJECT/repo.git", "ssh://git@other.example.test:7999/PROJECT/repo.git", "https://code.example.test:8443/scm/OTHER/repo.git"} {
		require.Error(t, ValidateRemoteIdentity(ref, remote))
	}
	require.Error(t, ValidateRemoteIdentity(RepoRef{Platform: KindBitbucket, Host: DefaultBitbucketHost, Owner: "PROJECT", Name: "repo"}, "https://bitbucket.org/scm/PROJECT/repo.git"))
}

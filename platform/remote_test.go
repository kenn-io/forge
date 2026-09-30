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

func TestDefaultCloneURLUsesEachProvidersHTTPRoute(t *testing.T) {
	tests := map[string]struct {
		kind     Kind
		host     string
		repoPath string
		want     string
	}{
		"github":               {KindGitHub, "github.com", "acme/widget", "https://github.com/acme/widget.git"},
		"gitlab nested":        {KindGitLab, "gitlab.example.com", "group/sub/widget", "https://gitlab.example.com/group/sub/widget.git"},
		"bitbucket cloud":      {KindBitbucket, DefaultBitbucketHost, "team/widget", "https://bitbucket.org/team/widget.git"},
		"bitbucket datacenter": {KindBitbucket, "bitbucket.example.com", "PROJ/widget", "https://bitbucket.example.com/scm/PROJ/widget.git"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := DefaultCloneURL(tt.kind, tt.host, tt.repoPath)

			require.Equal(t, tt.want, got)
			require.Equal(t, tt.repoPath, RemoteRepoPath(tt.kind, tt.host, got))
		})
	}
}

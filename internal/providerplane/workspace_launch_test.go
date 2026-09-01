package providerplane

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
)

func workspaceLaunchResponseForTest() (WorkspaceLaunchRequest, db.WorkspaceLaunchSpec) {
	issuedAt := time.Date(2026, 8, 22, 16, 0, 0, 0, time.UTC)
	request := WorkspaceLaunchRequest{
		Repository: RepositoryRoute{
			Provider: "github", PlatformHost: "github.com",
			Owner: "acme", Name: "widget",
		},
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
	}
	spec := db.WorkspaceLaunchSpec{
		Version: db.WorkspaceLaunchSpecVersion,
		Repository: db.WorkspaceLaunchRepository{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget",
			CloneURL: "https://github.com/acme/widget.git", DefaultBranch: "main",
		},
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		ItemKey: "42", GitHeadRef: "feature/widgets",
		Pull: &db.WorkspaceLaunchPull{
			HeadBranch: "feature/widgets", HeadRepoKind: "same_repo",
			SnapshotRevision: 7,
		},
		SourceVisible: true, IssuedAt: issuedAt,
		SourceVisibleUntil: issuedAt.Add(db.WorkspaceLaunchSpecVisibilityLease),
	}
	return request, spec
}

func TestWorkspaceLaunchResponseValidatesExactGitIdentity(t *testing.T) {
	request, spec := workspaceLaunchResponseForTest()
	require.NoError(t, ValidateFederationWorkspaceLaunchSpecResponse(request, spec))

	tests := []struct {
		name string
		edit func(*WorkspaceLaunchRequest, *db.WorkspaceLaunchSpec)
	}{
		{
			name: "base repository redirect",
			edit: func(_ *WorkspaceLaunchRequest, spec *db.WorkspaceLaunchSpec) {
				spec.Repository.CloneURL = "https://github.com/attacker/widget.git"
			},
		},
		{
			name: "local base repository path",
			edit: func(_ *WorkspaceLaunchRequest, spec *db.WorkspaceLaunchSpec) {
				spec.Repository.CloneURL = "/tmp/attacker/widget.git"
			},
		},
		{
			name: "local fork file URL",
			edit: func(_ *WorkspaceLaunchRequest, spec *db.WorkspaceLaunchSpec) {
				spec.Pull.HeadRepoKind = "fork"
				spec.Pull.HeadRepoCloneURL = "file:///tmp/attacker/widget.git"
			},
		},
		{
			name: "cross-host fork redirect",
			edit: func(_ *WorkspaceLaunchRequest, spec *db.WorkspaceLaunchSpec) {
				spec.Pull.HeadRepoKind = "fork"
				spec.Pull.HeadRepoCloneURL = "https://evil.example/contributor/widget.git"
			},
		},
		{
			name: "fork repository traversal",
			edit: func(_ *WorkspaceLaunchRequest, spec *db.WorkspaceLaunchSpec) {
				spec.Pull.HeadRepoKind = "fork"
				spec.Pull.HeadRepoCloneURL = "github.com:../../tmp/widget.git"
			},
		},
		{
			name: "requested branch changed",
			edit: func(request *WorkspaceLaunchRequest, spec *db.WorkspaceLaunchSpec) {
				request.GitHeadRef = "requested"
				spec.GitHeadRef = "different"
			},
		},
		{
			name: "noncanonical response route",
			edit: func(_ *WorkspaceLaunchRequest, spec *db.WorkspaceLaunchSpec) {
				spec.Repository.Owner = " ACME "
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, spec := workspaceLaunchResponseForTest()
			test.edit(&request, &spec)
			assert.Error(t, ValidateFederationWorkspaceLaunchSpecResponse(request, spec))
		})
	}
}

func TestWorkspaceLaunchRefreshAcceptsRenamedStableRepository(t *testing.T) {
	request, spec := workspaceLaunchResponseForTest()
	request.PlatformRepoID = spec.Repository.PlatformRepoID
	spec.Repository.Owner = "acme-renamed"
	spec.Repository.Name = "widget-renamed"
	spec.Repository.CloneURL = "https://github.com/acme-renamed/widget-renamed.git"

	require.NoError(t, ValidateFederationWorkspaceLaunchSpecResponse(request, spec))

	spec.Repository.PlatformRepoID = "different-repository"
	require.ErrorContains(
		t, ValidateFederationWorkspaceLaunchSpecResponse(request, spec),
		"repository identity",
	)

	for _, test := range []struct {
		name string
		edit func(*db.WorkspaceLaunchSpec)
	}{
		{
			name: "provider mismatch",
			edit: func(spec *db.WorkspaceLaunchSpec) {
				spec.Repository.Provider = "gitlab"
				spec.Repository.PlatformHost = "gitlab.com"
				spec.Repository.CloneURL =
					"https://gitlab.com/acme-renamed/widget-renamed.git"
			},
		},
		{
			name: "platform host mismatch",
			edit: func(spec *db.WorkspaceLaunchSpec) {
				spec.Repository.PlatformHost = "github.example.test"
				spec.Repository.CloneURL =
					"https://github.example.test/acme-renamed/widget-renamed.git"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, candidate := workspaceLaunchResponseForTest()
			candidate.Repository.Owner = "acme-renamed"
			candidate.Repository.Name = "widget-renamed"
			candidate.Repository.CloneURL =
				"https://github.com/acme-renamed/widget-renamed.git"
			test.edit(&candidate)
			require.ErrorContains(
				t, ValidateFederationWorkspaceLaunchSpecResponse(request, candidate),
				"repository route",
			)
		})
	}
}

func TestFederationNetworkRemoteRequiresEncryptedTransport(t *testing.T) {
	for _, remoteURL := range []string{
		"https://git.example.test/acme/widget.git",
		"ssh://git@git.example.test/acme/widget.git",
		"git@git.example.test:acme/widget.git",
		"http://127.0.0.1/acme/widget.git",
		"http://[::1]/acme/widget.git",
		"http://localhost/acme/widget.git",
	} {
		t.Run("accept "+remoteURL, func(t *testing.T) {
			require.NoError(t, validateFederationNetworkRemote(remoteURL))
		})
	}
	for _, remoteURL := range []string{
		"http://git.example.test/acme/widget.git",
		"git://git.example.test/acme/widget.git",
		"ftp://git.example.test/acme/widget.git",
	} {
		t.Run("reject "+remoteURL, func(t *testing.T) {
			assert.Error(t, validateFederationNetworkRemote(remoteURL))
		})
	}
}

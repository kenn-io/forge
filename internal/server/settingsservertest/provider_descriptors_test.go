package settingsservertest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/tokenauth"
)

type descriptorCloneRoutes struct {
	source tokenauth.Source
}

func (r descriptorCloneRoutes) SourceForRepo(
	_, _, owner, name string,
) tokenauth.Source {
	if owner == "acme" && (name == "widget" || name == "widgets") {
		return r.source
	}
	return nil
}

func (descriptorCloneRoutes) FallbackSource(string) tokenauth.Source { return nil }

func TestWorkspaceLaunchSpecRequiresForkCredentialRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	issuedAt := time.Date(2026, time.August, 22, 16, 30, 0, 0, time.UTC)
	spec := db.WorkspaceLaunchSpec{
		Version: db.WorkspaceLaunchSpecVersion,
		Repository: db.WorkspaceLaunchRepository{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget",
			CloneURL: "https://github.com/acme/widget.git", DefaultBranch: "main",
		},
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		ItemKey: "42", GitHeadRef: "feature/fork",
		Pull: &db.WorkspaceLaunchPull{
			HeadBranch: "feature/fork", HeadRepoKind: "fork",
			HeadRepoCloneURL: "https://github.com/contributor/widget.git",
			SnapshotRevision: 1,
		},
		SourceVisible: true, IssuedAt: issuedAt,
		SourceVisibleUntil: issuedAt.Add(db.WorkspaceLaunchSpecVisibilityLease),
	}
	encoded, err := json.Marshal(spec)
	require.NoError(err)
	source := &spokeapi.HubProviderSource{
		Client: providerPlaneClientFunc(func(
			_ context.Context, _ federationauth.Scope, _ *http.Request,
		) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(encoded)),
			}, nil
		}),
		Clones: gitclone.New(t.TempDir(), descriptorCloneRoutes{
			source: testTokenSource("spoke-git-token"),
		}),
	}

	_, err = source.ResolveWorkspaceLaunchSpec(
		t.Context(), providerplane.WorkspaceLaunchRequest{
			Repository: providerplane.RepositoryRoute{
				Provider: "github", PlatformHost: "github.com",
				Owner: "acme", Name: "widget",
			},
			ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		},
	)
	require.Error(err)
	problem, ok := errors.AsType[*httpapi.ProblemError](err)
	require.True(ok)
	assert.Equal(httpapi.CodeGitCredentialUnavailable, problem.Code)
	assert.Equal("contributor/widget", problem.Details["repoPath"])
}

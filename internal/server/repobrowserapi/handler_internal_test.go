package repobrowserapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

type staticRepositoryDescriptorSource struct {
	descriptor providerplane.RepositoryDescriptor
}

func (s staticRepositoryDescriptorSource) GetRepositoryDescriptor(
	context.Context, providerplane.RepositoryRoute,
) (providerplane.RepositoryDescriptor, error) {
	return s.descriptor, nil
}

func TestRepoBrowserRejectsDescriptorForDifferentStableRepository(t *testing.T) {
	database := dbtest.Open(t)
	repoID, err := reposeed.Seed(t.Context(), database, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: 1002,
		Owner:          "acme", Name: "widget", RepoPath: "acme/widget",
	})
	require.NoError(t, err)
	require.NoError(t, database.UpdateRepoProviderObservation(
		t.Context(), repoID, db.RepoProviderMetadata{
			CloneURL:      "https://github.com/acme/widget.git",
			DefaultBranch: "main",
		}, nil, nil,
	))
	handler := New(Deps{
		Resolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{
			DB: database,
		}),
		Clones: gitclone.New(t.TempDir(), nil),
		DescriptorSource: staticRepositoryDescriptorSource{descriptor: providerplane.RepositoryDescriptor{
			ProtocolVersion: federation.ProtocolVersion,
			Provider:        "github", PlatformHost: "github.com",
			PlatformRepoID: 1001,
			Owner:          "acme", Name: "widget",
			CloneURL: "https://github.com/acme/widget.git", DefaultBranch: "main",
			ObservedAt: time.Now().UTC(),
		}},
	})

	_, _, err = handler.ensureRepoBrowserClone(
		t.Context(), "github", "github.com", "acme", "widget", "acme/widget",
	)
	require.ErrorIs(t, err, db.ErrRepositoryIdentityChanged)
}

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
	repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "provider-repository-b",
		Owner:          "acme", Name: "widget", RepoPath: "acme/widget",
	})
	require.NoError(t, err)
	require.NoError(t, database.UpdateRepoProviderMetadata(
		t.Context(), repoID, db.RepoProviderMetadata{
			PlatformRepoID: "provider-repository-b",
			CloneURL:       "https://github.com/acme/widget.git",
			DefaultBranch:  "main",
		},
	))
	handler := New(Deps{
		Resolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{
			DB: database,
		}),
		Clones: gitclone.New(t.TempDir(), nil),
		DescriptorSource: staticRepositoryDescriptorSource{descriptor: providerplane.RepositoryDescriptor{
			ProtocolVersion: federation.ProtocolVersion,
			Provider:        "github", PlatformHost: "github.com",
			PlatformRepoID: "provider-repository-a",
			Owner:          "acme", Name: "widget",
			CloneURL: "https://github.com/acme/widget.git", DefaultBranch: "main",
			ObservedAt: time.Now().UTC(), SnapshotRevision: 1,
		}},
	})

	_, _, err = handler.ensureRepoBrowserClone(
		t.Context(), "github", "github.com", "acme", "widget", "acme/widget",
	)
	require.ErrorIs(t, err, db.ErrRepositoryRouteFenceChanged)
}

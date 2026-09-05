package apitest

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/forge/platform"
)

type landingAPIProvider struct {
	collect func(context.Context, platform.RepoRef, platform.Budget) (platform.LandingSnapshot, error)
}

func (p *landingAPIProvider) Platform() platform.Kind             { return platform.KindGitHub }
func (p *landingAPIProvider) Host() string                        { return "github.test" }
func (p *landingAPIProvider) Capabilities() platform.Capabilities { return platform.Capabilities{} }
func (p *landingAPIProvider) LandingCapabilities() platform.LandingCapabilities {
	return platform.LandingCapabilities{}
}
func (p *landingAPIProvider) CollectLandingEvidence(ctx context.Context, ref platform.RepoRef, budget platform.Budget) (platform.LandingSnapshot, error) {
	return p.collect(ctx, ref, budget)
}

func TestAPIArchiveLandingFencesRepositoryChangesOutsideDatabaseLocks(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "route-reused"}[change], func(t *testing.T) {
			require := require.New(t)
			t.Parallel()
			assert := assert.New(t)
			database := dbtest.Open(t)
			identity := db.RepoIdentity{Platform: "github", PlatformHost: "github.test", PlatformRepoID: "node-fixture-a", Owner: "team-a", Name: "project-a", RepoPath: "team-a/project-a"}
			_, err := database.UpsertRepo(t.Context(), identity)
			require.NoError(err)
			calls := 0
			provider := &landingAPIProvider{collect: func(ctx context.Context, ref platform.RepoRef, budget platform.Budget) (platform.LandingSnapshot, error) {
				calls++
				_, deadline := ctx.Deadline()
				assert.True(deadline)
				assert.Equal("node-fixture-a", ref.PlatformExternalID)
				assert.Equal(int64(10000), budget.MaxRecords)
				if change {
					replacement := identity
					replacement.PlatformRepoID = "node-fixture-b"
					_, _, err := database.ReconcileRepositoryObservation(ctx, replacement, time.Now().UTC().Add(time.Second))
					require.NoError(err, "provider work must not hold a reconciliation lock")
				}
				return platform.LandingSnapshot{Schema: platform.LandingSnapshotSchema, Repository: platform.LandingRepository{Identity: platform.RepositoryIdentity{Provider: platform.KindGitHub, Instance: "github.test", ID: "17"}}, Coverage: platform.LandingCoverage{Reason: "fixture-partial"}}, nil
			}}
			registry, err := platform.NewRegistry(provider)
			require.NoError(err)
			syncer := ghclient.NewSyncerWithRegistry(registry, database, nil, nil, time.Minute, nil, nil)
			t.Cleanup(syncer.Stop)
			srv := servertest.New(t, database, syncer, nil, "/", nil, server.ServerOptions{})
			client := setupTestClient(t, srv)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			response, err := client.HTTP.GetArchiveLandingEvidenceWithResponse(ctx, &generated.GetArchiveLandingEvidenceParams{Repo: "github|github.test/team-a/project-a"})
			require.NoError(err)
			assert.Equal(1, calls)
			if change {
				assert.Equal(http.StatusConflict, response.StatusCode())
				require.NotNil(response.ApplicationproblemJSONDefault)
				assert.Equal(generated.Conflict, response.ApplicationproblemJSONDefault.Code)
				return
			}
			require.Equal(http.StatusOK, response.StatusCode())
			require.NotNil(response.JSON200)
			assert.Equal(platform.LandingSnapshotSchema, response.JSON200.SnapshotSchema)
			assert.Equal("17", response.JSON200.Repository.Identity.Id)
			assert.Equal("fixture-partial", response.JSON200.Coverage.Reason)
		})
	}
}

func TestAPIArchiveLandingPreservesProviderFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   generated.ProblemErrorCode
	}{
		{"child-deadline", context.DeadlineExceeded, http.StatusBadGateway, generated.UpstreamError},
		{"output-limit", platform.ErrPageLimit, http.StatusRequestEntityTooLarge, generated.PayloadTooLarge},
		{"rejected-credential", platform.ErrCredentialRejected, http.StatusForbidden, generated.Forbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			database := dbtest.Open(t)
			_, err := database.UpsertRepo(t.Context(), db.RepoIdentity{Platform: "github", PlatformHost: "github.test", PlatformRepoID: "node-fixture-a", Owner: "team-a", Name: "project-a", RepoPath: "team-a/project-a"})
			require.NoError(err)
			provider := &landingAPIProvider{collect: func(context.Context, platform.RepoRef, platform.Budget) (platform.LandingSnapshot, error) {
				return platform.LandingSnapshot{}, tc.err
			}}
			registry, err := platform.NewRegistry(provider)
			require.NoError(err)
			syncer := ghclient.NewSyncerWithRegistry(registry, database, nil, nil, time.Minute, nil, nil)
			t.Cleanup(syncer.Stop)
			client := setupTestClient(t, servertest.New(t, database, syncer, nil, "/", nil, server.ServerOptions{}))
			response, err := client.HTTP.GetArchiveLandingEvidenceWithResponse(t.Context(), &generated.GetArchiveLandingEvidenceParams{Repo: "github|github.test/team-a/project-a"})
			require.NoError(err)
			assert.Equal(tc.status, response.StatusCode())
			require.NotNil(response.ApplicationproblemJSONDefault)
			assert.Equal(tc.code, response.ApplicationproblemJSONDefault.Code)
		})
	}
}

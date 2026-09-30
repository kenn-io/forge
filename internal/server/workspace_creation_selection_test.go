package server

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/platform"
)

func TestItemWorkspaceCreationValidatesCachedRepositorySelection(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"local", "spoke"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			assert, require := assert.New(t), require.New(t)
			database := dbtest.Open(t)
			serverfake.SeedPR(t, database, "acme", "widget", 42)
			serverfake.SeedIssue(t, database, "acme", "widget", 42, "open")
			serverfake.SeedPR(t, database, "acme", "widget", 43)
			serverfake.SeedIssue(t, database, "acme", "widget", 43, "open")
			renamed, err := database.ObserveRepository(t.Context(), db.RepoIdentity{
				Platform: "github", PlatformHost: "github.com", Key: platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "widget")),
				Owner: "acme", Name: "widgets",
			})
			require.NoError(err)
			require.NoError(database.UpdateRepoProviderObservation(t.Context(), renamed.Repository.ID, db.RepoProviderMetadata{
				CloneURL: "https://github.com/acme/widgets.git", DefaultBranch: "main",
			}, nil, nil))
			credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "hub-credentials.json"))
			require.NoError(err)
			hub := New(database, nil, nil, "/", &config.Config{
				Tmux: config.Tmux{Command: []string{"kenn-forge-no-such-tmux"}},
			}, ServerOptions{
				FederationSpokeID: proxyTestHubID, FederationCredentials: credentials,
				WorktreeDir: filepath.Join(t.TempDir(), "worktrees"), PtyOwnerInProcess: true,
				DisableWorkspaceBackgroundMonitors: true,
			})
			t.Cleanup(func() { serverfake.GracefulShutdown(t, hub) })
			server, workspaceDB, prefix := hub, database, "/api/v1"
			if target == "spoke" {
				remote := httptest.NewTLSServer(hub)
				t.Cleanup(remote.Close)
				token, err := credentials.MintInbound(proxyTestNodeID, federationauth.SpokeToHubScopes())
				require.NoError(err)
				nodeCredentials, err := federationauth.Open(filepath.Join(t.TempDir(), "spoke-credentials.json"))
				require.NoError(err)
				require.NoError(nodeCredentials.StoreOutbound(proxyTestHubID, token, federationauth.SpokeToHubScopes()))
				workspaceDB = dbtest.Open(t)
				server = New(workspaceDB, nil, nil, "/", &config.Config{
					Fleet: config.Fleet{Enabled: true, Role: config.FleetRoleSpoke, Hub: &config.FleetHub{NodeID: proxyTestHubID, BaseURL: remote.URL}},
					Tmux:  config.Tmux{Command: []string{"kenn-forge-no-such-tmux"}},
				}, ServerOptions{
					FederationSpokeID: proxyTestNodeID, FederationSpokeActive: true,
					FederationCredentials: nodeCredentials, FederationHTTPClient: remote.Client(),
					WorktreeDir: filepath.Join(t.TempDir(), "worktrees"), PtyOwnerInProcess: true,
					DisableWorkspaceBackgroundMonitors: true,
				})
				t.Cleanup(func() { serverfake.GracefulShutdown(t, server) })
				// Only launch admission needs credentials. The workspace manager has
				// no clone manager, so background setup cannot run external Git.
				server.providerSource.Clones = gitclone.New(t.TempDir(), descriptorCloneRoutes{
					source: serverfake.TestTokenSource("spoke-git-token"),
				})
				prefix += "/fleet/hosts/self"
			}
			requests := []struct {
				path string
				body map[string]any
			}{
				{path: "/workspaces", body: map[string]any{
					"provider": "github", "platform_host": "github.com", "owner": "acme", "name": "widget",
					"mr_number": 42, "platform_repo_id": testutil.FixtureRepoID("acme", "widget"), "suppress_auto_assign": true,
				}},
				{path: "/issues/gh/acme/widget/42/workspace", body: map[string]any{
					"platform_repo_id": testutil.FixtureRepoID("acme", "widget"), "suppress_auto_assign": true,
				}},
			}
			for _, request := range requests {
				response := testutil.DoJSON(t, server, http.MethodPost, prefix+request.path, request.body)
				require.Equal(http.StatusAccepted, response.Code, response.Body.String())
				var created workspaceapi.WorkspaceResponse
				require.NoError(json.Unmarshal(response.Body.Bytes(), &created))
				spec, err := workspaceDB.GetWorkspaceLaunchSpec(t.Context(), created.ID)
				require.NoError(err)
				require.NotNil(spec)
				assert.Equal(platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "widget")), spec.Repository.Key)
				assert.Equal("widgets", spec.Repository.Name)
			}

			_, err = database.ObserveRepository(t.Context(), db.RepoIdentity{
				Platform: "github", PlatformHost: "github.com", Key: platform.RepositoryIDKey(2002),
				Owner: "acme", Name: "widget",
			})
			require.NoError(err)
			requests[0].body["mr_number"] = 43
			requests[1].path = "/issues/gh/acme/widget/43/workspace"
			for _, request := range requests {
				response := testutil.DoJSON(t, server, http.MethodPost, prefix+request.path, request.body)
				assert.Equal(http.StatusNotFound, response.Code, response.Body.String())
			}
			workspaces, err := workspaceDB.ListWorkspaces(t.Context())
			require.NoError(err)
			assert.Len(workspaces, 2, "rejected selections must not persist another workspace")
			if target == "spoke" {
				// Spoke preparation uses this same read-only endpoint for an
				// existing workspace, whose stable identity survives route reuse.
				spec, err := server.providerSource.ResolveWorkspaceLaunchSpec(t.Context(), providerplane.WorkspaceLaunchRequest{
					Repository: providerplane.RepositoryRoute{Provider: "github", PlatformHost: "github.com", Owner: "acme", Name: "widget"},
					RepoKey:    platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "widget")), ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
				})
				require.NoError(err)
				assert.Equal(platform.RepositoryIDKey(testutil.FixtureRepoID("acme", "widget")), spec.Repository.Key)
				assert.Equal("widgets", spec.Repository.Name)
			}
		})
	}
}

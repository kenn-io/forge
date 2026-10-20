package workspaceapi

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/testutil/workspacetest"
)

func TestWorkspaceMergeTargetWithoutLocalPullCache(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		expired     bool
		hidden      bool
		unavailable bool
		missingBase bool
		wantStatus  int
		wantCode    httpapi.ProblemCode
	}{
		{name: "current launch facts", wantStatus: http.StatusOK},
		{name: "expired facts refresh the target", expired: true, wantStatus: http.StatusOK},
		{name: "expired facts require the hub", expired: true, unavailable: true, wantStatus: http.StatusServiceUnavailable, wantCode: httpapi.CodeHubUnavailable},
		{name: "hidden source stays unavailable", hidden: true, wantStatus: http.StatusNotFound, wantCode: httpapi.CodeNotFound},
		{name: "missing base stays unavailable", missingBase: true, wantStatus: http.StatusNotFound, wantCode: httpapi.CodeNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			assert := assert.New(t)
			ctx := t.Context()
			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			database := dbtest.Open(t)
			repoID, err := reposeed.Seed(ctx, database, db.GitHubRepoIdentity("github.com", "acme", "widget"))
			require.NoError(err)
			work := gitfixture.ScratchDir(t)
			gitfixture.Run(t, work, "init", "--initial-branch=main")
			gitfixture.Run(t, work, "config", "user.email", "test@example.com")
			gitfixture.Run(t, work, "config", "user.name", "Test")
			gitfixture.Run(t, work, "commit", "--allow-empty", "-m", "base")
			gitfixture.Run(t, work, "update-ref", "refs/remotes/origin/main", "HEAD")
			gitfixture.Run(t, work, "checkout", "-b", "release")
			require.NoError(os.WriteFile(filepath.Join(work, "release.txt"), []byte("release\n"), 0o600))
			gitfixture.Run(t, work, "add", ".")
			gitfixture.Run(t, work, "commit", "-m", "release change")
			gitfixture.Run(t, work, "update-ref", "refs/remotes/origin/release", "HEAD")
			gitfixture.Run(t, work, "checkout", "-b", "feature")
			require.NoError(os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o600))
			gitfixture.Run(t, work, "add", ".")
			gitfixture.Run(t, work, "commit", "-m", "workspace change")

			spec := workspaceLaunchSpecForRequest(providerplane.WorkspaceLaunchRequest{
				Repository: providerplane.RepositoryRoute{
					Provider: "github", PlatformHost: "github.com", Owner: "acme", Name: "widget",
				},
				ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 7,
			}, now)
			spec.Pull.BaseBranch = "release"
			spec.SourceVisible = !tc.hidden
			if tc.expired {
				spec.IssuedAt = now.Add(-db.WorkspaceLaunchSpecVisibilityLease)
				spec.SourceVisibleUntil = now
				spec.Pull.BaseBranch = "main"
			}
			if tc.missingBase {
				spec.Pull.BaseBranch = ""
			}
			require.NoError(database.CreateWorkspaceWithLaunchSpec(ctx, &db.Workspace{
				ID: "ws-target", RepoID: repoID, Platform: "github", PlatformHost: "github.com",
				RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypePullRequest,
				ItemNumber: 7, GitHeadRef: "feature", WorkspaceBranch: "feature", WorktreePath: work, Status: "ready",
			}, spec))
			pull, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
			require.NoError(err)
			require.Nil(pull)
			refreshes := 0
			resolver := stubLaunchSpecResolver{refresh: func(_ context.Context, current db.WorkspaceLaunchSpec) (db.WorkspaceLaunchSpec, error) {
				refreshes++
				assert.Equal(spec.Repository.Identity(), current.Repository.Identity())
				assert.Equal(7, current.ItemNumber)
				if tc.unavailable {
					return db.WorkspaceLaunchSpec{}, providerplane.ErrHubUnavailable
				}
				current.IssuedAt = now
				current.SourceVisibleUntil = now.Add(db.WorkspaceLaunchSpecVisibilityLease)
				current.Pull.BaseBranch = "release"
				return current, nil
			}}
			handler := New(Deps{
				DB: database, Workspaces: workspacetest.NewManager(t, database, t.TempDir()),
				LaunchSpecResolver: resolver, Now: func() time.Time { return now }, EnrichmentDisabled: true,
			})
			t.Cleanup(func() { require.NoError(handler.Shutdown(context.WithoutCancel(ctx))) })
			mux := http.NewServeMux()
			handler.Register(humago.NewWithPrefix(mux, "/api/v1", huma.DefaultConfig("workspace target test", "1")))
			for _, endpoint := range []string{"files?base=merge-target", "diff?base=merge-target", "commits"} {
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/workspaces/ws-target/"+endpoint, nil))
				require.Equal(tc.wantStatus, response.Code, "%s: %s", endpoint, response.Body.String())
				if tc.wantStatus != http.StatusOK {
					var problem httpapi.ProblemError
					require.NoError(json.Unmarshal(response.Body.Bytes(), &problem))
					assert.Equal(tc.wantCode, problem.Code)
					continue
				}
				if endpoint == "commits" {
					var commits commitsResponse
					require.NoError(json.Unmarshal(response.Body.Bytes(), &commits))
					require.Len(commits.Commits, 1)
					assert.Equal("workspace change", commits.Commits[0].Message)
				} else {
					var files filesResponse
					require.NoError(json.Unmarshal(response.Body.Bytes(), &files))
					require.Len(files.Files, 1)
					assert.Equal("feature.txt", files.Files[0].Path)
				}
			}
			if tc.expired && !tc.unavailable {
				assert.Equal(1, refreshes, "the refreshed facts must be reused")
			} else if !tc.expired {
				assert.Zero(refreshes)
			}
		})
	}
}

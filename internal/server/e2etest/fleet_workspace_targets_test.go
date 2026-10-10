package e2etest

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/platform"
)

func TestFleetWorkspaceTargetsE2E(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	fixture := newFederatedForgesFixture(t)
	hub, spoke := fixture.Hub, fixture.NodeA
	identity := verifiedRepoIdentity(db.GitHubRepoIdentity("github.com", "acme", "widget"))
	selection := generated.UpdateFleetWorkspaceTargetBody{
		"repository": platform.RepositoryIdentity{
			Provider: "github", PlatformHost: "github.com", Key: identity.Key,
		},
		"type": "pr", "number": 2, "hidden": false,
	}
	payload, err := json.Marshal(selection)
	require.NoError(t, err)
	targetURL := hub.HTTP.URL + "/api/v1/fleet/hosts/" + spoke.NodeID + "/workspaces/ws-spoke-a/targets"

	t.Run("peer list", func(t *testing.T) {
		response, body := fixture.browserRequest(t, http.MethodGet, targetURL, "", localBearer(hub))
		require.Equal(t, http.StatusOK, response.StatusCode, string(body))
		var targets workspaceapi.WorkspaceTargetsResponse
		require.NoError(t, json.Unmarshal(body, &targets))
		require.Len(t, targets.Targets, 1)
		assert.Equal(t, 1, targets.Targets[0].Number)
	})

	t.Run("peer update", func(t *testing.T) {
		response, body := fixture.browserRequest(t, http.MethodPut, targetURL, string(payload), localBearer(hub))
		require.Equal(t, http.StatusNoContent, response.StatusCode, string(body))
		stored, err := spoke.Database.ListWorkspaceTargets(t.Context(), "ws-spoke-a")
		require.NoError(t, err)
		require.Len(t, stored, 1)
		assert.Equal(t, 2, stored[0].ItemNumber)
		assert.False(t, stored[0].Hidden)
	})

	seedFederatedWorkspace(t, hub.Database, "ws-hub", 1, "local-targets")
	client, err := apiclient.NewWithHTTPClient(hub.HTTP.URL, fixture.HTTPClient)
	require.NoError(t, err)
	for _, target := range []struct{ name, host, workspace string }{
		{"self", "self", "ws-hub"},
		{"peer", spoke.NodeID, "ws-spoke-a"},
	} {
		t.Run("generated client/"+target.name, func(t *testing.T) {
			response, err := client.HTTP.UpdateFleetWorkspaceTargetWithResponse(t.Context(), &generated.UpdateFleetWorkspaceTargetRequestOptions{
				PathParams: &generated.UpdateFleetWorkspaceTargetPath{HostKey: target.host, ID: target.workspace},
				Body:       &selection,
			}, func(_ context.Context, request *http.Request) error {
				localBearer(hub)(request)
				return nil
			})
			require.NoError(t, err)
			require.Equal(t, http.StatusNoContent, response.StatusCode, string(response.Body))
		})
	}

	t.Run("read scope cannot update", func(t *testing.T) {
		credential, ok := hub.Credentials.Outbound(spoke.NodeID)
		require.True(t, ok)
		require.NoError(t, spoke.Credentials.StoreInbound(hub.NodeID, credential.Token, []federationauth.Scope{federationauth.ScopeWorkspaceRead}))
		response, body := fixture.browserRequest(t, http.MethodGet, targetURL, "", localBearer(hub))
		require.Equal(t, http.StatusOK, response.StatusCode, string(body))
		selection["hidden"] = true
		payload, err := json.Marshal(selection)
		require.NoError(t, err)
		response, body = fixture.browserRequest(t, http.MethodPut, targetURL, string(payload), localBearer(hub))
		require.Equal(t, http.StatusForbidden, response.StatusCode, string(body))
		problem := decodeFleetProblem(t, string(body))
		assert.Equal(t, "federationScopeDenied", problem.Details["reason"])
		stored, err := spoke.Database.ListWorkspaceTargets(t.Context(), "ws-spoke-a")
		require.NoError(t, err)
		require.Len(t, stored, 1)
		assert.False(t, stored[0].Hidden)
	})
}

func TestFleetWorkspaceTargetInUnobservedRepository(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert, require := assert.New(t), require.New(t)
	fixture := newFederatedForgesFixture(t)
	hub, spoke := fixture.Hub, fixture.NodeA
	identity := db.GitHubRepoIdentity("github.com", "acme", "other-project")
	identity.Key = platform.RepositoryIDKey(8675)
	stable := identity.ProviderIdentity()
	repoID, err := reposeed.Seed(t.Context(), hub.Database, identity)
	require.NoError(err)
	require.NoError(hub.Database.UpdateRepoProviderObservation(t.Context(), repoID, db.RepoProviderMetadata{CloneURL: "https://github.com/acme/other-project.git", DefaultBranch: "main"}, nil, nil))
	require.NoError(hub.Database.SetRepoHiddenFromUI(t.Context(), repoID, true))
	now := time.Now().UTC()
	_, err = hub.Database.UpsertIssue(t.Context(), &db.Issue{RepoID: repoID, PlatformID: 7, Number: 7, Title: "Related issue", State: "open", URL: "https://github.com/acme/other-project/issues/7", CreatedAt: now, UpdatedAt: now})
	require.NoError(err)
	local, err := spoke.Database.GetRepositoryByProviderID(t.Context(), stable)
	require.NoError(err)
	require.Nil(local)
	client, err := apiclient.NewWithHTTPClient(hub.HTTP.URL, fixture.HTTPClient)
	require.NoError(err)
	selection := generated.UpdateFleetWorkspaceTargetBody{"repository": stable, "type": "issue", "number": 7, "hidden": false}
	response, err := client.HTTP.UpdateFleetWorkspaceTargetWithResponse(t.Context(), &generated.UpdateFleetWorkspaceTargetRequestOptions{
		PathParams: &generated.UpdateFleetWorkspaceTargetPath{HostKey: spoke.NodeID, ID: "ws-spoke-a"}, Body: &selection,
	}, func(_ context.Context, request *http.Request) error { localBearer(hub)(request); return nil })
	require.NoError(err)
	require.Equal(http.StatusNoContent, response.StatusCode, string(response.Body))
	local, err = spoke.Database.GetRepositoryByProviderID(t.Context(), stable)
	require.NoError(err)
	require.NotNil(local)
	assert.Equal(identity.Key, local.Repository.Key)
	rows, err := spoke.Database.ListWorkspaceTargets(t.Context(), "ws-spoke-a")
	require.NoError(err)
	require.Len(rows, 1)
	assert.Equal(local.Repository.ID, rows[0].RepoID)
	_, body := fixture.browserRequest(t, http.MethodGet, hub.HTTP.URL+"/api/v1/fleet/hosts/"+spoke.NodeID+"/workspaces/ws-spoke-a/targets", "", localBearer(hub))
	var listed workspaceapi.WorkspaceTargetsResponse
	require.NoError(json.Unmarshal(body, &listed))
	require.Len(listed.Targets, 2)
	assert.Equal("Related issue", listed.Targets[0].Title)
	assert.Equal("open", listed.Targets[0].State)
	require.NotNil(listed.Targets[0].Repo)
	assert.Equal(stable, listed.Targets[0].Repo.Identity())
	item, err := spoke.Database.GetIssueByRepoIDAndNumber(t.Context(), local.Repository.ID, 7)
	require.NoError(err)
	assert.Nil(item, "the spoke observes the repository without replicating provider items")
}

func TestFleetWorkspaceTargetRefreshesRenamedRepository(t *testing.T) {
	for _, test := range []struct {
		name     string
		inactive bool
	}{
		{name: "active spoke repository"},
		{name: "inactive spoke repository", inactive: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			serverfake.RunParallelServerTest(t)
			assert, require := assert.New(t), require.New(t)
			ctx := t.Context()
			fixture := newFederatedForgesFixture(t)
			hub, spoke := fixture.Hub, fixture.NodeA
			identity := db.GitHubRepoIdentity("github.com", "acme", "old-project")
			identity.Key = platform.RepositoryIDKey(8675)
			stable := identity.ProviderIdentity()
			repoID, err := reposeed.Seed(ctx, hub.Database, identity)
			require.NoError(err)
			spokeRepoID, err := reposeed.Seed(ctx, spoke.Database, identity)
			require.NoError(err)
			if test.inactive {
				_, err = spoke.Database.DeactivateRepository(ctx, stable)
				require.NoError(err)
			}
			identity = db.GitHubRepoIdentity("github.com", "other-org", "renamed-project")
			identity.Key = stable.Key
			_, err = hub.Database.ObserveRepository(ctx, identity)
			require.NoError(err)
			require.NoError(hub.Database.UpdateRepoProviderObservation(ctx, repoID, db.RepoProviderMetadata{
				CloneURL: "https://github.com/other-org/renamed-project.git", DefaultBranch: "main",
			}, nil, nil))
			now := time.Now().UTC()
			_, err = hub.Database.UpsertIssue(ctx, &db.Issue{
				RepoID: repoID, PlatformID: 7, Number: 7, Title: "Renamed repository issue", State: "open",
				URL: "https://github.com/other-org/renamed-project/issues/7", CreatedAt: now, UpdatedAt: now,
			})
			require.NoError(err)
			client, err := apiclient.NewWithHTTPClient(hub.HTTP.URL, fixture.HTTPClient)
			require.NoError(err)
			selection := generated.UpdateFleetWorkspaceTargetBody{"repository": stable, "type": "issue", "number": 7, "hidden": false}
			response, err := client.HTTP.UpdateFleetWorkspaceTargetWithResponse(ctx, &generated.UpdateFleetWorkspaceTargetRequestOptions{
				PathParams: &generated.UpdateFleetWorkspaceTargetPath{HostKey: spoke.NodeID, ID: "ws-spoke-a"}, Body: &selection,
			}, func(_ context.Context, request *http.Request) error { localBearer(hub)(request); return nil })
			require.NoError(err)
			require.Equal(http.StatusNoContent, response.StatusCode, string(response.Body))
			local, err := spoke.Database.GetActiveRepoByProviderID(ctx, stable)
			require.NoError(err)
			require.NotNil(local)
			assert.Equal(spokeRepoID, local.ID)
			assert.Equal("other-org", local.Owner)
			assert.Equal("renamed-project", local.Name)
			rows, err := spoke.Database.ListWorkspaceTargets(ctx, "ws-spoke-a")
			require.NoError(err)
			require.Len(rows, 1)
			assert.Equal(spokeRepoID, rows[0].RepoID)
			assert.Equal("https://github.com/other-org/renamed-project/issues/7", rows[0].URL)
			listedResponse, body := fixture.browserRequest(t, http.MethodGet,
				hub.HTTP.URL+"/api/v1/fleet/hosts/"+spoke.NodeID+"/workspaces/ws-spoke-a/targets", "", localBearer(hub))
			require.Equal(http.StatusOK, listedResponse.StatusCode, string(body))
			var listed workspaceapi.WorkspaceTargetsResponse
			require.NoError(json.Unmarshal(body, &listed))
			require.Len(listed.Targets, 2)
			assert.Equal("Renamed repository issue", listed.Targets[0].Title)
			assert.False(listed.Targets[0].Unavailable)
		})
	}
}

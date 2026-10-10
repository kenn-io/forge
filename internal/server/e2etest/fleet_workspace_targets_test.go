package e2etest

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/workspaceapi"
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

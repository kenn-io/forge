package settingsservertest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/providerapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/server/routepolicy"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
)

const (
	proxyTestNodeID = "33333333333333333333333333333333"
	proxyTestHubID  = "44444444444444444444444444444444"
)

type providerPlaneClientFunc func(
	context.Context, federationauth.Scope, *http.Request,
) (*http.Response, error)

func (f providerPlaneClientFunc) Do(
	ctx context.Context, scope federationauth.Scope, request *http.Request,
) (*http.Response, error) {
	return f(ctx, scope, request)
}

type failingProviderResponseBody struct {
	data []byte
	err  error
}

func (b *failingProviderResponseBody) Read(p []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, b.err
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

func (*failingProviderResponseBody) Close() error { return nil }

func TestProviderWriteTransportFailureReportsUnknownMutationOutcome(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var dispatched atomic.Int64
	proxy := routepolicy.NewProviderProxy(providerPlaneClientFunc(func(
		_ context.Context, scope federationauth.Scope, request *http.Request,
	) (*http.Response, error) {
		dispatched.Add(1)
		assert.Equal(federationauth.ScopeProviderWrite, scope)
		assert.Equal(http.MethodPut, request.Method)
		return nil, providerplane.ErrHubUnavailable
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/settings", nil)
	proxy.ServeHTTP(recorder, request, routepolicy.ProviderRouteRule{
		Owner: routepolicy.ProviderHubOnly, PeerScope: federationauth.ScopeProviderWrite,
	})

	var problem httpapi.ProblemError
	require.NoError(json.NewDecoder(recorder.Body).Decode(&problem))
	assert.Equal(http.StatusBadGateway, recorder.Code)
	assert.Equal(httpapi.CodeMutationOutcomeUnknown, problem.Code)
	assert.Equal(int64(1), dispatched.Load())
}

func TestHubWorkspaceRefreshUsesProviderMutationBoundary(t *testing.T) {
	var gotScope federationauth.Scope
	var gotPath string
	source := &spokeapi.HubProviderSource{Client: providerPlaneClientFunc(func(
		_ context.Context, scope federationauth.Scope, request *http.Request,
	) (*http.Response, error) {
		gotScope = scope
		gotPath = request.URL.Path
		return nil, providerplane.ErrHubUnavailable
	})}

	_, _ = source.RefreshWorkspaceLaunchSpec(t.Context(), db.WorkspaceLaunchSpec{
		Repository: db.WorkspaceLaunchRepository{
			Provider: "github", PlatformHost: "github.com",
			Owner: "acme", Name: "widget",
		},
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 7,
		ItemKey: "7", GitHeadRef: "feature/seven",
	})

	assert.Equal(t, federationauth.ScopeProviderWrite, gotScope)
	assert.Equal(t, "/api/v1/federation/provider/workspace-launch-spec/refresh", gotPath)
}

func TestHubPullCandidatesUseProviderQualifiedRepositoryFilter(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	row := pullapi.MergeRequestResponse{
		Number: 7, State: db.MergeRequestStateOpen,
		Repo: httpapi.RepoRefResponse{
			Provider: "gitlab", PlatformHost: "gitlab.example.com",
			RepoPath: "group/project", Owner: "group", Name: "project",
		},
		RepoOwner: "group", RepoName: "project", PlatformHost: "gitlab.example.com",
	}
	encoded, err := json.Marshal([]pullapi.MergeRequestResponse{row})
	require.NoError(err)
	var gotRepo string
	source := &spokeapi.HubProviderSource{Client: providerPlaneClientFunc(func(
		_ context.Context, scope federationauth.Scope, request *http.Request,
	) (*http.Response, error) {
		assert.Equal(federationauth.ScopeProviderRead, scope)
		gotRepo = request.URL.Query().Get("repo")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(encoded)),
			Request:    request,
		}, nil
	})}

	candidates, err := source.ListOpenPullCandidates(t.Context(), workspace.Workspace{
		Platform: "gitlab", PlatformHost: "gitlab.example.com",
		RepoOwner: "group", RepoName: "project",
	})

	require.NoError(err)
	assert.Equal("gitlab|gitlab.example.com/group/project", gotRepo)
	require.Len(candidates, 1)
	assert.Equal(7, candidates[0].Number)
}

func TestHubListFiltersForwardUnassignedAndPullLabel(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	seen := make(map[string]bool)
	source := &spokeapi.HubProviderSource{Client: providerPlaneClientFunc(func(
		_ context.Context, scope federationauth.Scope, request *http.Request,
	) (*http.Response, error) {
		assert.Equal(federationauth.ScopeProviderRead, scope)
		seen[request.URL.Path] = request.URL.Query().Get("unassigned") == "true"
		if request.URL.Path == "/api/v1/pulls" {
			assert.Equal("priority: high", request.URL.Query().Get("label"))
		}
		body := "[]"
		if request.URL.Path == "/api/v1/activity" {
			body = "{}"
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(body)),
			Request:    request,
		}, nil
	})}

	_, err := source.ListPulls(t.Context(), pullapi.ListQuery{Unassigned: true, Label: "priority: high"})
	require.NoError(err)
	_, err = source.ListIssues(t.Context(), issueapi.ListQuery{Unassigned: true})
	require.NoError(err)
	_, err = source.ListActivity(t.Context(), &itemapi.ListActivityInput{Unassigned: true})
	require.NoError(err)

	assert.Equal(map[string]bool{
		"/api/v1/pulls": true, "/api/v1/issues": true, "/api/v1/activity": true,
	}, seen)
}

func TestHubUnassignedActivitySubjectFilterBatchesLargeSnapshots(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	const subjectCount = 11_000
	var requestCount int
	source := &spokeapi.HubProviderSource{Client: providerPlaneClientFunc(func(
		_ context.Context, scope federationauth.Scope, request *http.Request,
	) (*http.Response, error) {
		assert.Equal(federationauth.ScopeProviderRead, scope)
		assert.Equal("/api/v1/federation/provider/activity/unassigned-subjects/query", request.URL.Path)
		var body providerapi.FederationUnassignedActivitySubjectsRequest
		require.NoError(json.NewDecoder(request.Body).Decode(&body))
		assert.LessOrEqual(len(body.Subjects), 500)
		requestCount++
		encoded, err := json.Marshal(spokeapi.FederationUnassignedActivitySubjectsResponse(body))
		require.NoError(err)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(encoded)),
			Request:    request,
		}, nil
	})}

	subjects := make([]providerplane.ItemIdentity, subjectCount)
	for i := range subjects {
		subjects[i] = providerplane.ItemIdentity{
			Repository: providerplane.RepositoryIdentity{
				Provider: "github", PlatformHost: "github.com", PlatformRepoID: "repo-acme-widget",
			},
			ItemType: "pr", ItemNumber: i + 1,
		}
	}

	got, err := source.FilterUnassignedActivitySubjects(t.Context(), subjects)
	require.NoError(err)
	assert.Equal(22, requestCount)
	assert.Equal(subjects, got)
}

func TestNodeProviderFetchKeepsHubOrderAndAddsOnlyLocalWorkspace(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hubDB := dbtest.Open(t)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	pullID := seedPR(t, hubDB, "acme", "widget", 1,
		withSeedPRTimes(base, base, base))
	seedPR(t, hubDB, "acme", "widget", 2,
		withSeedPRTimes(base.Add(time.Minute), base.Add(time.Minute), base.Add(time.Minute)))
	require.NoError(hubDB.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: pullID, EventType: "issue_comment", Author: "reviewer",
		Body: "provider-owned activity", CreatedAt: base.Add(2 * time.Minute),
		DedupeKey: "federated-provider-activity",
	}}))
	hubRepo, err := hubDB.GetRepoByIdentity(
		t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(hubRepo)

	hubCredentials, err := federationauth.Open(t.TempDir() + "/hub-credentials.json")
	require.NoError(err)
	token, err := hubCredentials.MintInbound(
		proxyTestNodeID, federationauth.SpokeToHubScopes(),
	)
	require.NoError(err)
	hubServer := server.New(
		hubDB, nil, nil, "/", nil,
		server.ServerOptions{
			DaemonAccess: authapi.DaemonAccessOptions{
				Token: "hub-local-secret", RequireAPIAuth: true,
			},
			FederationSpokeID:                  proxyTestHubID,
			FederationCredentials:              hubCredentials,
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	t.Cleanup(func() { gracefulShutdown(t, hubServer) })
	hub := httptest.NewTLSServer(hubServer)
	t.Cleanup(hub.Close)

	nodeDB := dbtest.Open(t)
	seedPR(t, nodeDB, "acme", "widget", 1)
	seedWorkspace(
		t, nodeDB, "ws-spoke-only", "acme", "widget",
		db.WorkspaceItemTypePullRequest, 1,
	)
	spokeCredentials, err := federationauth.Open(t.TempDir() + "/spoke-credentials.json")
	require.NoError(err)
	require.NoError(spokeCredentials.StoreOutbound(
		proxyTestHubID, token, federationauth.SpokeToHubScopes(),
	))
	nodeConfig := &config.Config{
		Fleet: config.Fleet{
			Enabled: true, Role: config.FleetRoleSpoke,
			Hub: &config.FleetHub{
				NodeID: proxyTestHubID, BaseURL: hub.URL,
			},
		},
		Tmux: config.Tmux{Command: []string{"kenn-forge-no-such-tmux"}},
	}
	nodeServer := server.New(nodeDB, nil, nil, "/", nodeConfig, server.ServerOptions{
		FederationSpokeID:                  proxyTestNodeID,
		FederationSpokeActive:              true,
		FederationCredentials:              spokeCredentials,
		FederationHTTPClient:               hub.Client(),
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, nodeServer) })
	spoke := httptest.NewServer(nodeServer)
	t.Cleanup(spoke.Close)

	responseReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, spoke.URL+"/api/v1/pulls?state=open", nil)
	require.NoError(err)
	httpClient := spoke.Client()
	httpClient.Timeout = 5 * time.Second
	response, err := httpClient.Do(responseReq)
	require.NoError(err)
	defer response.Body.Close()
	require.Equal(http.StatusOK, response.StatusCode)
	var rows []pullapi.MergeRequestResponse
	require.NoError(json.NewDecoder(response.Body).Decode(&rows))
	require.Len(rows, 2)
	assert.Equal([]int{2, 1}, []int{rows[0].Number, rows[1].Number})
	assert.Nil(rows[0].Workspace)
	require.NotNil(rows[1].Workspace)
	assert.Equal("ws-spoke-only", rows[1].Workspace.ID)

	activityURL := spoke.URL + "/api/v1/activity?projection=events&item_types=pr&since=" +
		url.QueryEscape(base.Add(-time.Minute).Format(time.RFC3339))
	activityHTTPResponseReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, activityURL, nil)
	require.NoError(err)
	httpClient = spoke.Client()
	httpClient.Timeout = 5 * time.Second
	activityHTTPResponse, err := httpClient.Do(activityHTTPResponseReq)
	require.NoError(err)
	defer activityHTTPResponse.Body.Close()
	require.Equal(http.StatusOK, activityHTTPResponse.StatusCode)
	var activity itemapi.ActivityResponse
	require.NoError(json.NewDecoder(activityHTTPResponse.Body).Decode(&activity))
	require.NotEmpty(activity.Items)
	for _, item := range activity.Items {
		assert.NotEmpty(item.Cursor)
		if item.ItemNumber == 1 {
			require.NotNil(item.Workspace)
			assert.Equal("ws-spoke-only", item.Workspace.ID)
		} else {
			assert.Nil(item.Workspace)
		}
	}

	mcpPulls, err := nodeServer.MCPBackend().ListPulls(
		t.Context(), mcpserver.ItemListQuery{Repository: mcpserver.RepositoryIdentity{
			Provider: "github", PlatformHost: "github.com",
			PlatformRepoID: hubRepo.PlatformRepoID,
			Owner:          "acme", Name: "widget",
		}},
	)
	require.NoError(err)
	require.Len(mcpPulls, 2)
	assert.Equal([]int{2, 1}, []int{mcpPulls[0].Number, mcpPulls[1].Number})
	assert.Nil(mcpPulls[0].Workspace)
	require.NotNil(mcpPulls[1].Workspace)
	assert.Equal("ws-spoke-only", mcpPulls[1].Workspace.ID)
}

func TestProviderProxyReportsUnknownWriteOutcomeWhenResponseBufferingFails(t *testing.T) {
	tests := []struct {
		name  string
		body  io.ReadCloser
		limit int64
	}{
		{
			name: "truncated",
			body: &failingProviderResponseBody{
				data: []byte(`{"ok":`), err: io.ErrUnexpectedEOF,
			},
			limit: 32,
		},
		{
			name:  "oversized",
			body:  io.NopCloser(bytes.NewBufferString("four")),
			limit: 3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := providerPlaneClientFunc(func(
				context.Context, federationauth.Scope, *http.Request,
			) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK, Body: test.body,
				}, nil
			})
			proxy := routepolicy.NewProviderProxy(client)
			proxy.ResponseBodyLimit = test.limit
			recorder := httptest.NewRecorder()
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/provider-write", nil)

			proxy.ServeHTTP(recorder, request, routepolicy.ProviderRouteRule{
				PeerScope: federationauth.ScopeProviderWrite,
			})

			var problem httpapi.ProblemError
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &problem))
			assert.Equal(t, http.StatusBadGateway, recorder.Code)
			assert.Equal(t, httpapi.CodeMutationOutcomeUnknown, problem.Code)
		})
	}
}

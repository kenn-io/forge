package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func providerHandoffServerFixture(
	t *testing.T,
) (*httptest.Server, *federationauth.Store, db.ProviderStateRepository) {
	t.Helper()
	database := dbtest.Open(t)
	identity := verifiedGitHubRepoIdentity("github.com", "acme", "widget")
	repoID, err := database.UpsertRepo(t.Context(), identity)
	require.NoError(t, err)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 7007, Number: 7,
		Title: "Review me", Author: "alice", State: db.MergeRequestStateOpen,
		HeadBranch: "feature/seven", BaseBranch: "main", SnapshotRevision: 1,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(t, err)
	credentials, err := federationauth.Open(filepath.Join(t.TempDir(), "credentials.json"))
	require.NoError(t, err)
	srv := New(database, nil, nil, "/", nil, ServerOptions{
		FederationCredentials: credentials,
		DaemonAccess: DaemonAccessOptions{
			Token: "local-secret", RequireAPIAuth: true,
		},
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, credentials, db.ProviderStateRepository{
		Provider: identity.Platform, PlatformHost: identity.PlatformHost,
		PlatformRepoID: identity.PlatformRepoID, Owner: identity.Owner, Name: identity.Name,
	}
}

func postProviderHandoff(
	t *testing.T,
	ts *httptest.Server,
	token string,
	path string,
	body any,
) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, ts.URL+path, bytes.NewReader(raw),
	)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(providerplane.ProtocolVersionHeader, providerplane.ProtocolVersionHeaderValue())
	response, err := ts.Client().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func TestProviderStateHandoffHTTPRequiresHandoffScopeAndReturnsStableReceipt(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ts, credentials, repository := providerHandoffServerFixture(t)
	handoffToken, err := credentials.MintInbound(
		"fedcba9876543210fedcba9876543210", []federationauth.Scope{federationauth.ScopeProviderHandoff},
	)
	require.NoError(err)
	readToken, err := credentials.MintInbound(
		"11111111111111111111111111111111", []federationauth.Scope{federationauth.ScopeProviderRead},
	)
	require.NoError(err)
	payload := db.ProviderStateReviewDraftPayload{
		Repository: repository, PullNumber: 7, Body: "portable draft",
		Action: "comment", Comments: []db.ProviderStateReviewComment{},
	}

	denied := postProviderHandoff(
		t, ts, readToken,
		"/api/v1/federation/provider-state/review-drafts/import", payload,
	)
	assert.Equal(http.StatusForbidden, denied.StatusCode)

	first := postProviderHandoff(
		t, ts, handoffToken,
		"/api/v1/federation/provider-state/review-drafts/import", payload,
	)
	require.Equal(http.StatusOK, first.StatusCode)
	var firstResult db.ProviderStateImportResult
	require.NoError(json.NewDecoder(first.Body).Decode(&firstResult))
	assert.True(firstResult.Imported)
	assert.NotEmpty(firstResult.Receipt)

	retry := postProviderHandoff(
		t, ts, handoffToken,
		"/api/v1/federation/provider-state/review-drafts/import", payload,
	)
	require.Equal(http.StatusOK, retry.StatusCode)
	var retryResult db.ProviderStateImportResult
	require.NoError(json.NewDecoder(retry.Body).Decode(&retryResult))
	assert.False(retryResult.Imported)
	assert.Equal(firstResult.Receipt, retryResult.Receipt)
}

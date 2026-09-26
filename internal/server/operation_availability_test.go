package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/operationapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/tokenauth"
)

// splitTestDescriptor builds the github.com chain of a split host:
// an installed GitHub App candidate followed by the given user write
// candidate.
func splitTestDescriptor(writeCandidate tokenauth.Candidate) tokenauth.Descriptor {
	return tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: "github", Host: "github.com"},
		Candidates: []tokenauth.Candidate{
			{
				Kind:           tokenauth.SourceKindGitHubApp,
				Host:           "github.com",
				FilePath:       "/keys/app.pem",
				AppID:          7,
				InstallationID: 11,
			},
			writeCandidate,
		},
	}
}

func newSplitTestServerWithMock(
	t *testing.T, writeCandidate tokenauth.Candidate, mock *serverfake.MockGH,
) (*Server, *tokenauth.SourceSet, *ghclient.Syncer) {
	t.Helper()
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil,
		[]ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	set := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubApp: func(context.Context, tokenauth.Candidate) (string, time.Time, error) {
			return "ghs_probe", time.Now().Add(time.Hour), nil
		},
	})
	set.Upsert(splitTestDescriptor(writeCandidate))
	srv := New(database, syncer, nil, "/", nil, ServerOptions{TokenSources: set})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	_, err := database.UpsertRepo(
		t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(t, err)
	return srv, set, syncer
}

func TestAPIPullDetailOperationsSkipViewerLookupWhenSubmitReviewUnavailable(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	t.Setenv("SPLIT_WRITE_CRED_PAT", "")
	mock := &serverfake.MockGH{}
	srv, _, _ := newSplitTestServerWithMock(t, tokenauth.Candidate{
		Kind: tokenauth.SourceKindEnv, EnvName: "SPLIT_WRITE_CRED_PAT",
	}, mock)
	serverfake.SeedPR(t, srv.db, "acme", "widget", 1, serverfake.WithSeedPRAuthor("marius"))

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/github/acme/widget/1", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp pullapi.MergeRequestDetailResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	require.NotNil(resp.Repo.Operations)
	assert.Equal(operationapi.AvailabilityCodeMissingWriteCredential, resp.Repo.Operations.SubmitReview.Code)
	assert.Zero(mock.AuthenticatedViewerCalls,
		"viewer lookup must not run when the write credential already blocks review submission")
}

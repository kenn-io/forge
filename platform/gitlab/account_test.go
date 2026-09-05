package gitlab

import (
	"context"
	gitlabsdk "gitlab.com/gitlab-org/api/client-go/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func TestLookupAccountReadsExactImmutableIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, detail string
		kind         platform.AccountType
	}{
		{"human", `{"id":21,"username":"user-a","bot":false}`, platform.AccountUser},
		{"bot", `{"id":21,"username":"user-a","bot":true}`, platform.AccountBot},
		{"unknown", `{"id":21,"username":"user-a"}`, platform.AccountUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.RequestURI())
				switch r.URL.Path {
				case "/api/v4/users":
					assert.Equal("user-a", r.URL.Query().Get("username"))
					writeJSON(w, `[{"id":21,"username":"user-a"}]`)
				case "/api/v4/users/21":
					writeJSON(w, tc.detail)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := newTestClient(t, server.URL)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			account, err := client.LookupAccount(ctx, "user-a", platform.Budget{MaxRecords: 2, MaxNodes: 2, MaxBytes: 1024, MaxOutputBytes: 1024})
			require.NoError(t, err)
			assert.Equal("21", account.ID)
			assert.Equal(tc.kind, account.Type)
			assert.Len(paths, 2)
		})
	}
}

func TestMergeRequestKeepsAccountIdentityWithoutInventingType(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mr := NormalizeMergeRequest(testGitLabRepoRef(), &gitlabsdk.BasicMergeRequest{
		ID: 31, IID: 4,
		Author:    &gitlabsdk.BasicUser{ID: 21, Username: "author-a"},
		MergeUser: &gitlabsdk.BasicUser{ID: 22, Username: "merger-a"},
	}, nil)
	require.NotNil(mr.AuthorAccount)
	require.NotNil(mr.MergerAccount)
	assert.Equal("21", mr.AuthorAccount.ID)
	assert.Equal("22", mr.MergerAccount.ID)
	assert.Equal(platform.AccountUnknown, mr.AuthorAccount.Type)
}

func TestLookupAccountRefusesFuzzyAndAmbiguousResults(t *testing.T) {
	for _, body := range []string{
		`[{"id":21,"username":"user-other"}]`,
		`[{"id":21,"username":"user-a"},{"id":22,"username":"user-a"}]`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, body)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			_, err := newTestClient(t, server.URL).LookupAccount(ctx, "user-a", platform.Budget{MaxRecords: 2, MaxNodes: 2, MaxBytes: 1024, MaxOutputBytes: 1024})
			assert.Error(t, err)
		})
	}
}

func TestAccountLookupSharesItsByteBudgetAcrossRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/users" {
			writeJSON(w, `[{"id":21,"username":"user-a"}]`)
			return
		}
		writeJSON(w, `{"id":21,"username":"user-a","bot":false}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	_, err := newTestClient(t, server.URL).LookupAccount(ctx, "user-a", platform.Budget{MaxRecords: 2, MaxNodes: 2, MaxBytes: 45, MaxOutputBytes: 1024})
	require.ErrorIs(t, err, platform.ErrPageLimit)
}

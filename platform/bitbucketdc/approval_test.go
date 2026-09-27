package bitbucketdc_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

// The current DC pull-request API returns RestPullRequestParticipant with
// user.id and lastReviewedCommit, rather than an approval activity ID.
func TestApprovalIdentityUsesUserAndObservedCommit(t *testing.T) {
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	name := "reviewer"
	c := client(t, func(r *http.Request) (int, string) {
		switch r.Method {
		case http.MethodGet:
			assert.Equal(t, "/rest/api/latest/projects/PROJECT/repos/widgets/pull-requests/7", r.URL.Path)
			return 200, `{"id":7,"state":"OPEN","fromRef":{"latestCommit":"` + head + `"}}`
		case http.MethodPost:
			assert.Equal(t, "/rest/api/latest/projects/PROJECT/repos/widgets/pull-requests/7/approve", r.URL.Path)
			return 200, `{"user":{"id":101,"name":"` + name + `"},"approved":true,"status":"APPROVED","lastReviewedCommit":"` + head + `"}`
		default:
			require.FailNow(t, "unexpected request", "%s %s", r.Method, r.URL)
			return 500, `{}`
		}
	})
	first, err := c.ApproveMergeRequest(t.Context(), ref, 7, "", "")
	require.NoError(t, err)
	assert.Equal(t, "approval:101:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", first.DedupeKey)
	name = "renamed-reviewer"
	repeated, err := c.ApproveMergeRequest(t.Context(), ref, 7, "", head)
	require.NoError(t, err)
	assert.Equal(t, first.DedupeKey, repeated.DedupeKey)
	assert.Equal(t, first.PlatformExternalID, repeated.PlatformExternalID)
	assert.Equal(t, "renamed-reviewer", repeated.Author)
	head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	changed, err := c.ApproveMergeRequest(t.Context(), ref, 7, "", "")
	require.NoError(t, err)
	assert.Equal(t, "approval:101:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", changed.DedupeKey)
}

func TestApprovalDoesNotInventMissingIdentity(t *testing.T) {
	for _, body := range []string{
		`{"user":{"name":"reviewer"},"lastReviewedCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
		`{"user":{"id":101,"name":"reviewer"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			c := client(t, func(r *http.Request) (int, string) {
				if r.Method == http.MethodGet {
					return 200, `{"id":7,"state":"OPEN","fromRef":{"latestCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`
				}
				assert.Equal(t, http.MethodPost, r.Method)
				return 200, body
			})
			event, err := c.ApproveMergeRequest(t.Context(), ref, 7, "", "")
			require.ErrorIs(t, err, platform.ErrProviderContract)
			assert.Empty(t, event.DedupeKey)
		})
	}
}

func TestApprovalRevokesWhenObservedCommitDiffersFromExpected(t *testing.T) {
	revoked := false
	c := client(t, func(r *http.Request) (int, string) {
		switch r.Method {
		case http.MethodGet:
			return 200, `{"id":7,"state":"OPEN","fromRef":{"latestCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`
		case http.MethodPost:
			return 200, `{"user":{"id":101,"name":"reviewer"},"lastReviewedCommit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`
		case http.MethodDelete:
			revoked = true
			return 204, ""
		default:
			require.FailNow(t, "unexpected request", "%s", r.Method)
			return 500, `{}`
		}
	})
	event, err := c.ApproveMergeRequest(t.Context(), ref, 7, "", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.ErrorIs(t, err, platform.ErrStaleState)
	assert.True(t, revoked)
	assert.Empty(t, event.DedupeKey)
}

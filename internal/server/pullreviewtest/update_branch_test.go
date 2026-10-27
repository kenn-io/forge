package pullreviewtest

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/forge/platform"
)

func TestAPIUpdateBranch(t *testing.T) {
	for _, tc := range []struct {
		name, mergeable, head, base string
		providerErr                 error
		status, calls               int
	}{
		{name: "clean but behind", mergeable: "clean", head: "head-a", base: "base-a", status: http.StatusAccepted, calls: 1},
		{name: "accepted", mergeable: "behind", head: "head-a", status: http.StatusAccepted, calls: 1},
		{name: "stale head", mergeable: "behind", head: "head-b", status: http.StatusConflict},
		{name: "up to date", mergeable: "clean", head: "head-a", status: http.StatusConflict},
		{name: "missing pin", mergeable: "behind", status: http.StatusUnprocessableEntity},
		{name: "permission denied", mergeable: "behind", head: "head-a", providerErr: platform.ErrPermissionDenied, status: http.StatusForbidden, calls: 1},
		{name: "provider stale", mergeable: "behind", head: "head-a", providerErr: platform.ErrStaleState, status: http.StatusConflict, calls: 1},
		{name: "uncertain", mergeable: "behind", head: "head-a", providerErr: errors.New("response lost"), status: http.StatusBadGateway, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			calls := 0
			srv, database := servertest.SetupTestServerWithMock(t, &serverfake.MockGH{
				UpdatePullRequestBranchFn: func(_ context.Context, owner, repo string, number int, head string) error {
					calls++
					assert.Equal("acme", owner)
					assert.Equal("widget", repo)
					assert.Equal(7, number)
					assert.Equal("head-a", head)
					return tc.providerErr
				},
			})
			serverfake.SeedPR(t, database, "acme", "widget", 7, func(mr *db.MergeRequest) {
				mr.PlatformHeadSHA, mr.MergeableState, mr.PlatformBaseSHA = "head-a", tc.mergeable, tc.base
			})
			client := servertest.SetupTestClient(t, srv)
			resp, err := client.HTTP.UpdatePullBranchWithResponse(t.Context(), &generated.UpdatePullBranchRequestOptions{
				PathParams: &generated.UpdatePullBranchPath{Provider: "github", Owner: "acme", Name: "widget", Number: 7},
				Body:       &generated.UpdateBranchBody{ExpectedHeadSha: tc.head},
			})
			if tc.status == http.StatusAccepted {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NotNil(t, resp)
			assert.Equal(tc.status, resp.StatusCode)
			assert.Equal(tc.calls, calls)
			mr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 7)
			require.NoError(t, err)
			require.NotNil(t, mr)
			assert.Equal("head-a", mr.PlatformHeadSHA, "acceptance must not invent a completed update")
		})
	}
}

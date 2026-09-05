package githubapp_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
	"go.kenn.io/forge/platform"
)

func TestInstallationDeletionRequiresExpectedAppProof(t *testing.T) {
	for _, tc := range []struct {
		name      string
		appStatus int
		appID     int
		want      error
		paths     []string
	}{
		{"deleted", 200, 11, platform.ErrInstallationDeleted, []string{"/app", "/app/installations/21"}},
		{"wrong-app", 200, 12, platform.ErrProviderContract, []string{"/app"}},
		{"rejected-jwt", 401, 0, platform.ErrCredentialRejected, []string{"/app"}},
		{"transient", 503, 0, nil, []string{"/app"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			var paths []string
			hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				paths = append(paths, req.URL.Path)
				status, body := 404, `{"message":"not found"}`
				if req.URL.Path == "/app" {
					status, body = tc.appStatus, fmt.Sprintf(`{"id":%d}`, tc.appID)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})}
			_, err := githubapp.NewClient("github.com", hc).GetInstallation(appTestContext(t), "fixture-jwt", 11, 21, appTestMeter(t))
			require.Error(t, err)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			} else {
				require.NotErrorIs(t, err, platform.ErrInstallationDeleted)
			}
			assert.Equal(tc.paths, paths)
		})
	}
}

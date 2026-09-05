package githubapp_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
	"go.kenn.io/forge/platform"
)

func TestAppErrorsPreserveCredentialScopeAndTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		headers  http.Header
		expected error
	}{
		{"invalid-key", 401, nil, platform.ErrCredentialRejected},
		{"permission", 403, nil, platform.ErrPermissionDenied},
		{"secondary-rate-limit", 403, http.Header{"Retry-After": {"60"}}, platform.ErrRateLimited},
		{"rate-limit", 429, nil, platform.ErrRateLimited},
		{"server-failure", 503, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			client := githubapp.NewClient("github.com", &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: tc.headers, Body: io.NopCloser(strings.NewReader(`{"message":"fixture failure"}`)), Request: req}, nil
			})})
			_, err := client.GetApp(appTestContext(t), "fixture-jwt", appTestMeter(t))
			require.Error(err)
			if tc.expected != nil {
				require.ErrorIs(err, tc.expected)
			}
			assert.True(githubapp.IsStatus(err, tc.status))
			assert.NotErrorIs(err, platform.ErrInstallationDeleted)
		})
	}
	client := githubapp.NewClient("github.com", &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
	})})
	_, err := client.ListInstallationRepositoriesPage(appTestContext(t), "expired-token", 21, githubapp.PageQuery{Size: 1}, appTestMeter(t))
	require.ErrorIs(t, err, platform.ErrCredentialRejected)
	failure, ok := errors.AsType[*platform.Error](err)
	require.True(t, ok)
	assert.Equal(t, "installation_token", failure.Details["credential_scope"])
}

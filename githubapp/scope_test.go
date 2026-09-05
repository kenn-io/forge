package githubapp_test

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
	"go.kenn.io/forge/platform"
)

func TestMintRequiresExplicitScope(t *testing.T) {
	oversized := make([]int64, 501)
	for i := range oversized {
		oversized[i] = int64(i + 1)
	}
	for _, tc := range []struct {
		name    string
		scope   githubapp.TokenScope
		want    string
		invalid bool
	}{
		{"empty", githubapp.TokenScope{}, "", true},
		{"too-many-repositories", githubapp.TokenScope{RepositoryIDs: oversized}, "", true},
		{"both", githubapp.TokenScope{AllRepositories: true, RepositoryIDs: []int64{31}}, "", true},
		{"selected", githubapp.TokenScope{RepositoryIDs: []int64{32, 31}, Permissions: map[string]string{"contents": "read"}}, `{"permissions":{"contents":"read"},"repository_ids":[31,32]}`, false},
		{"all", githubapp.TokenScope{AllRepositories: true}, `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			calls := 0
			hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				var body any
				require.NoError(json.UnmarshalRead(req.Body, &body))
				encoded, err := json.Marshal(body, json.Deterministic(true))
				require.NoError(err)
				assert.Equal(tc.want, string(encoded))
				return &http.Response{StatusCode: 201, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"token":"fixture-token","expires_at":"2026-01-01T01:00:00Z"}`)), Request: req}, nil
			})}
			token, err := githubapp.NewClient("github.com", hc).CreateInstallationToken(appTestContext(t), "fixture-jwt", 21, tc.scope, appTestMeter(t))
			if tc.invalid {
				require.Error(err)
				assert.Zero(calls)
				return
			}
			require.NoError(err)
			assert.Equal("fixture-token", token.Token)
		})
	}
}

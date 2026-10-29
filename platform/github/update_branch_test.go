package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.kenn.io/forge/platform"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdatePullRequestBranch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		headers map[string]string
		want    error
	}{
		{name: "accepted", status: http.StatusAccepted},
		{name: "authentication", status: http.StatusUnauthorized, want: platform.ErrPermissionDenied},
		{name: "two factor", status: http.StatusUnauthorized, headers: map[string]string{"X-GitHub-OTP": "required"}, want: platform.ErrPermissionDenied},
		{name: "permission", status: http.StatusForbidden, want: platform.ErrPermissionDenied},
		{name: "not found", status: http.StatusNotFound, want: platform.ErrNotFound},
		{name: "conflict", status: http.StatusUnprocessableEntity, want: &platform.Error{Code: platform.ErrCodeConflict}},
		{name: "quota", status: http.StatusForbidden, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "2000000000"}, want: platform.ErrRateLimited},
		{name: "secondary quota", status: http.StatusForbidden, headers: map[string]string{"X-RateLimit-Remaining": "100", "X-RateLimit-Reset": "2000000000", "Retry-After": "60"}, want: platform.ErrRateLimited},
		{name: "throttled", status: http.StatusTooManyRequests, want: platform.ErrRateLimited},
		{name: "server error", status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(http.MethodPut, r.Method)
				assert.Equal("/api/v3/repos/acme/widget/pulls/7/update-branch", r.URL.Path)
				var body map[string]string
				assert.NoError(json.NewDecoder(r.Body).Decode(&body))
				assert.Equal(map[string]string{"expected_head_sha": "reviewed-head"}, body)
				for key, value := range tc.headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(tc.status)
				bodyText := `{"message":"Updating pull request branch."}`
				if tc.headers["Retry-After"] != "" {
					bodyText = `{"message":"secondary quota","documentation_url":"https://docs.github.com/rest/using-the-rest-api/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`
				}
				_, _ = w.Write([]byte(bodyText))
			}))
			defer upstream.Close()
			write, err := newEnterpriseGHClient(upstream.Client(), upstream.URL+"/api/v3/", upstream.URL+"/uploads/")
			require.NoError(t, err)
			clock := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
			client := &Client{ghWrite: write, now: func() time.Time { return clock }}
			err = client.UpdatePullRequestBranch(t.Context(), "acme", "widget", 7, "reviewed-head")
			if tc.status == http.StatusAccepted {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				if tc.want != nil {
					require.ErrorIs(t, err, tc.want)
				}
			}
			if tc.headers["Retry-After"] != "" {
				var problem *platform.Error
				require.ErrorAs(t, err, &problem)
				require.NotNil(t, problem.ResetAt)
				assert.Equal(clock.Add(time.Minute), *problem.ResetAt)
			}
			assert.Equal(1, calls, "never retry an asynchronous write")
		})
	}
}

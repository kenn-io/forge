package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdatePullRequestBranch(t *testing.T) {
	for _, status := range []int{http.StatusAccepted, http.StatusForbidden, http.StatusUnprocessableEntity, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			assert := assert.New(t)
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(http.MethodPut, r.Method)
				assert.Equal("/api/v3/repos/acme/widget/pulls/7/update-branch", r.URL.Path)
				var body map[string]string
				assert.NoError(json.NewDecoder(r.Body).Decode(&body))
				assert.Equal(map[string]string{"expected_head_sha": "reviewed-head"}, body)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"message":"Updating pull request branch."}`))
			}))
			defer upstream.Close()
			write, err := newEnterpriseGHClient(upstream.Client(), upstream.URL+"/api/v3/", upstream.URL+"/uploads/")
			require.NoError(t, err)
			client := &Client{ghWrite: write, now: time.Now}
			err = client.UpdatePullRequestBranch(t.Context(), "acme", "widget", 7, "reviewed-head")
			if status == http.StatusAccepted {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			assert.Equal(1, calls, "never retry an asynchronous write")
		})
	}
}

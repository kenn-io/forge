package github

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A required status context on a later page must still count as reported.
func TestGetCombinedStatusReadsEveryPage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var serverURL string
	client := requiredChecksClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/acme/widgets/commits/abc123/status" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		serverURL = "http://" + r.Host
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"state":"success","total_count":2,"statuses":[{"context":"deploy-gate","state":"success"}]}`))
			return
		}
		w.Header().Set("Link", `<`+serverURL+r.URL.Path+`?page=2>; rel="next"`)
		_, _ = w.Write([]byte(`{"state":"success","total_count":2,"statuses":[{"context":"build","state":"success"}]}`))
	})

	combined, err := client.GetCombinedStatus(t.Context(), "acme", "widgets", "abc123")

	require.NoError(err)
	assert.Equal("success", combined.GetState())
	contexts := make([]string, 0, len(combined.Statuses))
	for _, status := range combined.Statuses {
		contexts = append(contexts, status.GetContext())
	}
	assert.Equal([]string{"build", "deploy-gate"}, contexts)
}

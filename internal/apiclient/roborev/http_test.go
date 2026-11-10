package roborev

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPClientScopesCredential(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"", strings.Repeat("ab", 32)} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int64
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				want := ""
				if key != "" {
					want = "Bearer " + key
				}
				assert.Equal(t, want, r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusNoContent)
			}))
			defer daemon.Close()
			client := NewHTTPClient(daemon.URL, key)
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, daemon.URL, nil)
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer caller-secret")
			response, err := client.Do(request)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			assert.Equal(t, "Bearer caller-secret", request.Header.Get("Authorization"))
			for _, target := range []string{"http://other.example", daemon.URL + "0", strings.Replace(daemon.URL, "http:", "https:", 1)} {
				foreign := request.Clone(t.Context())
				parsed, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
				require.NoError(t, err)
				foreign.URL = parsed.URL
				_, err = client.Do(foreign)
				require.Error(t, err)
			}
			assert.Equal(t, int64(1), calls.Load())
		})
	}
}

func TestHTTPClientRefusesRedirects(t *testing.T) {
	t.Parallel()
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer daemon.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, daemon.URL, nil)
	require.NoError(t, err)
	response, err := NewHTTPClient(daemon.URL, strings.Repeat("cd", 32)).Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusTemporaryRedirect, response.StatusCode)
	assert.Zero(t, targetCalls.Load())
}

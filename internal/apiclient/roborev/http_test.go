package roborev

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func TestHTTPClientScopesCredential(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"", strings.Repeat("ab", 32)} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			const endpoint = "http://review.example:7373"
			client := NewHTTPClient(endpoint, key)
			transport, ok := client.Transport.(platform.AuthTransport)
			require.True(t, ok)
			var delivered []*http.Request
			transport.Base = platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				delivered = append(delivered, req)
				return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
			})
			client.Transport = transport

			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint, nil)
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer caller-secret")
			response, err := client.Do(request)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Len(t, delivered, 1)
			want := ""
			if key != "" {
				want = "Bearer " + key
			}
			assert := assert.New(t)
			assert.Equal(want, delivered[0].Header.Get("Authorization"))
			assert.Equal("Bearer caller-secret", request.Header.Get("Authorization"))
			for _, target := range []string{"https://review.example:7373", "http://other.example:7373", "http://review.example:7374"} {
				foreign, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
				require.NoError(t, err)
				foreign.Header = request.Header.Clone()
				_, err = client.Do(foreign)
				assert.Error(err, "target %s", target)
				require.Len(t, delivered, 1, "target %s", target)
			}
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

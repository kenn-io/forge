package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/kata"
)

func TestKataAPIClientExposesSnapshotEnrichmentMethods(t *testing.T) {
	t.Parallel()

	_ = kataAPIClient.ShowIssueByUIDWithResponse
	_ = kataAPIClient.PollEventsWithResponse
	_ = kataAPIClient.ReachableIssueGraphWithResponse
}

func TestNewKataAPIClientUsesResolvedTargetAuth(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	var authorization string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		assert.Equal(t, "/api/v1/instance", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instance_id":"test","version":"test"}`))
	}))
	t.Cleanup(daemon.Close)

	api, err := newKataAPIClient(t.Context(), kata.Daemon{
		ID:    "work",
		URL:   daemon.URL,
		Token: "secret-token",
	})
	require.NoError(err)

	response, err := api.InstanceWithResponse(t.Context())
	require.NoError(err)
	require.NotNil(response.JSON200)
	assert.Equal(t, "Bearer secret-token", authorization)
}

func TestKataAPIClientStreamEventsRawDoesNotBuffer(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	requestHeaders := make(chan http.Header, 1)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestHeaders <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("id: 1\ndata: {}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(daemon.Close)

	api, err := newKataAPIClient(t.Context(), kata.Daemon{ID: "work", URL: daemon.URL, Token: "secret-token"})
	require.NoError(err)

	type streamResult struct {
		response *http.Response
		err      error
	}
	result := make(chan streamResult, 1)
	streamCtx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		response, streamErr := api.StreamEventsRaw(streamCtx, nil)
		result <- streamResult{response: response, err: streamErr}
	}()

	select {
	case streamed := <-result:
		require.NoError(streamed.err)
		require.NotNil(streamed.response)
		require.NotNil(streamed.response.Body)
		require.NoError(streamed.response.Body.Close())
	case <-time.After(time.Second):
		require.Fail("StreamEventsRaw buffered the live response body")
	}

	headers := <-requestHeaders
	require.Equal("text/event-stream", headers.Get("Accept"))
	require.Equal("Bearer secret-token", headers.Get("Authorization"))
}

package generated

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamEventsReturnsLiveEventStream(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	started := make(chan struct{})
	release := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(": connected\n\n"))
		assert.NoError(err)
		w.(http.Flusher).Flush()
		close(started)
		<-release
	}))
	defer server.Close()
	defer close(release)

	client, err := NewClientWithResponses(server.URL)
	require.NoError(err)

	done := make(chan struct {
		resp *http.Response
		err  error
	}, 1)
	go func() {
		resp, err := client.StreamEvents(context.Background(), nil)
		done <- struct {
			resp *http.Response
			err  error
		}{resp: resp, err: err}
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		require.Fail("server did not start streaming")
	}

	select {
	case result := <-done:
		require.NoError(result.err)
		require.NotNil(result.resp)
		defer result.resp.Body.Close()

		assert.Equal(http.StatusOK, result.resp.StatusCode)

		line, err := bufio.NewReader(result.resp.Body).ReadString('\n')
		require.NoError(err)
		assert.Equal(": connected\n", line)
	case <-time.After(500 * time.Millisecond):
		require.Fail("StreamEvents did not return the live response body")
	}
}

func TestGeneratedClientOmitsStreamEventsResponseWrapper(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	source, err := os.ReadFile("client.gen.go")
	require.NoError(err)
	clientSource := string(source)

	assert.Contains(clientSource, "StreamEvents(ctx context.Context")
	assert.NotContains(clientSource, "StreamEventsWithResponse")
	assert.NotContains(clientSource, "ParseStreamEventsResponse")
	assert.NotContains(clientSource, "type StreamEventsResponse struct")
}

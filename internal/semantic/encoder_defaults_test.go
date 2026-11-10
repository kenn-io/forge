package semantic

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/embedclient"
	"go.kenn.io/kit/embedconfig"
	"go.kenn.io/kit/embedlocal"
)

func TestLocalEncoderDefaults(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		builtin                   bool
		batch, timeout, wantBatch int
		wantTimeout               time.Duration
	}{
		{"builtin", true, 0, 0, embedlocal.BatchItems, embedlocal.RequestTimeout},
		{"ollama", false, 0, 0, embedconfig.DefaultBatchItems, embedconfig.DefaultTimeout},
		{"builtin overrides", true, 12, 45, 12, 45 * time.Second},
		{"builtin batch override", true, 12, 0, 12, embedlocal.RequestTimeout},
		{"builtin timeout override", true, 0, 45, embedlocal.BatchItems, 45 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := embedconfig.Embedder{BaseURL: "http://127.0.0.1:1/v1", Model: "test", Dims: 2, BatchSize: tc.batch, TimeoutSeconds: tc.timeout}
			enc, err := NewEncoder(cfg, func(context.Context) (string, bool, error) { return cfg.BaseURL, tc.builtin, nil })
			require.NoError(t, err)
			options, _, err := enc.optionsForRequest(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tc.wantBatch, options.Batch.Items)
			assert.Equal(t, tc.wantTimeout, options.Transport.Timeout)
		})
	}
}

func TestConfiguredEncoderDefaults(t *testing.T) {
	cfg := embedconfig.Embedder{BaseURL: "http://127.0.0.1:1/v1", Model: "test", Dims: 2}
	enc, err := NewEncoder(cfg, nil)
	require.NoError(t, err)
	options, _, err := enc.optionsForRequest(t.Context())
	require.NoError(t, err)
	assert.Equal(t, embedconfig.DefaultBatchItems, options.Batch.Items)
	assert.Equal(t, embedconfig.DefaultTimeout, options.Transport.Timeout)
}

func TestEncoderSplitsTimedOutBatch(t *testing.T) {
	var sizes []int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		if !assert.NoError(t, json.UnmarshalRead(r.Body, &body)) {
			return
		}
		mu.Lock()
		sizes = append(sizes, len(body.Input))
		mu.Unlock()
		if len(body.Input) > 2 {
			<-r.Context().Done()
			return
		}
		data := make([]map[string]any, len(body.Input))
		for i := range body.Input {
			data[i] = map[string]any{"index": i, "embedding": []float32{1, 1}}
		}
		assert.NoError(t, json.MarshalWrite(w, map[string]any{"data": data}))
	}))
	t.Cleanup(server.Close)
	cfg := embedconfig.Embedder{BaseURL: server.URL, Model: "test", Dims: 2}
	enc, err := NewEncoder(cfg, nil)
	require.NoError(t, err)
	enc.options.HTTP = &http.Client{Timeout: 100 * time.Millisecond} //nolint:kennlint // deadlinetest: force an oversized request to time out
	enc.client, err = embedclient.New(enc.options)
	require.NoError(t, err)
	vectors, err := enc.documents(t.Context(), []string{"a", "b", "c", "d"})
	require.NoError(t, err)
	require.Len(t, vectors, 4)
	mu.Lock()
	assert.Equal(t, []int{4, 2, 2}, sizes)
	mu.Unlock()
}

func TestBuiltinEncoderSerializesRequests(t *testing.T) {
	started := make(chan struct{}, 2)
	finish := make(chan struct{})
	release := sync.OnceFunc(func() { close(finish) })
	defer release()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		assert.NoError(t, json.MarshalWrite(w, map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float32{1, 1}}}}))
	}))
	t.Cleanup(server.Close)
	cfg := embedconfig.Embedder{BaseURL: server.URL, Model: "test", Dims: 2}
	enc, err := NewEncoder(cfg, func(context.Context) (string, bool, error) { return server.URL, true, nil })
	require.NoError(t, err)
	first := make(chan error, 1)
	go func() { _, err := enc.query(t.Context(), "first"); first <- err }()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond) //nolint:kennlint // deadlinetest: the second request must wait for the first
	defer cancel()
	_, err = enc.query(ctx, "second")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, started)
	release()
	require.NoError(t, <-first)
}

func TestEncoderReportsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	cfg := embedconfig.Embedder{BaseURL: server.URL, Model: "test", Dims: 2}
	enc, err := NewEncoder(cfg, nil)
	require.NoError(t, err)
	enc.options.HTTP = &http.Client{Timeout: 100 * time.Millisecond} //nolint:kennlint // deadlinetest: test the request timeout message
	enc.options.Retry.MaxAttempts = 1
	enc.client, err = embedclient.New(enc.options)
	require.NoError(t, err)
	_, err = enc.documents(t.Context(), []string{"slow"})
	require.ErrorContains(t, err, "embed request timed out")
}

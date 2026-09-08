package collect_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork/collect"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/github"
)

// This is caller policy, deliberately not a production transport framework.
type wireLimit struct {
	base                http.RoundTripper
	attempts, bodyBytes int64
}

func (l *wireLimit) RoundTrip(r *http.Request) (*http.Response, error) {
	if l.attempts == 0 {
		return nil, platform.ErrLandingTransportLimit
	}
	l.attempts--
	resp, err := l.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &limitedBody{ReadCloser: resp.Body, remaining: l.bodyBytes}
	return resp, nil
}

type limitedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, platform.ErrLandingTransportLimit
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

type replacementCredential struct{ invalidated bool }

func (s *replacementCredential) Token(context.Context) (string, error) {
	if s.invalidated {
		return "replacement", nil
	}
	return "initial", nil
}
func (s *replacementCredential) Invalidate(string) { s.invalidated = true }

func connectedReader(t *testing.T, endpoint string, hc *http.Client) *github.Provider {
	t.Helper()
	c, err := github.NewClient(github.ClientConfig{Read: hc, Write: hc, Notifications: hc, Clock: time.Now, APIBase: endpoint + "/", UploadBase: endpoint + "/"})
	require.NoError(t, err)
	p, err := github.NewProvider(github.ProviderConfig{Host: "github.com", Client: c, Clock: time.Now})
	require.NoError(t, err)
	return p
}

func TestCollectorSeesLimitsBelowAuthentication(t *testing.T) {
	for _, mode := range []string{"retry", "body"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if mode == "retry" {
					w.WriteHeader(http.StatusUnauthorized)
				}
				_, _ = io.WriteString(w, `{"id":12,"name":"project","owner":{"login":"example"}}`)
			}))
			defer server.Close()
			source := &replacementCredential{}
			bodyBytes := int64(1000)
			if mode == "body" {
				bodyBytes = 10
			}
			transport := platform.AuthTransport{Source: source, SetHeader: platform.BearerAuthHeader, RetryOnUnauthorized: true, Base: &wireLimit{base: server.Client().Transport, attempts: 1, bodyBytes: bodyBytes}}
			reader := connectedReader(t, server.URL, &http.Client{Transport: transport})
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			got, err := collect.Collect(ctx, reader, route, query, limits)
			require.NoError(t, err)
			assert := assert.New(t)
			assert.False(got.Evidence.Inventory.Complete)
			assert.Equal("exhausted_limits", got.Evidence.Inventory.Reason)
			assert.Equal(int32(1), requests.Load())
			if mode == "retry" {
				assert.True(source.invalidated)
			}
		})
	}
}

func TestCanceledCollectionDoesNotBlockAnotherReader(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	reader := connectedReader(t, server.URL, server.Client())
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	type outcome struct {
		result collect.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() { r, err := collect.Collect(ctx, reader, route, query, limits); done <- outcome{r, err} }()
	select {
	case <-started:
	case <-ctx.Done():
		require.FailNow("provider request did not start")
	}
	otherCtx, otherCancel := context.WithTimeout(t.Context(), time.Minute)
	defer otherCancel()
	other, err := collect.Collect(otherCtx, mergedScript(t), route, query, limits)
	require.NoError(err)
	assert.True(other.Evidence.Inventory.Complete)
	cancel()
	select {
	case got := <-done:
		require.ErrorIs(got.err, context.Canceled)
		assert.Equal(collect.Result{}, got.result)
	case <-otherCtx.Done():
		require.FailNow("canceled collection did not exit")
	}
}

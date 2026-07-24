package kataapi

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/kata"
)

type closeTrackingTransport struct {
	closed atomic.Int32
}

func (*closeTrackingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("not used")
}

func (t *closeTrackingTransport) CloseIdleConnections() {
	t.closed.Add(1)
}

func TestKataShutdownClosesOwnedProxyTransportsOnce(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	transport := &closeTrackingTransport{}
	handler := New(Deps{NewHTTPTransport: func() http.RoundTripper { return transport }})
	handler.Start(context.Background())
	_, err := handler.kataProxyForDaemon(kata.Daemon{ID: "home", URL: "http://127.0.0.1:1"})
	require.NoError(err)

	require.NoError(handler.Shutdown(context.Background()))
	require.NoError(handler.Shutdown(context.Background()))
	assert.Equal(int32(1), transport.closed.Load())
}

func TestKataApplyConfigPublishesClonedSnapshot(t *testing.T) {
	assert := assert.New(t)
	initial := ConfigSnapshot{Repos: []config.Repo{{Owner: "acme", Name: "old"}}}
	handler := New(Deps{Config: initial})
	initial.Repos[0].Name = "mutated"
	assert.Equal("old", handler.configSnapshot().Repos[0].Name)

	next := ConfigSnapshot{KataProjects: []config.KataProjectRepoMapping{{ProjectUID: "project-2"}}}
	handler.ApplyConfig(next)
	next.KataProjects[0].ProjectUID = "mutated"
	assert.Equal("project-2", handler.configSnapshot().KataProjects[0].ProjectUID)
}

func TestKataParentCancellationStopsOwnedCaches(t *testing.T) {
	transport := &closeTrackingTransport{}
	handler := New(Deps{NewHTTPTransport: func() http.RoundTripper { return transport }})
	parent, cancel := context.WithCancel(context.Background())
	handler.Start(parent)
	_, err := handler.kataProxyForDaemon(kata.Daemon{ID: "home", URL: "http://127.0.0.1:1"})
	require.NoError(t, err)

	cancel()
	assert.Eventually(t, func() bool {
		return transport.closed.Load() == 1
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, handler.Shutdown(context.Background()))
}

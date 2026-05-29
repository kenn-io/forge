package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckSourceFlagsApplicationServeMuxRegistrations(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	src := `package server

import "net/http"

func routes() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls", nil)
	mux.Handle("/api/v1/repos", nil)
	http.HandleFunc("/api/v1/issues", nil)
	http.DefaultServeMux.Handle("/api/v1/stacks", nil)
	mux.HandleFunc("/metrics", nil)
}
`

	diagnostics, err := checkSource("internal/server/bad.go", src)

	require.NoError(err)
	require.Len(diagnostics, 5)
	assert.Equal("internal/server/bad.go", diagnostics[0].Path)
	assert.Equal(7, diagnostics[0].Line)
}

func TestCheckSourceAllowsHumaAdapterAndCurrentServerWrappers(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	src := `package server

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

func newServer(basePath string) {
	mux := http.NewServeMux()
	api.Adapter().Handle(&huma.Operation{}, nil)
	mux.HandleFunc("/", nil)
	outer := http.NewServeMux()
	outer.Handle("/healthz", mux)
	outer.Handle("/livez", mux)
	outer.Handle(basePath, http.StripPrefix("", mux))
}
`

	diagnostics, err := checkSource("/tmp/worktree/internal/server/server.go", src)

	require.NoError(err)
	assert.Empty(diagnostics)
}

func TestCheckSourceAllowsProfilerMuxRoutes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	src := `package profiler

import "net/http"

func NewHandler() {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", nil)
	mux.HandleFunc("/debug/pprof/cmdline", nil)
	mux.HandleFunc("/debug/pprof/profile", nil)
	mux.HandleFunc("/debug/pprof/symbol", nil)
	mux.HandleFunc("/debug/pprof/trace", nil)
	mux.HandleFunc("/api/v1/pulls", nil)
}
`

	diagnostics, err := checkSource(
		"internal/profiler/profiler.go", src,
	)

	require.NoError(err)
	require.Len(diagnostics, 1)
	assert.Equal(12, diagnostics[0].Line)
}

func TestCheckSourceIgnoresTestsAndUntrackedHttptestMuxes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	testSource := `package github

import "net/http"

func TestClient() {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", nil)
}
`
	nonServerSource := `package webhooktest

func newMux(mux interface{ HandleFunc(string, any) }) {
	mux.HandleFunc("/callback", nil)
}
`

	testDiagnostics, err := checkSource("internal/github/client_test.go", testSource)
	require.NoError(err)
	nonServerDiagnostics, err := checkSource("internal/github/httptest_helper.go", nonServerSource)
	require.NoError(err)

	assert.Empty(testDiagnostics)
	assert.Empty(nonServerDiagnostics)
}

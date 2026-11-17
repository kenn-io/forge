//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDevboxGitHubPullRequestActions(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "broker.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	require.NoError(t, err)
	var brokerCalls atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		brokerCalls.Add(1)
		assert.Equal(t, "/v1/credentials", r.URL.Path)
		var body bytes.Buffer
		_, err := body.ReadFrom(r.Body)
		assert.NoError(t, err)
		assert.JSONEq(t, `{"repository":"example/project","profile":"pr"}`, body.String())
		_, _ = fmt.Fprintf(w, `{"token":"test-token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nprintf '%s|%s|%s' \"$*\" \"$GH_TOKEN\" \"$GH_REPO\"\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, tc := range []struct {
		action string
		args   []string
		want   string
	}{
		{"close", []string{"pr", "close", "42", "--delete-branch"}, "pr close 42 --delete-branch --repo example/project"},
		{"reopen", []string{"pr", "reopen", "42"}, "pr reopen 42"},
		{"ready", []string{"pr", "ready", "42"}, "pr ready 42"},
		{"merge", []string{"pr", "merge", "42", "--delete-branch"}, "pr merge 42 --delete-branch --repo example/project"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			var output bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&output)
			err := runDevboxGitHub(t.Context(), cmd, socket, "example/project", tc.args)
			require.NoError(t, err)
			assert.Equal(t, tc.want+"|test-token|github.com/example/project", output.String())
		})
	}
	t.Run("reject unsupported verb", func(t *testing.T) {
		before := brokerCalls.Load()
		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)
		err := runDevboxGitHub(t.Context(), cmd, socket, "example/project", []string{"pr", "review", "42"})
		require.ErrorContains(t, err, "supported GitHub commands")
		assert.Equal(t, before, brokerCalls.Load())
		assert.Empty(t, output.String())
	})
}

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
	dir, err := os.MkdirTemp("/tmp", "forge-broker-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
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
		_, _ = fmt.Fprintf(w, `{"token":"test-token","app_slug":"example-app","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nif [ \"$2\" = view ]; then printf '%s|%s|%s' \"$*\" \"$GH_TOKEN\" \"$GH_REPO\" > \"$VIEW_LOG\"; printf '%s' \"$AUTHOR_JSON\"; exit \"${VIEW_EXIT:-0}\"; fi\nprintf '%s|%s|%s' \"$*\" \"$GH_TOKEN\" \"$GH_REPO\"\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VIEW_LOG", filepath.Join(dir, "view"))
	t.Setenv("AUTHOR_JSON", `{"url":"https://github.com/example/project/pull/42","author":{"login":"app/example-app","is_bot":true}}`)
	for _, tc := range []struct{ action, helpFlag string }{
		{"close", "--help"},
		{"close", "-h"},
	} {
		t.Run(tc.action+" help "+tc.helpFlag, func(t *testing.T) {
			before := brokerCalls.Load()
			var output bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&output)
			err := runDevboxGitHub(t.Context(), cmd, socket, "example/project", []string{"pr", tc.action, "42", tc.helpFlag})
			require.NoError(t, err)
			assert.Contains(t, output.String(), "pr "+tc.action+" 42 "+tc.helpFlag)
			assert.Equal(t, before, brokerCalls.Load())
		})
	}

	for _, tc := range []struct {
		action string
		args   []string
		want   string
	}{
		{"close", []string{"pr", "close", "feature"}, "pr close https://github.com/example/project/pull/42"},
		{"reopen", []string{"pr", "reopen", "42"}, "pr reopen https://github.com/example/project/pull/42"},
		{"ready", []string{"pr", "ready", "42"}, "pr ready https://github.com/example/project/pull/42"},
		{"comment starting with dash", []string{"pr", "reopen", "--comment=-message", "42"}, "pr reopen https://github.com/example/project/pull/42 --comment=-message"},
		{"ready current branch", []string{"pr", "ready"}, "pr ready https://github.com/example/project/pull/42"},
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
	t.Run("view uses selector and repository", func(t *testing.T) {
		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)
		require.NoError(t, runDevboxGitHub(t.Context(), cmd, socket, "example/project", []string{"pr", "ready", "-R", "example/other", "--", "--template=forged"}))
		view, err := os.ReadFile(filepath.Join(dir, "view"))
		require.NoError(t, err)
		assert.Equal(t, "pr view --json author,url --repo example/other -- --template=forged|test-token|github.com/example/project", string(view))
		assert.Equal(t, "pr ready https://github.com/example/project/pull/42 --repo=example/other|test-token|github.com/example/project", output.String())
	})
	for _, tc := range []struct{ action, json, want string }{
		{"close", `{"author":{"login":"alice","is_bot":false}}`, "alice"},
		{"reopen", `{"author":{"login":"alice","is_bot":false}}`, "alice"},
		{"ready", `{"author":{"login":"alice","is_bot":false}}`, "alice"},
		{"close", `{"author":{"login":"app/other-app","is_bot":true}}`, "app/other-app"},
		{"close", `invalid`, "read pull request author"},
	} {
		t.Run(tc.action+"/"+tc.want, func(t *testing.T) {
			t.Setenv("AUTHOR_JSON", tc.json)
			var output bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&output)
			err := runDevboxGitHub(t.Context(), cmd, socket, "example/project", []string{"pr", tc.action, "42"})
			require.ErrorContains(t, err, tc.want)
			assert.Empty(t, output.String())
		})
	}
	t.Run("failed view", func(t *testing.T) {
		t.Setenv("VIEW_EXIT", "1")
		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)
		require.ErrorContains(t, runDevboxGitHub(t.Context(), cmd, socket, "example/project", []string{"pr", "close", "42"}), "look up pull request author")
		assert.Empty(t, output.String())
	})
	for _, tc := range []struct{ action, flag string }{
		{"close", "--delete-branch"},
		{"ready", "--undo"},
	} {
		t.Run(tc.action+" rejects "+tc.flag, func(t *testing.T) {
			viewLog := filepath.Join(t.TempDir(), "view")
			t.Setenv("VIEW_LOG", viewLog)
			before := brokerCalls.Load()
			var output bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&output)
			err := runDevboxGitHub(t.Context(), cmd, socket, "example/project", []string{"pr", tc.action, "42", tc.flag})
			require.ErrorContains(t, err, tc.flag)
			assert.Equal(t, before, brokerCalls.Load())
			assert.NoFileExists(t, viewLog)
			assert.Empty(t, output.String())
		})
	}
	t.Run("help false still checks author", func(t *testing.T) {
		t.Setenv("AUTHOR_JSON", `{"author":{"login":"alice","is_bot":false}}`)
		before := brokerCalls.Load()
		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)
		err := runDevboxGitHub(t.Context(), cmd, socket, "example/project", []string{"pr", "close", "42", "--help=false"})
		require.ErrorContains(t, err, "alice")
		assert.Equal(t, before+1, brokerCalls.Load())
		assert.Empty(t, output.String())
	})
	t.Run("reject merge before broker request", func(t *testing.T) {
		before := brokerCalls.Load()
		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)
		err := runDevboxGitHub(t.Context(), cmd, socket, "example/project", []string{"pr", "merge", "42"})
		require.ErrorContains(t, err, "supported GitHub commands")
		assert.Equal(t, before, brokerCalls.Load())
		assert.Empty(t, output.String())
	})
}

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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nif [ \"$2\" = view ]; then printf '%s' \"$*\" > \"$VIEW_LOG\"; printf '%s' \"$AUTHOR_JSON\"; exit \"${VIEW_EXIT:-0}\"; fi\nprintf '%s|%s|%s' \"$*\" \"$GH_TOKEN\" \"$GH_REPO\"\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VIEW_LOG", filepath.Join(dir, "view"))
	t.Setenv("AUTHOR_JSON", `{"author":{"login":"app/example-app","is_bot":true}}`)
	for _, tc := range []struct{ action, helpFlag string }{
		{"close", "--help"},
		{"reopen", "--help"},
		{"ready", "--help"},
		{"merge", "--help"},
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
		{"close", []string{"pr", "close", "42"}, "pr close 42"},
		{"reopen", []string{"pr", "reopen", "42"}, "pr reopen 42"},
		{"ready", []string{"pr", "ready", "42"}, "pr ready 42"},
		{"merge", []string{"pr", "merge", "42"}, "pr merge 42"},
		{"reopen with comment", []string{"pr", "reopen", "42", "-c", "message"}, "pr reopen 42 -c message"},
		{"ready undo", []string{"pr", "ready", "42", "--undo"}, "pr ready 42 --undo"},
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
		cmd := &cobra.Command{}
		cmd.SetOut(&bytes.Buffer{})
		require.NoError(t, runDevboxGitHub(t.Context(), cmd, socket, "example/project", []string{"pr", "ready", "42", "-R", "example/other"}))
		view, err := os.ReadFile(filepath.Join(dir, "view"))
		require.NoError(t, err)
		assert.Equal(t, "pr view 42 --repo example/other --json author", string(view))
	})
	for _, action := range []string{"close", "reopen", "ready", "merge"} {
		for _, author := range []struct{ json, want string }{
			{`{"author":{"login":"alice","is_bot":false}}`, "alice"},
			{`{"author":{"login":"app/other-app","is_bot":true}}`, "app/other-app"},
			{`invalid`, "read pull request author"},
		} {
			t.Run(action+"/"+author.want, func(t *testing.T) {
				t.Setenv("AUTHOR_JSON", author.json)
				var output bytes.Buffer
				cmd := &cobra.Command{}
				cmd.SetOut(&output)
				err := runDevboxGitHub(t.Context(), cmd, socket, "example/project", []string{"pr", action, "42"})
				require.ErrorContains(t, err, author.want)
				assert.Empty(t, output.String())
			})
		}
	}
	t.Run("failed view", func(t *testing.T) {
		t.Setenv("VIEW_EXIT", "1")
		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)
		require.Error(t, runDevboxGitHub(t.Context(), cmd, socket, "example/project", []string{"pr", "close", "42"}))
		assert.Empty(t, output.String())
	})
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
	t.Run("invalid action flag", func(t *testing.T) {
		var output bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&output)
		require.Error(t, runDevboxGitHub(t.Context(), cmd, socket, "example/project", []string{"pr", "close", "42", "--unknown"}))
		assert.Empty(t, output.String())
	})
	for _, action := range []string{"reopen", "ready"} {
		t.Run(action+" rejects delete-branch", func(t *testing.T) {
			var output bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&output)
			err := runDevboxGitHub(t.Context(), cmd, socket, "example/project", []string{"pr", action, "42", "--delete-branch"})
			require.ErrorContains(t, err, "unknown flag")
			assert.Empty(t, output.String())
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
	})
}

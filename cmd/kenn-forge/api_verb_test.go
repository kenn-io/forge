package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIVerbScopesPaths(t *testing.T) {
	for _, tt := range []struct {
		name     string
		basePath string
		path     string
		want     string
	}{
		{name: "unprefixed", path: "/kata/daemons", want: "/api/v1/kata/daemons"},
		{name: "relative", path: "kata/daemons", want: "/api/v1/kata/daemons"},
		{name: "prefixed", path: "/api/v1/kata/daemons", want: "/api/v1/kata/daemons"},
		{
			name: "base path, query, and encoded owner", basePath: "/forge",
			path: "/host/gitlab.example.com/pulls/gl/Group%2FSubGroup/project/7?limit=3&search=one%20two",
			want: "/forge/api/v1/host/gitlab.example.com/pulls/gl/Group%2FSubGroup/project/7?limit=3&search=one+two",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, err := io.WriteString(w, r.RequestURI)
				assert.NoError(t, err)
			}))
			t.Cleanup(daemon.Close)
			configPath := archiveCLITestConfig(t, daemon.URL, tt.basePath, "")
			var stdout bytes.Buffer
			cmd := newRootCommand(cliOptions{Stdout: &stdout, Stderr: io.Discard})
			cmd.SetArgs([]string{"api", "--config", configPath, "GET", tt.path})

			require.NoError(t, cmd.Execute())
			assert.Equal(t, tt.want, stdout.String())
		})
	}
}

package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/mcpserver"
)

func TestMCPCommandIsPublicAndOwnsItsFlags(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := newRootCommand(cliOptions{MCPRunner: func(
		context.Context, mcpserver.Options, io.Reader, io.Writer,
	) error {
		return nil
	}})

	cmd, _, err := root.Find([]string{"mcp"})
	require.NoError(err)
	require.NotNil(cmd)
	assert.False(cmd.Hidden)

	var flags []string
	cmd.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
		flags = append(flags, flag.Name)
	})
	assert.ElementsMatch([]string{
		"addr", "config", "daemon-timeout", "http-token-env", "transport",
	}, flags)
}

func TestMCPCommandPassesEveryFlagAndCLIStreamToRunner(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	stdin := strings.NewReader("client input")
	var stdout bytes.Buffer
	var received mcpserver.Options
	var receivedIn io.Reader
	var receivedOut io.Writer
	root := newRootCommand(cliOptions{
		Stdin: stdin, Stdout: &stdout, Stderr: io.Discard,
		MCPRunner: func(
			_ context.Context,
			opts mcpserver.Options,
			in io.Reader,
			out io.Writer,
		) error {
			received, receivedIn, receivedOut = opts, in, out
			return nil
		},
	})
	root.SetArgs([]string{
		"mcp", "--config", "/tmp/forge.toml", "--transport", "http",
		"--addr", "127.0.0.1:8092", "--http-token-env", "KENN_FORGE_MCP_TOKEN",
		"--daemon-timeout", "3s",
	})

	require.NoError(root.Execute())
	assert.Equal("/tmp/forge.toml", received.ConfigPath)
	assert.Equal("http", received.Transport)
	assert.Equal("127.0.0.1:8092", received.Addr)
	assert.Equal("KENN_FORGE_MCP_TOKEN", received.HTTPTokenEnv)
	assert.Equal(3*time.Second, received.DaemonTimeout)
	assert.Same(stdin, receivedIn)
	assert.Same(&stdout, receivedOut)
}

func TestMCPCommandDefaultsToStdioAndRejectsUnknownTransport(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var received mcpserver.Options
	runs := 0
	runner := func(
		_ context.Context,
		opts mcpserver.Options,
		_ io.Reader,
		_ io.Writer,
	) error {
		runs++
		received = opts
		return nil
	}

	cmd := newMCPCommand(runner, strings.NewReader(""), io.Discard)
	cmd.SetArgs(nil)
	require.NoError(cmd.Execute())
	assert.Equal("stdio", received.Transport)
	assert.Equal("127.0.0.1:0", received.Addr)
	assert.Equal(10*time.Second, received.DaemonTimeout)

	cmd = newMCPCommand(runner, strings.NewReader(""), io.Discard)
	cmd.SetArgs([]string{"--transport", "invalid"})
	err := cmd.Execute()
	require.Error(err)
	assert.Contains(err.Error(), "unsupported transport")
	assert.Equal(1, runs)
}

func TestMCPCommandRejectsFlagsThatCannotAffectExecution(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "stdio address",
			args: []string{"--addr", "127.0.0.1:8092"},
			want: "--addr requires --transport=http",
		},
		{
			name: "stdio token environment",
			args: []string{"--http-token-env", "KENN_FORGE_MCP_TOKEN"},
			want: "--http-token-env requires --transport=http",
		},
		{
			name: "zero daemon timeout",
			args: []string{"--daemon-timeout", "0s"},
			want: "--daemon-timeout must be positive",
		},
		{
			name: "negative daemon timeout",
			args: []string{"--daemon-timeout=-1s"},
			want: "--daemon-timeout must be positive",
		},
		{
			name: "http implicit ephemeral address",
			args: []string{"--transport", "http"},
			want: "--addr with an explicit nonzero port is required",
		},
		{
			name: "http explicit ephemeral address",
			args: []string{"--transport", "http", "--addr", "127.0.0.1:0"},
			want: "--addr with an explicit nonzero port is required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runs := 0
			cmd := newMCPCommand(func(
				context.Context, mcpserver.Options, io.Reader, io.Writer,
			) error {
				runs++
				return nil
			}, strings.NewReader(""), io.Discard)
			cmd.SetArgs(tc.args)

			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Zero(t, runs)
		})
	}
}

package main

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingDaemonRunner struct {
	operation  string
	configPath string
	asJSON     bool
}

func (r *recordingDaemonRunner) Start(
	_ context.Context,
	configPath string,
	_ io.Writer,
) error {
	r.operation, r.configPath = "start", configPath
	return nil
}

func (r *recordingDaemonRunner) Status(
	_ context.Context,
	configPath string,
	asJSON bool,
	_ io.Writer,
) error {
	r.operation, r.configPath, r.asJSON = "status", configPath, asJSON
	return nil
}

func (r *recordingDaemonRunner) Stop(
	_ context.Context,
	configPath string,
	_ io.Writer,
) error {
	r.operation, r.configPath = "stop", configPath
	return nil
}

func (r *recordingDaemonRunner) Restart(
	_ context.Context,
	configPath string,
	_ io.Writer,
) error {
	r.operation, r.configPath = "restart", configPath
	return nil
}

func TestDaemonCommandRoutesLifecycleOperations(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantOperation string
		wantJSON      bool
	}{
		{
			name: "start", args: []string{
				"start", "--config", "/tmp/forge.toml",
			}, wantOperation: "start",
		},
		{
			name: "status json", args: []string{
				"status", "--json", "--config", "/tmp/forge.toml",
			}, wantOperation: "status", wantJSON: true,
		},
		{
			name: "stop", args: []string{
				"stop", "--config", "/tmp/forge.toml",
			}, wantOperation: "stop",
		},
		{
			name: "restart", args: []string{
				"restart", "--config", "/tmp/forge.toml",
			}, wantOperation: "restart",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			runner := &recordingDaemonRunner{}
			cmd := newDaemonCommand(runner)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(tt.args)

			require.NoError(cmd.Execute())
			assert.Equal(tt.wantOperation, runner.operation)
			assert.Equal("/tmp/forge.toml", runner.configPath)
			assert.Equal(tt.wantJSON, runner.asJSON)
		})
	}
}

func TestDaemonCommandOwnsOnlyLifecycleFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "start rejects status flag", args: []string{"start", "--json"}},
		{name: "stop rejects serve flag", args: []string{"stop", "--pprof-addr", "127.0.0.1:0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingDaemonRunner{}
			cmd := newDaemonCommand(runner)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(tt.args)

			require.Error(t, cmd.Execute())
			assert.Empty(t, runner.operation)
		})
	}

	assert := assert.New(t)
	require := require.New(t)
	runner := &recordingDaemonRunner{}
	cmd := newDaemonCommand(runner)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--config", "/tmp/forge.toml", "status", "--json"})

	require.NoError(cmd.Execute())
	assert.Equal("status", runner.operation)
	assert.Equal("/tmp/forge.toml", runner.configPath)
	assert.True(runner.asJSON)
}

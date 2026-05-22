package workspacetest

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

func TestWorkspaceRuntimeTargetsE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	resp, err := fixture.client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.LaunchTargets)
	require.NotNil(resp.JSON200.Sessions)
	assert.NotEmpty(*resp.JSON200.LaunchTargets)
	assert.Empty(*resp.JSON200.Sessions)
	assert.Nil(resp.JSON200.ShellSession)
	assertWorkspaceRuntimeTarget(
		t, *resp.JSON200.LaunchTargets, "plain_shell",
	)
	assertWorkspaceRuntimeTarget(t, *resp.JSON200.LaunchTargets, "tmux")
}

func TestWorkspaceRuntimeTargetsUseConfiguredTmuxCommandE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	tmuxPath := filepath.Join(dir, "tmux-wrapper")
	require.NoError(os.WriteFile(
		tmuxPath,
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	))
	cfg := &config.Config{Tmux: config.Tmux{
		Command: []string{tmuxPath, "--scope", "tmux"},
	}}
	fixture := setupWorkspaceServerFixture(t, cfg)
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	resp, err := fixture.client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.LaunchTargets)

	var tmux generated.LaunchTarget
	for _, target := range *resp.JSON200.LaunchTargets {
		if target.Key == "tmux" {
			tmux = target
			break
		}
	}
	assert.Equal([]string{tmuxPath, "--scope", "tmux"}, *tmux.Command)
	assert.True(tmux.Available)
}

func TestWorkspaceRuntimeLaunchUnavailableTargetE2E(t *testing.T) {
	disabled := false
	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "disabled",
		Label:   "Disabled",
		Enabled: &disabled,
	}}}
	fixture := setupWorkspaceServerFixture(t, cfg)
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	resp, err := fixture.client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "disabled",
		},
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode())
	require.Contains(t, string(resp.Body), "not available")
}

func TestWorkspaceRuntimeLaunchPlainShellUsesShellSessionE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	resp, err := fixture.client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "plain_shell",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	shell := resp.JSON200
	assert.Equal("plain_shell", shell.TargetKey)
	assert.Equal(string(localruntime.LaunchTargetPlainShell), shell.Kind)
	assert.Equal(string(localruntime.SessionStatusRunning), shell.Status)

	getResp, err := fixture.client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, getResp.StatusCode())
	require.NotNil(getResp.JSON200)
	require.NotNil(getResp.JSON200.ShellSession)
	require.NotNil(getResp.JSON200.Sessions)
	assert.Equal(shell.Key, getResp.JSON200.ShellSession.Key)
	assert.Empty(*getResp.JSON200.Sessions)
}

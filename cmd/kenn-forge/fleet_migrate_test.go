package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/runtimelock"
)

func TestFleetMigrateProtocolHelpDoesNotLoadConfig(t *testing.T) {
	var output bytes.Buffer
	command := newFleetCommand(fleetCLIOptions{Stdout: &output})
	command.SetOut(&output)
	command.SetArgs([]string{"migrate-protocol", "--config", filepath.Join(t.TempDir(), "missing", "config.toml"), "--help"})
	require.NoError(t, command.Execute())
}

func TestFleetMigrateProtocolUnenrolledNeedsNoDatabaseAndRefusesRunningDaemon(t *testing.T) {
	require := require.New(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")
	require.NoError(os.WriteFile(path, []byte("data_dir = "+strconv.Quote(directory)+"\n"), 0o600))
	cfg, err := config.Load(path)
	require.NoError(err)
	cfg.DataDir = directory
	require.NoError(cfg.Save(path))
	require.NoError(migrateFleetProtocol(t.Context(), path))
	_, err = os.Stat(cfg.DBPath())
	require.ErrorIs(err, os.ErrNotExist)
	_, err = federation.Open(federation.DefaultStorePath(directory), federation.StoreOptions{})
	require.NoError(err)
	require.NoError(migrateFleetProtocol(t.Context(), path))
	lock, err := runtimelock.Acquire(directory)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(lock.Release()) })
	err = migrateFleetProtocol(t.Context(), path)
	var collision *runtimelock.CollisionError
	assert.ErrorAs(t, err, &collision)
}

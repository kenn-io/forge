package runtimelock

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPathsAreUnderDataDir(t *testing.T) {
	dir := t.TempDir()

	require.Equal(t, filepath.Join(dir, "middleman.lock"), LockPath(dir))
	require.Equal(t, filepath.Join(dir, "middleman.run.json"), MetadataPath(dir))
	require.Equal(t, filepath.Join(dir, ".middleman.run.json.tmp"), metadataTmpPath(dir))
}

func TestMetadataAtomicWriteRoundTrip(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	meta := Metadata{
		PID:        4242,
		Host:       "127.0.0.1",
		Port:       8091,
		ListenAddr: "127.0.0.1:8091",
		StartedAt:  "2026-05-19T10:30:00Z",
		Version:    "1.2.3",
		Commit:     "abcd1234",
	}

	require.NoError(writeMetadata(dir, meta))

	got, err := readMetadata(dir)
	require.NoError(err)
	require.Equal(meta, got)
}

func TestMetadataWriteOverwritesStaleTempFile(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	// Simulate a previous run that crashed mid-rename: a leftover
	// temp file with garbage that we want overwritten.
	require.NoError(os.WriteFile(metadataTmpPath(dir), []byte("garbage"), 0o600))

	meta := Metadata{PID: 1, ListenAddr: "127.0.0.1:1"}
	require.NoError(writeMetadata(dir, meta))

	got, err := readMetadata(dir)
	require.NoError(err)
	require.Equal(meta, got)

	// The temp file from the simulated crash is gone.
	_, err = os.Stat(metadataTmpPath(dir))
	require.True(os.IsNotExist(err), "temp file should be cleaned up, got %v", err)
}

func TestReadMetadataMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := readMetadata(dir)
	require.ErrorIs(t, err, errMetadataMissing)
}

func TestReadMetadataCorrupt(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	require.NoError(os.WriteFile(MetadataPath(dir), []byte("not json"), 0o600))

	_, err := readMetadata(dir)
	require.Error(err)
	require.NotErrorIs(err, errMetadataMissing)
}

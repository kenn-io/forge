package runtimelock

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadOnEmptyDataDirReportsNotRunning(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	st, err := Read(dir)
	require.NoError(err)
	require.False(st.Running)
	require.Equal(dir, st.DataDir)
	require.Equal(LockPath(dir), st.LockPath)
	require.Nil(st.Metadata)
	require.Empty(st.MetadataUnavailable)
}

func TestReadWhileLockHeldWithMetadata(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h, err := Acquire(dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h.Release() })

	meta := Metadata{
		PID:        4242,
		Host:       "127.0.0.1",
		Port:       8091,
		ListenAddr: "127.0.0.1:8091",
		StartedAt:  "2026-05-19T10:30:00Z",
		Version:    "1.2.3",
		Commit:     "abcd1234",
	}
	require.NoError(h.WriteMetadata(meta))

	st, err := Read(dir)
	require.NoError(err)
	require.True(st.Running)
	require.NotNil(st.Metadata)
	require.Equal(meta, *st.Metadata)
	require.Empty(st.MetadataUnavailable)
}

func TestReadWhileLockHeldWithoutMetadata(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()

	h, err := Acquire(dir)
	require.NoError(err)
	t.Cleanup(func() { _ = h.Release() })

	st, err := Read(dir)
	require.NoError(err)
	require.True(st.Running)
	require.Nil(st.Metadata)
	require.Equal(ReasonMetadataMissing, st.MetadataUnavailable)
}

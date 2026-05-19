package runtimelock

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollisionErrorImplementsError(t *testing.T) {
	require := require.New(t)

	meta := Metadata{PID: 99, ListenAddr: "127.0.0.1:9999"}
	cerr := &CollisionError{
		DataDir:  "/tmp/dd",
		LockPath: "/tmp/dd/middleman.lock",
		Metadata: &meta,
	}

	require.Contains(cerr.Error(), "/tmp/dd")
	require.Contains(cerr.Error(), "already running")

	// errors.As pattern (this is the exact idiom Acquire callers use).
	var asTarget *CollisionError
	require.ErrorAs(error(cerr), &asTarget)
	require.Equal(cerr, asTarget)

	// A plain non-collision error must not match.
	asTarget = nil
	require.NotErrorAs(errors.New("unrelated"), &asTarget)
	require.Nil(asTarget)
}

func TestCollisionErrorMetadataUnavailable(t *testing.T) {
	require := require.New(t)

	cerr := &CollisionError{
		DataDir:             "/tmp/dd",
		LockPath:            "/tmp/dd/middleman.lock",
		Metadata:            nil,
		MetadataUnavailable: ReasonMetadataMissing,
	}

	require.Nil(cerr.Metadata)
	require.Equal(ReasonMetadataMissing, cerr.MetadataUnavailable)
}

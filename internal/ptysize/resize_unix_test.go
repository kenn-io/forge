//go:build !windows

package ptysize

import (
	"testing"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResizeConcurrentClose(t *testing.T) {
	master, slave, err := pty.Open()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = master.Close()
		_ = slave.Close()
	})
	geometry := Geometry{Cols: 100, Rows: 40, PixelWidth: 875, PixelHeight: 740}
	require.NoError(t, Resize(master, geometry))

	start := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-start
		for range 100 {
			_ = Resize(master, geometry)
		}
	}()
	close(start)
	err = master.Close()
	<-done
	require.NoError(t, err)
	assert.Error(t, Resize(master, geometry))
}

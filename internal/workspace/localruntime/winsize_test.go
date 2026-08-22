package localruntime

import (
	"math"
	"testing"

	"github.com/creack/pty/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/ptysize"
)

func TestClampWinsizeDim(t *testing.T) {
	assert := assert.New(t)
	cases := []struct {
		name string
		in   int
		want uint16
	}{
		{"zero floors to one", 0, 1},
		{"negative floors to one", -10, 1},
		{"minimum", 1, 1},
		{"typical", 120, 120},
		{"uint16 max", math.MaxUint16, math.MaxUint16},
		{"above uint16 max caps", math.MaxUint16 + 1, math.MaxUint16},
		{"large value caps", 5_000_000, math.MaxUint16},
	}
	for _, tc := range cases {
		assert.Equalf(tc.want, clampWinsizeDim(tc.in), "case %s", tc.name)
	}
}

func TestResizePTYLockedPreservesCellPixelSize(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := assert.New(t)

	ptmx, tty, err := pty.Open()
	require.NoError(err)
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = tty.Close()
	})
	require.NoError(pty.Setsize(ptmx, &pty.Winsize{
		Rows: 30,
		Cols: 120,
		X:    120 * 8,
		Y:    30 * 16,
	}))

	s := &session{ptmx: ptmx}
	require.NoError(s.resizePTYLocked(ptysize.Geometry{Cols: 100, Rows: 40}))

	size, err := pty.GetsizeFull(ptmx)
	require.NoError(err)
	assert.Equal(&pty.Winsize{
		Rows: 40,
		Cols: 100,
		X:    100 * 8,
		Y:    40 * 16,
	}, size)
}

package ptysize

import (
	"testing"

	"github.com/creack/pty/v2"
	"github.com/stretchr/testify/assert"
)

func TestApplyUsesMeasuredBrowserPixels(t *testing.T) {
	actual := Apply(&pty.Winsize{
		Cols: 120,
		Rows: 30,
		X:    960,
		Y:    480,
	}, Geometry{
		Cols:        100,
		Rows:        40,
		PixelWidth:  875,
		PixelHeight: 740,
	})

	assert.Equal(t, &pty.Winsize{
		Cols: 100,
		Rows: 40,
		X:    875,
		Y:    740,
	}, actual)
}

func TestApplyPreservesCellGeometryWithoutBrowserPixels(t *testing.T) {
	actual := Apply(&pty.Winsize{
		Cols: 120,
		Rows: 30,
		X:    960,
		Y:    480,
	}, Geometry{Cols: 100, Rows: 40})

	assert.Equal(t, &pty.Winsize{
		Cols: 100,
		Rows: 40,
		X:    800,
		Y:    640,
	}, actual)
}

func TestFallbackGeometryUsesEightBySixteenCells(t *testing.T) {
	assert.Equal(t, Geometry{
		Cols:        120,
		Rows:        30,
		PixelWidth:  960,
		PixelHeight: 480,
	}, FallbackGeometry(120, 30))
}

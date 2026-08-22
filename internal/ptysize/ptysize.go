package ptysize

import (
	"fmt"
	"math"
	"os"

	"github.com/creack/pty/v2"
)

// Geometry is the character and pixel size reported by the browser that owns
// terminal resize authority.
type Geometry struct {
	Cols        int
	Rows        int
	PixelWidth  int
	PixelHeight int
}

// FallbackGeometry supplies an 8x16 cell size until a browser reports
// measured pixels for a retained PTY.
func FallbackGeometry(cols, rows int) Geometry {
	return Normalize(Geometry{
		Cols:        cols,
		Rows:        rows,
		PixelWidth:  cols * 8,
		PixelHeight: rows * 16,
	})
}

// Normalize bounds browser-supplied geometry to the PTY wire range.
func Normalize(geometry Geometry) Geometry {
	return Geometry{
		Cols:        int(clamp(geometry.Cols)),
		Rows:        int(clamp(geometry.Rows)),
		PixelWidth:  int(clamp(geometry.PixelWidth)),
		PixelHeight: int(clamp(geometry.PixelHeight)),
	}
}

// Winsize builds a PTY size from browser geometry.
func Winsize(geometry Geometry) *pty.Winsize {
	return &pty.Winsize{
		Cols: clamp(geometry.Cols),
		Rows: clamp(geometry.Rows),
		X:    clamp(geometry.PixelWidth),
		Y:    clamp(geometry.PixelHeight),
	}
}

// Resize applies browser geometry to a PTY. When a caller has no measured
// pixel value, the existing per-cell geometry is retained across the character
// resize.
func Resize(ptmx *os.File, geometry Geometry) error {
	current, err := pty.GetsizeFull(ptmx)
	if err != nil {
		return fmt.Errorf("get PTY size before resize: %w", err)
	}
	if err := pty.Setsize(ptmx, Apply(current, geometry)); err != nil {
		return fmt.Errorf("set PTY size: %w", err)
	}
	return nil
}

// Apply combines measured browser geometry with a previous PTY size.
func Apply(current *pty.Winsize, geometry Geometry) *pty.Winsize {
	next := *current
	next.Cols = clamp(geometry.Cols)
	next.Rows = clamp(geometry.Rows)
	next.X = resizePixels(current.X, current.Cols, next.Cols, geometry.PixelWidth)
	next.Y = resizePixels(current.Y, current.Rows, next.Rows, geometry.PixelHeight)
	return &next
}

func resizePixels(currentPixels, currentCells, nextCells uint16, measured int) uint16 {
	if measured > 0 {
		return clamp(measured)
	}
	if currentPixels == 0 || currentCells == 0 || nextCells == 0 {
		return 0
	}
	scaled := (uint64(currentPixels)*uint64(nextCells) + uint64(currentCells)/2) /
		uint64(currentCells)
	return clampUint64(scaled)
}

func clamp(value int) uint16 {
	if value <= 0 {
		return 0
	}
	return clampUint64(uint64(value))
}

func clampUint64(value uint64) uint16 {
	if value > math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(value)
}

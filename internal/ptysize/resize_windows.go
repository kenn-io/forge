package ptysize

import (
	"os"

	"github.com/creack/pty"
)

// Resize is unsupported for file-based PTYs on Windows; owners use ConPTY.
func Resize(*os.File, Geometry) error {
	return pty.ErrUnsupported
}

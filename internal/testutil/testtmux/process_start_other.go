//go:build !darwin && !linux && !windows

package testtmux

import "errors"

func processStart(int) (string, error) {
	return "", errors.New("high-resolution process identity is unsupported")
}

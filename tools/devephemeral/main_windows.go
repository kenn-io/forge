//go:build windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "dev-ephemeral is not supported on Windows")
	os.Exit(1)
}

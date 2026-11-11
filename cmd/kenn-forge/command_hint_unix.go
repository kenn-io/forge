//go:build !windows

package main

import shellquote "github.com/kballard/go-shellquote"

func quoteCommand(args []string) string {
	return shellquote.Join(args...)
}

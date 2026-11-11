//go:build !windows

package main

import "go.kenn.io/kit/agenthook"

func quoteCommand(args []string) string {
	command, _ := agenthook.BuildCommand(args[0], args[1:]...)
	return command.POSIX
}

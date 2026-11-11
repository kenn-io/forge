//go:build !windows

package main

import (
	"strings"

	shellquote "github.com/kballard/go-shellquote"
)

func quoteCommand(args []string) string {
	return strings.Join(quoteWords(args), " ")
}

// quoteWords escapes a leading '#', which go-shellquote leaves bare and the
// shell would treat as the start of a comment.
func quoteWords(args []string) []string {
	words := make([]string, len(args))
	for i, arg := range args {
		words[i] = shellquote.Join(arg)
		if strings.HasPrefix(arg, "#") {
			words[i] = `\#` + shellquote.Join(arg[1:])
		}
	}
	return words
}

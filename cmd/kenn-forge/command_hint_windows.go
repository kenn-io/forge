package main

import (
	"strings"
	"syscall"
)

func quoteCommand(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = syscall.EscapeArg(arg)
		if strings.ContainsAny(arg, "&|<>()^") && !strings.HasPrefix(quoted[i], `"`) {
			trailing := len(arg) - len(strings.TrimRight(arg, `\`))
			quoted[i] = `"` + quoted[i] + strings.Repeat(`\`, trailing) + `"`
		}
		if strings.HasPrefix(quoted[i], `"`) {
			var escaped strings.Builder
			slashes := 0
			for _, char := range quoted[i] {
				if char == '%' {
					escaped.WriteString(strings.Repeat(`\`, slashes))
					escaped.WriteString(`"^%"`)
				} else {
					escaped.WriteRune(char)
				}
				if char == '\\' {
					slashes++
				} else {
					slashes = 0
				}
			}
			quoted[i] = escaped.String()
		} else {
			quoted[i] = strings.ReplaceAll(quoted[i], "%", "^%")
		}
	}
	return strings.Join(quoted, " ")
}

package procutil

import (
	"slices"
	"strings"
)

// MergeEnvironment sets extra's variables over env, in name order after the
// rest. It reuses env's backing array, so callers pass an environment they own.
func MergeEnvironment(env []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return env
	}
	env = slices.DeleteFunc(env, func(value string) bool {
		key, _, found := strings.Cut(value, "=")
		_, replaced := extra[key]
		return found && replaced
	})
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		env = append(env, key+"="+extra[key])
	}
	return env
}

// EnvironmentValue returns key's value in env, the last one when env sets
// it twice, as a process started with env would see it.
func EnvironmentValue(env []string, key string) string {
	for _, entry := range slices.Backward(env) {
		if value, ok := strings.CutPrefix(entry, key+"="); ok {
			return value
		}
	}
	return ""
}

// IsShellIdentifier reports whether value is a valid POSIX shell variable
// name, the only names every launch path can pass to a process.
func IsShellIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r == '_':
			continue
		case i > 0 && r >= '0' && r <= '9':
			continue
		default:
			return false
		}
	}
	return true
}

package procutil

import (
	"slices"
	"strings"
)

// MergeEnv returns env with overrides applied, as an "KEY=value" slice suitable
// for exec.Cmd.Env. Inherited entries for an overridden key are dropped rather
// than shadowed, because not every consumer of an environment slice honors
// last-wins duplicates. Overrides are appended in key order so callers can
// compare rendered environments.
func MergeEnv(env []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return env
	}
	kept := env[:0]
	for _, entry := range env {
		key, _, found := strings.Cut(entry, "=")
		if found {
			if _, overridden := overrides[key]; overridden {
				continue
			}
		}
		kept = append(kept, entry)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		kept = append(kept, key+"="+overrides[key])
	}
	return kept
}

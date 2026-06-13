// Package gitsafe strips inherited git location/identity environment
// variables (GIT_DIR, GIT_WORK_TREE, GIT_CONFIG*, GIT_AUTHOR_*, ...)
// from the current process the moment it is imported.
//
// The repository's pre-commit hook runs `go test` with GIT_DIR and
// GIT_WORK_TREE set, pointing at the host repository. Git gives those
// variables precedence over a child command's working directory, so a
// test fixture that shells out to bare `git init` / `git config` /
// `git clone` re-initializes and reconfigures the REAL repo — writing
// core.worktree and a fixture identity into its config and corrupting
// how Git resolves the user's working tree.
//
// Per-command wrappers like gitcmd.New() already strip these, but a raw
// exec/procutil git call does not. This package neutralizes the entire
// class at the process level, before any test or fixture runs, so it is
// safe even if a fixture forgets the wrapper. Blank-import it from every
// test package that runs git:
//
//	import _ "go.kenn.io/middleman/internal/testutil/gitsafe"
package gitsafe

import (
	"os"
	"strings"

	gitenv "go.kenn.io/kit/git/env"
)

func init() {
	UnsetInheritedGitEnv()
}

// UnsetInheritedGitEnv removes from the live process environment every
// variable that gitenv.StripInherited filters — the canonical set that
// can bind a child git process to an inherited parent repository,
// config, or identity. Reusing gitenv keeps that list in one place.
func UnsetInheritedGitEnv() {
	full := os.Environ()
	kept := make(map[string]struct{}, len(full))
	for _, kv := range gitenv.StripInherited(full) {
		if name, _, ok := strings.Cut(kv, "="); ok {
			kept[name] = struct{}{}
		}
	}
	for _, kv := range full {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, keep := kept[name]; !keep {
			_ = os.Unsetenv(name)
		}
	}
}

// Package gitsafe isolates test binaries from developer and system Git state.
package gitsafe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
	gitenv "go.kenn.io/kit/git/env"
)

// RunIsolatedMain runs a package's tests with one portable, empty Git config.
// The setup is shared by the test binary so Git-heavy suites do not create a
// new config directory for every test or command, which is costly on Windows.
func RunIsolatedMain(m *testing.M) int {
	root, err := os.MkdirTemp("", "kenn-forge-git-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create isolated Git test directory: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(root) }()

	if err := configureGitForTests(root); err != nil {
		fmt.Fprintf(os.Stderr, "configure isolated Git test environment: %v\n", err)
		return 1
	}
	return m.Run()
}

func configureGitForTests(root string) error {
	unsetInheritedGitEnv()

	// Git for Windows cannot reliably use NUL as GIT_CONFIG_GLOBAL. A regular,
	// empty file is a valid no-op config on every platform.
	globalConfig := filepath.Join(root, "global.gitconfig")
	if err := os.WriteFile(globalConfig, nil, 0o600); err != nil {
		return fmt.Errorf("create empty global config: %w", err)
	}
	xdgConfigHome := filepath.Join(root, "xdg")
	if err := os.MkdirAll(xdgConfigHome, 0o755); err != nil {
		return fmt.Errorf("create XDG config directory: %w", err)
	}

	for key, value := range map[string]string{
		"GIT_CONFIG_GLOBAL":   globalConfig,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
		"HOME":                root,
		"XDG_CONFIG_HOME":     xdgConfigHome,
	} {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
	}
	return nil
}

// Runner returns a real Git runner bound to the test binary's shared isolated
// config. It creates no files or directories; RunIsolatedMain owns that setup.
func Runner() gitcmd.Runner {
	return gitcmd.Runner{
		Env:                         isolatedCommandEnv(os.Environ()),
		DisableSafeDirectoryForward: true,
	}
}

// MutableRunner returns a runner with a per-test writable global config. Use
// it only when the behavior under test intentionally changes global config;
// ordinary Git tests should use the package-shared Runner.
func MutableRunner(t testing.TB) gitcmd.Runner {
	t.Helper()
	root := t.TempDir()
	globalConfig := filepath.Join(root, "global.gitconfig")
	require.NoError(t, os.WriteFile(globalConfig, nil, 0o600))
	xdgConfigHome := filepath.Join(root, "xdg")
	require.NoError(t, os.Mkdir(xdgConfigHome, 0o755))

	return gitcmd.Runner{
		Env: replaceEnvValues(gitenv.StripAll(os.Environ()), map[string]string{
			"GIT_CONFIG_GLOBAL":   globalConfig,
			"GIT_CONFIG_NOSYSTEM": "1",
			"GIT_TERMINAL_PROMPT": "0",
			"HOME":                root,
			"XDG_CONFIG_HOME":     xdgConfigHome,
		}),
		DisableSafeDirectoryForward: true,
	}
}

func isolatedCommandEnv(base []string) []string {
	replacements := make(map[string]string)
	for _, key := range []string{
		"GIT_CONFIG_GLOBAL",
		"GIT_CONFIG_NOSYSTEM",
		"GIT_TERMINAL_PROMPT",
		"HOME",
		"XDG_CONFIG_HOME",
	} {
		if value, ok := os.LookupEnv(key); ok {
			replacements[key] = value
		}
	}
	return replaceEnvValues(gitenv.StripAll(base), replacements)
}

func replaceEnvValues(base []string, replacements map[string]string) []string {
	out := make([]string, 0, len(base)+len(replacements))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := replacements[strings.ToUpper(key)]; replaced {
			continue
		}
		out = append(out, entry)
	}
	for key, value := range replacements {
		out = append(out, key+"="+value)
	}
	return out
}

func unsetInheritedGitEnv() {
	full := os.Environ()
	kept := make(map[string]struct{}, len(full))
	for _, entry := range gitenv.StripAll(full) {
		if name, _, ok := strings.Cut(entry, "="); ok {
			kept[name] = struct{}{}
		}
	}
	for _, entry := range full {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, keep := kept[name]; !keep {
			_ = os.Unsetenv(name)
		}
	}
}

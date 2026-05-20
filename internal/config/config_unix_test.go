//go:build !windows

package config

import (
	"fmt"
	"path/filepath"
	"testing"
)

func fakeGHCLIPath(dir string) string {
	return filepath.Join(dir, "gh")
}

func fakeGHCLIScript(t *testing.T, opts fakeGHCLIOptions) string {
	t.Helper()
	script := "#!/bin/sh\n"
	script += "printf '%s\\n' \"$*\" >> \"$FAKE_GH_ARGV\"\n"
	if opts.SleepSeconds > 0 {
		// exec replaces the shell so there's no orphaned child
		// holding stdout open after the parent gets SIGKILL'd by
		// CommandContext; without exec, cmd.Output() blocks for
		// the full sleep duration even after the shell is dead.
		// SleepSeconds is therefore terminal in the script: any
		// stderr/stdout/exit configured below is unreachable when
		// SleepSeconds > 0, by design (the test stops the helper
		// via context timeout, not by letting the fake finish).
		sleepBin := resolveSleepBinary(t)
		script += fmt.Sprintf("exec %s %d\n", sleepBin, opts.SleepSeconds)
	}
	if opts.Stderr != "" {
		script += "printf '%s\\n' " + shellSingleQuote(opts.Stderr) + " 1>&2\n"
	}
	if opts.Stdout != "" {
		script += "printf '%s\\n' " + shellSingleQuote(opts.Stdout) + "\n"
	}
	script += fmt.Sprintf("exit %d\n", opts.ExitCode)
	return script
}

func fakeGHCLIRejectHostnameThenBareScript() string {
	return `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GH_ARGV"
case "$*" in
*--hostname*)
	printf 'unknown flag: --hostname\n' 1>&2
	exit 2
	;;
*)
	printf 'gh-secret-bare\n'
	exit 0
	;;
esac
`
}

func fakeGHCLIRejectHostnameScript() string {
	return `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_GH_ARGV"
printf 'unknown flag: --hostname\n' 1>&2
exit 2
`
}

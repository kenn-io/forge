// Package panebootstrap provides a shell-free tmux pane launcher.
//
// tmux runs a window's command through the user's default shell. Routing
// the real pane command through any /bin/sh hop breaks pane teardown on
// macOS: tmux fails to destroy a pane on PTY fd-close while the process
// is still alive once bash 3.2 (the system /bin/sh) has been anywhere in
// the exec chain, even after it has exec'd away. That delays the websocket
// exit frame from PTY EOF (~prompt) to cmd.Wait (~the command's lifetime).
//
// Instead the launcher emits a single dialect-neutral token,
//
//	exec '<exe>' __tmux-pane-bootstrap '<handoff>'
//
// that re-execs the middleman binary itself (resolved via os.Executable,
// the same idiom internal/ptyowner uses for its pty-owner subcommand). The
// token parses identically under POSIX shells and fish, needs no /bin/sh
// or /bin/rm on PATH (Nix-safe), and keeps preserved env values out of
// tmux argv. This package is that re-exec target: ExecIfRequested reads the
// handoff file (env + argv) written by WriteHandoff, deletes it, and
// replaces the process with the real command under a clean environment
// (env -i semantics). The pane is then a single process, so PTY EOF
// reaches attach clients the instant the command exits.
package panebootstrap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Subcommand is the hidden argv[1] that requests the pane bootstrap.
const Subcommand = "__tmux-pane-bootstrap"

// handoffHeader is the first NUL-delimited field of a handoff file; the
// trailing version guards against format drift across binary upgrades.
const handoffHeader = "middleman-pane-handoff/1"

// WriteHandoff serializes env and argv into a 0600 file in dir and returns
// its path. dir may be empty, in which case the OS default temp dir is
// used. The format is NUL-framed because env and argv entries may contain
// newlines, quotes, or invalid UTF-8 but — by the execve contract — never
// a NUL byte:
//
//	middleman-pane-handoff/1\x00<envCount>\x00<argvCount>\x00
//	<env-0>\x00 ... <env-N>\x00 <argv-0>\x00 ... <argv-M>\x00
//
// The explicit counts let ReadHandoff split env from argv unambiguously
// even when trailing entries are empty strings.
func WriteHandoff(dir string, env, argv []string) (string, error) {
	var buf strings.Builder
	buf.WriteString(handoffHeader)
	buf.WriteByte(0)
	buf.WriteString(strconv.Itoa(len(env)))
	buf.WriteByte(0)
	buf.WriteString(strconv.Itoa(len(argv)))
	buf.WriteByte(0)
	for _, entry := range env {
		buf.WriteString(entry)
		buf.WriteByte(0)
	}
	for _, entry := range argv {
		buf.WriteString(entry)
		buf.WriteByte(0)
	}

	file, err := os.CreateTemp(dir, "middleman-tmux-handoff-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if _, err := file.WriteString(buf.String()); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// ReadHandoff parses a file written by WriteHandoff. It is exported so the
// launcher's tests can assert handoff contents without re-deriving the
// format.
func ReadHandoff(path string) (env, argv []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	parts := strings.Split(string(data), "\x00")
	if len(parts) < 3 {
		return nil, nil, fmt.Errorf("pane handoff: malformed (need header and counts)")
	}
	if parts[0] != handoffHeader {
		return nil, nil, fmt.Errorf("pane handoff: bad header %q", parts[0])
	}
	envCount, err := strconv.Atoi(parts[1])
	if err != nil || envCount < 0 {
		return nil, nil, fmt.Errorf("pane handoff: bad env count %q", parts[1])
	}
	argvCount, err := strconv.Atoi(parts[2])
	if err != nil || argvCount < 0 {
		return nil, nil, fmt.Errorf("pane handoff: bad argv count %q", parts[2])
	}
	// Every entry is NUL-terminated, so Split yields a trailing "" past
	// the final NUL; require at least the declared entry count.
	rest := parts[3:]
	if len(rest) < envCount+argvCount {
		return nil, nil, fmt.Errorf(
			"pane handoff: truncated (want %d entries, have %d)",
			envCount+argvCount, len(rest),
		)
	}
	env = append([]string(nil), rest[:envCount]...)
	argv = append([]string(nil), rest[envCount:envCount+argvCount]...)
	return env, argv, nil
}

// ExecIfRequested checks whether this process was invoked as the tmux pane
// bootstrap (os.Args is exactly [exe, Subcommand, handoffPath]). If so it
// reads the handoff, removes it, and replaces the process image with the
// real command under the handoff environment — it never returns in that
// case (it execs or calls os.Exit(127)). Otherwise it returns immediately
// so normal startup proceeds. Call it as the first statement of a binary's
// main/TestMain, before any logging or flag parsing.
func ExecIfRequested() {
	if len(os.Args) != 3 || os.Args[1] != Subcommand {
		return
	}
	path := os.Args[2]
	env, argv, readErr := ReadHandoff(path)
	// Remove before exec: exec never returns, and leaking a 0600 temp
	// file is better than killing the user's pane, so a Remove failure is
	// logged but must not abort the launch.
	if rmErr := os.Remove(path); rmErr != nil {
		fmt.Fprintf(os.Stderr, "pane bootstrap: remove handoff: %v\n", rmErr)
	}
	if readErr != nil {
		bootstrapFail("read handoff", readErr)
	}
	if len(argv) == 0 {
		bootstrapFail("handoff", errors.New("empty command"))
	}
	argv0, resolveErr := resolveArgv0(argv[0], env)
	if resolveErr != nil {
		bootstrapFail("resolve command", resolveErr)
	}
	bootstrapFail("exec", execProcess(argv0, argv, env))
}

// bootstrapFail reports a launch failure on stderr and exits 127, matching
// the exit code the old POSIX handoff script used for an unreadable env
// file. execProcess on success never returns, so reaching here always
// means a real failure.
func bootstrapFail(stage string, err error) {
	if err == nil {
		err = errors.New("unknown error")
	}
	fmt.Fprintf(os.Stderr, "pane bootstrap: %s: %v\n", stage, err)
	os.Exit(127)
}

// resolveArgv0 returns the path to exec for argv[0]. Absolute paths are
// used as-is (the launcher already resolves the command to an absolute
// path before writing the handoff). A bare name is looked up against the
// handoff's PATH — not the process environment, which env -i leaves bare.
// A relative path containing a separator is rejected, mirroring
// ptyowner/runtime.ResolveExecutable: it would resolve inside the pane's
// working directory, which may be an untrusted worktree.
func resolveArgv0(name string, env []string) (string, error) {
	if name == "" {
		return "", errors.New("empty argv[0]")
	}
	if filepath.IsAbs(name) {
		return name, nil
	}
	if strings.ContainsRune(name, filepath.Separator) {
		return "", fmt.Errorf(
			"command %q must be an absolute path or a PATH-resolvable name",
			name,
		)
	}
	var pathEnv string
	for _, kv := range env {
		if rest, ok := strings.CutPrefix(kv, "PATH="); ok {
			pathEnv = rest
		}
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if !filepath.IsAbs(candidate) {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("command %q not found in handoff PATH", name)
}

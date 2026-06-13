package localruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	shellquote "github.com/kballard/go-shellquote"
	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/workspace/panebootstrap"
)

type tmuxEnvPolicy struct {
	preserveShellEnv bool
}

type tmuxPaneEnvironment struct {
	keys       []string
	command    []string
	commandEnv []string
}

var (
	tmuxAgentEnvPolicy = tmuxEnvPolicy{}
	tmuxShellEnvPolicy = tmuxEnvPolicy{preserveShellEnv: true}
)

func (p tmuxEnvPolicy) paneEnvironment(
	baseEnv []string,
	command []string,
	extraStripVars []string,
) tmuxPaneEnvironment {
	env := p.environment(baseEnv, extraStripVars)
	envWithTerm := append(slices.Clone(env), "TERM=xterm-256color")
	return tmuxPaneEnvironment{
		keys:       tmuxEnvironmentKeys(envWithTerm),
		command:    slices.Clone(command),
		commandEnv: envWithTerm,
	}
}

// handoffEnv returns the KEY=VALUE entries the pane runs under, reproducing
// the old `exec env -i KEY="${KEY-}" ...` dance: only the policy's
// preserved, shell-identifier keys, with last-wins values from commandEnv
// (so the appended TERM=xterm-256color overrides any inherited TERM). The
// pane bootstrap applies these as the process environment via env -i
// semantics; commandEnv itself is left intact for tmux subprocess argv.
func (e tmuxPaneEnvironment) handoffEnv() []string {
	values := make(map[string]string, len(e.commandEnv))
	for _, kv := range e.commandEnv {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		values[kv[:eq]] = kv[eq+1:]
	}
	out := make([]string, 0, len(e.keys))
	for _, key := range e.keys {
		if !isShellIdentifier(key) {
			continue
		}
		out = append(out, key+"="+values[key])
	}
	return out
}

func (p tmuxEnvPolicy) keys(extraStripVars []string) []string {
	return p.paneEnvironment(os.Environ(), nil, extraStripVars).keys
}

func tmuxEnvironmentKeys(env []string) []string {
	keysByName := make(map[string]struct{}, len(env))
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		key := kv[:eq]
		if !isShellIdentifier(key) {
			continue
		}
		keysByName[key] = struct{}{}
	}

	keys := make([]string, 0, len(keysByName))
	for key := range keysByName {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func (p tmuxEnvPolicy) environment(
	baseEnv []string,
	extraStripVars []string,
) []string {
	if p.preserveShellEnv {
		return sessionEnvironment(baseEnv, extraStripVars)
	}
	return tmuxSessionEnvironment(baseEnv, extraStripVars)
}

type tmuxLauncher struct {
	TmuxCommand []string
	Session     string
	CWD         string
	Pane        tmuxPaneEnvironment
	OwnerMarker string
}

type tmuxLaunchResult struct {
	AttachCommand []string
	Created       bool
}

func (l tmuxLauncher) prepare(ctx context.Context) (tmuxLaunchResult, error) {
	if l.Session == "" {
		return tmuxLaunchResult{}, fmt.Errorf("tmux session is empty")
	}
	exists, err := l.sessionExists(ctx)
	if err != nil {
		return tmuxLaunchResult{}, err
	}
	if exists {
		if err := l.validateOwner(ctx); err != nil {
			return tmuxLaunchResult{}, err
		}
		return tmuxLaunchResult{AttachCommand: l.attachSessionCommand()}, nil
	}

	paneCommand, cleanupEnvFile, err := l.newSessionPaneCommand()
	if err != nil {
		return tmuxLaunchResult{}, err
	}
	created := false
	defer func() {
		if !created {
			cleanupEnvFile()
		}
	}()
	if err := l.run(ctx, l.newSessionCommand(paneCommand)); err != nil {
		if retryErr := l.validateExistingAfterCreateRace(ctx); retryErr == nil {
			return tmuxLaunchResult{AttachCommand: l.attachSessionCommand()}, nil
		}
		return tmuxLaunchResult{}, fmt.Errorf("tmux new-session: %w", err)
	}
	created = true
	return tmuxLaunchResult{
		AttachCommand: l.attachSessionCommand(),
		Created:       true,
	}, nil
}

func (l tmuxLauncher) validateExistingAfterCreateRace(
	ctx context.Context,
) error {
	exists, err := l.sessionExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("tmux session %q still absent", l.Session)
	}
	return l.validateOwner(ctx)
}

func (l tmuxLauncher) sessionExists(ctx context.Context) (bool, error) {
	err := l.run(ctx, l.hasSessionCommand())
	if err == nil {
		return true, nil
	}
	var tmuxErr tmuxCommandError
	if errors.As(err, &tmuxErr) && isTmuxSessionAbsent(tmuxErr.stderr, tmuxErr.err) {
		return false, nil
	}
	return false, fmt.Errorf("tmux has-session: %w", err)
}

func (l tmuxLauncher) validateOwner(ctx context.Context) error {
	if l.OwnerMarker == "" {
		return nil
	}
	out, err := l.output(ctx, l.showOwnerCommand())
	if err != nil {
		return fmt.Errorf("tmux show owner: %w", err)
	}
	if strings.TrimSpace(string(out)) != l.OwnerMarker {
		return fmt.Errorf("tmux session %q is not owned by this manager", l.Session)
	}
	return nil
}

func (l tmuxLauncher) run(ctx context.Context, command []string) error {
	_, err := l.output(ctx, command)
	return err
}

func (l tmuxLauncher) output(
	ctx context.Context,
	command []string,
) ([]byte, error) {
	if len(command) == 0 || command[0] == "" {
		return nil, fmt.Errorf("tmux command is empty")
	}
	cmd := procutil.CommandContext(ctx, command[0], command[1:]...)
	cmd.Env = l.Pane.commandEnv
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := procutil.Run(ctx, cmd, "tmux subprocess capacity")
	if err != nil {
		return nil, tmuxCommandError{
			err:    err,
			stderr: slices.Clone(stderr.Bytes()),
		}
	}
	return stdout.Bytes(), nil
}

type tmuxCommandError struct {
	err    error
	stderr []byte
}

func (e tmuxCommandError) Error() string {
	msg := strings.TrimSpace(string(e.stderr))
	if msg == "" {
		return e.err.Error()
	}
	return e.err.Error() + ": " + msg
}

func (e tmuxCommandError) Unwrap() error {
	return e.err
}

// newSessionPaneCommand writes the pane's env and argv to a 0600 handoff
// file and returns a single dialect-neutral launch token,
// `exec '<exe>' __tmux-pane-bootstrap '<handoff>'`, plus a cleanup that
// removes the file if the session is never created. tmux runs a lone
// shell-command argument through the user's default shell, so the token
// must parse identically under POSIX shells and fish; this one is just
// three quoted words, and the leading exec keeps the pane a single process
// so PTY EOF reaches attach clients the instant the command exits. The exe
// re-execs middleman itself into panebootstrap, which applies the handoff
// env (env -i semantics) and execs the real command with no shell in the
// chain — see internal/workspace/panebootstrap for why any /bin/sh hop
// breaks pane teardown on macOS.
//
// Caveat: tmux still interposes the user's default-shell to run this token,
// so a host whose SHELL is /bin/sh re-enters the slow path. That is not
// fixable while tmux runs window commands through default-shell; the common
// case (zsh, bash 5, fish) is unaffected because the shell exec's away
// before the bootstrap runs.
func (l tmuxLauncher) newSessionPaneCommand() (string, func(), error) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("resolve middleman executable: %w", err)
	}
	path, err := panebootstrap.WriteHandoff(
		tmuxPaneEnvironmentTempDir(), l.Pane.handoffEnv(), l.Pane.command,
	)
	if err != nil {
		return "", nil, fmt.Errorf("write tmux pane handoff: %w", err)
	}
	cleanup := func() {
		_ = os.Remove(path)
	}
	token := "exec " + shellCommand([]string{exe, panebootstrap.Subcommand, path})
	return token, cleanup, nil
}

func tmuxPaneEnvironmentTempDir() string {
	return os.Getenv("MIDDLEMAN_TMUX_ENV_DIR")
}

func (l tmuxLauncher) hasSessionCommand() []string {
	return append(slices.Clone(l.TmuxCommand), "has-session", "-t", l.Session)
}

func (l tmuxLauncher) showOwnerCommand() []string {
	return append(
		slices.Clone(l.TmuxCommand),
		"show-options", "-qv", "-t", l.Session, "@middleman_owner",
	)
}

func (l tmuxLauncher) newSessionCommand(paneCommand string) []string {
	command := append(slices.Clone(l.TmuxCommand), "new-session")
	command = append(command, "-E", "-d", "-s", l.Session)
	if l.CWD != "" {
		command = append(command, "-c", l.CWD)
	}
	// paneCommand is a single dialect-neutral token (see
	// newSessionPaneCommand); tmux hands a lone shell-command argument
	// to the user's default shell, where `exec /bin/sh '<script>'`
	// parses identically under POSIX shells and fish.
	command = append(command, paneCommand)
	if l.OwnerMarker != "" {
		command = append(
			command,
			";", "set-option", "-q", "-t", l.Session,
			"@middleman_owner", l.OwnerMarker,
		)
	}
	return command
}

func (l tmuxLauncher) attachSessionCommand() []string {
	return append(
		slices.Clone(l.TmuxCommand), "attach-session", "-t", l.Session,
	)
}

func shellCommand(command []string) string {
	return shellquote.Join(command...)
}

func tmuxSessionEnvironment(env []string, extraStrip []string) []string {
	sanitized := sessionEnvironment(env, extraStrip)
	out := make([]string, 0, len(sanitized))
	for _, kv := range sanitized {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		key := kv[:eq]
		if shouldAllowTmuxSessionVar(key) {
			out = append(out, kv)
		}
	}
	return out
}

func isShellIdentifier(value string) bool {
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

var tmuxSessionEnvAllowlist = []string{
	"COLORTERM",
	"EDITOR",
	"HOME",
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"LESS",
	"LOGNAME",
	"NO_COLOR",
	"PAGER",
	"PATH",
	"SHELL",
	"SSH_AUTH_SOCK",
	"TERM",
	"TMP",
	"TMPDIR",
	"TEMP",
	"USER",
	"VISUAL",
}

var tmuxSessionEnvPrefixAllowlist = []string{
	"LC_",
	"XDG_",
}

func shouldAllowTmuxSessionVar(key string) bool {
	if slices.Contains(tmuxSessionEnvAllowlist, key) {
		return true
	}
	for _, prefix := range tmuxSessionEnvPrefixAllowlist {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

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
)

type tmuxEnvPolicy struct {
	preserveShellEnv bool
}

type tmuxPaneEnvironment struct {
	keys        []string
	paneCommand string
	commandEnv  []string
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
	return paneEnvironmentFromEnv(
		p.environment(baseEnv, extraStripVars), command,
	)
}

// paneEnvironmentWithExtra applies the policy to baseEnv and then appends
// caller-supplied variables, which always reach the pane even when the
// policy's allowlist would drop them. Keys must be shell identifiers; the
// caller validates them.
func (p tmuxEnvPolicy) paneEnvironmentWithExtra(
	baseEnv []string,
	command []string,
	extraStripVars []string,
	extraEnv map[string]string,
) tmuxPaneEnvironment {
	env := p.environment(baseEnv, extraStripVars)
	keys := make([]string, 0, len(extraEnv))
	for key := range extraEnv {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		env = append(env, key+"="+extraEnv[key])
	}
	return paneEnvironmentFromEnv(env, command)
}

func paneEnvironmentFromEnv(
	env []string,
	command []string,
) tmuxPaneEnvironment {
	envWithTerm := append(slices.Clone(env), "TERM=xterm-256color")
	keys := tmuxEnvironmentKeys(envWithTerm)
	parts := make([]string, 0, len(keys)+4)
	parts = append(parts, "exec", "env", "-i")
	for _, key := range keys {
		parts = append(parts, key+"=\"${"+key+"-}\"")
	}
	parts = append(parts, shellCommand(command))
	return tmuxPaneEnvironment{
		keys:        keys,
		paneCommand: strings.Join(parts, " "),
		commandEnv:  envWithTerm,
	}
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
	HideStatus  bool
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
	if l.HideStatus {
		if err := l.run(ctx, l.hideStatusCommand()); err != nil {
			if killErr := l.run(ctx, l.killSessionCommand()); killErr != nil {
				return tmuxLaunchResult{}, fmt.Errorf(
					"hide tmux status: %w; cleanup new tmux session: %v",
					err, killErr,
				)
			}
			return tmuxLaunchResult{}, fmt.Errorf("hide tmux status: %w", err)
		}
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

func (l tmuxLauncher) newSessionPaneCommand() (string, func(), error) {
	path, err := writeTmuxPaneEnvironment(l.Pane.commandEnv, l.Pane.keys)
	if err != nil {
		return "", nil, fmt.Errorf("write tmux pane environment: %w", err)
	}
	// tmux parses shell-command with its default shell, which may not be
	// POSIX-compatible. Keep the POSIX handoff in a script run by /bin/sh.
	scriptPath, err := writeTmuxPaneScript(path, l.Pane.paneCommand)
	if err != nil {
		_ = os.Remove(path)
		return "", nil, fmt.Errorf("write tmux pane script: %w", err)
	}
	cleanup := func() {
		_ = os.Remove(path)
		_ = os.Remove(scriptPath)
	}
	return shellCommand([]string{"/bin/sh", scriptPath}), cleanup, nil
}

func writeTmuxPaneScript(envPath string, paneCommand string) (string, error) {
	file, err := os.CreateTemp(tmuxPaneEnvironmentTempDir(), "middleman-tmux-pane-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}

	content := strings.Join([]string{
		"__middleman_env_file=" + shellCommand([]string{envPath}),
		"__middleman_script_file=" + shellCommand([]string{path}),
		`__middleman_cleanup_tmux_files() { /bin/rm -f "$__middleman_env_file" "$__middleman_script_file"; }`,
		`trap __middleman_cleanup_tmux_files EXIT`,
		`if [ ! -r "$__middleman_env_file" ]; then exit 127; fi`,
		`. "$__middleman_env_file"`,
		`__middleman_cleanup_tmux_files`,
		`trap - EXIT`,
		`unset -f __middleman_cleanup_tmux_files`,
		`unset __middleman_env_file`,
		`unset __middleman_script_file`,
		paneCommand,
	}, "\n")
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if _, err := file.WriteString("\n"); err != nil {
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

func writeTmuxPaneEnvironment(env []string, keys []string) (string, error) {
	values := make(map[string]string, len(env))
	for _, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		values[kv[:eq]] = kv[eq+1:]
	}

	var content strings.Builder
	for _, key := range keys {
		if !isShellIdentifier(key) {
			continue
		}
		content.WriteString("export ")
		content.WriteString(key)
		content.WriteByte('=')
		content.WriteString(shellCommand([]string{values[key]}))
		content.WriteByte('\n')
	}

	// This short-lived handoff keeps preserved values out of tmux argv. The
	// file is 0600 and cleaned on tmux launch failure and pane shell exit, but
	// it is not intended to be a same-user sandbox boundary.
	file, err := os.CreateTemp(tmuxPaneEnvironmentTempDir(), "middleman-tmux-env-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if _, err := file.WriteString(content.String()); err != nil {
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

func (l tmuxLauncher) hideStatusCommand() []string {
	return append(
		slices.Clone(l.TmuxCommand),
		"set-option", "-q", "-t", l.Session, "status", "off",
	)
}

func (l tmuxLauncher) killSessionCommand() []string {
	return append(
		slices.Clone(l.TmuxCommand), "kill-session", "-t", l.Session,
	)
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

// IsShellIdentifier reports whether value is a valid POSIX shell variable
// name, the requirement for env keys passed to command sessions.
func IsShellIdentifier(value string) bool {
	return isShellIdentifier(value)
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

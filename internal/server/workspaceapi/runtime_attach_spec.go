package workspaceapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func runtimeAttachSpec(
	ctx context.Context,
	tmuxCommand []string,
	sessionKey string,
	targetKey string,
	tmuxSession string,
) (runtimeAttachSpecResponse, error) {
	tmuxSession = strings.TrimSpace(tmuxSession)
	if tmuxSession == "" {
		return runtimeAttachSpecResponse{}, httpapi.BadRequest(
			httpapi.CodeBadRequest, "runtime session is not tmux-backed", nil,
		)
	}
	exists, err := attachSpecTmuxSessionExists(ctx, tmuxCommand, tmuxSession)
	if err != nil {
		return runtimeAttachSpecResponse{}, httpapi.ServiceUnavailable(
			"check tmux session: " + err.Error(),
		)
	}
	if !exists {
		return runtimeAttachSpecResponse{}, httpapi.NotFound(
			httpapi.CodeNotFound, "runtime tmux session not found", nil,
		)
	}
	command := runtimeAttachCommand(tmuxCommand, tmuxSession)
	return runtimeAttachSpecResponse{
		Version:           1,
		Kind:              "tmux",
		SessionKey:        sessionKey,
		TargetKey:         targetKey,
		TmuxSession:       tmuxSession,
		Command:           command,
		RequiresLocalHost: true,
	}, nil
}

// RuntimeAttachSpec builds a local tmux attachment contract shared by host,
// workspace, project-worktree, and Fleet runtime routes.
func RuntimeAttachSpec(
	ctx context.Context,
	tmuxCommand []string,
	sessionKey string,
	targetKey string,
	tmuxSession string,
) (RuntimeAttachSpecResponse, error) {
	return runtimeAttachSpec(ctx, tmuxCommand, sessionKey, targetKey, tmuxSession)
}

// RuntimeAttachSpecOutput is the shared Huma response envelope.
type RuntimeAttachSpecOutput = httpapi.BodyOutput[runtimeAttachSpecResponse]

func runtimeAttachCommand(tmuxCommand []string, tmuxSession string) []string {
	command := append([]string{}, tmuxCommand...)
	if len(command) == 0 {
		command = config.DefaultTmuxCommand()
	}
	// Remote consumers may launch this command without locale variables.
	// Force UTF-8 so tmux preserves non-ASCII terminal output. This
	// command runs in the caller's own shell, whose environment is not
	// sanitized; -E stops a widened update-environment from copying the
	// caller's variables (including provider tokens) into the session
	// environment where pane processes could read them back.
	command = append(command, "-u", "attach-session", "-E", "-t", tmuxSession)
	// The command runs in the caller's shell, whose environment must
	// not steer the attach: TMUX would make tmux refuse the nested
	// attach, and tmux resolves -L sockets under TMUX_TMPDIR, so the
	// caller's value must be replaced by the daemon's — set when the
	// daemon has one, unset when it does not — or the attach targets a
	// different tmux server than the daemon owns.
	envPrefix := []string{"env", "-u", "TMUX"}
	if dir := os.Getenv("TMUX_TMPDIR"); dir != "" {
		envPrefix = append(envPrefix, "TMUX_TMPDIR="+dir)
	} else {
		envPrefix = append(envPrefix, "-u", "TMUX_TMPDIR")
	}
	return append(envPrefix, command...)
}

func attachSpecTmuxSessionExists(
	ctx context.Context,
	command []string,
	session string,
) (bool, error) {
	command = append([]string{}, command...)
	if len(command) == 0 {
		command = config.DefaultTmuxCommand()
	}
	if strings.TrimSpace(command[0]) == "" {
		return false, errors.New("tmux command is empty")
	}
	args := append([]string{}, command[1:]...)
	args = append(args, "has-session", "-t", session)
	cmd := procutil.CommandContext(ctx, command[0], args...)
	cmd.Env = localruntime.TmuxClientEnvironment(os.Environ(), nil)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := procutil.Run(ctx, cmd, "runtime attach tmux probe")
	if err == nil {
		return true, nil
	}
	if attachSpecTmuxSessionAbsent(stderr.Bytes(), err) {
		return false, nil
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		return false, err
	}
	return false, fmt.Errorf("%w: %s", err, msg)
}

func attachSpecTmuxSessionAbsent(stderr []byte, err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		return false
	}
	msg := string(stderr)
	return strings.Contains(msg, "can't find session") ||
		strings.Contains(msg, "no server running") ||
		(strings.Contains(msg, "error connecting to") &&
			strings.Contains(msg, "No such file or directory"))
}

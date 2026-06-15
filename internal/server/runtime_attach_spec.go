package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"go.kenn.io/middleman/internal/procutil"
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
		return runtimeAttachSpecResponse{}, problemBadRequest(
			CodeBadRequest, "runtime session is not tmux-backed", nil,
		)
	}
	exists, err := attachSpecTmuxSessionExists(ctx, tmuxCommand, tmuxSession)
	if err != nil {
		return runtimeAttachSpecResponse{}, problemServiceUnavailable(
			"check tmux session: " + err.Error(),
		)
	}
	if !exists {
		return runtimeAttachSpecResponse{}, problemNotFound(
			CodeNotFound, "runtime tmux session not found", nil,
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

func runtimeAttachCommand(tmuxCommand []string, tmuxSession string) []string {
	command := append([]string{}, tmuxCommand...)
	if len(command) == 0 {
		command = []string{"tmux"}
	}
	return append(command, "attach-session", "-t", tmuxSession)
}

func attachSpecTmuxSessionExists(
	ctx context.Context,
	command []string,
	session string,
) (bool, error) {
	command = append([]string{}, command...)
	if len(command) == 0 {
		command = []string{"tmux"}
	}
	if strings.TrimSpace(command[0]) == "" {
		return false, errors.New("tmux command is empty")
	}
	args := append([]string{}, command[1:]...)
	args = append(args, "has-session", "-t", session)
	cmd := procutil.CommandContext(ctx, command[0], args...)
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

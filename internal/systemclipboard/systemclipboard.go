// Package systemclipboard writes text to the operating system clipboard.
package systemclipboard

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf16"

	"go.kenn.io/middleman/internal/procutil"
)

var ErrUnavailable = errors.New("system clipboard unavailable")

type Writer interface {
	WriteText(context.Context, string) error
}

type commandRunner func(
	context.Context,
	string,
	[]string,
	string,
) error

type nativeWriter struct {
	goos     string
	getenv   func(string) string
	lookPath func(string) (string, error)
	run      commandRunner
}

type clipboardCommand struct {
	name string
	args []string
}

func NewWriter() Writer {
	return nativeWriter{
		goos:     runtime.GOOS,
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		run:      runCommand,
	}
}

func (w nativeWriter) WriteText(
	ctx context.Context,
	text string,
) error {
	command, err := w.resolveCommand()
	if err != nil {
		return err
	}
	input := text
	if w.goos == "windows" {
		input = encodeUTF16LE(text)
	}
	if err := w.run(ctx, command.name, command.args, input); err != nil {
		return fmt.Errorf("write system clipboard: %w", err)
	}
	return nil
}

func encodeUTF16LE(text string) string {
	codeUnits := utf16.Encode([]rune(text))
	encoded := make([]byte, len(codeUnits)*2)
	for index, codeUnit := range codeUnits {
		binary.LittleEndian.PutUint16(encoded[index*2:], codeUnit)
	}
	return string(encoded)
}

func (w nativeWriter) resolveCommand() (clipboardCommand, error) {
	for _, candidate := range w.commandCandidates() {
		path, err := w.lookPath(candidate.name)
		if err == nil {
			candidate.name = path
			return candidate, nil
		}
	}
	return clipboardCommand{}, ErrUnavailable
}

func (w nativeWriter) commandCandidates() []clipboardCommand {
	switch w.goos {
	case "darwin":
		return []clipboardCommand{{name: "pbcopy"}}
	case "windows":
		return []clipboardCommand{{name: "clip.exe"}}
	case "linux", "freebsd", "openbsd", "netbsd":
		var candidates []clipboardCommand
		if w.getenv("WAYLAND_DISPLAY") != "" {
			candidates = append(candidates, clipboardCommand{name: "wl-copy"})
		}
		if w.getenv("DISPLAY") != "" {
			candidates = append(
				candidates,
				clipboardCommand{
					name: "xclip",
					args: []string{"-selection", "clipboard"},
				},
				clipboardCommand{
					name: "xsel",
					args: []string{"--clipboard", "--input"},
				},
			)
		}
		return candidates
	default:
		return nil
	}
}

func runCommand(
	ctx context.Context,
	name string,
	args []string,
	text string,
) error {
	release, err := procutil.TryAcquire(
		ctx,
		"clipboard subprocess capacity",
	)
	if err != nil {
		return err
	}
	defer release()

	command := procutil.CommandContext(ctx, name, args...)
	command.Stdin = strings.NewReader(text)
	return command.Run()
}

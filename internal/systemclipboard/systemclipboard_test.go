package systemclipboard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeWriterSelectsPlatformClipboardCommand(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		env       map[string]string
		paths     map[string]string
		wantName  string
		wantArgs  []string
		text      string
		wantInput string
	}{
		{
			name:      "macOS",
			goos:      "darwin",
			paths:     map[string]string{"pbcopy": "/usr/bin/pbcopy"},
			wantName:  "/usr/bin/pbcopy",
			text:      "clipboard — Unicode\u00a0text",
			wantInput: "clipboard — Unicode\u00a0text",
		},
		{
			name: "Wayland",
			goos: "linux",
			env:  map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
			paths: map[string]string{
				"wl-copy": "/usr/bin/wl-copy",
				"xclip":   "/usr/bin/xclip",
			},
			wantName: "/usr/bin/wl-copy",
		},
		{
			name: "X11",
			goos: "linux",
			env:  map[string]string{"DISPLAY": ":0"},
			paths: map[string]string{
				"xclip": "/usr/bin/xclip",
			},
			wantName: "/usr/bin/xclip",
			wantArgs: []string{"-selection", "clipboard"},
		},
		{
			name: "Windows",
			goos: "windows",
			paths: map[string]string{
				"clip.exe": `C:\Windows\System32\clip.exe`,
			},
			wantName: `C:\Windows\System32\clip.exe`,
			text:     "Hé😀",
			wantInput: string([]byte{
				0x48, 0x00,
				0xe9, 0x00,
				0x3d, 0xd8, 0x00, 0xde,
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			t.Setenv("LC_ALL", "C")
			t.Setenv("MIDDLEMAN_CLIPBOARD_TEST_ENV", "preserved")

			var gotEnvironment []string
			var gotInput string
			writer := nativeWriter{
				goos: tt.goos,
				getenv: func(name string) string {
					return tt.env[name]
				},
				lookPath: func(name string) (string, error) {
					path, ok := tt.paths[name]
					if !ok {
						return "", errors.New("not found")
					}
					return path, nil
				},
				run: func(
					_ context.Context,
					name string,
					args []string,
					environment []string,
					text string,
				) error {
					assert.Equal(tt.wantName, name)
					assert.Equal(tt.wantArgs, args)
					gotEnvironment = environment
					gotInput = text
					return nil
				},
			}

			text := tt.text
			if text == "" {
				text = "copied through the native clipboard"
			}
			wantInput := tt.wantInput
			if wantInput == "" {
				wantInput = text
			}
			err := writer.WriteText(
				context.Background(),
				text,
			)

			require.NoError(t, err)
			assert.Equal(wantInput, gotInput)
			if tt.goos == "darwin" {
				assert.Equal("en_US.UTF-8", environmentValue(gotEnvironment, "LC_ALL"))
				assert.Equal(
					"preserved",
					environmentValue(gotEnvironment, "MIDDLEMAN_CLIPBOARD_TEST_ENV"),
				)
				assert.Equal(1, environmentKeyCount(gotEnvironment, "LC_ALL"))
			} else {
				assert.Nil(gotEnvironment)
			}
		})
	}
}

func TestNativeWriterRunsPbcopyWithUTF8Locale(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake pbcopy requires a POSIX shell")
	}

	assert := assert.New(t)
	require := require.New(t)
	tempDir := t.TempDir()
	pbcopyPath := filepath.Join(tempDir, "pbcopy")
	localePath := filepath.Join(tempDir, "locale")
	stdinPath := filepath.Join(tempDir, "stdin")
	require.NoError(os.WriteFile(pbcopyPath, []byte(`#!/bin/sh
set -eu
printf '%s' "$LC_ALL" > "$MIDDLEMAN_TEST_PBCOPY_LOCALE"
cat > "$MIDDLEMAN_TEST_PBCOPY_STDIN"
`), 0o755))
	t.Setenv("LC_ALL", "C")
	t.Setenv("MIDDLEMAN_TEST_PBCOPY_LOCALE", localePath)
	t.Setenv("MIDDLEMAN_TEST_PBCOPY_STDIN", stdinPath)

	writer := nativeWriter{
		goos:   "darwin",
		getenv: os.Getenv,
		lookPath: func(string) (string, error) {
			return pbcopyPath, nil
		},
		run: runCommand,
	}
	const text = "clipboard — Unicode\u00a0text"

	require.NoError(writer.WriteText(t.Context(), text))
	locale, err := os.ReadFile(localePath)
	require.NoError(err)
	input, err := os.ReadFile(stdinPath)
	require.NoError(err)

	assert.Equal("en_US.UTF-8", string(locale))
	assert.Equal(text, string(input))
}

func TestNativeWriterReportsUnavailableClipboard(t *testing.T) {
	writer := nativeWriter{
		goos:   "linux",
		getenv: func(string) string { return "" },
		lookPath: func(string) (string, error) {
			return "", errors.New("not found")
		},
		run: func(
			context.Context,
			string,
			[]string,
			[]string,
			string,
		) error {
			return nil
		},
	}

	err := writer.WriteText(context.Background(), "copy me")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnavailable)
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if after, ok := strings.CutPrefix(entry, prefix); ok {
			return after
		}
	}
	return ""
}

func environmentKeyCount(environment []string, key string) int {
	prefix := key + "="
	count := 0
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			count++
		}
	}
	return count
}

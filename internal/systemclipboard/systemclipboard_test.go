package systemclipboard

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeWriterSelectsPlatformClipboardCommand(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		env      map[string]string
		paths    map[string]string
		wantName string
		wantArgs []string
	}{
		{
			name:     "macOS",
			goos:     "darwin",
			paths:    map[string]string{"pbcopy": "/usr/bin/pbcopy"},
			wantName: "/usr/bin/pbcopy",
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
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			var gotText string
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
					text string,
				) error {
					assert.Equal(tt.wantName, name)
					assert.Equal(tt.wantArgs, args)
					gotText = text
					return nil
				},
			}

			err := writer.WriteText(
				context.Background(),
				"copied through the native clipboard",
			)

			require.NoError(t, err)
			assert.Equal("copied through the native clipboard", gotText)
		})
	}
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
			string,
		) error {
			return nil
		},
	}

	err := writer.WriteText(context.Background(), "copy me")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnavailable)
}

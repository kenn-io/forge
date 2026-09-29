//go:build windows

package ptyowner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func ignorePtyOwnerHangupForTest() {}

func TestClientRejectsOversizedWindowsCommandBeforeLaunch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command []string
		tooLong bool
	}{
		{"short", []string{"claude.exe", "--", "review this PR"}, false},
		{"long prompt", []string{"claude.exe", "--", strings.Repeat("x", 40000)}, true},
		// JSON and Windows quoting make this exceed the limit only in the helper command.
		{"quoted prompt", []string{"claude.exe", "--", strings.Repeat(`"`, 9000)}, true},
		{"unicode and flags", []string{"claude.exe", "--settings", strings.Repeat("x", 1000), "--", strings.Repeat("\U00010400", 16000)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			root := t.TempDir()
			client := &Client{
				Root: root, ExePath: filepath.Join(root, "missing-helper.exe"), Command: tc.command,
			}
			err := client.Ensure(t.Context(), "test-session", root)
			if tc.tooLong {
				require.ErrorIs(err, ErrCommandLineTooLong)
			} else {
				// A short command reaches process creation; no helper is installed in the fixture.
				require.ErrorIs(err, os.ErrNotExist)
			}
		})
	}
}

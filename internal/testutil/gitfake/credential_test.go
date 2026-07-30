package gitfake

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
)

func TestCredentialHelperRunner(t *testing.T) {
	dir := t.TempDir()
	executableHelper := filepath.Join(dir, "credential-helper")
	require.NoError(t, os.WriteFile(
		executableHelper,
		[]byte("#!/bin/sh\nprintf 'password=executable\\n'\n"),
		0o755,
	))

	tests := []struct {
		name   string
		helper string
		want   string
	}{
		{
			name:   "inline shell snippet",
			helper: `!f() { [ "$1" = get ] && printf 'password=inline\n'; }; f`,
			want:   "password=inline\n",
		},
		{
			name:   "executable path",
			helper: executableHelper,
			want:   "password=executable\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			script := filepath.Join(t.TempDir(), "run-helper")
			require.NoError(os.WriteFile(
				script,
				[]byte("#!/bin/sh\nset -eu\n"+CredentialHelperRunner+`run_credential_helper "$HELPER" get
`),
				0o755,
			))
			cmd := procutil.Command("sh", script)
			cmd.Env = append(os.Environ(), "HELPER="+tt.helper)
			got, err := cmd.Output()
			require.NoError(err)
			assert.Equal(tt.want, string(got))
		})
	}
}

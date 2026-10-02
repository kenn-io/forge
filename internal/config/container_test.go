package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Authentication must gate wildcard exposure without changing specific binds.
func TestContainerBindRequiresAuthForWildcard(t *testing.T) {
	for _, tc := range []struct {
		host      string
		auth      bool
		wantError bool
	}{
		{"0.0.0.0", true, false},
		{"::", true, false},
		{"0.0.0.0", false, true},
		{"::", false, true},
		{"127.0.0.1", false, false},
		{"192.0.2.10", false, false},
	} {
		t.Run(fmt.Sprintf("%s/auth=%t", tc.host, tc.auth), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, "host = %q\n[api]\nrequire_auth = %t\n", tc.host, tc.auth), 0o600))
			_, err := Load(path)
			if tc.wantError {
				require.ErrorContains(t, err, "is unspecified; bind a specific address")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

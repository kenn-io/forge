package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeImportPlatformRejectsUnsafeHosts(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{"url userinfo", "https://gitlab.com@attacker.example/"},
		{"bare userinfo", "gitlab.com@attacker.example"},
		{"malformed port", "gitlab.example.com:bad"},
		{"control character", "gitlab.example.com\nattacker.example"},
		{"whitespace", "gitlab.example.com attacker.example"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := normalizeImportPlatform("gitlab", tt.host)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "platform_host")
		})
	}
}

package daemonruntime

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWildcardDiscoveryPublishesConcreteLoopback(t *testing.T) {
	for _, tc := range []struct{ bind, want string }{
		{"0.0.0.0", "127.0.0.1:8091"},
		{"::", "[::1]:8091"},
		{"192.0.2.10", "192.0.2.10:8091"},
	} {
		t.Run(tc.bind, func(t *testing.T) {
			identity, err := NewIdentity(&net.TCPAddr{IP: net.ParseIP(tc.bind), Port: 8091}, IdentityOptions{
				Version: "v-test", RequireAuth: true, DataDir: t.TempDir(),
				ConfigPath: filepath.Join(t.TempDir(), "config.toml"),
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, identity.Record.Address)
			assert.Equal(t, tc.want, identity.LockMetadata.ListenAddr)
		})
	}
}

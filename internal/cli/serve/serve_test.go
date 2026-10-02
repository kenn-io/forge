package serve

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListenerFlagsRemainOptionalAndDoNotChangeEnvironment(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		explicit bool
	}{
		{nil, false}, {[]string{"--host", "127.0.0.1", "--port", "8093"}, true},
	} {
		t.Run("flags", func(t *testing.T) {
			t.Setenv("KENN_FORGE_HOST", "0.0.0.0")
			t.Setenv("KENN_FORGE_PORT", "broken")
			called := false
			cmd := NewCommand(func(opts Options) error {
				called = true
				if tc.explicit {
					require.NotNil(t, opts.ConfigOverrides.Host)
					require.NotNil(t, opts.ConfigOverrides.Port)
					assert.Equal(t, "127.0.0.1", *opts.ConfigOverrides.Host)
					assert.Equal(t, 8093, *opts.ConfigOverrides.Port)
				} else {
					assert.Nil(t, opts.ConfigOverrides.Host)
					assert.Nil(t, opts.ConfigOverrides.Port)
				}
				return nil
			})
			cmd.SetArgs(tc.args)
			require.NoError(t, cmd.Execute())
			assert.True(t, called)
			assert.Equal(t, "0.0.0.0", os.Getenv("KENN_FORGE_HOST"))
			assert.Equal(t, "broken", os.Getenv("KENN_FORGE_PORT"))
		})
	}
}

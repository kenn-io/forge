package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/embedlocal"
)

func TestResolveSemanticEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name             string
		forced           bool
		preferred, local string
		wantBuiltin      bool
	}{
		{"automatic builtin", false, "http://127.0.0.1:1234/v1", "http://127.0.0.1:1234/v1", true},
		{"automatic ollama", false, "http://127.0.0.1:11434/v1", "http://127.0.0.1:1234/v1", false},
		{"forced builtin", true, "http://127.0.0.1:1234/v1", "http://127.0.0.1:1234/v1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := localEmbeddingDeps{endpoint: func(_ context.Context, opts embedlocal.Options) (string, error) {
				if opts.SkipOllama {
					return tc.local, nil
				}
				return tc.preferred, nil
			}}
			endpoint, builtin, err := deps.resolveSemantic(t.Context(), tc.forced)
			require.NoError(t, err)
			assert.Equal(t, tc.preferred, endpoint)
			assert.Equal(t, tc.wantBuiltin, builtin)
		})
	}
}

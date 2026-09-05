package platform_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func TestRawBytesCanonicalRepresentation(t *testing.T) {
	assert := assert.New(t)
	for _, tc := range []struct{ raw, encoded string }{{"", ""}, {"\xff", "/w=="}, {"é", "w6k="}, {"text", "dGV4dA=="}} {
		value := platform.NewRawBytes([]byte(tc.raw))
		assert.Equal(tc.encoded, value.Base64)
		decoded, err := value.Bytes()
		require.NoError(t, err)
		assert.Equal(tc.raw, string(decoded))
	}
	for _, encoded := range []string{"/w", "/x==", "/w==\n", "_w==", "====", "!"} {
		_, err := (platform.RawBytes{Base64: encoded}).Bytes()
		assert.Error(err)
	}
}

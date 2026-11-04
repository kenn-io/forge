package contentcoding

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEveryLadderCodingRoundTrips(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte(`{"title":"Add widget"}`), 200)
	for _, coding := range []string{"br", "zstd", "gzip"} {
		t.Run(coding, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			var encoded bytes.Buffer
			writer, err := NewWriter(&encoded, coding)
			require.NoError(err)
			_, err = writer.Write(payload)
			require.NoError(err)
			require.NoError(writer.Close())
			assert.Less(t, encoded.Len(), len(payload))

			reader, err := NewReader(&encoded, coding)
			require.NoError(err)
			decoded, err := io.ReadAll(reader)
			require.NoError(err)
			require.NoError(reader.Close())
			assert.Equal(t, payload, decoded)
		})
	}
}

func TestLadderPrefersBrotliThenZstdThenGzip(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "br, zstd, gzip", AcceptEncoding)
	_, ok := Rank("deflate")
	assert.False(t, ok)
}

func TestUnsupportedCodingIsRejected(t *testing.T) {
	t.Parallel()
	_, err := NewWriter(io.Discard, "compress")
	require.ErrorContains(t, err, "compress")
	_, err = NewReader(bytes.NewReader(nil), "compress")
	assert.ErrorContains(t, err, "compress")
}

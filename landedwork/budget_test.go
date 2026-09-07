package landedwork

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A ReaderFrom promoted from an embedded buffer would bypass the byte meter.
func TestBufferMetersCopy(t *testing.T) {
	b := &boundedBuffer{meter: &meter{limits: Limits{InputBytes: 3}}}
	_, err := io.Copy(b, struct{ io.Reader }{strings.NewReader("oversized")})
	require.ErrorIs(t, err, ErrInputBudget)
	assert.Empty(t, b.Bytes())
}

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

func TestCommitStreamStopsBeforeRetainingOverBudgetRecord(t *testing.T) {
	stream := &commitStream{meter: &meter{limits: Limits{InputBytes: 1024, Records: 1, Nodes: 10}}}
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	_, err := stream.Write([]byte(first + "\n" + second + "\n"))
	require.ErrorIs(t, err, ErrInputBudget)
	assert.Equal(t, []string{first}, stream.ids)
}

func TestResultOwnershipBudget(t *testing.T) {
	// One landing plus two labels and three retained commit-list entries.
	r := Result{Landings: []Landing{{CandidateID: "7", Before: "a", Terminal: "b",
		Proofs: []string{"rebase", "squash"}, Spine: []string{"b"}, Source: []string{"c"}, Introduced: []string{"b"}}}}
	for _, tc := range []struct {
		name           string
		bytes, records int64
		fails          bool
	}{
		{"exact", 18, 6, false},
		{"bytes", 17, 6, true},
		{"records", 18, 5, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkResultOutput(r, Limits{OutputBytes: tc.bytes, Records: tc.records})
			if tc.fails {
				require.ErrorIs(t, err, ErrOutputBudget)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

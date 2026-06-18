package localruntime

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClampWinsizeDim(t *testing.T) {
	assert := assert.New(t)
	cases := []struct {
		name string
		in   int
		want uint16
	}{
		{"zero floors to one", 0, 1},
		{"negative floors to one", -10, 1},
		{"minimum", 1, 1},
		{"typical", 120, 120},
		{"uint16 max", math.MaxUint16, math.MaxUint16},
		{"above uint16 max caps", math.MaxUint16 + 1, math.MaxUint16},
		{"large value caps", 5_000_000, math.MaxUint16},
	}
	for _, tc := range cases {
		assert.Equalf(tc.want, clampWinsizeDim(tc.in), "case %s", tc.name)
	}
}

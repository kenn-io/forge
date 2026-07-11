package platform

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMutationDefinitelyRejected(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "stale state", err: ErrStaleState, want: true},
		{name: "permission denied", err: ErrPermissionDenied, want: true},
		{name: "wrapped conflict", err: fmt.Errorf("apply: %w", ErrConflict), want: true},
		{name: "unknown typed outcome", err: &Error{Code: PlatformErrorCode("upstream_failure")}},
		{name: "transport error", err: errors.New("connection reset")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MutationDefinitelyRejected(tt.err))
		})
	}
}

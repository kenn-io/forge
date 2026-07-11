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
		{name: "explicit rejection", err: &Error{
			Code: ErrCodeStaleState, MutationOutcome: MutationOutcomeDefinitelyRejected,
		}, want: true},
		{name: "wrapped explicit rejection", err: fmt.Errorf("apply: %w", &Error{
			Code: ErrCodeConflict, MutationOutcome: MutationOutcomeDefinitelyRejected,
		}), want: true},
		{name: "code without outcome", err: ErrStaleState},
		{name: "unknown typed outcome", err: &Error{Code: PlatformErrorCode("upstream_failure")}},
		{name: "transport error", err: errors.New("connection reset")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MutationDefinitelyRejected(tt.err))
		})
	}
}

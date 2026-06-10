package msgvault

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"

	Assert "github.com/stretchr/testify/assert"
)

func TestClassifyMapsStatusToCode(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		wantCode string
	}{
		{"401 unauthorized", http.StatusUnauthorized, "unauthorized"},
		{"403 unauthorized", http.StatusForbidden, "unauthorized"},
		{"404 not found", http.StatusNotFound, "not_found"},
		{"429 rate limited", http.StatusTooManyRequests, "rate_limited"},
		{"500 upstream error", http.StatusInternalServerError, "upstream_error"},
		{"503 upstream error", http.StatusServiceUnavailable, "upstream_error"},
		{"418 upstream error", http.StatusTeapot, "upstream_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Assert.Equal(t, tc.wantCode, Classify(&Error{Status: tc.status}))
		})
	}
}

func TestClassifyNetworkErrorsAsDown(t *testing.T) {
	Assert.Equal(t, "down", Classify(&net.OpError{Op: "dial"}))
	Assert.Equal(t, "timeout", Classify(context.DeadlineExceeded))
}

func TestClassifyUnknownErrorIsUnknown(t *testing.T) {
	Assert.Equal(t, "unknown", Classify(errors.New("???")))
	Assert.Empty(t, Classify(nil))
}

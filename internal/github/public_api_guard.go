package github

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// ErrPublicGitHubAPIBlocked marks a default-test request to the public GitHub API.
var ErrPublicGitHubAPIBlocked = errors.New("blocked public GitHub API request during tests")

type publicGitHubAPIGuardTransport struct {
	base http.RoundTripper
}

func wrapPublicGitHubAPIGuard(base http.RoundTripper) http.RoundTripper {
	if !publicGitHubAPIGuardEnabled() {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return publicGitHubAPIGuardTransport{base: base}
}

func publicGitHubAPIGuardEnabled() bool {
	return testing.Testing()
}

func (transport publicGitHubAPIGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req != nil && req.URL != nil && strings.EqualFold(req.URL.Hostname(), "api.github.com") {
		return nil, fmt.Errorf("%w: %s %s", ErrPublicGitHubAPIBlocked, req.Method, req.URL.Redacted())
	}
	base := transport.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

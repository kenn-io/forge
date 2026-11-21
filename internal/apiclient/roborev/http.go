package roborev

import (
	"context"
	"net/http"
	"strings"

	"go.kenn.io/forge/internal/tracing"
	"go.kenn.io/forge/platform"
)

type fixedCredential string

func (key fixedCredential) Token(context.Context) (string, error) { return string(key), nil }
func (fixedCredential) Invalidate(string)                         {}

// NewHTTPClient binds a startup credential to one daemon origin. Callers own
// request timeouts so the same transport can also serve long-lived streams.
func NewHTTPClient(endpoint, key string) *http.Client {
	if key == "" {
		return &http.Client{Transport: tracing.HTTPTransport(nil)}
	}
	var base http.RoundTripper
	if strings.HasPrefix(strings.ToLower(endpoint), "http:") {
		// A forward proxy would see the reusable bearer key on cleartext HTTP.
		direct := http.DefaultTransport.(*http.Transport).Clone() //nolint:forbidigo // trace the dedicated transport below
		direct.Proxy = nil
		base = direct
	}
	return &http.Client{
		Transport: platform.AuthTransport{
			Source:        fixedCredential(key),
			Base:          tracing.HTTPTransport(base),
			AllowedOrigin: endpoint,
			SetHeader:     platform.BearerAuthHeader,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

package roborev

import (
	"context"
	"net/http"

	"go.kenn.io/forge/internal/tracing"
	"go.kenn.io/forge/platform"
)

type fixedCredential string

func (key fixedCredential) Token(context.Context) (string, error) { return string(key), nil }
func (fixedCredential) Invalidate(string)                         {}

// NewHTTPClient binds a startup credential to one daemon origin. Callers own
// request timeouts so the same transport can also serve long-lived streams.
func NewHTTPClient(endpoint, key string) *http.Client {
	return &http.Client{
		Transport: platform.AuthTransport{
			Source:        fixedCredential(key),
			Base:          tracing.HTTPTransport(nil),
			AllowedOrigin: endpoint,
			SetHeader: func(req *http.Request, token string) {
				req.Header.Del("Authorization")
				if token != "" {
					platform.BearerAuthHeader(req, token)
				}
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

package tokenauthtest

import (
	"context"

	"go.kenn.io/forge/internal/tokenauth"
)

type staticSource string

// Source returns an immutable token source for provider and transport tests.
func Source(token string) tokenauth.Source {
	return staticSource(token)
}

func (s staticSource) Token(context.Context) (string, error) {
	return string(s), nil
}

func (staticSource) Invalidate(string) {}

func (staticSource) Descriptor() tokenauth.Descriptor {
	return tokenauth.Descriptor{Key: tokenauth.Key{Platform: "test", Host: "test"}}
}

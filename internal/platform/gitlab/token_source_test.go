package gitlab

import (
	"context"

	"go.kenn.io/middleman/internal/tokenauth"
)

type staticTestTokenSource string

func testTokenSource(token string) tokenauth.Source {
	return staticTestTokenSource(token)
}

func (s staticTestTokenSource) Token(context.Context) (string, error) {
	return string(s), nil
}

func (s staticTestTokenSource) Invalidate() {}

func (s staticTestTokenSource) Descriptor() tokenauth.Descriptor {
	return tokenauth.Descriptor{Key: tokenauth.Key{Platform: "test", Host: "test"}}
}

type mutableTestTokenSource struct {
	token       string
	invalidated int
}

func newMutableTestTokenSource(token string) *mutableTestTokenSource {
	return &mutableTestTokenSource{token: token}
}

func (s *mutableTestTokenSource) Token(context.Context) (string, error) {
	return s.token, nil
}

func (s *mutableTestTokenSource) Invalidate() {
	s.invalidated++
}

func (s *mutableTestTokenSource) Descriptor() tokenauth.Descriptor {
	return tokenauth.Descriptor{Key: tokenauth.Key{Platform: "test", Host: "test"}}
}

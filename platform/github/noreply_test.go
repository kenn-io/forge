package github_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/platform/github"
)

func TestParseNoreply(t *testing.T) {
	for _, tc := range []struct{ email, host, id string }{
		{"123+user-a@users.noreply.github.com", "github.com", "123"},
		{" <123+User-A@Users.NoReply.Code.Example.Org> ", "code.example.org", "123"},
		{"9007199254740993+user-a@users.noreply.github.com", "github.com", "9007199254740993"},
		{"user-a@users.noreply.github.com", "", ""},
		{"noreply@github.com", "", ""},
		{"0+user-a@users.noreply.github.com", "", ""},
		{"0123+user-a@users.noreply.github.com", "", ""},
		{"-123+user-a@users.noreply.github.com", "", ""},
		{"123++user-a@users.noreply.github.com", "", ""},
		{"123+user+a@users.noreply.github.com", "", ""},
		{"123+@users.noreply.github.com", "", ""},
		{"123+user-a@@users.noreply.github.com", "", ""},
		{"123+user-a@users.noreply.", "", ""},
		{"123+user-a@users.noreply.github.com:8443", "", ""},
		{"123+user-a@users.noreply.github.com/path", "", ""},
		{"123+user a@users.noreply.github.com", "", ""},
		{"123+user\x7fa@users.noreply.github.com", "", ""},
	} {
		t.Run(tc.email, func(t *testing.T) {
			host, id, ok := github.ParseNoreply(tc.email)
			assert.Equal(t, tc.host, host)
			assert.Equal(t, tc.id, id)
			assert.Equal(t, tc.id != "", ok)
		})
	}
}

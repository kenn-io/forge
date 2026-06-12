package config

import (
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHostKey(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
		want    HostKey
	}{
		{name: "bare host lowercases", in: "mm.local", want: HostKey{Host: "mm.local", Port: ""}},
		{name: "bare host uppercase folds", in: "LOCALHOST", want: HostKey{Host: "localhost", Port: ""}},
		{name: "host with port", in: "mm.local:8091", want: HostKey{Host: "mm.local", Port: "8091"}},
		{name: "ipv4 with port", in: "127.0.0.1:8091", want: HostKey{Host: "127.0.0.1", Port: "8091"}},
		{name: "mixed case with port", in: "MM.Local:8091", want: HostKey{Host: "mm.local", Port: "8091"}},
		{name: "bracketed ipv6 with port", in: "[::1]:8091", want: HostKey{Host: "[::1]", Port: "8091"}},
		{name: "bracketed ipv6 no port", in: "[::1]", want: HostKey{Host: "[::1]", Port: ""}},
		{name: "bracketed ipv6 uppercase folds", in: "[::FFFF:7F00:1]:8091", want: HostKey{Host: "[::ffff:7f00:1]", Port: "8091"}},
		{name: "whitespace trimmed", in: "  mm.local:8091  ", want: HostKey{Host: "mm.local", Port: "8091"}},

		{name: "empty string rejected", in: "", wantErr: true},
		{name: "whitespace only rejected", in: "   ", wantErr: true},
		{name: "port-only rejected", in: ":8091", wantErr: true},
		{name: "unbracketed ipv6 with port rejected", in: "::1:8091", wantErr: true},
		{name: "unbracketed ipv6 no port rejected", in: "::1", wantErr: true},
		{name: "port zero rejected", in: "mm.local:0", wantErr: true},
		{name: "port too high rejected", in: "mm.local:99999", wantErr: true},
		{name: "port negative rejected", in: "mm.local:-1", wantErr: true},
		{name: "non-numeric port rejected", in: "mm.local:abc", wantErr: true},
		{name: "missing closing bracket rejected", in: "[::1:8091", wantErr: true},
		{name: "bracketed non-ipv6 rejected", in: "[127.0.0.1]:8091", wantErr: true},
		{name: "trailing garbage after bracketed host", in: "[::1]junk", wantErr: true},
		{name: "empty port after colon", in: "mm.local:", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			got, err := ParseHostKey(tc.in)
			if tc.wantErr {
				assert.Error(err)
				return
			}
			require.NoError(t, err)
			assert.Equal(tc.want, got)
		})
	}
}

func TestHostKeyString(t *testing.T) {
	cases := []struct {
		name string
		key  HostKey
		want string
	}{
		{name: "bare host", key: HostKey{Host: "mm.local"}, want: "mm.local"},
		{name: "with port", key: HostKey{Host: "mm.local", Port: "8091"}, want: "mm.local:8091"},
		{name: "ipv6 bracketed", key: HostKey{Host: "[::1]", Port: "8091"}, want: "[::1]:8091"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Assert.Equal(t, tc.want, tc.key.String())
		})
	}
}

func TestHostKeyEqual(t *testing.T) {
	assert := Assert.New(t)
	a := HostKey{Host: "mm.local", Port: "8091"}
	b := HostKey{Host: "mm.local", Port: "8091"}
	c := HostKey{Host: "mm.local", Port: ""}
	d := HostKey{Host: "other.local", Port: "8091"}
	assert.True(a.Equal(b))
	assert.False(a.Equal(c))
	assert.False(a.Equal(d))
}

func TestHostKeyValid(t *testing.T) {
	assert := Assert.New(t)
	assert.False(HostKey{}.Valid())
	assert.False(HostKey{Host: "mm.local"}.Valid())
	assert.False(HostKey{Port: "8091"}.Valid())
	assert.True(HostKey{Host: "mm.local", Port: "8091"}.Valid())
}

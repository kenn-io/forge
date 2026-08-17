package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActivityCursorPreservesTimestampPrecision(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	want := time.Date(2026, 8, 17, 20, 52, 50, 123456000, time.UTC)
	got, source, sourceID, err := DecodeCursor(EncodeCursor(want, "pre", 41))
	require.NoError(err)
	assert.Equal(want, got)
	assert.Equal("pre", source)
	assert.Equal(int64(41), sourceID)
}

func TestIsBotLogin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		login string
		want  bool
	}{
		{login: "dependabot[bot]", want: true},
		{login: "release-bot", want: true},
		{login: "BuildBot", want: true},
		{login: "robotics", want: false},
		{login: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.login, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsBotLogin(tt.login))
		})
	}
}

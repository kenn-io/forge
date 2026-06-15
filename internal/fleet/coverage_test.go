package fleet

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformCoverageActiveUnsupportedOnly(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	gh := "github.com"
	active := PlatformCoverage(RawProject{PlatformRepo: "o/r", PlatformHost: "github.com"}, &gh)
	require.NotNil(active, "want active")
	assert.Equal("active", *active)

	gl := "gitlab.com"
	other := PlatformCoverage(RawProject{PlatformRepo: "o/r", PlatformHost: "github.com"}, &gl)
	require.NotNil(other, "want unsupported")
	assert.Equal("unsupported", *other)

	none := PlatformCoverage(RawProject{}, &gh) // no platform repo
	assert.Nil(none, "want nil for no platform repo")
}

func TestPlatformCoverageNoActiveHost(t *testing.T) {
	none := PlatformCoverage(RawProject{PlatformRepo: "o/r", PlatformHost: "github.com"}, nil)
	assert.Nil(t, none, "want nil when no active host")
}

func TestActivePlatformHostPicksMajority(t *testing.T) {
	host := ActivePlatformHost([]RawProject{
		{PlatformRepo: "a/b", PlatformHost: "github.com"},
		{PlatformRepo: "c/d", PlatformHost: "github.com"},
		{PlatformRepo: "e/f", PlatformHost: "gitlab.com"},
		{PlatformRepo: "g/h"}, // empty host -> defaults to github.com
	})
	require.NotNil(t, host, "want github.com")
	assert.Equal(t, "github.com", *host)
	assert.Nil(t, ActivePlatformHost([]RawProject{{Name: "x"}}), "want nil when no project has a platform repo")
}

func TestActivePlatformHostTieBreakDefault(t *testing.T) {
	host := ActivePlatformHost([]RawProject{
		{PlatformRepo: "user/repo1", PlatformHost: "github.com"},
		{PlatformRepo: "corp/repo1", PlatformHost: "ghe.corp.com"},
	})
	require.NotNil(t, host, "want github.com on tie")
	assert.Equal(t, "github.com", *host)
}

func TestActivePlatformHostNonDefaultMajorityWins(t *testing.T) {
	host := ActivePlatformHost([]RawProject{
		{PlatformRepo: "user/repo1", PlatformHost: "github.com"},
		{PlatformRepo: "corp/repo1", PlatformHost: "ghe.corp.com"},
		{PlatformRepo: "corp/repo2", PlatformHost: "ghe.corp.com"},
	})
	require.NotNil(t, host, "want ghe.corp.com")
	assert.Equal(t, "ghe.corp.com", *host)
}

func TestActivePlatformHostTiedNonDefaultLexical(t *testing.T) {
	host := ActivePlatformHost([]RawProject{
		{PlatformRepo: "a/one", PlatformHost: "ghe-b.corp.com"},
		{PlatformRepo: "b/one", PlatformHost: "ghe-a.corp.com"},
	})
	require.NotNil(t, host, "want ghe-a.corp.com")
	assert.Equal(t, "ghe-a.corp.com", *host)
}

func TestPlatformCoverageDegradedWhenBackendNotReady(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	host := "github.com"
	no := false
	yes := true
	base := RawProject{PlatformRepo: "o/r", PlatformHost: "github.com"}

	notReady := base
	notReady.BackendReady = &no
	got := PlatformCoverage(notReady, &host)
	require.NotNil(got, "want degraded")
	assert.Equal("degraded", *got)

	ready := base
	ready.BackendReady = &yes
	readyGot := PlatformCoverage(ready, &host)
	require.NotNil(readyGot, "want active when ready")
	assert.Equal("active", *readyGot)

	// nil readiness = unknown = active (back-compat with today's behavior).
	unknownGot := PlatformCoverage(base, &host)
	require.NotNil(unknownGot, "want active when readiness unknown")
	assert.Equal("active", *unknownGot)
}

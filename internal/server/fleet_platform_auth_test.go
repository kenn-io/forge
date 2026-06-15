package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
)

func TestFleetPlatformAuthMonitorCachesResolvedSignal(t *testing.T) {
	require := require.New(t)
	calls := 0
	m := &fleetPlatformAuthMonitor{
		resolve:  func() bool { calls++; return true },
		interval: time.Hour,
	}
	require.Nil(m.authenticated(), "auth is unknown until the first resolve")

	m.runOnce()
	got := m.authenticated()
	require.NotNil(got)
	require.True(*got)
	require.Equal(1, calls)
}

func TestFleetPlatformAuthMonitorStoresUnauthenticated(t *testing.T) {
	require := require.New(t)
	m := &fleetPlatformAuthMonitor{
		resolve:  func() bool { return false },
		interval: time.Hour,
	}
	m.runOnce()
	got := m.authenticated()
	require.NotNil(got, "a resolved false is a concrete signal, not unknown")
	require.False(*got)
}

func TestNilFleetPlatformAuthMonitorAuthenticatedIsNil(t *testing.T) {
	var m *fleetPlatformAuthMonitor
	require.Nil(t, m.authenticated())
}

func TestPlatformAuthResolverNilConfigIsUnauthenticated(t *testing.T) {
	require.False(t, platformAuthResolver(func() *config.Config { return nil })())
}

func TestPlatformAuthResolverEnvTokenIsAuthenticated(t *testing.T) {
	require := require.New(t)
	t.Setenv("MIDDLEMAN_PLATFORM_AUTH_TEST_TOKEN", "tok")
	cfg := &config.Config{GitHubTokenEnv: "MIDDLEMAN_PLATFORM_AUTH_TEST_TOKEN"}
	require.True(platformAuthResolver(func() *config.Config { return cfg })(),
		"a resolvable env token means the platform backend is authenticated")
}

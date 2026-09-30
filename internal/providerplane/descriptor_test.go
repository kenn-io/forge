package providerplane

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/platform"
)

func TestRepositoryDescriptorValidatesHubFacts(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	observedAt := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	descriptor, err := BuildRepositoryDescriptor(RepositorySnapshot{
		Provider:      "github",
		PlatformHost:  "github.com",
		Key:           platform.RepositoryIDKey(1001),
		Owner:         "acme",
		Name:          "widget",
		CloneURL:      "https://github.com/acme/widget.git",
		DefaultBranch: "main",
		ObservedAt:    observedAt,
		Stale:         true,
	})
	require.NoError(err)
	assert.Equal(federation.ProtocolVersion, descriptor.ProtocolVersion)
	assert.Equal(platform.RepositoryIDKey(1001), descriptor.Key)
	assert.Equal(observedAt, descriptor.ObservedAt)
	assert.True(descriptor.Stale)
	require.NoError(descriptor.Validate())
	require.NoError(descriptor.ValidateRoute(RepositoryRoute{
		Provider: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}))
}

func TestRepositoryDescriptorRejectsUntrustedFacts(t *testing.T) {
	valid := RepositoryDescriptor{
		ProtocolVersion: federation.ProtocolVersion,
		Provider:        "github",
		PlatformHost:    "github.com",
		Key:             platform.RepositoryIDKey(1001),
		Owner:           "acme",
		Name:            "widget",
		CloneURL:        "https://github.com/acme/widget.git",
		DefaultBranch:   "main",
		ObservedAt: time.Date(
			2026, time.August, 22, 12, 0, 0, 0, time.UTC,
		),
	}

	tests := map[string]func(*RepositoryDescriptor){
		"missing stable identity": func(value *RepositoryDescriptor) {
			value.Key = platform.RepositoryKey{}
		},
		"invalid clone URL": func(value *RepositoryDescriptor) {
			value.CloneURL = "https://github.com/other/widget.git"
		},
		"provider host mismatch": func(value *RepositoryDescriptor) {
			value.PlatformHost = "gitlab.com"
			value.CloneURL = "https://gitlab.com/acme/widget.git"
		},
		"bitbucket public host mismatch": func(value *RepositoryDescriptor) {
			value.PlatformHost = "bitbucket.org"
			value.CloneURL = "https://bitbucket.org/acme/widget.git"
		},
		"protocol mismatch": func(value *RepositoryDescriptor) {
			value.ProtocolVersion++
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := valid
			mutate(&value)
			assert.Error(t, value.Validate())
		})
	}
}

func TestDiffDescriptorUsesOneHubSnapshot(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	descriptor, err := BuildDiffDescriptor(DiffSnapshot{
		Repository: RepositorySnapshot{
			Provider: "github", PlatformHost: "github.com", Key: platform.RepositoryIDKey(1001),
			Owner: "acme", Name: "widget",
			CloneURL: "https://github.com/acme/widget.git", DefaultBranch: "main",
			ObservedAt: time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC),
		},
		PullNumber:       42,
		SnapshotRevision: 19,
		PlatformBaseSHA:  "platform-base-sha",
		PlatformHeadSHA:  "platform-head-sha",
		DiffBaseSHA:      "base-sha",
		MergeBaseSHA:     "merge-base-sha",
		DiffHeadSHA:      "head-sha",
		Stale:            true,
	})
	require.NoError(err)
	assert.Equal(platform.RepositoryIDKey(1001), descriptor.Repository.Key)
	assert.Equal("base-sha", descriptor.DiffBaseSHA)
	assert.Equal("merge-base-sha", descriptor.MergeBaseSHA)
	assert.Equal("head-sha", descriptor.DiffHeadSHA)
	assert.Equal(uint64(19), descriptor.SnapshotRevision)
	assert.True(descriptor.Stale)
	require.NoError(descriptor.Validate())
}

func TestBitbucketDataCenterRepositoryDescriptor(t *testing.T) {
	for _, remote := range []string{"https://code.example.test:8443/scm/PROJECT/repo.git", "ssh://git@code.example.test:7999/PROJECT/repo.git"} {
		descriptor, err := BuildRepositoryDescriptor(RepositorySnapshot{Provider: "bitbucket", PlatformHost: "code.example.test:8443", Key: platform.RepositoryIDKey(17), Owner: "PROJECT", Name: "repo", CloneURL: remote, DefaultBranch: "main", ObservedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		require.NoError(t, err)
		require.NoError(t, descriptor.Validate())
		route, err := FederationRemoteRepositoryRoute("bitbucket", "code.example.test:8443", remote)
		require.NoError(t, err)
		require.Equal(t, "PROJECT", route.Owner)
		require.Equal(t, "repo", route.Name)
	}
}

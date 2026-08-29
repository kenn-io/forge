package providerplane

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/federation"
)

func TestRepositoryDescriptorValidatesHubFacts(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	observedAt := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)
	descriptor, err := BuildRepositoryDescriptor(RepositorySnapshot{
		Provider:         "github",
		PlatformHost:     "github.com",
		PlatformRepoID:   "R_1",
		Owner:            "acme",
		Name:             "widget",
		CloneURL:         "https://github.com/acme/widget.git",
		DefaultBranch:    "main",
		SnapshotRevision: 7,
		ObservedAt:       observedAt,
		Stale:            true,
	})
	require.NoError(err)
	assert.Equal(federation.ProtocolVersion, descriptor.ProtocolVersion)
	assert.Equal("R_1", descriptor.PlatformRepoID)
	assert.Equal(uint64(7), descriptor.SnapshotRevision)
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
		ProtocolVersion:  federation.ProtocolVersion,
		Provider:         "github",
		PlatformHost:     "github.com",
		PlatformRepoID:   "R_1",
		Owner:            "acme",
		Name:             "widget",
		CloneURL:         "https://github.com/acme/widget.git",
		DefaultBranch:    "main",
		SnapshotRevision: 1,
		ObservedAt: time.Date(
			2026, time.August, 22, 12, 0, 0, 0, time.UTC,
		),
	}

	tests := map[string]func(*RepositoryDescriptor){
		"missing stable identity": func(value *RepositoryDescriptor) {
			value.PlatformRepoID = ""
		},
		"invalid clone URL": func(value *RepositoryDescriptor) {
			value.CloneURL = "https://github.com/other/widget.git"
		},
		"provider host mismatch": func(value *RepositoryDescriptor) {
			value.PlatformHost = "gitlab.com"
			value.CloneURL = "https://gitlab.com/acme/widget.git"
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
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: "R_1",
			Owner: "acme", Name: "widget",
			CloneURL: "https://github.com/acme/widget.git", DefaultBranch: "main",
			SnapshotRevision: 7,
			ObservedAt:       time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC),
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
	assert.Equal("R_1", descriptor.Repository.PlatformRepoID)
	assert.Equal("base-sha", descriptor.DiffBaseSHA)
	assert.Equal("merge-base-sha", descriptor.MergeBaseSHA)
	assert.Equal("head-sha", descriptor.DiffHeadSHA)
	assert.Equal(uint64(19), descriptor.SnapshotRevision)
	assert.True(descriptor.Stale)
	require.NoError(descriptor.Validate())
}

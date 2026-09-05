package landedwork_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
)

func TestRepositoryCorrespondenceUsesProviderIdentityNotRemoteRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	head := commitFiles(t, dir, "next", map[string]string{"main.go": "one\ntwo\n"})
	gitRun(t, dir, "remote", "add", "origin", "https://ignored-secret@github.com/old-team/old-name.git")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	meter, err := platform.NewMeter(ctx, platform.Budget{MaxRecords: 100, MaxNodes: 100, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20})
	require.NoError(err)
	source, err := landedwork.OpenGit(ctx, dir, meter)
	require.NoError(err)
	defer source.Close()
	for _, id := range []string{"17", "18"} { // Forks share objects, not identity.
		descriptor := platform.LandingRepository{Identity: platform.RepositoryIdentity{Provider: platform.KindGitHub, Instance: "github.com", ID: id}, Owner: "new-team", Name: "new-name", HeadSHA: head}
		proof, err := source.CheckCorrespondence(ctx, descriptor, base, meter)
		require.NoError(err)
		require.True(proof.Complete)
		require.Len(proof.Warnings, 1)
		assert.Equal("remote_route_differs", proof.Warnings[0].Reason)
		assert.Equal("github.com/old-team/old-name", proof.Warnings[0].RemoteRoute)
	}
	gitRun(t, dir, "remote", "set-url", "origin", "ssh://git@github.com:2222/new-team/new-name.git")
	descriptor := platform.LandingRepository{Identity: platform.RepositoryIdentity{Provider: platform.KindGitHub, Instance: "github.com", ID: "17"}, Owner: "new-team", Name: "new-name", HeadSHA: head}
	proof, err := source.CheckCorrespondence(ctx, descriptor, base, meter)
	require.NoError(err)
	assert.True(proof.Complete)
	assert.Empty(proof.Warnings)
	proof, err = source.CheckCorrespondence(ctx, descriptor, strings.Repeat("a", 40), meter)
	require.NoError(err)
	assert.Equal("correspondence_objects_unavailable", proof.Reason)
	descriptor.HeadSHA = base
	_, err = source.CheckCorrespondence(ctx, descriptor, head, meter)
	require.ErrorIs(err, &platform.Error{Code: platform.ErrCodeConflict})
	descriptor.Identity.Instance = "elsewhere.example.org"
	_, err = source.CheckCorrespondence(ctx, descriptor, base, meter)
	require.ErrorIs(err, platform.ErrInvalidArgument)
}

func TestOpenGitRequiresExplicitDirectory(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	meter, err := platform.NewMeter(ctx, platform.Budget{MaxRecords: 10, MaxNodes: 10, MaxBytes: 1024, MaxOutputBytes: 1024})
	require.NoError(t, err)
	_, err = landedwork.OpenGit(ctx, "", meter)
	require.ErrorIs(t, err, platform.ErrInvalidArgument)
}

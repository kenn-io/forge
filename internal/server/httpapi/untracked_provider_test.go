package httpapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"
)

func TestUntrackedOpenDistinguishesProvidersOnSameHost(t *testing.T) {
	for _, tc := range []struct {
		tracked platform.Kind
		opened  string
	}{
		{platform.KindGitHub, "gitlab"},
		{platform.KindGitLab, "github"},
	} {
		t.Run(tc.opened, func(t *testing.T) {
			database := dbtest.Open(t)
			syncer := ghclient.NewSyncer(nil, database, nil, []ghclient.RepoRef{{Platform: tc.tracked, PlatformHost: "code.example.com", Owner: "group", Name: "project"}}, time.Minute, nil, nil)
			t.Cleanup(syncer.Stop)
			require.NoError(t, RecordUntrackedRepoOpen(t.Context(), database, syncer, tc.opened, "code.example.com", "group", "project", "issue", 1))
			rows, err := database.ListUntrackedRepoOpens(t.Context())
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, tc.opened, rows[0].Provider)
		})
	}
}

package ghshim

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

func BenchmarkReadListPRHistory(b *testing.B) {
	for _, count := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprintf("prs=%d", count), func(b *testing.B) {
			database := dbtest.Open(b)
			ctx := b.Context()
			repoID, err := reposeed.Seed(ctx, database, db.RepoIdentity{Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: "widget"})
			require.NoError(b, err)
			now := time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC)
			body := strings.Repeat("x", 1500)
			for number := 1; number <= count; number++ {
				state := db.MergeRequestStateClosed
				if number%20 == 0 {
					state = db.MergeRequestStateOpen
				}
				created := now.Add(time.Duration(number) * time.Minute)
				_, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
					RepoID: repoID, PlatformID: int64(number), Number: number,
					Title: "Example pull request", Body: body, State: state,
					HeadBranch: fmt.Sprintf("feature/%d", number), BaseBranch: "main",
					CreatedAt: created, UpdatedAt: created, LastActivityAt: created,
				})
				require.NoError(b, err)
			}
			require.NoError(b, database.UpdateRepoSyncCompleted(ctx, repoID, now, ""))
			require.NoError(b, database.EnsureDiscoveryArchives(ctx, []int64{repoID}, now))
			_, err = database.WriteDB().ExecContext(ctx, `UPDATE forge_archive_repos
				SET collection_mode = 'full', initial_completed_at = ? WHERE repo_id = ?`, now.Format(time.RFC3339), repoID)
			require.NoError(b, err)
			repo, err := database.GetRepoByID(ctx, repoID)
			require.NoError(b, err)
			require.NotNil(b, repo)
			for _, tc := range []struct {
				name, state, head, sparseState string
			}{
				{"open", "open", "", ""},
				{"all", "all", "", ""},
				{"closed", "closed", "", ""},
				{"open_head", "open", "feature/20", ""},
				{"all_head", "all", "feature/20", ""},
				{"open_sparse", "open", "", "open"},
				{"merged_sparse", "merged", "", "merged"},
			} {
				if tc.sparseState != "" {
					// Keep the 30 matching PRs older than all other history.
					_, err := database.WriteDB().ExecContext(ctx, `UPDATE forge_merge_requests
						SET state = CASE WHEN number <= 30 THEN ? ELSE 'closed' END
						WHERE repo_id = ?`, tc.sparseState, repoID)
					require.NoError(b, err)
				}
				b.Run(tc.name, func(b *testing.B) {
					q := Query{Command: "list", State: tc.state, Head: tc.head, Limit: 30, Fields: []string{"number"}}
					b.ReportAllocs()
					for b.Loop() {
						_, err := Read(b.Context(), database, *repo, q)
						require.NoError(b, err)
					}
				})
			}
		})
	}
}

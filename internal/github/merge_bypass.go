package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/shurcooL/githubv4"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
)

var errMergeBypassUnsupported = errors.New("GitHub merge bypass permission is not supported by this host")

// gqlPullRequestMergeBypassQuery reads whether the viewer may merge a pull
// request that branch protection or repository rules block, the same field
// GitHub's own merge box uses to offer a bypass.
type gqlPullRequestMergeBypassQuery struct {
	Repository struct {
		PullRequest *struct {
			ViewerCanMergeAsAdmin bool
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// FetchPullRequestViewerCanMergeAsAdmin reads whether the fetcher's credential
// may bypass the rules blocking a pull request's merge. found is false when
// GitHub reports no such pull request. Hosts whose schema lacks the field
// return errMergeBypassUnsupported without another request once rejected.
func (g *GraphQLFetcher) FetchPullRequestViewerCanMergeAsAdmin(
	ctx context.Context, owner, name string, number int,
) (allowed, found bool, err error) {
	if g.mergeBypassRejected.Load() {
		return false, false, errMergeBypassUnsupported
	}
	ctx = tokenauth.WithGitHubOwner(ctx, owner)
	var q gqlPullRequestMergeBypassQuery
	if err := g.client.Query(ctx, &q, map[string]any{
		"owner":  githubv4.String(owner),
		"name":   githubv4.String(name),
		"number": githubv4.Int(number),
	}); err != nil {
		if isSchemaRejectionOf(err, "viewercanmergeasadmin") {
			slog.Warn("GraphQL rejected the merge bypass field, disabling bypass reads for host",
				"host", g.host, "err", err,
			)
			g.mergeBypassRejected.Store(true)
			return false, false, errMergeBypassUnsupported
		}
		return false, false, fmt.Errorf(
			"fetch merge bypass permission for %s/%s#%d: %w", owner, name, number, err,
		)
	}
	if q.Repository.PullRequest == nil {
		return false, false, nil
	}
	return q.Repository.PullRequest.ViewerCanMergeAsAdmin, true, nil
}

// refreshMergeBypassAfterDetailSync records whether the merge credential may
// bypass the rules blocking a pull request, under the same conditions as the
// merge queue check that follows a detail sync. GitHub answers for the
// credential that asks, so the read uses the merge credential. Only a blocked
// open pull request is read; any other state, or a read that cannot run or
// fails, stores false so the view never offers a bypass Forge has not seen
// GitHub allow.
func (s *Syncer) refreshMergeBypassAfterDetailSync(
	ctx context.Context, repo RepoRef, repoID int64, number int, syncErr *error,
) {
	if !detailSyncAdmitsFollowUpReads(ctx, repo, syncErr) {
		return
	}
	if repoID <= 0 || repoPlatform(repo) != platform.KindGitHub {
		return
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
	if err != nil || mr == nil {
		return
	}
	allowed := false
	if mr.State == db.MergeRequestStateOpen && mr.MergeableState == "blocked" {
		allowed = s.readViewerCanMergeAsAdmin(ctx, repo, number)
	}
	if allowed == mr.ViewerCanMergeAsAdmin {
		return
	}
	if err := s.db.UpdateMergeRequestViewerCanMergeAsAdmin(ctx, repoID, number, allowed); err != nil {
		slog.Error("store merge bypass permission failed",
			"repo", repo.Owner+"/"+repo.Name, "number", number, "err", err)
		return
	}
	if s.onMergeBypassChanged != nil {
		s.onMergeBypassChanged(ctx, repoID, number)
	}
}

// readViewerCanMergeAsAdmin asks GitHub, as the merge credential, whether it
// may bypass the rules blocking the pull request. It reports false whenever
// the answer is unavailable.
func (s *Syncer) readViewerCanMergeAsAdmin(ctx context.Context, repo RepoRef, number int) bool {
	fetcher := s.mergeCredentialFetcher(ctx, repo)
	if fetcher == nil {
		return false
	}
	allowed, found, err := fetcher.FetchPullRequestViewerCanMergeAsAdmin(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		if !errors.Is(err, errMergeBypassUnsupported) {
			logSyncFailure(ctx, slog.LevelWarn, "merge bypass read failed", err,
				"repo", repo.Owner+"/"+repo.Name, "number", number)
		}
		return false
	}
	return found && allowed
}

// mergeCredentialFetcher returns a GraphQL fetcher for the credential that
// merges: the route's separate write credential when it has one, otherwise
// the read credential, which then also merges. It returns nil when that
// credential's GraphQL quota does not allow a read; it never substitutes the
// read credential for a separate write credential.
func (s *Syncer) mergeCredentialFetcher(ctx context.Context, repo RepoRef) *GraphQLFetcher {
	if router := s.routers[repoHost(repo)]; router != nil {
		if write, err := router.WriteFetcherForRepo(repo.Owner, repo.Name); err == nil && write != nil {
			return s.mergeQueueWriteFetcher(ctx, repo)
		}
	}
	fetcher := s.fetcherForContext(ctx, repo)
	if fetcher == nil || !s.graphQLReadAllowed(ctx, repo, fetcher) {
		return nil
	}
	return fetcher
}

// detailSyncAdmitsFollowUpReads reports whether a finished detail sync may
// run the per-pull-request reads that follow it. A sync that failed for
// anything but its diff, an archived repository, and archive budget work run
// none.
func detailSyncAdmitsFollowUpReads(ctx context.Context, repo RepoRef, syncErr *error) bool {
	if *syncErr != nil {
		if _, onlyDiffFailed := (*syncErr).(*DiffSyncError); !onlyDiffFailed { //nolint:errorlint // joined hard failures must not count as synced
			return false
		}
	}
	return !repo.Archived && !IsArchiveSyncBudgetContext(ctx)
}

// isSchemaRejectionOf reports whether err is GraphQL schema validation
// rejecting the named field (lowercased) rather than a transient failure.
func isSchemaRejectionOf(err error, field string) bool {
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, field) {
		return false
	}
	for _, phrase := range []string{
		"doesn't exist on type",
		"does not exist on type",
		"cannot query field",
		"unknown field",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

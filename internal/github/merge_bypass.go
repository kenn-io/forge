package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shurcooL/githubv4"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
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
// may bypass the rules blocking a pull request's merge, false when GitHub
// reports no such pull request. Hosts whose schema lacks the field
// return errMergeBypassUnsupported without another request once rejected.
func (g *GraphQLFetcher) FetchPullRequestViewerCanMergeAsAdmin(
	ctx context.Context, owner, name string, number int,
) (bool, error) {
	if g.mergeBypassRejected.Load() {
		return false, errMergeBypassUnsupported
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
			return false, errMergeBypassUnsupported
		}
		return false, fmt.Errorf(
			"fetch merge bypass permission for %s/%s#%d: %w", owner, name, number, err,
		)
	}
	if q.Repository.PullRequest == nil {
		return false, nil
	}
	return q.Repository.PullRequest.ViewerCanMergeAsAdmin, nil
}

// gqlPullRequestMergeStateQuery reads the pull request's merge state as the
// credential that asks, with the head commit that state describes.
type gqlPullRequestMergeStateQuery struct {
	Repository struct {
		PullRequest *struct {
			MergeStateStatus string
			HeadRefOid       string
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// FetchPullRequestMergeState reads the pull request's mergeable state, in REST
// terms, and its head commit as the fetcher's credential. GitHub computes the
// state for the credential that asks: rules that restrict who may update a
// branch block a read-only sync App while the user who merges is exempt.
func (g *GraphQLFetcher) FetchPullRequestMergeState(
	ctx context.Context, owner, name string, number int,
) (state, headSHA string, err error) {
	ctx = tokenauth.WithGitHubOwner(ctx, owner)
	var q gqlPullRequestMergeStateQuery
	if err := g.client.Query(ctx, &q, map[string]any{
		"owner":  githubv4.String(owner),
		"name":   githubv4.String(name),
		"number": githubv4.Int(number),
	}); err != nil {
		return "", "", fmt.Errorf(
			"fetch merge state for %s/%s#%d: %w", owner, name, number, err,
		)
	}
	if q.Repository.PullRequest == nil {
		return "", "", nil
	}
	pr := q.Repository.PullRequest
	return platformgithub.MergeStateToREST(pr.MergeStateStatus, ""), pr.HeadRefOid, nil
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
	changed := false
	if mr.State == db.MergeRequestStateOpen &&
		(mr.MergeableState == "blocked" || mr.MergeCredentialMergeableState != "") &&
		s.refreshMergeCredentialMergeableState(ctx, repo, repoID, mr) {
		changed = true
		if mr, err = s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number); err != nil || mr == nil {
			s.announceMergeBypassChanged(ctx, repoID, number)
			return
		}
	}
	allowed := false
	if mr.State == db.MergeRequestStateOpen && mr.MergeableState == "blocked" {
		allowed = s.readViewerCanMergeAsAdmin(ctx, repo, number)
	}
	if allowed != mr.ViewerCanMergeAsAdmin {
		if err := s.db.UpdateMergeRequestViewerCanMergeAsAdmin(ctx, repoID, number, allowed); err != nil {
			slog.Error("store merge bypass permission failed",
				"repo", repo.Owner+"/"+repo.Name, "number", number, "err", err)
		} else {
			changed = true
		}
	}
	if changed {
		s.announceMergeBypassChanged(ctx, repoID, number)
	}
}

func (s *Syncer) announceMergeBypassChanged(ctx context.Context, repoID int64, number int) {
	if s.onMergeBypassChanged != nil {
		s.onMergeBypassChanged(ctx, repoID, number)
	}
}

// mergeCredentialStateTTL bounds how long a stored merge credential state
// outlasts sync credential "blocked" reads without a detail sync confirming
// it. Rules or reviews can block the merge credential without a new commit;
// pull requests being viewed are re-read every fast sync, and the rest show
// "blocked" again once it expires.
const mergeCredentialStateTTL = time.Hour

// refreshMergeCredentialMergeableState stores the mergeable state GitHub
// reports to a separate merge credential, because the sync credential's
// "blocked" can come from rules that only refuse the sync credential. A stored
// state that cannot be confirmed, because the read failed or the route no
// longer has a separate merge credential, is dropped so the sync credential's
// "blocked" applies again. It reports whether the stored state changed.
func (s *Syncer) refreshMergeCredentialMergeableState(
	ctx context.Context, repo RepoRef, repoID int64, mr *db.MergeRequest,
) bool {
	requestedAt := s.now().UTC()
	state := ""
	if mr.PlatformHeadSHA != "" {
		state = s.readMergeCredentialMergeableState(ctx, repo, mr.Number, mr.PlatformHeadSHA)
	}
	if state == "" {
		if mr.MergeCredentialMergeableState == "" {
			return false
		}
		cleared, err := s.db.ClearMergeRequestMergeCredentialMergeableState(ctx, repoID, mr.Number)
		if err != nil {
			slog.Error("clear merge credential mergeable state failed",
				"repo", repo.Owner+"/"+repo.Name, "number", mr.Number, "err", err)
			return false
		}
		return cleared
	}
	override := state
	if state == "blocked" {
		override = ""
	}
	changed := mr.MergeableState != state || mr.MergeCredentialMergeableState != override
	stored, err := s.db.UpdateMergeRequestMergeCredentialMergeableState(
		ctx, repoID, mr.Number, mr.PlatformHeadSHA, mr.PlatformBaseSHA, state,
		requestedAt, requestedAt.Add(mergeCredentialStateTTL),
	)
	if err != nil {
		slog.Error("store merge credential mergeable state failed",
			"repo", repo.Owner+"/"+repo.Name, "number", mr.Number, "err", err)
		return false
	}
	return stored && changed
}

// expireMergeCredentialMergeableStates drops stored merge credential states
// that outlived mergeCredentialStateTTL. Snapshot writes also stop honoring
// them, but a pull request whose provider reads come back unchanged is not
// rewritten, so this runs on the sync interval whether or not automatic sync
// is enabled.
func (s *Syncer) expireMergeCredentialMergeableStates(ctx context.Context) {
	if _, err := s.db.ExpireMergeRequestMergeCredentialMergeableStates(ctx, s.now().UTC()); err != nil {
		slog.Error("expire merge credential mergeable states failed", "err", err)
	}
}

// readMergeCredentialMergeableState asks GitHub, as a merge credential
// separate from the sync credential, for the pull request's mergeable state.
// It returns "" when no separate merge credential exists or has quota, the
// read fails, the state is not computed yet, or GitHub answered for a
// different head commit.
func (s *Syncer) readMergeCredentialMergeableState(
	ctx context.Context, repo RepoRef, number int, headSHA string,
) string {
	fetcher := s.mergeQueueWriteFetcher(ctx, repo)
	if fetcher == nil {
		return ""
	}
	state, observedHead, err := fetcher.FetchPullRequestMergeState(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		logSyncFailure(ctx, slog.LevelWarn, "merge credential merge state read failed", err,
			"repo", repo.Owner+"/"+repo.Name, "number", number)
		return ""
	}
	if state == "" || state == "unknown" || observedHead != headSHA {
		return ""
	}
	return state
}

// MergeCredentialMergeableStateForRepository reads, live, the mergeable state
// GitHub reports to the repository's separate merge credential at headSHA. It
// returns "" when the repository has no separate merge credential or the state
// is unavailable.
func (s *Syncer) MergeCredentialMergeableStateForRepository(
	ctx context.Context, repoID int64, number int, headSHA string,
) string {
	stored, err := s.db.GetRepoByID(ctx, repoID)
	if err != nil || stored == nil || headSHA == "" {
		return ""
	}
	repo, ok := s.trackedRepoByProviderKey(
		platform.Kind(stored.Platform), stored.PlatformHost, stored.Key,
	)
	if !ok || repoPlatform(repo) != platform.KindGitHub {
		return ""
	}
	return s.readMergeCredentialMergeableState(
		ctx, repoRefFromStoredIdentity(repo, *stored), number, headSHA,
	)
}

// readViewerCanMergeAsAdmin asks GitHub, as the merge credential, whether it
// may bypass the rules blocking the pull request. It reports false whenever
// the answer is unavailable.
func (s *Syncer) readViewerCanMergeAsAdmin(ctx context.Context, repo RepoRef, number int) bool {
	fetcher := s.mergeCredentialFetcher(ctx, repo)
	if fetcher == nil {
		return false
	}
	allowed, err := fetcher.FetchPullRequestViewerCanMergeAsAdmin(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		if !errors.Is(err, errMergeBypassUnsupported) {
			logSyncFailure(ctx, slog.LevelWarn, "merge bypass read failed", err,
				"repo", repo.Owner+"/"+repo.Name, "number", number)
		}
		return false
	}
	return allowed
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

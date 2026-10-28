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

var errMergeBypassUnsupported = errors.New("GitHub merge rules are not readable on this host")

// Merge rules record how branch protection and repository rules treat the
// merge credential's merge of an open pull request. The empty value is
// unknown: not read, or the read failed.
const (
	MergeRulesClear             = "clear"
	MergeRulesBlocked           = "blocked"
	MergeRulesBlockedBypassable = "blocked_bypassable"
)

// gqlPullRequestMergeRulesQuery reads the viewer's merge state and whether it
// may merge over the rules that block it, the fields GitHub's own merge box
// uses. GitHub computes both for the credential that asks.
type gqlPullRequestMergeRulesQuery struct {
	Repository struct {
		PullRequest *struct {
			MergeStateStatus      string
			ViewerCanMergeAsAdmin bool
		} `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// FetchPullRequestMergeRules reads how the rules treat the fetcher's
// credential merging a pull request, "" when GitHub has not computed the
// merge state or reports no such pull request. Hosts whose schema lacks the
// fields return errMergeBypassUnsupported without another request once
// rejected.
func (g *GraphQLFetcher) FetchPullRequestMergeRules(
	ctx context.Context, owner, name string, number int,
) (string, error) {
	if g.mergeBypassRejected.Load() {
		return "", errMergeBypassUnsupported
	}
	ctx = tokenauth.WithGitHubOwner(ctx, owner)
	var q gqlPullRequestMergeRulesQuery
	if err := g.client.Query(ctx, &q, map[string]any{
		"owner":  githubv4.String(owner),
		"name":   githubv4.String(name),
		"number": githubv4.Int(number),
	}); err != nil {
		if isSchemaRejectionOf(err, "viewercanmergeasadmin") || isSchemaRejectionOf(err, "mergestatestatus") {
			slog.Warn("GraphQL rejected the merge rules fields, disabling merge rules reads for host",
				"host", g.host, "err", err,
			)
			g.mergeBypassRejected.Store(true)
			return "", errMergeBypassUnsupported
		}
		return "", fmt.Errorf(
			"fetch merge rules for %s/%s#%d: %w", owner, name, number, err,
		)
	}
	pr := q.Repository.PullRequest
	switch {
	case pr == nil || pr.MergeStateStatus == "" || pr.MergeStateStatus == "UNKNOWN":
		return "", nil
	case pr.MergeStateStatus != "BLOCKED":
		return MergeRulesClear, nil
	case pr.ViewerCanMergeAsAdmin:
		return MergeRulesBlockedBypassable, nil
	default:
		return MergeRulesBlocked, nil
	}
}

// refreshMergeBypassAfterDetailSync records how the rules treat the merge
// credential's merge of a pull request, under the same conditions as the
// merge queue check that follows a detail sync.
func (s *Syncer) refreshMergeBypassAfterDetailSync(
	ctx context.Context, repo RepoRef, repoID int64, number int, syncErr *error,
) {
	if !detailSyncAdmitsFollowUpReads(ctx, repo, syncErr) {
		return
	}
	s.storeMergeRules(ctx, repo, repoID, number, false)
}

// refreshMergeRulesAfterBulkDetail reads unknown rules after bulk hydration
// postponed the detail sync that would read them.
func (s *Syncer) refreshMergeRulesAfterBulkDetail(
	ctx context.Context, repo RepoRef, repoID int64, number int,
) {
	if repo.Archived || IsArchiveSyncBudgetContext(ctx) {
		return
	}
	s.storeMergeRules(ctx, repo, repoID, number, true)
}

// storeMergeRules reads the rules as the merge credential, because GitHub
// answers for the credential that asks. Only a pull request the sync saw
// blocked is read; any other state, or a failed read, stores unknown, which
// falls back to the synced state and never offers a bypass. Pull request
// syncs never write the rules, so they cannot flip them back; a change to the
// state they were read against resets them (migration 000079).
func (s *Syncer) storeMergeRules(ctx context.Context, repo RepoRef, repoID int64, number int, onlyUnknown bool) {
	if repoID <= 0 || repoPlatform(repo) != platform.KindGitHub {
		return
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
	if err != nil || mr == nil || (onlyUnknown && mr.MergeRules != "") {
		return
	}
	rules := ""
	if mr.State == db.MergeRequestStateOpen && mr.MergeableState == "blocked" {
		rules, _ = s.ReadMergeRules(ctx, repo, number)
	}
	if rules == mr.MergeRules {
		return
	}
	applied, err := s.db.UpdateMergeRequestMergeRules(ctx, mr, rules)
	if err != nil {
		slog.Error("store merge rules failed",
			"repo", repo.Owner+"/"+repo.Name, "number", number, "err", err)
	}
	if applied && s.onMergeBypassChanged != nil {
		s.onMergeBypassChanged(ctx, repoID, number)
	}
}

// ReadMergeRules asks GitHub, as the merge credential, how the rules treat
// its merge of the pull request. It returns "" when the answer is
// unavailable, and readable false when this host can never answer: no
// GraphQL credential is configured or its schema lacks the fields.
func (s *Syncer) ReadMergeRules(ctx context.Context, repo RepoRef, number int) (rules string, readable bool) {
	fetcher, configured := s.mergeCredentialFetcher(ctx, repo)
	if fetcher == nil {
		return "", configured
	}
	rules, err := fetcher.FetchPullRequestMergeRules(ctx, repo.Owner, repo.Name, number)
	if errors.Is(err, errMergeBypassUnsupported) {
		return "", false
	}
	if err != nil {
		logSyncFailure(ctx, slog.LevelWarn, "merge rules read failed", err,
			"repo", repo.Owner+"/"+repo.Name, "number", number)
	}
	return rules, true
}

// mergeCredentialFetcher returns a GraphQL fetcher for the credential that
// merges: the route's separate write credential when it has one, otherwise
// the read credential, which then also merges. It returns nil when that
// credential's GraphQL quota does not allow a read, and configured false when
// there is no such credential; it never substitutes the read credential for
// a separate write credential.
func (s *Syncer) mergeCredentialFetcher(ctx context.Context, repo RepoRef) (_ *GraphQLFetcher, configured bool) {
	if router := s.routers[repoHost(repo)]; router != nil {
		if write, err := router.WriteFetcherForRepo(repo.Owner, repo.Name); err == nil && write != nil {
			return s.mergeQueueWriteFetcher(ctx, repo), true
		}
	}
	fetcher := s.fetcherForContext(ctx, repo)
	if fetcher == nil {
		return nil, false
	}
	if !s.graphQLReadAllowed(ctx, repo, fetcher) {
		return nil, true
	}
	return fetcher, true
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

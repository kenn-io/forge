package github

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	gh "github.com/google/go-github/v92/github"
	"go.kenn.io/forge/platform"
)

// LandingAPI is the optional client surface for landing evidence. Client
// wrappers must preserve repository-scoped routing for each operation.
type LandingAPI interface {
	ListLandingAssociations(ctx context.Context, repo platform.RepoRef, commit, cursor string) (platform.Page[platform.LandingChangeRef], error)
	GetLandingChange(ctx context.Context, repo platform.RepoRef, change platform.LandingChangeRef) (platform.LandingChange, error)
	ListLandingSource(ctx context.Context, repo platform.RepoRef, change platform.LandingChangeRef, cursor string) (platform.Page[string], error)
	ListLandingSourceRange(ctx context.Context, repo platform.RepoRef, change platform.LandingChangeRef, base, head, cursor string) (platform.Page[string], error)
}

// The pull request commit list stops at 250 commits (it returns the newest
// 250), as does GraphQL's PullRequest.commits. The paginated three-dot
// comparison from the pull request's base.sha to its head.sha lists the same
// commits in the same order past that cap; read-only probes on 2026-10-19
// matched it to the pull request list for ordinary and branch-updated pull
// requests. The comparison's total_commits saturates at 10,000 and later pages
// are empty, so a count of 10,000 cannot be told apart from a truncated range.
const (
	pullRequestCommitListCap = 250
	compareCommitCeiling     = 10000
)

func (p *Provider) LandingEvidenceSupport() platform.LandingEvidenceSupport {
	// REST 2022-11-28 is the pinned SDK's default. Its commit association
	// contract identifies the introducing PR, including rewritten commits:
	// https://docs.github.com/en/rest/commits/commits?apiVersion=2022-11-28#list-pull-requests-associated-with-a-commit
	// Read-only public probes on 2026-09-08 covered ordinary merge markers,
	// rewritten ranges, absent associations, and a feature-branch integration
	// whose inner commit and outer marker discover different PRs. This does
	// not establish the corresponding contract on Enterprise Server versions.
	_, ok := p.client.(LandingAPI)
	if !ok || p.host != "github.com" {
		return platform.LandingEvidenceSupport{Reason: "unverified_endpoint_contract"}
	}
	return platform.LandingEvidenceSupport{
		Inventory: true, OrdinaryMerge: true, SingleParentCorrespondence: true,
		Sources: platform.LandingSourcePolicy{RequireCount: true, MaxCommits: pullRequestCommitListCap, RangeMaxCommits: compareCommitCeiling - 1},
	}
}

func (p *Provider) ListLandingAssociations(ctx context.Context, ref platform.RepoRef, sha, cursor string) (platform.Page[platform.LandingChangeRef], error) {
	if c, ok := p.client.(LandingAPI); ok {
		return c.ListLandingAssociations(ctx, ref, sha, cursor)
	}
	return platform.Page[platform.LandingChangeRef]{}, platform.UnsupportedCapability(p.Platform(), p.host, "landing_evidence")
}

// LandingInventoryAPI lists every merged change that targets a repository.
// Clients without it leave foreign associations unresolved.
type LandingInventoryAPI interface {
	ListMergedLandingChanges(ctx context.Context, repo platform.RepoRef, cursor string) (platform.Page[platform.LandingChangeRef], error)
}

func (p *Provider) ListMergedLandingChanges(ctx context.Context, ref platform.RepoRef, cursor string) (platform.Page[platform.LandingChangeRef], error) {
	if c, ok := p.client.(LandingInventoryAPI); ok {
		return c.ListMergedLandingChanges(ctx, ref, cursor)
	}
	return platform.Page[platform.LandingChangeRef]{}, platform.UnsupportedCapability(p.Platform(), p.host, "landing_inventory")
}

func (p *Provider) GetLandingChange(ctx context.Context, ref platform.RepoRef, change platform.LandingChangeRef) (platform.LandingChange, error) {
	if c, ok := p.client.(LandingAPI); ok {
		return c.GetLandingChange(ctx, ref, change)
	}
	return platform.LandingChange{}, platform.UnsupportedCapability(p.Platform(), p.host, "landing_evidence")
}

func (p *Provider) ListLandingSource(ctx context.Context, ref platform.RepoRef, change platform.LandingChangeRef, cursor string) (platform.Page[string], error) {
	if c, ok := p.client.(LandingAPI); ok {
		return c.ListLandingSource(ctx, ref, change, cursor)
	}
	return platform.Page[string]{}, platform.UnsupportedCapability(p.Platform(), p.host, "landing_evidence")
}

func (p *Provider) ListLandingSourceRange(ctx context.Context, ref platform.RepoRef, change platform.LandingChangeRef, base, head, cursor string) (platform.Page[string], error) {
	if c, ok := p.client.(LandingAPI); ok {
		return c.ListLandingSourceRange(ctx, ref, change, base, head, cursor)
	}
	return platform.Page[string]{}, platform.UnsupportedCapability(p.Platform(), p.host, "landing_evidence")
}

type landingCursor struct {
	Host, Owner, Repository, Dataset string
	Page                             int
}

func (c *Client) landingPage(ref platform.RepoRef, dataset, cursor string) (landingCursor, error) {
	want := landingCursor{Host: c.platformHost, Owner: ref.Owner, Repository: ref.Name, Dataset: dataset, Page: 1}
	if err := platform.ValidateCanonicalRepoRef(ref); err != nil {
		return want, err
	}
	if ref.Platform != platform.KindGitHub || ref.Host != c.platformHost {
		return want, errors.New("landing reader repository scope mismatch")
	}
	if cursor == "" {
		return want, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return want, err
	}
	var got landingCursor
	if err = json.Unmarshal(data, &got); err != nil {
		return want, err
	}
	want.Page = got.Page
	if got != want || got.Page < 1 {
		return want, errors.New("landing cursor scope mismatch")
	}
	return got, nil
}

func landingPageResult[T any](items []T, response *gh.Response, cursor landingCursor) (platform.Page[T], error) {
	if response == nil {
		return platform.Page[T]{}, errors.New("missing landing page response")
	}
	p := platform.Page[T]{Items: items, Exhausted: response.NextPage == 0}
	if p.Exhausted {
		return p, nil
	}
	if response.NextPage <= cursor.Page {
		return p, errors.New("nonadvancing landing page")
	}
	cursor.Page = response.NextPage
	data, err := json.Marshal(cursor)
	if err != nil {
		return p, err
	}
	p.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	p.ProgressOnly = len(items) == 0
	return p, nil
}

func (c *Client) ListLandingAssociations(ctx context.Context, ref platform.RepoRef, sha, cursor string) (platform.Page[platform.LandingChangeRef], error) {
	state, err := c.landingPage(ref, "association/"+sha, cursor)
	if err != nil {
		return platform.Page[platform.LandingChangeRef]{}, err
	}
	ctx = c.authContext(WithUnconditionalRead(ctx), ref.Owner, false)
	prs, resp, err := c.gh.PullRequests.ListPullRequestsWithCommit(ctx, ref.Owner, ref.Name, sha, &gh.ListOptions{Page: state.Page, PerPage: 100})
	c.trackRate(resp)
	if err != nil {
		return platform.Page[platform.LandingChangeRef]{}, err
	}
	items := make([]platform.LandingChangeRef, 0, len(prs))
	for _, pr := range prs {
		items = append(items, platform.LandingChangeRef{ID: pr.GetID(), Number: pr.GetNumber(), TargetID: pr.GetBase().GetRepo().GetID()})
	}
	return landingPageResult(items, resp, state)
}

// ListMergedLandingChanges pages through every pull request in creation order.
// Including open and closed ones keeps pages stable: a state change never moves
// a pull request between pages, and new ones only append. A pull request that
// merges during the listing lands after the commits already collected.
func (c *Client) ListMergedLandingChanges(ctx context.Context, ref platform.RepoRef, cursor string) (platform.Page[platform.LandingChangeRef], error) {
	state, err := c.landingPage(ref, "inventory", cursor)
	if err != nil {
		return platform.Page[platform.LandingChangeRef]{}, err
	}
	ctx = c.authContext(WithUnconditionalRead(ctx), ref.Owner, false)
	prs, resp, err := c.gh.PullRequests.List(ctx, ref.Owner, ref.Name, &gh.PullRequestListOptions{
		State: "all", Sort: "created", Direction: "asc", Page: state.Page, PerPage: 100,
	})
	c.trackRate(resp)
	if err != nil {
		return platform.Page[platform.LandingChangeRef]{}, err
	}
	items := make([]platform.LandingChangeRef, 0, len(prs))
	for _, pr := range prs {
		if pr.MergedAt != nil {
			items = append(items, platform.LandingChangeRef{ID: pr.GetID(), Number: pr.GetNumber(), TargetID: pr.GetBase().GetRepo().GetID()})
		}
	}
	return landingPageResult(items, resp, state)
}

func (c *Client) GetLandingChange(ctx context.Context, ref platform.RepoRef, change platform.LandingChangeRef) (platform.LandingChange, error) {
	if _, err := c.landingPage(ref, "detail", ""); err != nil {
		return platform.LandingChange{}, err
	}
	ctx = c.authContext(WithUnconditionalRead(ctx), ref.Owner, false)
	pr, resp, err := c.gh.PullRequests.Get(ctx, ref.Owner, ref.Name, change.Number)
	c.trackRate(resp)
	if err != nil {
		return platform.LandingChange{}, err
	}
	if pr.GetID() != change.ID || pr.GetNumber() != change.Number {
		return platform.LandingChange{}, platform.ErrLandingIdentityMismatch
	}
	d := platform.LandingChange{Ref: change, TargetID: pr.GetBase().GetRepo().GetID(), TargetBranch: pr.GetBase().GetRef(), Merged: pr.Merged, MergeSHA: pr.MergeCommitSHA}
	d.Title = pr.Title
	d.Author, d.Merger = landingAccount(pr.User), landingAccount(pr.MergedBy)
	d.OpenedAt, d.MergedAt = landingTime(pr.CreatedAt), landingTime(pr.MergedAt)
	if pr.Head != nil {
		d.SourceHead = pr.Head.SHA
	}
	if pr.Base != nil {
		d.SourceBase = pr.Base.SHA
	}
	if pr.Commits != nil {
		d.SourceCount = new(int64(*pr.Commits))
	}
	if source := pr.GetHead().GetRepo(); source != nil {
		if source.HTMLURL != nil {
			u, err := url.Parse(*source.HTMLURL)
			if err != nil || !strings.EqualFold(u.Host, c.platformHost) {
				return platform.LandingChange{}, platform.ErrLandingIdentityMismatch
			}
		}
		d.SourceID = source.ID
	}
	if pr.GetMerged() && pr.GetMergeCommitSHA() != "" {
		d.Terminal = pr.GetMergeCommitSHA()
		d.TerminalEvidence = "merged_commit_sha"
	}
	return d, nil
}

func landingAccount(user *gh.User) *platform.Account {
	if user == nil {
		return nil
	}
	a := &platform.Account{Type: platform.AccountTypeUnknown}
	if user.ID != nil {
		a.ID = new(*user.ID)
	}
	if user.Login != nil {
		a.Login = new(*user.Login)
	}
	switch user.GetType() {
	case "User":
		a.Type = platform.AccountTypeUser
	case "Bot":
		a.Type = platform.AccountTypeBot
	case "Organization":
		a.Type = platform.AccountTypeOrganization
	}
	return a
}

func landingTime(stamp *gh.Timestamp) *time.Time {
	if stamp == nil {
		return nil
	}
	return new(stamp.UTC())
}

func (c *Client) ListLandingSource(ctx context.Context, ref platform.RepoRef, change platform.LandingChangeRef, cursor string) (platform.Page[string], error) {
	state, err := c.landingPage(ref, fmt.Sprintf("source/%d/%d", change.ID, change.Number), cursor)
	if err != nil {
		return platform.Page[string]{}, err
	}
	ctx = c.authContext(WithUnconditionalRead(ctx), ref.Owner, false)
	commits, resp, err := c.gh.PullRequests.ListCommits(ctx, ref.Owner, ref.Name, change.Number, &gh.ListOptions{Page: state.Page, PerPage: 100})
	c.trackRate(resp)
	if err != nil {
		return platform.Page[string]{}, err
	}
	items := make([]string, 0, len(commits))
	for _, commit := range commits {
		items = append(items, commit.GetSHA())
	}
	return landingPageResult(items, resp, state)
}

// ListLandingSourceRange pages GitHub's three-dot comparison. base and head
// must be object IDs: they are path segments, never branch names.
func (c *Client) ListLandingSourceRange(ctx context.Context, ref platform.RepoRef, change platform.LandingChangeRef, base, head, cursor string) (platform.Page[string], error) {
	if !landingObjectID(base) || !landingObjectID(head) {
		return platform.Page[string]{}, errors.New("landing source range requires object IDs")
	}
	state, err := c.landingPage(ref, fmt.Sprintf("range/%d/%d/%s/%s", change.ID, change.Number, base, head), cursor)
	if err != nil {
		return platform.Page[string]{}, err
	}
	ctx = c.authContext(WithUnconditionalRead(ctx), ref.Owner, false)
	comparison, resp, err := c.gh.Repositories.CompareCommits(ctx, ref.Owner, ref.Name, base, head, &gh.ListOptions{Page: state.Page, PerPage: 100})
	c.trackRate(resp)
	if err != nil {
		return platform.Page[string]{}, err
	}
	items := make([]string, 0, len(comparison.Commits))
	for _, commit := range comparison.Commits {
		items = append(items, commit.GetSHA())
	}
	return landingPageResult(items, resp, state)
}

func landingObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

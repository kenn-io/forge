package github

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"strings"

	gh "github.com/google/go-github/v89/github"
	"go.kenn.io/forge/platform"
)

// LandingAPI is the optional client surface for landing evidence. Client
// wrappers must preserve repository-scoped routing for each operation.
type LandingAPI interface {
	ListLandingAssociations(ctx context.Context, repo platform.RepoRef, commit, cursor string) (platform.Page[platform.LandingChangeRef], error)
	GetLandingChange(ctx context.Context, repo platform.RepoRef, change platform.LandingChangeRef) (platform.LandingChange, error)
	ListLandingSource(ctx context.Context, repo platform.RepoRef, change platform.LandingChangeRef, cursor string) (platform.Page[string], error)
}

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
	return platform.LandingEvidenceSupport{Inventory: true, OrdinaryMerge: true, Sources: platform.LandingSourcePolicy{RequireCount: true, MaxCommits: 250}}
}

func (p *Provider) ListLandingAssociations(ctx context.Context, ref platform.RepoRef, sha, cursor string) (platform.Page[platform.LandingChangeRef], error) {
	if c, ok := p.client.(LandingAPI); ok {
		return c.ListLandingAssociations(ctx, ref, sha, cursor)
	}
	return platform.Page[platform.LandingChangeRef]{}, platform.UnsupportedCapability(p.Platform(), p.host, "landing_evidence")
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
	if pr.Head != nil {
		d.SourceHead = pr.Head.SHA
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

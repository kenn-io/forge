package github

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	gh "github.com/google/go-github/v89/github"
	"go.kenn.io/forge/platform"
)

func (c *Client) LandingCapabilities() platform.LandingCapabilities {
	// REST 2022-11-28 binds merge_commit_sha to the landed terminal. It does
	// not identify squash/rebase method. Ordinary two-parent topology can be
	// proven by the analyzer; a one-parent terminal remains unsupported.
	return platform.LandingCapabilities{OrdinaryMerge: true, CompleteCandidateInventory: true, AccountType: true}
}

func (p *Provider) LandingCapabilities() platform.LandingCapabilities {
	if reader, ok := p.client.(platform.LandingReader); ok {
		return reader.LandingCapabilities()
	}
	return platform.LandingCapabilities{}
}

func (p *Provider) CollectLandingEvidence(ctx context.Context, ref platform.RepoRef, budget platform.Budget) (platform.LandingSnapshot, error) {
	reader, ok := p.client.(platform.LandingReader)
	if !ok {
		return platform.LandingSnapshot{}, platform.UnsupportedCapability(platform.KindGitHub, p.host, "landing_evidence")
	}
	return reader.CollectLandingEvidence(ctx, ref, budget)
}

func (c *Client) evidenceRead(ctx context.Context, owner, path string, meter *platform.Meter) (http.Header, []byte, error) {
	ctx = WithUnconditionalRead(ctx)
	req, err := c.gh.NewRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, body, err := meter.ReadHTTP(c.authContext(ctx, owner, false), c.httpClient, req)
	if resp != nil && c.rateTracker != nil {
		c.rateTracker.RecordRequest()
		limit, limitErr := strconv.Atoi(resp.Header.Get("X-RateLimit-Limit"))
		remaining, remainingErr := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
		reset, resetErr := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
		if limitErr == nil && remainingErr == nil && resetErr == nil {
			c.rateTracker.UpdateFromRate(RateFromHeaders(limit, remaining, time.Unix(reset, 0).UTC()))
		}
	}
	if err != nil {
		return nil, nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if err := gh.CheckResponse(resp); err != nil {
		return nil, nil, mapGitHubReadError(c.platformHost, c.now, "landing_evidence", err)
	}
	return resp.Header, body, nil
}

func (c *Client) evidenceRecord(ctx context.Context, owner, path string, out any, meter *platform.Meter) error {
	if err := meter.Records(1); err != nil {
		return err
	}
	_, body, err := c.evidenceRead(ctx, owner, path, meter)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func (c *Client) landingRepository(ctx context.Context, ref platform.RepoRef, meter *platform.Meter) (platform.LandingRepository, error) {
	var repo gh.Repository
	path := "repos/" + url.PathEscape(ref.Owner) + "/" + url.PathEscape(ref.Name)
	if err := c.evidenceRecord(ctx, ref.Owner, path, &repo, meter); err != nil {
		return platform.LandingRepository{}, err
	}
	if repo.GetID() <= 0 || repo.GetDefaultBranch() == "" || (ref.PlatformID > 0 && ref.PlatformID != repo.GetID()) || (ref.PlatformExternalID != "" && ref.PlatformExternalID != repo.GetNodeID()) {
		return platform.LandingRepository{}, c.landingError("repository_identity")
	}
	var branch struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := c.evidenceRecord(ctx, ref.Owner, path+"/branches/"+url.PathEscape(repo.GetDefaultBranch()), &branch, meter); err != nil {
		return platform.LandingRepository{}, err
	}
	if branch.Commit.SHA == "" {
		return platform.LandingRepository{}, c.landingError("default_branch_head")
	}
	return platform.LandingRepository{Identity: platform.RepositoryIdentity{Provider: platform.KindGitHub, Instance: c.platformHost, ID: strconv.FormatInt(repo.GetID(), 10)}, Owner: repo.GetOwner().GetLogin(), Name: repo.GetName(), DefaultBranch: repo.GetDefaultBranch(), HeadSHA: branch.Commit.SHA, CloneURL: repo.GetCloneURL()}, nil
}

func (c *Client) landingError(field string) error {
	return &platform.Error{Code: platform.ErrCodeProviderContract, Provider: platform.KindGitHub, PlatformHost: c.platformHost, Capability: "landing_evidence", Field: field}
}

func (c *Client) CollectLandingEvidence(ctx context.Context, ref platform.RepoRef, budget platform.Budget) (platform.LandingSnapshot, error) {
	if err := platform.ValidateCanonicalRepoRef(ref); err != nil {
		return platform.LandingSnapshot{}, err
	}
	if ref.Platform != platform.KindGitHub || ref.Host != c.platformHost {
		return platform.LandingSnapshot{}, c.landingError("repository_instance")
	}
	meter, err := platform.NewMeter(ctx, budget)
	if err != nil {
		return platform.LandingSnapshot{}, err
	}
	snapshot := platform.LandingSnapshot{Schema: platform.LandingSnapshotSchema, StartedAt: c.now().UTC(), Capabilities: c.LandingCapabilities(), Candidates: []platform.LandingCandidate{}}
	snapshot.Repository, err = c.landingRepository(ctx, ref, meter)
	if err != nil {
		return platform.LandingSnapshot{}, err
	}
	finish := func(reason string) (platform.LandingSnapshot, error) {
		snapshot.Coverage.Reason = reason
		snapshot.CompletedAt = c.now().UTC()
		return platform.PublishLandingSnapshot(ctx, snapshot, meter)
	}
	fail := func(reason string, cause error) (platform.LandingSnapshot, error) {
		// A rejected credential must reach the credential owner, not be hidden
		// as ordinary incomplete inventory after earlier reads succeeded.
		if errors.Is(cause, platform.ErrCredentialRejected) || errors.Is(cause, platform.ErrInstallationSuspended) || errors.Is(cause, platform.ErrInstallationDeleted) {
			return platform.LandingSnapshot{}, cause
		}
		return finish(reason)
	}
	path := "repos/" + url.PathEscape(ref.Owner) + "/" + url.PathEscape(ref.Name) + "/pulls"
	seen := make(map[int64]bool)
	for page := 1; ; page++ {
		header, body, readErr := c.evidenceRead(ctx, ref.Owner, fmt.Sprintf("%s?state=all&sort=created&direction=asc&per_page=100&page=%d", path, page), meter)
		if readErr != nil {
			if errors.Is(readErr, platform.ErrPageLimit) {
				return finish("input_budget_exhausted")
			}
			return fail("candidate_inventory_unavailable", readErr)
		}
		items, readErr := platform.DecodeEvidencePage[gh.PullRequest](body, 100, meter)
		if readErr != nil {
			return finish("candidate_inventory_incomplete")
		}
		for _, item := range items {
			if item.GetID() <= 0 || item.GetNumber() <= 0 || seen[item.GetID()] {
				return finish("candidate_inventory_changed")
			}
			seen[item.GetID()] = true
			if item.MergedAt == nil {
				continue
			}
			candidate, readErr := c.landingCandidate(ctx, ref, item.GetNumber(), meter)
			if readErr != nil {
				return fail("candidate_detail_unavailable", readErr)
			}
			if candidate.ID != strconv.FormatInt(item.GetID(), 10) || candidate.Number != item.GetNumber() || candidate.BaseRepository == nil || *candidate.BaseRepository != snapshot.Repository.Identity {
				return finish("candidate_identity_changed")
			}
			if candidate.TargetBranch == snapshot.Repository.DefaultBranch {
				snapshot.Candidates = append(snapshot.Candidates, candidate)
			}
		}
		// GitHub created-order, all-state inventory avoids the shifting prefix
		// of a merged-only list. An exhausted page is still an observation,
		// not a claim that upstream reads were atomic.
		more, err := platform.NextEvidencePage(header, page)
		if err != nil {
			return finish("candidate_inventory_incomplete")
		}
		if !more {
			break
		}
		if len(items) == 0 {
			return finish("candidate_inventory_incomplete")
		}
	}
	after, err := c.landingRepository(ctx, ref, meter)
	if err != nil {
		return fail("repository_recheck_unavailable", err)
	}
	if after.Identity != snapshot.Repository.Identity {
		return platform.LandingSnapshot{}, c.landingError("repository_identity_changed")
	}
	if after.HeadSHA != snapshot.Repository.HeadSHA || after.DefaultBranch != snapshot.Repository.DefaultBranch {
		return finish("default_branch_changed")
	}
	snapshot.Coverage.Complete = true
	return finish("")
}

type landingPull struct {
	ID       int64      `json:"id"`
	Number   int        `json:"number"`
	Merged   bool       `json:"merged"`
	Author   *gh.User   `json:"user"`
	Merger   *gh.User   `json:"merged_by"`
	OpenedAt *time.Time `json:"created_at"`
	MergedAt *time.Time `json:"merged_at"`
	Base     struct {
		Ref  string         `json:"ref"`
		Repo *gh.Repository `json:"repo"`
	} `json:"base"`
	Head struct {
		SHA  jsontext.Value `json:"sha"`
		Repo *gh.Repository `json:"repo"`
	} `json:"head"`
	MergeSHA     jsontext.Value `json:"merge_commit_sha"`
	Commits      *int           `json:"commits"`
	Additions    *int64         `json:"additions"`
	Deletions    *int64         `json:"deletions"`
	FilesChanged *int64         `json:"changed_files"`
}

func (c *Client) landingCandidate(ctx context.Context, ref platform.RepoRef, number int, meter *platform.Meter) (platform.LandingCandidate, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d", url.PathEscape(ref.Owner), url.PathEscape(ref.Name), number)
	var pull landingPull
	if err := c.evidenceRecord(ctx, ref.Owner, path, &pull, meter); err != nil {
		return platform.LandingCandidate{}, err
	}
	if pull.ID <= 0 || pull.Number != number || !pull.Merged || pull.MergedAt == nil {
		return platform.LandingCandidate{}, c.landingError("merged_change")
	}
	merge, err := platform.ObserveSHA(pull.MergeSHA)
	if err != nil {
		return platform.LandingCandidate{}, err
	}
	head, err := platform.ObserveSHA(pull.Head.SHA)
	if err != nil {
		return platform.LandingCandidate{}, err
	}
	candidate := platform.LandingCandidate{ID: strconv.FormatInt(pull.ID, 10), Number: number, TargetBranch: pull.Base.Ref, Author: NormalizeAccount(pull.Author), Merger: NormalizeAccount(pull.Merger), OpenedAt: pull.OpenedAt, MergedAt: pull.MergedAt, MergeSHA: merge, SourceHeadSHA: head, TerminalSHA: merge.Value, Additions: pull.Additions, Deletions: pull.Deletions, FilesChanged: pull.FilesChanged, SourceCommits: []string{}}
	identity := func(repo *gh.Repository) *platform.RepositoryIdentity {
		if repo == nil || repo.GetID() <= 0 {
			return nil
		}
		return &platform.RepositoryIdentity{Provider: platform.KindGitHub, Instance: c.platformHost, ID: strconv.FormatInt(repo.GetID(), 10)}
	}
	candidate.BaseRepository = identity(pull.Base.Repo)
	candidate.SourceRepository = identity(pull.Head.Repo)
	if merge.Value != "" {
		candidate.TerminalProof = "github-rest-2022-11-28/merged-merge-commit-sha"
	}
	for page := 1; ; page++ {
		header, body, err := c.evidenceRead(ctx, ref.Owner, fmt.Sprintf("%s/commits?per_page=100&page=%d", path, page), meter)
		if err != nil {
			return candidate, err
		}
		commits, err := platform.DecodeEvidencePage[struct {
			SHA string `json:"sha"`
		}](body, 100, meter)
		if err != nil {
			return candidate, err
		}
		for _, commit := range commits {
			if commit.SHA == "" {
				return candidate, c.landingError("source_sha")
			}
			candidate.SourceCommits = append(candidate.SourceCommits, commit.SHA)
		}
		more, err := platform.NextEvidencePage(header, page)
		if err != nil {
			return candidate, err
		}
		if !more {
			break
		}
		if len(commits) == 0 {
			return candidate, c.landingError("source_page")
		}
	}
	// The endpoint exposes at most 250 commits. A returned short list without
	// matching the detail's count is not complete source evidence.
	candidate.SourceComplete = pull.Commits != nil && *pull.Commits == len(candidate.SourceCommits) && len(candidate.SourceCommits) <= 250
	return candidate, nil
}

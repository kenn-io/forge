package gitlab

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gitlabsdk "gitlab.com/gitlab-org/api/client-go/v2"
	"go.kenn.io/forge/platform"
)

func (c *Client) LandingCapabilities() platform.LandingCapabilities {
	// MR REST fields separately identify merge and squash commits. Project MR
	// inventory has no documented monotonic tie-breaker across offset pages.
	return platform.LandingCapabilities{OrdinaryMerge: true, Squash: true, AccountType: true}
}

func (c *Client) landingError(field string) error {
	return &platform.Error{Code: platform.ErrCodeProviderContract, Provider: platform.KindGitLab, PlatformHost: c.host, Capability: "landing_evidence", Field: field}
}

func (c *Client) landingRead(ctx context.Context, path string, out any, meter *platform.Meter) (http.Header, []byte, error) {
	if out != nil {
		if err := meter.Records(1); err != nil {
			return nil, nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.baseURL, "/")+"/"+path, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, body, err := meter.ReadHTTP(ctx, c.httpClient, req)
	if err != nil {
		return nil, nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if err := gitlabsdk.CheckResponse(resp); err != nil {
		return nil, nil, c.mapGitLabError("landing_evidence", err)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return nil, nil, err
		}
	}
	return resp.Header, body, nil
}

func gitlabNextEvidencePage(header http.Header, page int) (bool, error) {
	next := header.Get("X-Next-Page")
	if next == "" || next == "0" {
		return platform.NextEvidencePage(header, page)
	}
	value, err := strconv.Atoi(next)
	if err != nil || value <= page || value-page != 1 {
		return false, &platform.Error{Code: platform.ErrCodeProviderContract, Field: "page_progress"}
	}
	return true, nil
}

func (c *Client) landingRepository(ctx context.Context, ref platform.RepoRef, meter *platform.Meter) (platform.LandingRepository, error) {
	var repo struct {
		ID            int64  `json:"id"`
		Path          string `json:"path"`
		FullPath      string `json:"path_with_namespace"`
		DefaultBranch string `json:"default_branch"`
		CloneURL      string `json:"http_url_to_repo"`
	}
	_, _, err := c.landingRead(ctx, "projects/"+url.PathEscape(ref.Owner+"/"+ref.Name), &repo, meter)
	if err != nil {
		return platform.LandingRepository{}, err
	}
	if repo.ID <= 0 || repo.DefaultBranch == "" || repo.Path == "" || !strings.HasSuffix(repo.FullPath, "/"+repo.Path) || (ref.PlatformID > 0 && ref.PlatformID != repo.ID) {
		return platform.LandingRepository{}, c.landingError("repository_identity")
	}
	var branch struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	_, _, err = c.landingRead(ctx, "projects/"+strconv.FormatInt(repo.ID, 10)+"/repository/branches/"+url.PathEscape(repo.DefaultBranch), &branch, meter)
	if err != nil {
		return platform.LandingRepository{}, err
	}
	if branch.Commit.ID == "" {
		return platform.LandingRepository{}, c.landingError("default_branch_head")
	}
	return platform.LandingRepository{Identity: platform.RepositoryIdentity{Provider: platform.KindGitLab, Instance: c.host, ID: strconv.FormatInt(repo.ID, 10)}, Owner: strings.TrimSuffix(repo.FullPath, "/"+repo.Path), Name: repo.Path, DefaultBranch: repo.DefaultBranch, CloneURL: repo.CloneURL, HeadSHA: branch.Commit.ID}, nil
}

func (c *Client) CollectLandingEvidence(ctx context.Context, ref platform.RepoRef, budget platform.Budget) (platform.LandingSnapshot, error) {
	if err := platform.ValidateCanonicalRepoRef(ref); err != nil {
		return platform.LandingSnapshot{}, err
	}
	if ref.Platform != platform.KindGitLab || ref.Host != c.host || c.clock == nil {
		return platform.LandingSnapshot{}, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "instance_or_clock"}
	}
	meter, err := platform.NewMeter(ctx, budget)
	if err != nil {
		return platform.LandingSnapshot{}, err
	}
	snapshot := platform.LandingSnapshot{Schema: platform.LandingSnapshotSchema, StartedAt: c.clock().UTC(), Capabilities: c.LandingCapabilities(), Candidates: []platform.LandingCandidate{}}
	snapshot.Repository, err = c.landingRepository(ctx, ref, meter)
	if err != nil {
		return platform.LandingSnapshot{}, err
	}
	finish := func(reason string) (platform.LandingSnapshot, error) {
		snapshot.Coverage.Reason = reason
		snapshot.CompletedAt = c.clock().UTC()
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
	path := "projects/" + snapshot.Repository.Identity.ID + "/merge_requests"
	seen := make(map[int64]bool)
	for page := 1; ; page++ {
		header, body, err := c.landingRead(ctx, path+"?state=all&scope=all&order_by=created_at&sort=asc&per_page=100&page="+strconv.Itoa(page), nil, meter)
		if err != nil {
			return fail("candidate_inventory_unavailable", err)
		}
		items, err := platform.DecodeEvidencePage[struct {
			ID    int64  `json:"id"`
			IID   int    `json:"iid"`
			State string `json:"state"`
		}](body, 100, meter)
		if err != nil {
			return finish("candidate_inventory_incomplete")
		}
		for _, item := range items {
			if item.ID <= 0 || item.IID <= 0 || seen[item.ID] {
				return finish("candidate_inventory_changed")
			}
			seen[item.ID] = true
			if item.State != "merged" {
				continue
			}
			candidate, err := c.landingCandidate(ctx, path, item.IID, meter)
			if err != nil {
				return fail("candidate_detail_unavailable", err)
			}
			if candidate.ID != strconv.FormatInt(item.ID, 10) || candidate.Number != item.IID || candidate.BaseRepository == nil || *candidate.BaseRepository != snapshot.Repository.Identity {
				return finish("candidate_identity_changed")
			}
			if candidate.TargetBranch == snapshot.Repository.DefaultBranch {
				snapshot.Candidates = append(snapshot.Candidates, candidate)
			}
		}
		more, err := gitlabNextEvidencePage(header, page)
		if err != nil || (more && len(items) == 0) {
			return finish("candidate_inventory_incomplete")
		}
		if !more {
			break
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
	return finish("candidate_inventory_order_unproven")
}

type landingUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Bot      *bool  `json:"bot"`
}

func normalizeLandingUser(user *landingUser) *platform.Account {
	if user == nil || user.ID <= 0 {
		return nil
	}
	kind := platform.AccountUnknown
	if user.Bot != nil {
		kind = platform.AccountUser
		if *user.Bot {
			kind = platform.AccountBot
		}
	}
	return &platform.Account{ID: strconv.FormatInt(user.ID, 10), Login: user.Username, DisplayName: user.Name, Type: kind}
}

func (c *Client) landingCandidate(ctx context.Context, path string, number int, meter *platform.Meter) (platform.LandingCandidate, error) {
	path += "/" + strconv.Itoa(number)
	var mr struct {
		ID              int64          `json:"id"`
		IID             int            `json:"iid"`
		State           string         `json:"state"`
		TargetBranch    string         `json:"target_branch"`
		SourceProjectID *int64         `json:"source_project_id"`
		TargetProjectID *int64         `json:"target_project_id"`
		Author          *landingUser   `json:"author"`
		Merger          *landingUser   `json:"merge_user"`
		CreatedAt       *time.Time     `json:"created_at"`
		MergedAt        *time.Time     `json:"merged_at"`
		Merge           jsontext.Value `json:"merge_commit_sha"`
		Squash          jsontext.Value `json:"squash_commit_sha"`
		SHA             jsontext.Value `json:"sha"`
	}
	if _, _, err := c.landingRead(ctx, path, &mr, meter); err != nil {
		return platform.LandingCandidate{}, err
	}
	if mr.ID <= 0 || mr.IID != number || mr.State != "merged" || mr.MergedAt == nil {
		return platform.LandingCandidate{}, c.landingError("merged_change")
	}
	merge, err := platform.ObserveSHA(mr.Merge)
	if err != nil {
		return platform.LandingCandidate{}, err
	}
	squash, err := platform.ObserveSHA(mr.Squash)
	if err != nil {
		return platform.LandingCandidate{}, err
	}
	head, err := platform.ObserveSHA(mr.SHA)
	if err != nil {
		return platform.LandingCandidate{}, err
	}
	candidate := platform.LandingCandidate{ID: strconv.FormatInt(mr.ID, 10), Number: number, TargetBranch: mr.TargetBranch, Author: normalizeLandingUser(mr.Author), Merger: normalizeLandingUser(mr.Merger), OpenedAt: mr.CreatedAt, MergedAt: mr.MergedAt, MergeSHA: merge, SquashSHA: squash, SourceHeadSHA: head, SourceCommits: []string{}}
	identity := func(id *int64) *platform.RepositoryIdentity {
		if id == nil || *id <= 0 {
			return nil
		}
		return &platform.RepositoryIdentity{Provider: platform.KindGitLab, Instance: c.host, ID: strconv.FormatInt(*id, 10)}
	}
	candidate.SourceRepository = identity(mr.SourceProjectID)
	candidate.BaseRepository = identity(mr.TargetProjectID)
	if merge.Value != "" {
		candidate.TerminalSHA = merge.Value
		candidate.TerminalProof = "gitlab-rest-v4/merge-commit-sha"
	} else if squash.Value != "" {
		candidate.TerminalSHA = squash.Value
		candidate.TerminalProof = "gitlab-rest-v4/squash-commit-sha"
		candidate.Method = platform.LandingSquash
		candidate.MethodProof = candidate.TerminalProof
	}
	for page := 1; ; page++ {
		header, body, err := c.landingRead(ctx, path+"/commits?per_page=100&page="+strconv.Itoa(page), nil, meter)
		if err != nil {
			return candidate, err
		}
		items, err := platform.DecodeEvidencePage[struct {
			ID string `json:"id"`
		}](body, 100, meter)
		if err != nil {
			return candidate, err
		}
		for _, item := range items {
			if item.ID == "" {
				return candidate, c.landingError("source_sha")
			}
			candidate.SourceCommits = append(candidate.SourceCommits, item.ID)
		}
		more, err := gitlabNextEvidencePage(header, page)
		if err != nil {
			return candidate, err
		}
		if !more {
			candidate.SourceComplete = true
			break
		}
		if len(items) == 0 {
			return candidate, c.landingError("source_page")
		}
	}
	return candidate, nil
}

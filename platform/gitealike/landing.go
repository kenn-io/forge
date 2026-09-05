package gitealike

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/platform"
)

// LandingCapabilities are shared only where the pinned Forgejo v3 and Gitea
// v0.25 API shapes agree. Neither account type nor merge method is reported.
func LandingCapabilities() platform.LandingCapabilities {
	return platform.LandingCapabilities{OrdinaryMerge: true}
}

type landingCollector struct {
	kind          platform.Kind
	host, baseURL string
	client        *http.Client
	clock         func() time.Time
	meter         *platform.Meter
}

func (c *landingCollector) failure(field string) error {
	return &platform.Error{Code: platform.ErrCodeProviderContract, Provider: c.kind, PlatformHost: c.host, Capability: "landing_evidence", Field: field}
}
func (c *landingCollector) read(ctx context.Context, path string, out any) (http.Header, []byte, error) {
	if out != nil {
		if err := c.meter.Records(1); err != nil {
			return nil, nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.baseURL, "/")+"/api/v1/"+path, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, body, err := c.meter.ReadHTTP(ctx, c.client, req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, mapTransportError(c.kind, c.host, &HTTPError{StatusCode: resp.StatusCode})
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return nil, nil, err
		}
	}
	return resp.Header, body, nil
}

type landingRepo struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Owner struct {
		Login string `json:"login"`
	} `json:"owner"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
}

func (c *landingCollector) repository(ctx context.Context, ref platform.RepoRef) (platform.LandingRepository, error) {
	path := "repos/" + url.PathEscape(ref.Owner) + "/" + url.PathEscape(ref.Name)
	var repo landingRepo
	if _, _, err := c.read(ctx, path, &repo); err != nil {
		return platform.LandingRepository{}, err
	}
	if repo.ID <= 0 || repo.DefaultBranch == "" || repo.Name == "" || repo.Owner.Login == "" || (ref.PlatformID > 0 && ref.PlatformID != repo.ID) {
		return platform.LandingRepository{}, c.failure("repository_identity")
	}
	var branch struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if _, _, err := c.read(ctx, path+"/branches/"+url.PathEscape(repo.DefaultBranch), &branch); err != nil {
		return platform.LandingRepository{}, err
	}
	if branch.Commit.ID == "" {
		return platform.LandingRepository{}, c.failure("default_branch_head")
	}
	return platform.LandingRepository{Identity: platform.RepositoryIdentity{Provider: c.kind, Instance: c.host, ID: strconv.FormatInt(repo.ID, 10)}, Owner: repo.Owner.Login, Name: repo.Name, DefaultBranch: repo.DefaultBranch, HeadSHA: branch.Commit.ID, CloneURL: repo.CloneURL}, nil
}

// ReadLandingEvidence uses the caller's authenticated transport directly, not
// the SDK response-capture transports that pre-read bodies for interactive UI.
func ReadLandingEvidence(ctx context.Context, client *http.Client, baseURL string, kind platform.Kind, host string, clock func() time.Time, ref platform.RepoRef, budget platform.Budget) (platform.LandingSnapshot, error) {
	if err := platform.ValidateCanonicalRepoRef(ref); err != nil {
		return platform.LandingSnapshot{}, err
	}
	if ref.Platform != kind || ref.Host != host || clock == nil || client == nil {
		return platform.LandingSnapshot{}, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "instance_transport_or_clock"}
	}
	meter, err := platform.NewMeter(ctx, budget)
	if err != nil {
		return platform.LandingSnapshot{}, err
	}
	c := landingCollector{kind: kind, host: host, baseURL: baseURL, client: client, clock: clock, meter: meter}
	snapshot := platform.LandingSnapshot{Schema: platform.LandingSnapshotSchema, StartedAt: clock().UTC(), Capabilities: LandingCapabilities(), Candidates: []platform.LandingCandidate{}}
	snapshot.Repository, err = c.repository(ctx, ref)
	if err != nil {
		return platform.LandingSnapshot{}, err
	}
	finish := func(reason string) (platform.LandingSnapshot, error) {
		snapshot.Coverage.Reason = reason
		snapshot.CompletedAt = clock().UTC()
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
		header, body, err := c.read(ctx, path+"?state=all&sort=oldest&limit=100&page="+strconv.Itoa(page), nil)
		if err != nil {
			return fail("candidate_inventory_unavailable", err)
		}
		items, err := platform.DecodeEvidencePage[struct {
			ID     int64 `json:"id"`
			Number int   `json:"number"`
			Merged bool  `json:"merged"`
		}](body, 100, meter)
		if err != nil {
			return finish("candidate_inventory_incomplete")
		}
		for _, item := range items {
			if item.ID <= 0 || item.Number <= 0 || seen[item.ID] {
				return finish("candidate_inventory_changed")
			}
			seen[item.ID] = true
			if !item.Merged {
				continue
			}
			candidate, err := c.candidate(ctx, path, item.Number)
			if err != nil {
				return fail("candidate_detail_unavailable", err)
			}
			if candidate.ID != strconv.FormatInt(item.ID, 10) || candidate.Number != item.Number || candidate.BaseRepository == nil || *candidate.BaseRepository != snapshot.Repository.Identity {
				return finish("candidate_identity_changed")
			}
			if candidate.TargetBranch == snapshot.Repository.DefaultBranch {
				snapshot.Candidates = append(snapshot.Candidates, candidate)
			}
		}
		more, err := platform.NextEvidencePage(header, page)
		if err != nil || (more && len(items) == 0) {
			return finish("candidate_inventory_incomplete")
		}
		if !more {
			break
		}
	}
	after, err := c.repository(ctx, ref)
	if err != nil {
		return fail("repository_recheck_unavailable", err)
	}
	if after.Identity != snapshot.Repository.Identity {
		return platform.LandingSnapshot{}, c.failure("repository_identity_changed")
	}
	if after.HeadSHA != snapshot.Repository.HeadSHA || after.DefaultBranch != snapshot.Repository.DefaultBranch {
		return finish("default_branch_changed")
	}
	return finish("candidate_inventory_order_unproven")
}

type landingAccount struct {
	ID       int64  `json:"id"`
	Login    string `json:"login"`
	FullName string `json:"full_name"`
}

func landingAccountFact(user *landingAccount) *platform.Account {
	if user == nil || user.ID <= 0 {
		return nil
	}
	return &platform.Account{ID: strconv.FormatInt(user.ID, 10), Login: user.Login, DisplayName: user.FullName, Type: platform.AccountUnknown}
}
func (c *landingCollector) candidate(ctx context.Context, path string, number int) (platform.LandingCandidate, error) {
	path += "/" + strconv.Itoa(number)
	var pull struct {
		ID        int64           `json:"id"`
		Number    int             `json:"number"`
		Merged    bool            `json:"merged"`
		Author    *landingAccount `json:"user"`
		Merger    *landingAccount `json:"merged_by"`
		CreatedAt *time.Time      `json:"created_at"`
		MergedAt  *time.Time      `json:"merged_at"`
		Base      struct {
			Ref  string       `json:"ref"`
			Repo *landingRepo `json:"repo"`
		} `json:"base"`
		Head struct {
			SHA  jsontext.Value `json:"sha"`
			Repo *landingRepo   `json:"repo"`
		} `json:"head"`
		Merge        jsontext.Value `json:"merge_commit_sha"`
		Additions    *int64         `json:"additions"`
		Deletions    *int64         `json:"deletions"`
		FilesChanged *int64         `json:"changed_files"`
	}
	if _, _, err := c.read(ctx, path, &pull); err != nil {
		return platform.LandingCandidate{}, err
	}
	if pull.ID <= 0 || pull.Number != number || !pull.Merged || pull.MergedAt == nil {
		return platform.LandingCandidate{}, c.failure("merged_change")
	}
	merge, err := platform.ObserveSHA(pull.Merge)
	if err != nil {
		return platform.LandingCandidate{}, err
	}
	head, err := platform.ObserveSHA(pull.Head.SHA)
	if err != nil {
		return platform.LandingCandidate{}, err
	}
	candidate := platform.LandingCandidate{ID: strconv.FormatInt(pull.ID, 10), Number: number, TargetBranch: pull.Base.Ref, Author: landingAccountFact(pull.Author), Merger: landingAccountFact(pull.Merger), OpenedAt: pull.CreatedAt, MergedAt: pull.MergedAt, MergeSHA: merge, SourceHeadSHA: head, TerminalSHA: merge.Value, Additions: pull.Additions, Deletions: pull.Deletions, FilesChanged: pull.FilesChanged, SourceCommits: []string{}}
	identity := func(repo *landingRepo) *platform.RepositoryIdentity {
		if repo == nil || repo.ID <= 0 {
			return nil
		}
		return &platform.RepositoryIdentity{Provider: c.kind, Instance: c.host, ID: strconv.FormatInt(repo.ID, 10)}
	}
	candidate.BaseRepository = identity(pull.Base.Repo)
	candidate.SourceRepository = identity(pull.Head.Repo)
	if merge.Value != "" {
		candidate.TerminalProof = string(c.kind) + "-rest-v1/merged-merge-commit-sha"
	}
	seen := make(map[string]bool)
	for page := 1; ; page++ {
		header, body, err := c.read(ctx, path+"/commits?limit=100&page="+strconv.Itoa(page), nil)
		if err != nil {
			return candidate, err
		}
		items, err := platform.DecodeEvidencePage[struct {
			SHA string `json:"sha"`
		}](body, 100, c.meter)
		if err != nil {
			return candidate, err
		}
		for _, item := range items {
			if item.SHA == "" || seen[item.SHA] {
				return candidate, c.failure("source_page")
			}
			seen[item.SHA] = true
			candidate.SourceCommits = append(candidate.SourceCommits, item.SHA)
		}
		more, err := platform.NextEvidencePage(header, page)
		if err != nil {
			return candidate, err
		}
		if !more && len(items) == 0 {
			candidate.SourceComplete = true
			break
		}
		if more && len(items) == 0 {
			return candidate, c.failure("source_page")
		}
		if total, err := strconv.Atoi(header.Get("X-Total-Count")); err == nil && !more && total == len(candidate.SourceCommits) {
			candidate.SourceComplete = true
			break
		}
	}
	return candidate, nil
}

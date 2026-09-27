package bitbucketdc

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/platform"
)

type link struct {
	Href string `json:"href"`
	Name string `json:"name"`
}
type links struct {
	Self  []link `json:"self"`
	Clone []link `json:"clone"`
}

func (l links) webURL() string {
	if len(l.Self) > 0 {
		return l.Self[0].Href
	}
	return ""
}

func (l links) cloneURL() string {
	for _, v := range l.Clone {
		if v.Name == "http" || v.Name == "https" {
			u, err := url.Parse(v.Href)
			if err == nil {
				u.User = nil
				return u.String()
			}
		}
	}
	return ""
}

type user struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}
type repository struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	Public      bool   `json:"public"`
	Archived    bool   `json:"archived"`
	Project     struct {
		Key string `json:"key"`
	} `json:"project"`
	Links links `json:"links"`
}

func (r repository) normalize(host string) (platform.Repository, error) {
	if r.ID <= 0 || r.Project.Key == "" || r.Slug == "" {
		return platform.Repository{}, platform.ProviderContract(platform.KindBitbucket, host, "repository", errors.New("missing repository identity"))
	}
	id := strconv.FormatInt(r.ID, 10)
	ref := platform.RepoRef{Platform: platform.KindBitbucket, Host: host, Owner: r.Project.Key, Name: r.Slug, RepoPath: r.Project.Key + "/" + r.Slug, PlatformID: r.ID, PlatformExternalID: id, WebURL: r.Links.webURL(), CloneURL: r.Links.cloneURL()}
	return platform.Repository{Ref: ref, PlatformID: r.ID, PlatformExternalID: id, Description: r.Description, Private: !r.Public, Archived: r.Archived, WebURL: ref.WebURL, CloneURL: ref.CloneURL, Features: platform.RepositoryFeatures{IssuesEnabled: new(false), MergeRequestsEnabled: new(true)}}, nil
}

type branch struct {
	ID           string     `json:"id"`
	DisplayID    string     `json:"displayId"`
	LatestCommit string     `json:"latestCommit"`
	Repository   repository `json:"repository"`
}
type participant struct {
	User               user   `json:"user"`
	Status             string `json:"status"`
	Approved           bool   `json:"approved"`
	LastReviewedCommit string `json:"lastReviewedCommit"`
}
type pull struct {
	ID          int           `json:"id"`
	Version     int           `json:"version"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	State       string        `json:"state"`
	Draft       bool          `json:"draft"`
	Created     int64         `json:"createdDate"`
	Updated     int64         `json:"updatedDate"`
	From        branch        `json:"fromRef"`
	To          branch        `json:"toRef"`
	Author      participant   `json:"author"`
	Reviewers   []participant `json:"reviewers"`
	Links       links         `json:"links"`
	Properties  struct {
		MergeCommit struct {
			ID string `json:"id"`
		} `json:"mergeCommit"`
	} `json:"properties"`
}

func (p pull) normalize(ref platform.RepoRef) (platform.MergeRequest, error) {
	if p.ID <= 0 {
		return platform.MergeRequest{}, platform.ProviderContract(ref.Platform, ref.Host, "pull request", errors.New("missing pull request id"))
	}
	state := strings.ToLower(p.State)
	switch state {
	case "open", "merged":
	case "declined":
		state = "closed"
	default:
		return platform.MergeRequest{}, platform.ProviderContract(ref.Platform, ref.Host, "state", errors.New("unknown pull request state"))
	}
	reviewers := make([]string, 0, len(p.Reviewers))
	decision := ""
	for _, r := range p.Reviewers {
		reviewers = append(reviewers, r.User.Name)
		if r.Status == "NEEDS_WORK" {
			decision = "CHANGES_REQUESTED"
		} else if r.Approved && decision == "" {
			decision = "APPROVED"
		}
	}
	return platform.MergeRequest{Repo: ref, PlatformID: int64(p.ID), PlatformExternalID: strconv.Itoa(p.ID), Number: p.ID, URL: p.Links.webURL(), Title: p.Title, Body: p.Description, State: state, IsDraft: p.Draft, Author: p.Author.User.Name, AuthorDisplayName: p.Author.User.DisplayName, HeadBranch: strings.TrimPrefix(p.From.ID, "refs/heads/"), BaseBranch: strings.TrimPrefix(p.To.ID, "refs/heads/"), HeadSHA: p.From.LatestCommit, BaseSHA: p.To.LatestCommit, HeadRepoCloneURL: p.From.Repository.Links.cloneURL(), HeadRepoCloneURLUnknown: p.From.Repository.Links.cloneURL() == "", CreatedAt: time.UnixMilli(p.Created).UTC(), UpdatedAt: time.UnixMilli(p.Updated).UTC(), LastActivityAt: time.UnixMilli(p.Updated).UTC(), RequestedReviewers: reviewers, ReviewDecision: decision, MergeCommitSHA: p.Properties.MergeCommit.ID}, nil
}

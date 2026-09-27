package bitbucket

import (
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
	HTML  link   `json:"html"`
	Clone []link `json:"clone"`
}
type user struct {
	UUID        string `json:"uuid"`
	DisplayName string `json:"display_name"`
}
type content struct {
	Raw string `json:"raw"`
}
type commit struct {
	Hash string `json:"hash"`
}
type repository struct {
	UUID        string `json:"uuid"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Private     bool   `json:"is_private"`
	HasIssues   bool   `json:"has_issues"`
	MainBranch  struct {
		Name string `json:"name"`
	} `json:"mainbranch"`
	Links   links     `json:"links"`
	Created time.Time `json:"created_on"`
	Updated time.Time `json:"updated_on"`
}

func (r repository) normalize() (platform.Repository, error) {
	owner, name, ok := strings.Cut(r.FullName, "/")
	if !ok || owner == "" || name == "" || r.UUID == "" {
		return platform.Repository{}, missing("repository identity")
	}
	ref := platform.RepoRef{Platform: platform.KindBitbucket, Host: platform.DefaultBitbucketHost, Owner: owner, Name: name, RepoPath: r.FullName, PlatformExternalID: r.UUID, WebURL: r.Links.HTML.Href, DefaultBranch: r.MainBranch.Name, CloneURL: r.cloneURL()}
	return platform.Repository{Ref: ref, PlatformExternalID: r.UUID, Description: r.Description, Private: r.Private, DefaultBranch: ref.DefaultBranch, WebURL: ref.WebURL, CloneURL: ref.CloneURL, CreatedAt: r.Created.UTC(), UpdatedAt: r.Updated.UTC(), Features: platform.RepositoryFeatures{IssuesEnabled: new(r.HasIssues), MergeRequestsEnabled: new(true)}}, nil
}

func (r repository) cloneURL() string {
	for _, l := range r.Links.Clone {
		if l.Name == "https" {
			u, err := url.Parse(l.Href)
			if err == nil {
				u.User = nil
				return u.String()
			}
		}
	}
	owner, name, ok := strings.Cut(r.FullName, "/")
	if ok && owner != "" && name != "" {
		return "https://bitbucket.org/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + ".git"
	}
	return ""
}

type branch struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
	Commit     commit     `json:"commit"`
	Repository repository `json:"repository"`
}
type participant struct {
	ParticipatedOn time.Time `json:"participated_on"`
	User           user      `json:"user"`
	Approved       bool      `json:"approved"`
	State          string    `json:"state"`
}
type pull struct {
	ID           int           `json:"id"`
	Title        string        `json:"title"`
	Description  string        `json:"description"`
	State        string        `json:"state"`
	Draft        bool          `json:"draft"`
	Author       user          `json:"author"`
	ClosedBy     user          `json:"closed_by"`
	Source       branch        `json:"source"`
	Destination  branch        `json:"destination"`
	Links        links         `json:"links"`
	MergeCommit  commit        `json:"merge_commit"`
	CommentCount int           `json:"comment_count"`
	Created      time.Time     `json:"created_on"`
	Updated      time.Time     `json:"updated_on"`
	Reviewers    []user        `json:"reviewers"`
	Participants []participant `json:"participants"`
}

func (p pull) normalize(ref platform.RepoRef) (platform.MergeRequest, error) {
	if p.ID <= 0 {
		return platform.MergeRequest{}, missing("pull request id")
	}
	state := "open"
	switch p.State {
	case "MERGED":
		state = "merged"
	case "DECLINED", "SUPERSEDED":
		state = "closed"
	case "OPEN":
	default:
		return platform.MergeRequest{}, missing("known pull request state")
	}
	mergedBy := ""
	if state == "merged" {
		mergedBy = p.ClosedBy.UUID
	}
	reviewers := make([]string, 0, len(p.Reviewers))
	for _, reviewer := range p.Reviewers {
		reviewers = append(reviewers, reviewer.UUID)
	}
	decision := ""
	for _, reviewer := range p.Participants {
		if reviewer.State == "changes_requested" {
			decision = "CHANGES_REQUESTED"
			break
		}
		if reviewer.Approved {
			decision = "APPROVED"
		}
	}
	return platform.MergeRequest{Repo: ref, PlatformID: int64(p.ID), PlatformExternalID: strconv.Itoa(p.ID), Number: p.ID, URL: p.Links.HTML.Href, Title: p.Title, Body: p.Description, State: state, IsDraft: p.Draft, Author: p.Author.UUID, AuthorDisplayName: p.Author.DisplayName, HeadBranch: p.Source.Branch.Name, BaseBranch: p.Destination.Branch.Name, HeadSHA: p.Source.Commit.Hash, BaseSHA: p.Destination.Commit.Hash, HeadRepoCloneURL: p.Source.Repository.cloneURL(), HeadRepoCloneURLUnknown: p.Source.Repository.UUID != "" && p.Source.Repository.cloneURL() == "", MergeCommitSHA: p.MergeCommit.Hash, MergedBy: mergedBy, CommentCount: p.CommentCount, CreatedAt: p.Created.UTC(), UpdatedAt: p.Updated.UTC(), LastActivityAt: p.Updated.UTC(), RequestedReviewers: reviewers, ReviewDecision: decision}, nil
}

type issue struct {
	ID       int       `json:"id"`
	Title    string    `json:"title"`
	Content  content   `json:"content"`
	State    string    `json:"state"`
	Reporter user      `json:"reporter"`
	Assignee *user     `json:"assignee"`
	Links    links     `json:"links"`
	Created  time.Time `json:"created_on"`
	Updated  time.Time `json:"updated_on"`
}

func (i issue) normalize(ref platform.RepoRef) (platform.Issue, error) {
	if i.ID <= 0 {
		return platform.Issue{}, missing("issue id")
	}
	state := "open"
	switch i.State {
	case "new", "open", "on hold":
	case "resolved", "invalid", "duplicate", "wontfix", "closed":
		state = "closed"
	default:
		return platform.Issue{}, missing("known issue state")
	}
	assignees := []string{}
	if i.Assignee != nil {
		assignees = append(assignees, i.Assignee.UUID)
	}
	return platform.Issue{Repo: ref, PlatformID: int64(i.ID), PlatformExternalID: strconv.Itoa(i.ID), Number: i.ID, URL: i.Links.HTML.Href, Title: i.Title, Body: i.Content.Raw, State: state, Author: i.Reporter.UUID, Assignees: assignees, CreatedAt: i.Created.UTC(), UpdatedAt: i.Updated.UTC(), LastActivityAt: i.Updated.UTC()}, nil
}

type comment struct {
	Updated    time.Time `json:"updated_on"`
	Resolution *struct {
		Created time.Time `json:"created_on"`
	} `json:"resolution"`
	ID      int64     `json:"id"`
	Content content   `json:"content"`
	User    user      `json:"user"`
	Created time.Time `json:"created_on"`
	Deleted bool      `json:"deleted"`
	Links   links     `json:"links"`
	Parent  *struct {
		ID int64 `json:"id"`
	} `json:"parent"`
	Inline *struct {
		Path      string `json:"path"`
		StartFrom *int   `json:"start_from"`
		StartTo   *int   `json:"start_to"`
		From      *int   `json:"from"`
		To        *int   `json:"to"`
	} `json:"inline"`
}

func (c comment) pullEvent(ref platform.RepoRef, number int) platform.MergeRequestEvent {
	id := strconv.FormatInt(c.ID, 10)
	thread := id
	if c.Parent != nil {
		thread = strconv.FormatInt(c.Parent.ID, 10)
	}
	return platform.MergeRequestEvent{Repo: ref, PlatformID: c.ID, PlatformExternalID: id, MergeRequestNumber: number, EventType: "comment", Author: c.User.UUID, Body: c.Content.Raw, CreatedAt: c.Created.UTC(), DirectURL: c.Links.HTML.Href, ThreadID: thread, DedupeKey: "comment:" + id}
}

func (c comment) issueEvent(ref platform.RepoRef, number int) platform.IssueEvent {
	id := strconv.FormatInt(c.ID, 10)
	return platform.IssueEvent{Repo: ref, PlatformID: c.ID, PlatformExternalID: id, IssueNumber: number, EventType: "comment", Author: c.User.UUID, Body: c.Content.Raw, CreatedAt: c.Created.UTC(), DirectURL: c.Links.HTML.Href, DedupeKey: "comment:" + id}
}

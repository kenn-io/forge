package db

import (
	"errors"
	"strings"
	"time"
)

type KataLinkSubjectKind string

const (
	KataLinkSubjectPullRequest KataLinkSubjectKind = "pull_request"
	KataLinkSubjectIssue       KataLinkSubjectKind = "issue"
	KataLinkSubjectWorkspace   KataLinkSubjectKind = "workspace"
)

type KataLinkSubject struct {
	Kind                   KataLinkSubjectKind
	RepoID                 int64
	ProviderItemExternalID string
	WorkspaceID            string
}

type KataIssueLink struct {
	ID         int64
	Subject    KataLinkSubject
	DaemonID   string
	ProjectUID string
	IssueUID   string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (s KataLinkSubject) normalized() (KataLinkSubject, error) {
	s.ProviderItemExternalID = strings.TrimSpace(s.ProviderItemExternalID)
	s.WorkspaceID = strings.TrimSpace(s.WorkspaceID)
	switch s.Kind {
	case KataLinkSubjectPullRequest, KataLinkSubjectIssue:
		if s.RepoID <= 0 || s.ProviderItemExternalID == "" || s.WorkspaceID != "" {
			return KataLinkSubject{}, errors.New("provider Kata link subject requires only repo and external item identity")
		}
	case KataLinkSubjectWorkspace:
		if s.RepoID != 0 || s.ProviderItemExternalID != "" || s.WorkspaceID == "" {
			return KataLinkSubject{}, errors.New("workspace Kata link subject requires only workspace identity")
		}
	default:
		return KataLinkSubject{}, errors.New("unknown Kata link subject kind")
	}
	return s, nil
}

func (l KataIssueLink) normalized() (KataIssueLink, error) {
	var err error
	l.Subject, err = l.Subject.normalized()
	if err != nil {
		return KataIssueLink{}, err
	}
	l.DaemonID = strings.TrimSpace(l.DaemonID)
	l.ProjectUID = strings.TrimSpace(l.ProjectUID)
	l.IssueUID = strings.TrimSpace(l.IssueUID)
	if l.DaemonID == "" || l.ProjectUID == "" || l.IssueUID == "" {
		return KataIssueLink{}, errors.New("kata link requires daemon, project, and issue identity")
	}
	return l, nil
}

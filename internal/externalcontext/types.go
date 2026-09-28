// Package externalcontext executes configured PR context commands.
package externalcontext

import (
	"errors"

	"github.com/danielgtaylor/huma/v2"
)

type PullRequest struct {
	Provider       string `json:"provider"`
	PlatformHost   string `json:"platform_host"`
	PlatformRepoID string `json:"platform_repo_id"`
	RepoPath       string `json:"repo_path"`
	Number         int    `json:"number"`
	URL            string `json:"url"`
	State          string `json:"state"`
	HeadSHA        string `json:"head_sha"`
	BaseSHA        string `json:"base_sha"`
}

type ExternalContextSourceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ExternalContextResult struct {
	Card *ExternalContextCard `json:"card"`
}

type ExternalContextCard struct {
	Status              string                  `json:"status" enum:"neutral,pending,success,warning,error"`
	Summary             string                  `json:"summary"`
	Markdown            string                  `json:"markdown,omitempty"`
	ResultHeadSHA       string                  `json:"result_head_sha,omitempty"`
	Actions             []ExternalContextAction `json:"actions,omitempty"`
	RefreshAfterSeconds int                     `json:"refresh_after_seconds,omitempty"`
}

// A source returns null when it does not apply to the pull request.
func (*ExternalContextCard) TransformSchema(_ huma.Registry, schema *huma.Schema) *huma.Schema {
	schema.Nullable = true
	return schema
}

type ExternalContextAction struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	DisabledReason string `json:"disabled_reason,omitempty"`
}

var (
	ErrUnknownSource   = errors.New("external context source not found")
	ErrTimeout         = errors.New("external context command timed out")
	ErrOutputLimit     = errors.New("external context command exceeded its output limit")
	ErrInvalidResponse = errors.New("external context command returned an invalid response")
	ErrInvocation      = errors.New("external context command failed")
	ErrClosed          = errors.New("external context runner is closed")
)

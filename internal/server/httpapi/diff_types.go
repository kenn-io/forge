package httpapi

import (
	"time"

	"go.kenn.io/middleman/internal/gitclone"
)

type DiffResponse struct {
	Stale               bool                `json:"stale"`
	WhitespaceOnlyCount int                 `json:"whitespace_only_count"`
	Files               []gitclone.DiffFile `json:"files"`
	SnapshotVersion     string              `json:"snapshot_version,omitempty" doc:"Opaque workspace diff snapshot version used to keep files and patches coherent."`
	DiffHeadSHA         string              `json:"diff_head_sha,omitempty" doc:"Synced PR diff snapshot head this diff was computed from. Always set for pull request diffs (the endpoint fails when no snapshot head is synced); empty for commit and workspace diffs. Compare with the pull detail's platform_head_sha to detect stale cached diff context; unrelated to 'stale', which reports clone-refresh staleness."`
}

type FilesResponse struct {
	Stale               bool                `json:"stale"`
	WhitespaceOnlyCount int                 `json:"whitespace_only_count"`
	Files               []gitclone.DiffFile `json:"files"`
	SnapshotVersion     string              `json:"snapshot_version,omitempty" doc:"Opaque workspace diff snapshot version to pin on the following workspace diff request."`
}

type FilePreviewResponse struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Encoding  string `json:"encoding"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
}

type CommitResponse struct {
	SHA        string    `json:"sha"         doc:"Full commit SHA"`
	Message    string    `json:"message"     doc:"First line of commit message"`
	AuthorName string    `json:"author_name" doc:"Commit author display name"`
	AuthoredAt time.Time `json:"authored_at" doc:"Commit author date (RFC3339)"`
	Pushed     *bool     `json:"pushed,omitempty" doc:"Whether the commit is reachable from the workspace branch's upstream tracking ref; false means it has not been pushed. Omitted when push status is unknown, such as pull request commits."`
}

type CommitsResponse struct {
	Commits []CommitResponse `json:"commits" doc:"Commits in newest-first order"`
}

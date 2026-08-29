package fleet

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"go.kenn.io/forge/internal/db"
)

// EnrichProviderState overlays hub-owned provider facts onto an
// observer-neutral aggregate. Repository lookup crosses the federation
// boundary only through provider identity; daemon-local numeric repository IDs
// are used only inside this function and never copied into the result.
func EnrichProviderState(
	ctx context.Context,
	database *db.DB,
	aggregate NeutralSnapshot,
) (NeutralSnapshot, error) {
	out := aggregate
	out.Worktrees = append([]RawWorktree(nil), aggregate.Worktrees...)
	out.Workspaces = append([]RawWorkspace(nil), aggregate.Workspaces...)

	projects := make(map[providerProjectKey]RepositoryIdentity, len(aggregate.Projects))
	for _, project := range aggregate.Projects {
		projects[providerProjectKey{project.HostKey, project.ScopedKey}] = project.Repository
	}
	repositories := make(map[string]*db.RepositoryCatalogEntry)

	for index := range out.Worktrees {
		worktree := &out.Worktrees[index]
		clearWorktreeProviderState(worktree)
		if worktree.LinkedPRNumber == nil {
			continue
		}
		identity := projects[providerProjectKey{worktree.HostKey, worktree.ProjectKey}]
		repository, err := providerRepository(ctx, database, repositories, identity)
		if err != nil {
			return NeutralSnapshot{}, err
		}
		if repository == nil {
			continue
		}
		pull, err := database.GetVisibleMergeRequestByRepoIDAndNumber(
			ctx, repository.Repository.ID, *worktree.LinkedPRNumber,
		)
		if err != nil {
			return NeutralSnapshot{}, err
		}
		if pull != nil {
			enrichWorktreeFromPull(worktree, pull)
		}
	}

	for index := range out.Workspaces {
		workspace := &out.Workspaces[index]
		clearWorkspaceProviderState(workspace)
		repository, err := providerRepository(
			ctx, database, repositories, workspace.Repository,
		)
		if err != nil {
			return NeutralSnapshot{}, err
		}
		if repository == nil || workspace.ItemNumber <= 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(workspace.ItemType)) {
		case "pr", "pull_request", "merge_request":
			pull, err := database.GetVisibleMergeRequestByRepoIDAndNumber(
				ctx, repository.Repository.ID, workspace.ItemNumber,
			)
			if err != nil {
				return NeutralSnapshot{}, err
			}
			if pull != nil {
				enrichWorkspaceFromPull(workspace, pull)
			}
		case "issue":
			issue, err := database.GetVisibleIssueByRepoIDAndNumber(
				ctx, repository.Repository.ID, workspace.ItemNumber,
			)
			if err != nil {
				return NeutralSnapshot{}, err
			}
			if issue != nil {
				enrichWorkspaceFromIssue(workspace, issue)
			}
		}
	}
	return out, nil
}

type providerProjectKey struct {
	host    string
	project string
}

func providerRepository(
	ctx context.Context,
	database *db.DB,
	cache map[string]*db.RepositoryCatalogEntry,
	identity RepositoryIdentity,
) (*db.RepositoryCatalogEntry, error) {
	if database == nil || strings.TrimSpace(identity.Provider) == "" ||
		strings.TrimSpace(identity.PlatformHost) == "" ||
		strings.TrimSpace(identity.PlatformRepoID) == "" {
		return nil, nil
	}
	key := strings.ToLower(strings.TrimSpace(identity.Provider)) + "\x00" +
		strings.ToLower(strings.TrimSpace(identity.PlatformHost)) + "\x00" +
		strings.TrimSpace(identity.PlatformRepoID)
	if repository, ok := cache[key]; ok {
		return repository, nil
	}
	repository, err := database.GetRepositoryByProviderID(
		ctx, identity.Provider, identity.PlatformHost, identity.PlatformRepoID,
	)
	if err != nil {
		return nil, err
	}
	cache[key] = repository
	return repository, nil
}

func clearWorktreeProviderState(worktree *RawWorktree) {
	worktree.PRState = nil
	worktree.PRTitle = nil
	worktree.ChecksStatus = nil
	worktree.PRReviewDecision = nil
	worktree.PRMergeable = nil
	worktree.PRAdditions = nil
	worktree.PRDeletions = nil
	worktree.PRCommentCount = nil
	worktree.PRURL = nil
	worktree.PRUpdatedAt = nil
	worktree.ChecksDetail = nil
	worktree.LastPolledAt = nil
}

func enrichWorktreeFromPull(worktree *RawWorktree, pull *db.MergeRequest) {
	state := strings.TrimSpace(string(pull.State))
	if strings.EqualFold(state, "open") && pull.IsDraft {
		state = "draft"
	}
	worktree.PRState = pointerIfNotEmpty(state)
	worktree.PRTitle = pointerIfNotEmpty(pull.Title)
	worktree.ChecksStatus = pointerIfNotEmpty(pull.CIStatus)
	worktree.PRReviewDecision = pointerIfNotEmpty(pull.ReviewDecision)
	worktree.PRMergeable = pointerIfNotEmpty(pull.MergeableState)
	worktree.PRAdditions = pointerIfKnown(pull.Additions, pull.AdditionsKnown)
	worktree.PRDeletions = pointerIfKnown(pull.Deletions, pull.DeletionsKnown)
	worktree.PRCommentCount = pointerIfNonzero(pull.CommentCount)
	worktree.PRURL = pointerIfNotEmpty(pull.URL)
	worktree.PRUpdatedAt = pointerTime(pull.UpdatedAt)
	worktree.LastPolledAt = pointerTimeValue(pull.DetailFetchedAt)
	worktree.ChecksDetail = decodeCheckDetails(pull.CIChecksJSON)
}

func clearWorkspaceProviderState(workspace *RawWorkspace) {
	workspace.ItemLastActivityAt = nil
	workspace.MRTitle = nil
	workspace.MRState = nil
	workspace.MRIsDraft = nil
	workspace.MRCIStatus = nil
	workspace.MRReviewDecision = nil
	workspace.MRAdditions = nil
	workspace.MRDeletions = nil
}

func enrichWorkspaceFromPull(workspace *RawWorkspace, pull *db.MergeRequest) {
	workspace.ItemLastActivityAt = pointerTime(pull.LastActivityAt)
	workspace.MRTitle = pointerIfNotEmpty(pull.Title)
	workspace.MRState = pointerIfNotEmpty(string(pull.State))
	workspace.MRIsDraft = &pull.IsDraft
	workspace.MRCIStatus = pointerIfNotEmpty(pull.CIStatus)
	workspace.MRReviewDecision = pointerIfNotEmpty(pull.ReviewDecision)
	workspace.MRAdditions = pointerIfKnown(pull.Additions, pull.AdditionsKnown)
	workspace.MRDeletions = pointerIfKnown(pull.Deletions, pull.DeletionsKnown)
}

func enrichWorkspaceFromIssue(workspace *RawWorkspace, issue *db.Issue) {
	workspace.ItemLastActivityAt = pointerTime(issue.LastActivityAt)
	workspace.MRTitle = pointerIfNotEmpty(issue.Title)
	workspace.MRState = pointerIfNotEmpty(issue.State)
}

func pointerIfNotEmpty(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func pointerIfNonzero(value int) *int {
	if value == 0 {
		return nil
	}
	return &value
}

func pointerIfKnown(value int, known bool) *int {
	if !known && value == 0 {
		return nil
	}
	return &value
}

func pointerTime(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

func pointerTimeValue(value *time.Time) *string {
	if value == nil {
		return nil
	}
	return pointerTime(*value)
}

func decodeCheckDetails(raw string) []CheckDetail {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var checks []db.CICheck
	if err := json.Unmarshal([]byte(raw), &checks); err != nil {
		return nil
	}
	out := make([]CheckDetail, 0, len(checks))
	for _, check := range checks {
		out = append(out, CheckDetail{
			Name:       check.Name,
			Status:     strings.ToLower(check.Status),
			URL:        check.URL,
			Conclusion: strings.ToLower(check.Conclusion),
		})
	}
	return out
}

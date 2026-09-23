package itemapi

import (
	"strings"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/platform"
)

type StarredRequest struct {
	ItemType     string `json:"item_type"`
	Provider     string `json:"provider"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Number       int    `json:"number"`
	PlatformHost string `json:"platform_host"`
}

func WorkspaceItemTypeFromActivity(itemType string) string {
	switch itemType {
	case "pr":
		return db.WorkspaceItemTypePullRequest
	case "issue":
		return db.WorkspaceItemTypeIssue
	default:
		return ""
	}
}

func WorkspaceRefForActivityItem(
	snapshot workspaceapi.WorkspaceSubjectSnapshot,
	item db.ActivityItem,
) *workspaceapi.WorkspaceRef {
	workspaceItemType := WorkspaceItemTypeFromActivity(item.ItemType)
	if workspaceItemType == "" {
		return nil
	}
	return WorkspaceRefForActivitySubjectKey(snapshot, db.WorkspaceSubjectKey{
		RepoID: item.RepoID, ItemType: workspaceItemType, ItemNumber: item.ItemNumber,
	})
}

func WorkspaceRefForActivitySubjectKey(
	snapshot workspaceapi.WorkspaceSubjectSnapshot,
	key db.WorkspaceSubjectKey,
) *workspaceapi.WorkspaceRef {
	var ref workspaceapi.WorkspaceRef
	var ok bool
	if key.ItemType == db.WorkspaceItemTypeIssue {
		ref, ok = snapshot.OwnReferences[key]
	} else if subject, exists := snapshot.Subjects[key]; exists {
		ref, ok = subject.Workspace, true
	}
	if !ok {
		return nil
	}
	return &ref
}

// parseRepoFilter splits the shared repo query parameter used by pull, issue,
// and activity list endpoints. Repository filters must be provider-qualified as
// provider|platform_host/repo_path. Repo paths can contain slashes, so hosted
// filters keep everything after the host together as repoPath.
func ParseRepoFilter(repo string) (provider, platformHost, owner, name, repoPath string) {
	repo = strings.Trim(repo, "/ ")
	if providerPart, hostedPath, ok := strings.Cut(repo, "|"); ok {
		provider := strings.ToLower(strings.TrimSpace(providerPart))
		if _, ok := platform.MetadataFor(platform.Kind(provider)); !ok {
			return "", "", "", "", ""
		}
		parts := strings.Split(strings.Trim(hostedPath, "/ "), "/")
		if len(parts) < 2 {
			return "", "", "", "", ""
		}
		return provider, parts[0], "", "", strings.Join(parts[1:], "/")
	}
	return "", "", "", "", ""
}

func ParseRepoFilters(repo string) []db.RepoFilter {
	parts := strings.Split(repo, ",")
	filters := make([]db.RepoFilter, 0, len(parts))
	for _, part := range parts {
		provider, platformHost, owner, name, repoPath := ParseRepoFilter(part)
		if repoPath != "" {
			filters = append(filters, db.RepoFilter{
				Platform:     provider,
				PlatformHost: platformHost,
				RepoPath:     repoPath,
			})
		} else if owner != "" {
			filters = append(filters, db.RepoFilter{
				Platform:     provider,
				PlatformHost: platformHost,
				RepoOwner:    owner,
				RepoName:     name,
			})
		}
	}
	return filters
}

func HasInvalidRepoFilter(repo string) bool {
	for part := range strings.SplitSeq(repo, ",") {
		part = strings.Trim(part, "/ ")
		if part == "" {
			continue
		}
		_, _, owner, _, repoPath := ParseRepoFilter(part)
		if owner == "" && repoPath == "" {
			return true
		}
	}
	return false
}

func ValidateStarredRequest(body StarredRequest) bool {
	return body.ItemType == "pr" || body.ItemType == "issue"
}

// formatUTCRFC3339 is the server's API boundary formatter for timestamps.
// Handlers pass absolute instants through this helper so JSON always leaves
// kenn-forge as explicit UTC RFC3339, regardless of how a test or caller
// constructed the original time.Time.
func FormatUTCRFC3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

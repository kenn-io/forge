package stacks

import (
	"context"
	"slices"
	"strings"

	"go.kenn.io/middleman/internal/db"
)

// DetectChains finds linear PR chains from branch metadata.
// Returns chains of length >= 2, ordered base-to-tip.
func DetectChains(prs []db.MergeRequest) [][]db.MergeRequest {
	// Sort by number for deterministic tie-breaking.
	sorted := slices.Clone(prs)
	slices.SortFunc(sorted, db.MergeRequest.Compare)

	// head_branch -> PR. Prefer open over merged; within same state, lowest number wins.
	headMap := make(map[string]db.MergeRequest, len(sorted))
	for _, pr := range sorted {
		existing, exists := headMap[pr.HeadBranch]
		if !exists {
			headMap[pr.HeadBranch] = pr
		} else if existing.State == "merged" && pr.State == "open" {
			headMap[pr.HeadBranch] = pr
		}
	}

	// Keep only preferred PR per head_branch to avoid ambiguous chains.
	preferred := make([]db.MergeRequest, 0, len(headMap))
	for _, pr := range sorted {
		if p, ok := headMap[pr.HeadBranch]; ok && p.ID == pr.ID {
			preferred = append(preferred, pr)
		}
	}

	// base_branch -> []PR (children targeting that base).
	childMap := make(map[string][]db.MergeRequest)
	for _, pr := range preferred {
		childMap[pr.BaseBranch] = append(childMap[pr.BaseBranch], pr)
	}

	// Find bases: PRs whose base_branch is NOT in headMap.
	var bases []db.MergeRequest
	for _, pr := range preferred {
		if _, isHead := headMap[pr.BaseBranch]; !isHead {
			bases = append(bases, pr)
		}
	}

	// Walk chains from each base.
	var chains [][]db.MergeRequest
	for _, base := range bases {
		chain := walkChain(base, childMap)
		if len(chain) >= 2 {
			chains = append(chains, chain)
		}
	}

	return chains
}

func walkChain(
	start db.MergeRequest,
	childMap map[string][]db.MergeRequest,
) []db.MergeRequest {
	visited := make(map[string]bool)
	var chain []db.MergeRequest
	current := start

	for {
		if visited[current.HeadBranch] {
			return nil // cycle
		}
		visited[current.HeadBranch] = true
		chain = append(chain, current)

		children := childMap[current.HeadBranch]
		if len(children) == 0 {
			break
		}
		current = preferredChild(children)
	}

	return chain
}

func preferredChild(children []db.MergeRequest) db.MergeRequest {
	// Children inherit deterministic number ordering from DetectChains.
	for _, child := range children {
		if child.State == "open" {
			return child
		}
	}
	return children[0]
}

func hasOpenMember(chain []db.MergeRequest) bool {
	for _, pr := range chain {
		if pr.State == "open" {
			return true
		}
	}
	return false
}

var conventionalPrefixes = []string{
	"feature/", "feat/", "fix/", "bugfix/",
	"hotfix/", "chore/", "refactor/", "docs/",
}

// DeriveStackName computes a stack name from branch names.
func DeriveStackName(chain []db.MergeRequest) string {
	if len(chain) == 0 {
		return ""
	}
	branches := make([]string, len(chain))
	for i, pr := range chain {
		b := pr.HeadBranch
		for _, prefix := range conventionalPrefixes {
			b = strings.TrimPrefix(b, prefix)
		}
		branches[i] = b
	}

	prefix := tokenBoundaryPrefix(branches)
	if prefix != "" {
		return prefix
	}
	return chain[0].Title
}

func tokenBoundaryPrefix(names []string) string {
	if len(names) < 2 {
		return ""
	}
	prefix := names[0]
	for _, name := range names[1:] {
		prefix = commonPrefix(prefix, name)
		if prefix == "" {
			return ""
		}
	}
	// Trim to last token boundary.
	separators := "/-_"
	trimmed := strings.TrimRight(prefix, separators)
	if trimmed == "" {
		return ""
	}
	// Verify we stopped at a boundary, not mid-word.
	for _, name := range names {
		if len(name) > len(trimmed) {
			next := name[len(trimmed)]
			if !strings.ContainsRune(separators, rune(next)) {
				return ""
			}
		}
	}
	return trimmed
}

func commonPrefix(a, b string) string {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}

// RunDetection detects stacks for a single repo and persists results.
func RunDetection(ctx context.Context, database *db.DB, repoID int64) error {
	prs, err := database.ListPRsForStackDetection(ctx, repoID)
	if err != nil {
		return err
	}

	chains := DetectChains(prs)

	var activeIDs []int64
	for _, chain := range chains {
		// Skip fully-merged chains — no open PRs means the stack is done.
		if !hasOpenMember(chain) {
			continue
		}
		name := DeriveStackName(chain)
		baseNumber := chain[0].Number
		stackID, err := database.UpsertStack(ctx, repoID, baseNumber, name)
		if err != nil {
			return err
		}
		activeIDs = append(activeIDs, stackID)

		members := make([]db.StackMember, len(chain))
		for i, pr := range chain {
			members[i] = db.StackMember{
				StackID:        stackID,
				MergeRequestID: pr.ID,
				Position:       i + 1,
			}
		}
		if err := database.ReplaceStackMembers(ctx, stackID, members); err != nil {
			return err
		}
	}

	return database.DeleteStaleStacks(ctx, repoID, activeIDs)
}

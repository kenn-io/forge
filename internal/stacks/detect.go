package stacks

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"go.kenn.io/middleman/internal/db"
)

type branchKey struct {
	repo   string
	branch string
}

// DetectChains finds linear PR chains from branch metadata.
// Returns chains of length >= 2, ordered base-to-tip.
func DetectChains(prs []db.MergeRequest, repoCloneURL string) [][]db.MergeRequest {
	// Sort by number for deterministic tie-breaking.
	sorted := slices.Clone(prs)
	slices.SortFunc(sorted, db.MergeRequest.Compare)

	// Stack edges require a known head repository identity. When providers can
	// list a fork MR but cannot read the fork project metadata, keeping the MR
	// with an empty head repo would make it look like a target-repo branch.
	sorted = slices.DeleteFunc(sorted, func(pr db.MergeRequest) bool {
		return strings.TrimSpace(pr.HeadRepoCloneURL) == ""
	})

	// Head and base branches only form a stack edge when they are in the same
	// repository. Forks can reuse upstream branch names, so branch-only keys
	// would let a fork's head shadow a real upstream stack root.
	headKey := func(pr db.MergeRequest) branchKey {
		return branchKey{repo: normalizeRepoKey(pr.HeadRepoCloneURL), branch: pr.HeadBranch}
	}
	baseKey := func(pr db.MergeRequest) branchKey {
		return branchKey{repo: normalizeRepoKey(repoCloneURL), branch: pr.BaseBranch}
	}

	// A same-repo PR from a branch to itself cannot be a stack edge. If it is
	// allowed into headMap, real PRs targeting that branch stop looking like
	// stack roots. Fork PRs such as fork:main -> upstream:main are kept because
	// their repo-aware head and base keys differ.
	sorted = slices.DeleteFunc(sorted, func(pr db.MergeRequest) bool {
		return headKey(pr) == baseKey(pr)
	})

	// head repo+branch -> PR. Prefer open over merged; within same state, lowest number wins.
	headMap := make(map[branchKey]db.MergeRequest, len(sorted))
	for _, pr := range sorted {
		key := headKey(pr)
		existing, exists := headMap[key]
		if !exists {
			headMap[key] = pr
			continue
		}
		if existing.State == "merged" && pr.State == "open" {
			headMap[key] = pr
		}
	}

	// Keep only preferred PR per head repo+branch to avoid ambiguous chains.
	preferred := make([]db.MergeRequest, 0, len(headMap))
	for _, pr := range sorted {
		if p, ok := headMap[headKey(pr)]; ok && p.ID == pr.ID {
			preferred = append(preferred, pr)
		}
	}

	// base repo+branch -> []PR (children targeting that base).
	childMap := make(map[branchKey][]db.MergeRequest)
	for _, pr := range preferred {
		key := baseKey(pr)
		childMap[key] = append(childMap[key], pr)
	}

	// Find bases: PRs whose base_branch is NOT in headMap.
	var bases []db.MergeRequest
	for _, pr := range preferred {
		if _, isHead := headMap[baseKey(pr)]; !isHead {
			bases = append(bases, pr)
		}
	}

	// Walk chains from each base.
	var chains [][]db.MergeRequest
	for _, base := range bases {
		chain := walkChain(base, childMap, headKey)
		if len(chain) >= 2 {
			chains = append(chains, chain)
		}
	}

	return chains
}

func normalizeRepoKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.Path = strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), ".git")
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return strings.TrimRight(parsed.String(), "/")
	}
	return strings.TrimSuffix(strings.TrimRight(raw, "/"), ".git")
}

func walkChain(
	start db.MergeRequest,
	childMap map[branchKey][]db.MergeRequest,
	headKey func(db.MergeRequest) branchKey,
) []db.MergeRequest {
	visited := make(map[branchKey]bool)
	var chain []db.MergeRequest
	current := start

	for {
		key := headKey(current)
		if visited[key] {
			return nil // cycle
		}
		visited[key] = true
		chain = append(chain, current)

		children := childMap[key]
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
	repo, err := database.GetRepoByID(ctx, repoID)
	if err != nil {
		return err
	}
	if repo == nil {
		return nil
	}

	prs, err := database.ListPRsForStackDetection(ctx, repoID)
	if err != nil {
		return err
	}

	chains := DetectChains(prs, repo.CloneURL)

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

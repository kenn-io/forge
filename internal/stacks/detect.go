package stacks

import (
	"context"
	"log/slog"
	"net/url"
	"slices"
	"strings"

	"go.kenn.io/forge/internal/db"
)

type branchKey struct {
	repo   string
	branch string
}

func headKey(pr db.MergeRequest) branchKey {
	return branchKey{repo: normalizeRepoKey(pr.HeadRepoCloneURL), branch: pr.HeadBranch}
}

func baseKey(pr db.MergeRequest, repoCloneURL string) branchKey {
	return branchKey{repo: normalizeRepoKey(repoCloneURL), branch: pr.BaseBranch}
}

type chainIndex struct {
	eligible  []db.MergeRequest
	headMap   map[branchKey]db.MergeRequest
	preferred []db.MergeRequest
	childMap  map[branchKey][]db.MergeRequest
}

func newChainIndex(prs []db.MergeRequest, repoCloneURL string) chainIndex {
	// Sort by number for deterministic tie-breaking.
	sorted := slices.Clone(prs)
	slices.SortFunc(sorted, db.MergeRequest.Compare)

	// Stack edges require a known head repository identity. When providers can
	// list a fork MR but cannot read the fork project metadata, keeping the MR
	// with an empty head repo would make it look like a target-repo branch.
	sorted = slices.DeleteFunc(sorted, func(pr db.MergeRequest) bool {
		return strings.TrimSpace(pr.HeadRepoCloneURL) == ""
	})

	// A same-repo PR from a branch to itself cannot be a stack edge. If it is
	// allowed into headMap, real PRs targeting that branch stop looking like
	// stack roots. Fork PRs such as fork:main -> upstream:main are kept because
	// their repo-aware head and base keys differ.
	sorted = slices.DeleteFunc(sorted, func(pr db.MergeRequest) bool {
		return headKey(pr) == baseKey(pr, repoCloneURL)
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
		key := baseKey(pr, repoCloneURL)
		childMap[key] = append(childMap[key], pr)
	}

	return chainIndex{eligible: sorted, headMap: headMap, preferred: preferred, childMap: childMap}
}

// DetectChains finds linear PR chains from branch metadata.
// Returns chains of length >= 2, ordered base-to-tip.
func DetectChains(prs []db.MergeRequest, repoCloneURL string) [][]db.MergeRequest {
	index := newChainIndex(prs, repoCloneURL)
	// Find bases: PRs whose base_branch is NOT in headMap.
	var bases []db.MergeRequest
	for _, pr := range index.preferred {
		if _, isHead := index.headMap[baseKey(pr, repoCloneURL)]; !isHead {
			bases = append(bases, pr)
		}
	}

	// Walk chains from each base.
	var chains [][]db.MergeRequest
	for _, base := range bases {
		chain := walkChain(base, index.childMap, headKey)
		if len(chain) >= 2 {
			chains = append(chains, chain)
		}
	}

	return chains
}

func cloneSpellings(key string) []string {
	return []string{key, key + ".git", key + "/", key + ".git/"}
}

type detectionPRReader interface {
	ListOpenPRsForStackDetection(context.Context, int64) ([]db.MergeRequest, error)
	ListPRsForStackDetectionByHeads(context.Context, int64, []string, []string) ([]db.MergeRequest, error)
	ListMergedPRsForStackDetectionByBase(context.Context, int64, string, int, int) ([]db.MergeRequest, error)
}

// loadDetectionPRs follows parents and preferred children from the open seeds.
func loadDetectionPRs(ctx context.Context, database detectionPRReader, repoID int64, cloneURL string, claimed map[int64]bool) ([]db.MergeRequest, error) {
	prs, err := database.ListOpenPRsForStackDetection(ctx, repoID)
	if err != nil {
		return nil, err
	}
	var loaded []db.MergeRequest
	seen := make(map[int64]bool)
	add := func(rows []db.MergeRequest) {
		for _, pr := range rows {
			if !claimed[pr.ID] && !seen[pr.ID] {
				seen[pr.ID] = true
				loaded = append(loaded, pr)
			}
		}
	}
	add(prs)
	repoKey := normalizeRepoKey(cloneURL)
	parents := make(map[string]bool)
	children := make(map[branchKey]bool)
	for {
		before := len(loaded)
		index := newChainIndex(loaded, cloneURL)
		var branches []string
		for _, pr := range index.eligible {
			if !parents[pr.BaseBranch] {
				parents[pr.BaseBranch] = true
				branches = append(branches, pr.BaseBranch)
			}
		}
		rows, err := database.ListPRsForStackDetectionByHeads(ctx, repoID, cloneSpellings(repoKey), branches)
		if err != nil {
			return nil, err
		}
		add(rows)
		index = newChainIndex(loaded, cloneURL)
		for _, pr := range index.preferred {
			key := headKey(pr)
			if key.repo != repoKey || children[key] {
				continue
			}
			children[key] = true
			if candidates := index.childMap[key]; len(candidates) > 0 && preferredChild(candidates).State == "open" {
				continue
			}
			for after := 0; ; {
				candidates, err := database.ListMergedPRsForStackDetectionByBase(ctx, repoID, pr.HeadBranch, after, 1)
				if err != nil {
					return nil, err
				}
				if len(candidates) == 0 {
					break
				}
				candidate := candidates[0]
				after = candidate.Number
				if claimed[candidate.ID] || strings.TrimSpace(candidate.HeadRepoCloneURL) == "" || headKey(candidate) == baseKey(candidate, cloneURL) {
					continue
				}
				if seen[candidate.ID] {
					if index.headMap[headKey(candidate)].ID == candidate.ID {
						break
					}
					continue
				}
				group, err := database.ListPRsForStackDetectionByHeads(ctx, repoID,
					cloneSpellings(headKey(candidate).repo), []string{candidate.HeadBranch})
				if err != nil {
					return nil, err
				}
				add(group)
				group = slices.DeleteFunc(group, func(row db.MergeRequest) bool { return claimed[row.ID] })
				winner := newChainIndex(group, cloneURL).headMap[headKey(candidate)]
				if winner.ID == candidate.ID {
					break
				}
			}
		}
		if len(loaded) == before {
			return loaded, nil
		}
	}
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
	return RunDetectionWithNativeStacks(ctx, database, repoID, nil)
}

// RunDetectionWithNativeStacks projects confirmed GitHub-native stacks first,
// then runs branch inference across only the merge requests left unclaimed.
func RunDetectionWithNativeStacks(
	ctx context.Context,
	database *db.DB,
	repoID int64,
	confirmedNativeNumbers []int,
) error {
	repo, err := database.GetRepoByID(ctx, repoID)
	if err != nil {
		return err
	}
	if repo == nil {
		return nil
	}

	activeIDs := make([]int64, 0)
	claimed := make(map[int64]bool)
	if len(confirmedNativeNumbers) > 0 {
		confirmed := make(map[int]bool, len(confirmedNativeNumbers))
		for _, number := range confirmedNativeNumbers {
			confirmed[number] = true
		}
		nativeStacks, err := database.ListGitHubNativeStacks(ctx, repoID)
		if err != nil {
			return err
		}
		// A pull request can belong to only one persisted stack. Persisting an
		// overlap would evict the shared member from whichever stack was
		// written first, silently shortening it and hiding a preceding merge
		// blocker, and projecting only one side does the same to the side that
		// loses. Overlap therefore makes the whole native projection ambiguous
		// and branch inference owns the repository for this pass.
		//
		// This scan runs over declared membership before rows are resolved: a
		// member missing from the database must not stop the search and let a
		// later duplicate through.
		usableStacks := make([]db.GitHubNativeStack, 0, len(nativeStacks))
		claimedBy := make(map[int]int, len(nativeStacks))
		overlapping := false
		for _, nativeStack := range nativeStacks {
			if !confirmed[nativeStack.Number] || !nativeStack.IsOpen ||
				len(nativeStack.Members) != nativeStack.Size || len(nativeStack.Members) < 2 {
				continue
			}
			usableStacks = append(usableStacks, nativeStack)
			for _, member := range nativeStack.Members {
				other, duplicate := claimedBy[member.PullRequestNumber]
				if duplicate {
					slog.Warn("native stacks overlap; falling back to branch inference",
						"repo_id", repoID, "stack_number", nativeStack.Number,
						"other_stack_number", other,
						"pull_request", member.PullRequestNumber)
					overlapping = true
					continue
				}
				claimedBy[member.PullRequestNumber] = nativeStack.Number
			}
		}

		var numbers []int64
		if !overlapping {
			for _, nativeStack := range usableStacks {
				for _, member := range nativeStack.Members {
					numbers = append(numbers, int64(member.PullRequestNumber))
				}
			}
		}
		prsByNumber := make(map[int]db.MergeRequest, len(numbers))
		if len(numbers) > 0 {
			memberRows, err := database.ListMergeRequests(ctx, db.ListMergeRequestsOpts{RepoID: repoID, Numbers: numbers, State: "all"})
			if err != nil {
				return err
			}
			for _, pr := range memberRows {
				prsByNumber[pr.Number] = pr
			}
		}

		chains := make([][]db.MergeRequest, 0, len(usableStacks))
		for _, nativeStack := range usableStacks {
			if overlapping {
				break
			}
			chain := make([]db.MergeRequest, 0, len(nativeStack.Members))
			usable := true
			for _, member := range nativeStack.Members {
				pr, ok := prsByNumber[member.PullRequestNumber]
				if !ok {
					usable = false
					break
				}
				chain = append(chain, pr)
			}
			if !usable || !hasOpenMember(chain) {
				continue
			}
			chains = append(chains, chain)
		}
		if !overlapping {
			for _, chain := range chains {
				stackID, err := persistStackChain(ctx, database, repoID, chain, true)
				if err != nil {
					return err
				}
				activeIDs = append(activeIDs, stackID)
				for _, pr := range chain {
					claimed[pr.ID] = true
				}
			}
		}
	}

	prs, err := loadDetectionPRs(ctx, database, repoID, repo.CloneURL, claimed)
	if err != nil {
		return err
	}
	chains := DetectChains(prs, repo.CloneURL)
	for _, chain := range chains {
		// Skip fully-merged chains — no open PRs means the stack is done.
		if !hasOpenMember(chain) {
			continue
		}
		stackID, err := persistStackChain(ctx, database, repoID, chain, false)
		if err != nil {
			return err
		}
		activeIDs = append(activeIDs, stackID)
	}

	return database.DeleteStaleStacks(ctx, repoID, activeIDs)
}

func persistStackChain(
	ctx context.Context,
	database *db.DB,
	repoID int64,
	chain []db.MergeRequest,
	native bool,
) (int64, error) {
	stackID, err := database.UpsertStack(
		ctx, repoID, chain[0].Number, DeriveStackName(chain), native,
	)
	if err != nil {
		return 0, err
	}
	members := make([]db.StackMember, len(chain))
	for i, pr := range chain {
		members[i] = db.StackMember{
			StackID: stackID, MergeRequestID: pr.ID, Position: i + 1,
		}
	}
	if err := database.ReplaceStackMembers(ctx, stackID, members); err != nil {
		return 0, err
	}
	return stackID, nil
}

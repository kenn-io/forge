package github

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/db"
)

// GitHubNativeStackSyncResult carries the native cache rows that were safe to
// use for this repository sync. Numbers omitted here must fall back to branch
// inference even when an older cache row exists.
type GitHubNativeStackSyncResult struct {
	ConfirmedNumbers []int
}

func nativeStackHintsFromBulk(result *RepoBulkResult) map[int]*NativeStackHint {
	if result == nil {
		return nil
	}
	hints := make(map[int]*NativeStackHint, len(result.PullRequests))
	for i := range result.PullRequests {
		bulk := &result.PullRequests[i]
		hints[bulk.PR.GetNumber()] = bulk.NativeStack
	}
	return hints
}

func (s *Syncer) refreshGitHubNativeStackCache(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	hints map[int]*NativeStackHint,
	listUnchanged bool,
) *GitHubNativeStackSyncResult {
	result := &GitHubNativeStackSyncResult{}
	confirmationKey := repoFailKey(repo)
	if listUnchanged {
		if confirmed, ok := s.nativeStackConfirmations.Load(confirmationKey); ok {
			result.ConfirmedNumbers = slices.Clone(confirmed.([]int))
		} else if client, ok := s.optionalGitHubClientFor(repo); ok {
			// This can happen when the setting changes during an in-flight list
			// request. Force a fresh representation on the next sync rather than
			// projecting cache rows that this ETag lifecycle never confirmed.
			client.InvalidateListETagsForRepo(repo.Owner, repo.Name, "pulls")
		}
		return result
	}
	defer func() {
		s.nativeStackConfirmations.Store(
			confirmationKey, slices.Clone(result.ConfirmedNumbers),
		)
	}()
	cached, err := s.db.ListGitHubNativeStacks(ctx, repoID)
	if err != nil {
		slog.Warn("load github native stack cache failed",
			"platform", repoPlatform(repo), "host", repoHost(repo),
			"repo", repo.Owner+"/"+repo.Name, "err", err)
		return result
	}
	if hints == nil {
		return result
	}

	cachedByNumber := make(map[int]db.GitHubNativeStack, len(cached))
	for _, stack := range cached {
		cachedByNumber[stack.Number] = stack
	}
	targets := make(map[int]bool)
	confirmed := make(map[int]bool)

	for prNumber, hint := range hints {
		if hint == nil {
			continue
		}
		stack, ok := cachedByNumber[hint.Number]
		if !ok || !cachedStackMatchesHint(stack, prNumber, *hint) {
			targets[hint.Number] = true
		}
	}
	for _, stack := range cached {
		if !stack.IsOpen {
			continue
		}
		if cachedStackMatchesCurrentHints(stack, hints) && !targets[stack.Number] {
			confirmed[stack.Number] = true
		} else {
			targets[stack.Number] = true
			delete(confirmed, stack.Number)
		}
	}
	if len(targets) == 0 {
		result.ConfirmedNumbers = sortedStackNumbers(confirmed)
		return result
	}

	client, ok := s.optionalGitHubClientFor(repo)
	if !ok {
		result.ConfirmedNumbers = sortedStackNumbers(confirmed)
		return result
	}
	nativeClient, ok := client.(NativeStackClient)
	if !ok {
		result.ConfirmedNumbers = sortedStackNumbers(confirmed)
		return result
	}

	pageNumber := 1
	for len(targets) > 0 {
		page, err := nativeClient.ListNativeStacksPage(ctx, repo.Owner, repo.Name, pageNumber)
		if err != nil {
			if githubStatusCode(err) == http.StatusNotFound {
				return result
			}
			slog.Warn("refresh github native stack cache failed",
				"platform", repoPlatform(repo), "host", repoHost(repo),
				"repo", repo.Owner+"/"+repo.Name, "page", pageNumber, "err", err)
			result.ConfirmedNumbers = sortedStackNumbers(confirmed)
			return result
		}

		lowest := 0
		for _, stack := range page.Stacks {
			if lowest == 0 || stack.Number < lowest {
				lowest = stack.Number
			}
			if !targets[stack.Number] {
				continue
			}
			delete(targets, stack.Number)
			if err := validateNativeStack(stack); err != nil {
				slog.Warn("ignore malformed github native stack",
					"platform", repoPlatform(repo), "host", repoHost(repo),
					"repo", repo.Owner+"/"+repo.Name,
					"stack_number", stack.Number, "err", err)
				continue
			}
			if !nativeStackMatchesCurrentHints(stack, hints) {
				slog.Warn("ignore github native stack inconsistent with pull request hints",
					"platform", repoPlatform(repo), "host", repoHost(repo),
					"repo", repo.Owner+"/"+repo.Name,
					"stack_number", stack.Number)
				continue
			}
			if err := s.db.ReplaceGitHubNativeStack(ctx, dbGitHubNativeStack(repoID, stack, s.now().UTC())); err != nil {
				slog.Warn("persist github native stack failed",
					"platform", repoPlatform(repo), "host", repoHost(repo),
					"repo", repo.Owner+"/"+repo.Name,
					"stack_number", stack.Number, "err", err)
				continue
			}
			if stack.Open {
				confirmed[stack.Number] = true
			}
		}

		var absent []int
		if page.NextPage == 0 {
			for number := range targets {
				absent = append(absent, number)
			}
		} else if lowest > 0 {
			for number := range targets {
				if number > lowest {
					absent = append(absent, number)
				}
			}
		}
		if err := s.db.DeleteGitHubNativeStacks(ctx, repoID, absent); err != nil {
			slog.Warn("delete absent github native stacks failed",
				"platform", repoPlatform(repo), "host", repoHost(repo),
				"repo", repo.Owner+"/"+repo.Name, "err", err)
			result.ConfirmedNumbers = sortedStackNumbers(confirmed)
			return result
		}
		for _, number := range absent {
			delete(targets, number)
			delete(confirmed, number)
		}
		if page.NextPage == 0 {
			break
		}
		pageNumber = page.NextPage
	}

	result.ConfirmedNumbers = sortedStackNumbers(confirmed)
	return result
}

func nativeStackMatchesCurrentHints(stack NativeStack, hints map[int]*NativeStackHint) bool {
	for prNumber, hint := range hints {
		if hint == nil || hint.Number != stack.Number {
			continue
		}
		if hint.Size != len(stack.Members) || hint.BaseRef != stack.BaseRef ||
			hint.Position < 1 || hint.Position > len(stack.Members) ||
			stack.Members[hint.Position-1].PullRequestNumber != prNumber {
			return false
		}
	}
	for _, member := range stack.Members {
		if !strings.EqualFold(member.State, "open") {
			continue
		}
		hint, ok := hints[member.PullRequestNumber]
		if !ok || hint == nil || hint.Number != stack.Number ||
			hint.Size != len(stack.Members) || hint.BaseRef != stack.BaseRef ||
			hint.Position != member.Position {
			return false
		}
	}
	return true
}

func cachedStackMatchesHint(stack db.GitHubNativeStack, prNumber int, hint NativeStackHint) bool {
	if !stack.IsOpen || stack.Number != hint.Number || stack.Size != hint.Size || stack.BaseRef != hint.BaseRef {
		return false
	}
	for _, member := range stack.Members {
		if member.PullRequestNumber == prNumber {
			return member.Position == hint.Position
		}
	}
	return false
}

func cachedStackMatchesCurrentHints(stack db.GitHubNativeStack, hints map[int]*NativeStackHint) bool {
	if !stack.IsOpen || len(stack.Members) != stack.Size {
		return false
	}
	foundOpen := false
	for _, member := range stack.Members {
		if !strings.EqualFold(member.State, "open") {
			continue
		}
		foundOpen = true
		hint, ok := hints[member.PullRequestNumber]
		if !ok || hint == nil || !cachedStackMatchesHint(stack, member.PullRequestNumber, *hint) {
			return false
		}
	}
	return foundOpen
}

func sortedStackNumbers(numbers map[int]bool) []int {
	result := make([]int, 0, len(numbers))
	for number := range numbers {
		result = append(result, number)
	}
	slices.Sort(result)
	return result
}

func dbGitHubNativeStack(repoID int64, stack NativeStack, observedAt time.Time) db.GitHubNativeStack {
	result := db.GitHubNativeStack{
		RepoID: repoID, GitHubID: stack.ID, Number: stack.Number,
		Size: len(stack.Members), BaseRef: stack.BaseRef, IsOpen: stack.Open,
		GitHubCreatedAt:    stack.CreatedAt.UTC(),
		ContentFingerprint: nativeStackFingerprint(stack),
		LastObservedAt:     observedAt,
		Members:            make([]db.GitHubNativeStackMember, 0, len(stack.Members)),
	}
	for _, member := range stack.Members {
		var mergedAt *time.Time
		if member.MergedAt != nil {
			utc := member.MergedAt.UTC()
			mergedAt = &utc
		}
		result.Members = append(result.Members, db.GitHubNativeStackMember{
			Position: member.Position, PullRequestNumber: member.PullRequestNumber,
			State: member.State, Draft: member.Draft, MergedAt: mergedAt,
			HeadRef: member.HeadRef, HeadSHA: member.HeadSHA,
		})
	}
	return result
}

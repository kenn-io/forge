package gitclone

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.kenn.io/forge/platform"
)

// FetchBitbucketMergeRequestHead fetches and checks the head observed by the
// API. Data Center's private PR ref is only an optimization: missing or stale
// refs fall back to the source branch. Cloud requires the source branch.
func (m *Manager) FetchBitbucketMergeRequestHead(
	ctx context.Context, host, owner, name string, number int,
	sourceURL, branch, expectedSHA string,
) error {
	if number <= 0 || expectedSHA == "" {
		return errors.New("pull request number and expected head are required")
	}
	dir, err := m.clonePathForContext(ctx, string(platform.KindBitbucket), host, owner, name)
	if err != nil {
		return err
	}
	destination := "refs/forge/bitbucket/pull-requests/" + strconv.Itoa(number) + "/head"
	verify := func() error {
		out, err := m.git(ctx, dir, "rev-parse", "--verify", destination+"^{commit}")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(out)) != expectedSHA {
			return fmt.Errorf("%w: fetched Bitbucket head differs from API snapshot", platform.ErrStaleState)
		}
		return nil
	}
	if host != platform.DefaultBitbucketHost {
		ref := "refs/pull-requests/" + strconv.Itoa(number) + "/from"
		_, err = m.RunGitForRepo(ctx, "bitbucket", host, owner, name, dir, "fetch", "--no-tags", "--recurse-submodules=no", "origin", "+"+ref+":"+destination)
		if err == nil && verify() == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if branch == "" {
		return errors.New("bitbucket source branch is unavailable")
	}
	ref := "refs/heads/" + branch
	if _, err := m.git(ctx, dir, "check-ref-format", ref); err != nil {
		return fmt.Errorf("invalid Bitbucket source branch: %w", err)
	}
	args := []string{"fetch", "--no-tags", "--recurse-submodules=no"}
	if sourceURL == "" {
		_, err = m.RunGitForRepo(ctx, "bitbucket", host, owner, name, dir, append(args, "origin", "+"+ref+":"+destination)...)
	} else {
		_, err = m.RunGitForRemote(ctx, "bitbucket", host, sourceURL, dir, append(args, sourceURL, "+"+ref+":"+destination)...)
	}
	if err != nil {
		return fmt.Errorf("fetch Bitbucket source branch: %w", err)
	}
	return verify()
}

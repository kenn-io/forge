package gitclone

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
)

// FilterMissingCommits reports which SHAs do not resolve to commit objects in
// the clone. All candidates are checked by one git cat-file process.
func (m *Manager) FilterMissingCommits(
	ctx context.Context,
	platform, host, owner, name string,
	shas []string,
) (map[string]bool, error) {
	missing := make(map[string]bool)
	if len(shas) == 0 {
		return missing, nil
	}
	dir, err := m.ClonePath(platform, host, owner, name)
	if err != nil {
		return nil, err
	}

	expressions := make([]string, len(shas))
	for i, sha := range shas {
		expressions[i] = sha + "^{commit}"
	}
	input := []byte(strings.Join(expressions, "\n") + "\n")
	out, err := m.gitWithInput(ctx, dir, input, "cat-file", "--batch-check")
	if err != nil {
		return nil, fmt.Errorf("batch-check commits: %w", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for i, sha := range shas {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return nil, fmt.Errorf("read batch-check result: %w", err)
			}
			return nil, fmt.Errorf(
				"read batch-check result: got %d lines for %d commits",
				i, len(shas),
			)
		}
		fields := strings.Fields(scanner.Text())
		switch {
		case len(fields) == 2 && fields[1] == "missing":
			missing[sha] = true
		case len(fields) >= 3 && fields[1] == "commit":
		default:
			return nil, fmt.Errorf(
				"read batch-check result for commit %d: unexpected output %q",
				i, scanner.Text(),
			)
		}
	}
	if scanner.Scan() {
		return nil, fmt.Errorf(
			"read batch-check result: got more than %d lines", len(shas),
		)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read batch-check result: %w", err)
	}
	return missing, nil
}

// UnreachableFrom reports which already-verified-present candidate commits
// are not reachable from head. All candidates are checked by one git rev-list
// process; output commits outside the candidate set are ignored.
func (m *Manager) UnreachableFrom(
	ctx context.Context,
	platform, host, owner, name, head string,
	candidates []string,
) (map[string]bool, error) {
	unreachable := make(map[string]bool)
	if len(candidates) == 0 {
		return unreachable, nil
	}
	dir, err := m.ClonePath(platform, host, owner, name)
	if err != nil {
		return nil, err
	}

	args := make([]string, 0, len(candidates)+3)
	args = append(args, "rev-list")
	args = append(args, candidates...)
	args = append(args, "--not", head)
	out, err := m.git(ctx, dir, args...)
	if err != nil {
		return nil, fmt.Errorf("list commits unreachable from %s: %w", head, err)
	}

	candidatesByLowerSHA := make(map[string][]string, len(candidates))
	for _, candidate := range candidates {
		lowerSHA := strings.ToLower(candidate)
		candidatesByLowerSHA[lowerSHA] = append(
			candidatesByLowerSHA[lowerSHA], candidate,
		)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		for _, candidate := range candidatesByLowerSHA[strings.ToLower(scanner.Text())] {
			unreachable[candidate] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read unreachable commits: %w", err)
	}
	return unreachable, nil
}

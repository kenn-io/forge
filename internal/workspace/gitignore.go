package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// generatedContextTempPattern ignores the atomic writer's staging files.
	generatedContextTempPattern = "/.tmp-agent-context-*"
	// generatedContextTempProbePath is a representative path used to check
	// whether the temp pattern is already effective.
	generatedContextTempProbePath = ".tmp-agent-context-probe"
)

// EnsureGeneratedContextFilesIgnored guarantees the requested generated
// context paths are ignored by git before they are written, adding local
// exclude rules (never touching tracked .gitignore) only for the paths that
// will actually be generated.
func EnsureGeneratedContextFilesIgnored(
	ctx context.Context,
	worktreePath string,
	generatedRelPaths []string,
) error {
	missingPaths := make([]string, 0, len(generatedRelPaths)+1)
	missingPatterns := make([]string, 0, len(generatedRelPaths)+1)
	seenPatterns := make(map[string]bool, len(generatedRelPaths)+1)
	checks := make([][2]string, 0, len(generatedRelPaths)+1)
	for _, rel := range generatedRelPaths {
		clean, pattern, err := generatedContextIgnorePattern(rel)
		if err != nil {
			return err
		}
		checks = append(checks, [2]string{clean, pattern})
	}
	if len(checks) > 0 {
		// The atomic writer stages content as .tmp-agent-context-* next to
		// the target; a crash between create and rename must not leave an
		// untracked file dirtying the workspace.
		checks = append(checks, [2]string{
			generatedContextTempProbePath, generatedContextTempPattern,
		})
	}
	for _, check := range checks {
		clean, pattern := check[0], check[1]
		ignored, err := gitPathIgnored(ctx, worktreePath, clean)
		if err != nil {
			return err
		}
		if ignored {
			continue
		}
		missingPaths = append(missingPaths, clean)
		if !seenPatterns[pattern] {
			seenPatterns[pattern] = true
			missingPatterns = append(missingPatterns, pattern)
		}
	}
	if len(missingPaths) == 0 {
		return nil
	}

	excludePathOut, err := gitCombinedOutput(ctx, worktreePath, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("resolve git exclude path: %w", err)
	}
	excludePath := strings.TrimSpace(excludePathOut)
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(worktreePath, excludePath)
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("create git exclude directory: %w", err)
	}

	content, err := os.ReadFile(excludePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read git exclude: %w", err)
	}
	text := string(content)
	add := make([]string, 0, len(missingPatterns))
	for _, pattern := range missingPatterns {
		if !gitExcludeContainsLine(text, pattern) {
			add = append(add, pattern)
		}
	}
	if len(add) > 0 {
		var block strings.Builder
		if len(text) > 0 && !strings.HasSuffix(text, "\n") {
			block.WriteString("\n")
		}
		block.WriteString("# middleman generated agent context\n")
		for _, pattern := range add {
			block.WriteString(pattern)
			block.WriteString("\n")
		}
		f, err := os.OpenFile(excludePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open git exclude: %w", err)
		}
		defer f.Close()
		if _, err := f.WriteString(block.String()); err != nil {
			return fmt.Errorf("write git exclude: %w", err)
		}
	}

	for _, clean := range missingPaths {
		ignored, err := gitPathIgnored(ctx, worktreePath, clean)
		if err != nil {
			return err
		}
		if !ignored {
			return fmt.Errorf(
				"generated context path %s is still not ignored after updating %s (a later rule may negate it)",
				clean, excludePath,
			)
		}
	}
	return nil
}

// gitPathIgnored reports whether git ignores rel inside worktreePath,
// distinguishing check-ignore's "not ignored" exit status 1 from fatal
// git failures.
func gitPathIgnored(ctx context.Context, worktreePath, rel string) (bool, error) {
	_, err := gitCombinedOutput(ctx, worktreePath, "check-ignore", "--quiet", "--", rel)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git check-ignore %s: %w", rel, err)
}

// generatedContextIgnorePattern maps a generated context path to the exact
// local ignore rule that covers it. Only known middleman-generated paths are
// allowed; anything else is rejected rather than silently ignored.
func generatedContextIgnorePattern(rel string) (cleanPath, pattern string, err error) {
	rel = strings.TrimSpace(rel)
	if rel == "AGENTS.md" || rel == "CLAUDE.md" {
		return "", "", fmt.Errorf("refusing to add root instruction file to generated ignore list: %s", rel)
	}
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return "", "", fmt.Errorf("invalid generated context path: %s", rel)
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	switch clean {
	case "AGENTS.local.md", "CLAUDE.local.md":
		return clean, "/" + clean, nil
	default:
		return "", "", fmt.Errorf("unknown generated context path: %s", clean)
	}
}

func gitExcludeContainsLine(text, pattern string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(line) == pattern {
			return true
		}
	}
	return false
}

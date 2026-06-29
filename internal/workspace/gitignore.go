package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var generatedContextIgnorePatterns = []string{
	"/.middleman/",
	"/AGENTS.local.md",
	"/CLAUDE.local.md",
}

func EnsureGeneratedContextFilesIgnored(
	ctx context.Context,
	worktreePath string,
	generatedRelPaths []string,
) error {
	missing := false
	for _, rel := range generatedRelPaths {
		clean, err := cleanGeneratedContextRelPath(rel)
		if err != nil {
			return err
		}
		_, err = gitCombinedOutput(ctx, worktreePath, "check-ignore", "--quiet", "--", clean)
		if err != nil {
			missing = true
		}
	}
	if !missing {
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
	add := make([]string, 0, len(generatedContextIgnorePatterns))
	for _, pattern := range generatedContextIgnorePatterns {
		if !gitExcludeContainsLine(text, pattern) {
			add = append(add, pattern)
		}
	}
	if len(add) == 0 {
		return nil
	}
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
	return nil
}

func cleanGeneratedContextRelPath(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "AGENTS.md" || rel == "CLAUDE.md" {
		return "", fmt.Errorf("refusing to add root instruction file to generated ignore list: %s", rel)
	}
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return "", fmt.Errorf("invalid generated context path: %s", rel)
	}
	return filepath.ToSlash(filepath.Clean(rel)), nil
}

func gitExcludeContainsLine(text, pattern string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(line) == pattern {
			return true
		}
	}
	return false
}

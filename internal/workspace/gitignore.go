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
	PastedImageDirectory = ".kenn-forge/pasted-images"

	// generatedContextTempPattern ignores the atomic writer's staging files.
	generatedContextTempPattern = "/.tmp-agent-context-*"
	// generatedContextTempProbePath is a representative path used to check
	// whether the temp pattern is already effective.
	generatedContextTempProbePath = ".tmp-agent-context-probe"
)

type generatedIgnoreCheck struct {
	probe   string
	pattern string
}

// EnsureWorkspaceGeneratedPathsIgnored guarantees that known Forge-generated
// paths are ignored by git before they are written. It updates only the
// repository-local exclude file, never a tracked .gitignore.
func EnsureWorkspaceGeneratedPathsIgnored(
	ctx context.Context,
	worktreePath string,
	generatedRelPaths []string,
) error {
	missingPaths := make([]string, 0, len(generatedRelPaths)+1)
	missingPatterns := make([]string, 0, len(generatedRelPaths)+1)
	seenPatterns := make(map[string]bool, len(generatedRelPaths)+1)
	checks := make([]generatedIgnoreCheck, 0, len(generatedRelPaths)+1)
	for _, rel := range generatedRelPaths {
		pathChecks, err := generatedIgnoreChecks(rel)
		if err != nil {
			return err
		}
		checks = append(checks, pathChecks...)
	}
	for _, check := range checks {
		clean, pattern := check.probe, check.pattern
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
		block.WriteString("# kenn-forge generated files\n")
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
				"generated path %s is still not ignored after updating %s (a later rule may negate it)",
				clean, excludePath,
			)
		}
	}
	return nil
}

// EnsureGeneratedContextFilesIgnored retains the context-writer entry point
// while sharing the generic known-generated-path implementation.
func EnsureGeneratedContextFilesIgnored(
	ctx context.Context,
	worktreePath string,
	generatedRelPaths []string,
) error {
	return EnsureWorkspaceGeneratedPathsIgnored(ctx, worktreePath, generatedRelPaths)
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

// generatedIgnoreChecks maps a known generated path to the local ignore rules
// and representative paths needed to verify them.
func generatedIgnoreChecks(rel string) ([]generatedIgnoreCheck, error) {
	rel = strings.TrimSpace(rel)
	if rel == "AGENTS.md" || rel == "CLAUDE.md" {
		return nil, fmt.Errorf("refusing to add root instruction file to generated ignore list: %s", rel)
	}
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return nil, fmt.Errorf("invalid generated path: %s", rel)
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	switch clean {
	case "AGENTS.override.md", "CLAUDE.local.md":
		return []generatedIgnoreCheck{
			{probe: clean, pattern: "/" + clean},
			{
				probe:   generatedContextTempProbePath,
				pattern: generatedContextTempPattern,
			},
		}, nil
	case PastedImageDirectory:
		return []generatedIgnoreCheck{{
			probe:   PastedImageDirectory + "/.ignore-probe",
			pattern: "/" + PastedImageDirectory + "/",
		}}, nil
	default:
		return nil, fmt.Errorf("unknown generated path: %s", clean)
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

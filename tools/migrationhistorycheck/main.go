package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	gitcmd "go.kenn.io/kit/git/cmd"
	gitenv "go.kenn.io/kit/git/env"
)

const (
	defaultBaseRef      = "origin/main"
	defaultMigrationDir = "internal/db/migrations"
)

var gitEnv []string

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Stderr))
}

func run(ctx context.Context, stderr io.Writer) int {
	baseRef := getenvDefault("KENN_FORGE_MIGRATION_BASE_REF", defaultBaseRef)
	comparisonRef := baseRef
	prBaseRef := os.Getenv("KENN_FORGE_MIGRATION_PR_BASE_REF")
	if prBaseRef != "" {
		comparisonRef = prBaseRef
	}
	migrationDir := strings.TrimRight(getenvDefault("KENN_FORGE_MIGRATION_DIR", defaultMigrationDir), "/")

	if _, err := git(ctx, "rev-parse", "--git-dir"); err != nil {
		fmt.Fprintln(stderr, "migration history check must run inside a git worktree")
		return 1
	}

	if _, err := git(ctx, "rev-parse", "--verify", "--quiet", comparisonRef+"^{commit}"); err != nil {
		fmt.Fprintf(stderr, "Cannot verify migration history because %s is unavailable.\n", comparisonRef)
		fmt.Fprintln(stderr, "Fetch the comparison ref or set KENN_FORGE_MIGRATION_BASE_REF or KENN_FORGE_MIGRATION_PR_BASE_REF to an available commit.")
		return 1
	}

	diffArgs := []string{"diff", "--cached", "--name-status"}
	if prBaseRef != "" {
		mergeBase, mergeBaseErr := git(ctx, "merge-base", comparisonRef, "HEAD")
		if mergeBaseErr != nil {
			fmt.Fprintf(stderr, "failed to find the pull request merge base: %v\n", mergeBaseErr)
			return 1
		}
		diffArgs = append(diffArgs, strings.TrimSpace(mergeBase))
	}
	diffArgs = append(diffArgs, "--", migrationDir)
	diff, err := git(ctx, diffArgs...)
	if err != nil {
		fmt.Fprintf(stderr, "failed to inspect staged migrations: %v\n", err)
		return 1
	}

	changedViolations := changedBaseMigrations(ctx, comparisonRef, migrationDir, diff)
	duplicateViolations, err := duplicateMigrationNumberViolations(ctx, comparisonRef, migrationDir, diff)
	if err != nil {
		fmt.Fprintf(stderr, "failed to verify migration numbers: %v\n", err)
		return 1
	}
	newMigrationViolations, err := multipleNewMigrationViolations(ctx, comparisonRef, migrationDir, diff)
	if err != nil {
		fmt.Fprintf(stderr, "failed to verify the pull request migration count: %v\n", err)
		return 1
	}

	if len(changedViolations) == 0 && len(duplicateViolations) == 0 && len(newMigrationViolations) == 0 {
		return 0
	}

	fmt.Fprintln(stderr, "Refusing to commit staged migration history changes.")
	if len(changedViolations) > 0 {
		fmt.Fprintf(stderr, "\nEdits to migrations that already exist on %s are not allowed.\n", comparisonRef)
		fmt.Fprintln(stderr, "Migrations inherited from the comparison base belong to earlier history. Add or amend the current pull request's migration instead.")
		fmt.Fprintln(stderr, "\nBlocked files:")
		for _, path := range changedViolations {
			fmt.Fprintf(stderr, "  %s\n", path)
		}
	}
	if len(duplicateViolations) > 0 {
		fmt.Fprintln(stderr, "\nEach migration number may identify only one migration. Found duplicate migration number assignments:")
		for _, violation := range duplicateViolations {
			fmt.Fprintf(stderr, "  %s: %s\n", violation.number, strings.Join(violation.names, ", "))
		}
	}
	if len(newMigrationViolations) > 0 {
		fmt.Fprintln(stderr, "\nA pull request may introduce only one new migration. Found:")
		for _, name := range newMigrationViolations {
			fmt.Fprintf(stderr, "  %s\n", name)
		}
		fmt.Fprintln(stderr, "Amend the pull request's existing migration instead of stacking fix-up migrations.")
	}
	return 1
}

func changedBaseMigrations(ctx context.Context, baseRef, migrationDir, diff string) []string {
	var violations []string
	for line := range strings.SplitSeq(diff, "\n") {
		if line == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}

		for _, path := range changedPaths(fields) {
			if !strings.HasPrefix(path, migrationDir+"/") {
				continue
			}
			if _, err := git(ctx, "cat-file", "-e", baseRef+":"+path); err == nil {
				if stagedPathMatchesBase(ctx, baseRef, path) {
					continue
				}
				violations = append(violations, path)
			}
		}
	}
	return violations
}

func multipleNewMigrationViolations(ctx context.Context, baseRef, migrationDir, diff string) ([]string, error) {
	baseByNumber, err := migrationNamesByNumberOnRef(ctx, baseRef, migrationDir)
	if err != nil {
		return nil, err
	}

	baseNames := map[string]struct{}{}
	for _, names := range baseByNumber {
		maps.Copy(baseNames, names)
	}

	newNames := map[string]struct{}{}
	for _, path := range stagedMigrationPaths(diff, migrationDir) {
		_, name, ok := migrationIdentityFromPath(path)
		if !ok {
			continue
		}
		if _, exists := baseNames[name]; exists {
			continue
		}
		newNames[name] = struct{}{}
	}
	if len(newNames) <= 1 {
		return nil, nil
	}
	return sortedKeys(newNames), nil
}

func stagedPathMatchesBase(ctx context.Context, baseRef, path string) bool {
	baseContent, err := git(ctx, "show", baseRef+":"+path)
	if err != nil {
		return false
	}
	stagedContent, err := git(ctx, "show", ":"+path)
	if err != nil {
		return false
	}
	return stagedContent == baseContent
}

func changedPaths(fields []string) []string {
	status := fields[0]
	paths := fields[1:]
	if strings.HasPrefix(status, "R") {
		return paths
	}
	if len(paths) == 0 {
		return nil
	}
	if strings.HasPrefix(status, "C") && len(paths) > 1 {
		return paths[1:]
	}
	return paths[:1]
}

type duplicateNumberViolation struct {
	number string
	names  []string
}

func (v duplicateNumberViolation) Compare(other duplicateNumberViolation) int {
	return strings.Compare(v.number, other.number)
}

func duplicateMigrationNumberViolations(ctx context.Context, baseRef, migrationDir, diff string) ([]duplicateNumberViolation, error) {
	baseByNumber, err := migrationNamesByNumberOnRef(ctx, baseRef, migrationDir)
	if err != nil {
		return nil, err
	}

	stagedByNumber := map[string]map[string]struct{}{}
	for _, path := range stagedMigrationPaths(diff, migrationDir) {
		number, name, ok := migrationIdentityFromPath(path)
		if !ok {
			continue
		}
		if _, exists := stagedByNumber[number]; !exists {
			stagedByNumber[number] = map[string]struct{}{}
		}
		stagedByNumber[number][name] = struct{}{}
	}

	var violations []duplicateNumberViolation
	for number, stagedNames := range stagedByNumber {
		allNames := maps.Clone(stagedNames)
		maps.Copy(allNames, baseByNumber[number])
		if len(allNames) <= 1 {
			continue
		}

		names := sortedKeys(allNames)
		violations = append(violations, duplicateNumberViolation{
			number: number,
			names:  names,
		})
	}

	slices.SortFunc(violations, duplicateNumberViolation.Compare)
	return violations, nil
}

func migrationNamesByNumberOnRef(ctx context.Context, ref, migrationDir string) (map[string]map[string]struct{}, error) {
	output, err := git(ctx, "ls-tree", "-r", "--name-only", ref, "--", migrationDir)
	if err != nil {
		return nil, err
	}

	byNumber := map[string]map[string]struct{}{}
	for line := range strings.SplitSeq(output, "\n") {
		number, name, ok := migrationIdentityFromPath(line)
		if !ok {
			continue
		}
		if _, exists := byNumber[number]; !exists {
			byNumber[number] = map[string]struct{}{}
		}
		byNumber[number][name] = struct{}{}
	}
	return byNumber, nil
}

func stagedMigrationPaths(diff, migrationDir string) []string {
	var paths []string
	for line := range strings.SplitSeq(diff, "\n") {
		if line == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}

		path, ok := stagedPath(fields)
		if !ok || !strings.HasPrefix(path, migrationDir+"/") {
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

func stagedPath(fields []string) (string, bool) {
	status := fields[0]
	paths := fields[1:]
	if len(paths) == 0 || strings.HasPrefix(status, "D") {
		return "", false
	}
	if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
		return paths[len(paths)-1], true
	}
	return paths[0], true
}

func migrationIdentityFromPath(path string) (string, string, bool) {
	base := filepath.Base(path)
	switch {
	case strings.HasSuffix(base, ".up.sql"):
		base = strings.TrimSuffix(base, ".up.sql")
	case strings.HasSuffix(base, ".down.sql"):
		base = strings.TrimSuffix(base, ".down.sql")
	default:
		return "", "", false
	}

	number, _, ok := strings.Cut(base, "_")
	if !ok || number == "" {
		return "", "", false
	}
	return number, base, true
}

func sortedKeys(values map[string]struct{}) []string {
	return slices.Sorted(maps.Keys(values))
}

func getenvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func git(ctx context.Context, args ...string) (string, error) {
	runner := gitcmd.New()
	runner.Env = gitHookEnv(os.Environ())
	runner.StripEnv = false
	output, err := runner.Output(ctx, "", args...)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func gitHookEnv(env []string) []string {
	if gitEnv != nil {
		env = gitEnv
	}

	cleaned := gitenv.StripAll(env)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if isGitHookContextVar(key) {
			cleaned = append(cleaned, entry)
		}
	}
	return cleaned
}

func isGitHookContextVar(key string) bool {
	switch key {
	case "GIT_DIR",
		"GIT_WORK_TREE",
		"GIT_INDEX_FILE",
		"GIT_COMMON_DIR",
		"GIT_PREFIX",
		"GIT_NAMESPACE":
		return true
	default:
		return false
	}
}

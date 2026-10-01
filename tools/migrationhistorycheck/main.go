package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"slices"
	"strconv"
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
	code := run(ctx, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, stderr io.Writer) int {
	baseRef := getenvDefault("KENN_FORGE_MIGRATION_BASE_REF", defaultBaseRef)
	comparisonRef := baseRef
	prBaseRef := os.Getenv("KENN_FORGE_MIGRATION_PR_BASE_REF")
	if prBaseRef != "" {
		comparisonRef = pullRequestComparisonRef(ctx, prBaseRef)
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

	baseByNumber, err := migrationNamesByNumberOnRef(ctx, comparisonRef, migrationDir)
	if err != nil {
		fmt.Fprintf(stderr, "failed to read base migrations: %v\n", err)
		return 1
	}
	changedViolations := changedBaseMigrations(ctx, comparisonRef, migrationDir, diff, baseByNumber)
	resultingPaths, err := resultingMigrationPaths(ctx, comparisonRef, migrationDir, diff)
	if err != nil {
		fmt.Fprintf(stderr, "failed to read the resulting migrations: %v\n", err)
		return 1
	}
	resulting := migrationNamesByNumber(strings.Join(resultingPaths, "\n"))
	duplicateViolations := duplicateMigrationNumberViolations(resulting)
	layoutViolations := migrationLayoutViolations(migrationDir, resultingPaths, resulting)
	newMigrationViolations, err := multipleNewMigrationViolations(ctx, comparisonRef, migrationDir, diff)
	if err != nil {
		fmt.Fprintf(stderr, "failed to verify the pull request migration count: %v\n", err)
		return 1
	}

	if len(changedViolations) == 0 && len(duplicateViolations) == 0 && len(layoutViolations) == 0 && len(newMigrationViolations) == 0 {
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
	if len(layoutViolations) > 0 {
		fmt.Fprintln(stderr, "\nMigrations must sit directly in the migration directory and be numbered from 000001 without gaps. Found:")
		for _, violation := range layoutViolations {
			fmt.Fprintf(stderr, "  %s\n", violation)
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

func changedBaseMigrations(ctx context.Context, baseRef, migrationDir, diff string, baseByNumber map[string]map[string]struct{}) []string {
	var violations []string
	for line := range strings.SplitSeq(diff, "\n") {
		if line == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		if isPureRenumbering(ctx, baseRef, migrationDir, baseByNumber, fields) {
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

// isPureRenumbering reports whether a staged rename only changes the number of
// a base migration file that shares its number with another base migration.
// Two pull requests that each pass this check can still merge the same number,
// and renumbering one of them is the only repair. The file keeps its directory,
// direction, description, and content; the resulting-history checks reject a
// renumbering that leaves duplicates, gaps, or misplaced files.
func isPureRenumbering(ctx context.Context, baseRef, migrationDir string, baseByNumber map[string]map[string]struct{}, fields []string) bool {
	if !strings.HasPrefix(fields[0], "R") || len(fields) != 3 {
		return false
	}
	oldPath, newPath := fields[1], fields[2]
	if path.Dir(oldPath) != migrationDir || path.Dir(newPath) != migrationDir {
		return false
	}
	oldNumber, oldName, oldOK := migrationIdentityFromPath(oldPath)
	newNumber, newName, newOK := migrationIdentityFromPath(newPath)
	if !oldOK || !newOK || len(baseByNumber[oldNumber]) < 2 {
		return false
	}
	if strings.TrimPrefix(path.Base(oldPath), oldNumber) != strings.TrimPrefix(path.Base(newPath), newNumber) ||
		strings.TrimPrefix(oldName, oldNumber) != strings.TrimPrefix(newName, newNumber) {
		return false
	}
	return stagedPathMatchesRef(ctx, baseRef, oldPath, newPath)
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
	return stagedPathMatchesRef(ctx, baseRef, path, path)
}

func stagedPathMatchesRef(ctx context.Context, baseRef, basePath, stagedPath string) bool {
	baseContent, err := git(ctx, "show", baseRef+":"+basePath)
	if err != nil {
		return false
	}
	stagedContent, err := git(ctx, "show", ":"+stagedPath)
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

// duplicateMigrationNumberViolations lists numbers that the resulting history
// assigns to more than one migration, including unresolved duplicates
// inherited from the base.
func duplicateMigrationNumberViolations(resulting map[string]map[string]struct{}) []duplicateNumberViolation {
	var violations []duplicateNumberViolation
	for number, names := range resulting {
		if len(names) > 1 {
			violations = append(violations, duplicateNumberViolation{number: number, names: sortedKeys(names)})
		}
	}
	slices.SortFunc(violations, duplicateNumberViolation.Compare)
	return violations
}

// resultingMigrationPaths applies the staged migration changes to the
// comparison base, so migrations the base gained after this branch diverged
// still count.
func resultingMigrationPaths(ctx context.Context, baseRef, migrationDir, diff string) ([]string, error) {
	listing, err := git(ctx, "ls-tree", "-r", "--name-only", baseRef, "--", migrationDir)
	if err != nil {
		return nil, err
	}
	paths := map[string]struct{}{}
	for line := range strings.SplitSeq(strings.TrimSpace(listing), "\n") {
		if line != "" {
			paths[line] = struct{}{}
		}
	}
	for line := range strings.SplitSeq(diff, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		if strings.HasPrefix(fields[0], "D") || strings.HasPrefix(fields[0], "R") {
			delete(paths, fields[1])
		}
		if added, ok := stagedPath(fields); ok {
			paths[added] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(paths)), nil
}

// migrationLayoutViolations reports files the embedded migration filesystem
// would not load as a migration and numbering that does not run from 000001
// without gaps.
func migrationLayoutViolations(migrationDir string, resultingPaths []string, resulting map[string]map[string]struct{}) []string {
	var violations []string
	for _, file := range resultingPaths {
		if _, _, ok := migrationIdentityFromPath(file); !ok || path.Dir(file) != migrationDir {
			violations = append(violations, "unexpected file "+file)
		}
	}
	return append(violations, numberingGaps(resulting)...)
}

func numberingGaps(resulting map[string]map[string]struct{}) []string {
	numbers := slices.Sorted(maps.Keys(resulting))
	var gaps []string
	if len(numbers) > 0 {
		if first, err := strconv.Atoi(numbers[0]); err != nil || first != 1 {
			gaps = append(gaps, "numbering starts at "+numbers[0]+", not 000001")
		}
	}
	for i := 1; i < len(numbers); i++ {
		previous, previousErr := strconv.Atoi(numbers[i-1])
		current, currentErr := strconv.Atoi(numbers[i])
		if previousErr != nil || currentErr != nil || current != previous+1 {
			gaps = append(gaps, "gap between "+numbers[i-1]+" and "+numbers[i])
		}
	}
	return gaps
}

func migrationNamesByNumberOnRef(ctx context.Context, ref, migrationDir string) (map[string]map[string]struct{}, error) {
	output, err := git(ctx, "ls-tree", "-r", "--name-only", ref, "--", migrationDir)
	if err != nil {
		return nil, err
	}
	return migrationNamesByNumber(output), nil
}

func migrationNamesByNumber(listing string) map[string]map[string]struct{} {
	byNumber := map[string]map[string]struct{}{}
	for line := range strings.SplitSeq(listing, "\n") {
		number, name, ok := migrationIdentityFromPath(line)
		if !ok {
			continue
		}
		if _, exists := byNumber[number]; !exists {
			byNumber[number] = map[string]struct{}{}
		}
		byNumber[number][name] = struct{}{}
	}
	return byNumber
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

func pullRequestComparisonRef(ctx context.Context, fallback string) string {
	githubRef := os.Getenv("GITHUB_REF")
	if !strings.HasPrefix(githubRef, "refs/pull/") || !strings.HasSuffix(githubRef, "/merge") {
		return fallback
	}
	if _, err := git(ctx, "rev-parse", "--verify", "--quiet", "HEAD^2"); err != nil {
		return fallback
	}
	return "HEAD^1"
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

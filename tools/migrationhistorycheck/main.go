package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wesm/middleman/internal/procutil"
)

const (
	defaultBaseRef      = "origin/main"
	defaultMigrationDir = "internal/db/migrations"
)

var gitEnv []string

func main() {
	os.Exit(run(os.Stderr))
}

func run(stderr io.Writer) int {
	baseRef := getenvDefault("MIDDLEMAN_MIGRATION_BASE_REF", defaultBaseRef)
	migrationDir := strings.TrimRight(getenvDefault("MIDDLEMAN_MIGRATION_DIR", defaultMigrationDir), "/")

	if _, err := git("rev-parse", "--git-dir"); err != nil {
		fmt.Fprintln(stderr, "migration history check must run inside a git worktree")
		return 1
	}

	if _, err := git("rev-parse", "--verify", "--quiet", baseRef+"^{commit}"); err != nil {
		fmt.Fprintf(stderr, "Cannot verify migration history because %s is unavailable.\n", baseRef)
		fmt.Fprintln(stderr, "Fetch the main branch or set MIDDLEMAN_MIGRATION_BASE_REF to the main-branch ref to compare against.")
		return 1
	}

	diff, err := git("diff", "--cached", "--name-status", "--", migrationDir)
	if err != nil {
		fmt.Fprintf(stderr, "failed to inspect staged migrations: %v\n", err)
		return 1
	}

	changedViolations := changedMainBranchMigrations(baseRef, migrationDir, diff)
	duplicateViolations, err := duplicateMigrationNumberViolations(baseRef, migrationDir, diff)
	if err != nil {
		fmt.Fprintf(stderr, "failed to verify migration numbers: %v\n", err)
		return 1
	}

	if len(changedViolations) == 0 && len(duplicateViolations) == 0 {
		return 0
	}

	fmt.Fprintln(stderr, "Refusing to commit staged migration history changes.")
	if len(changedViolations) > 0 {
		fmt.Fprintf(stderr, "\nEdits to migrations that already exist on %s are not allowed.\n", baseRef)
		fmt.Fprintln(stderr, "Migrations are append-only once they land on main. Add a new numbered migration instead.")
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
	return 1
}

func changedMainBranchMigrations(baseRef, migrationDir, diff string) []string {
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
			if _, err := git("cat-file", "-e", baseRef+":"+path); err == nil {
				violations = append(violations, path)
			}
		}
	}
	return violations
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

func duplicateMigrationNumberViolations(baseRef, migrationDir, diff string) ([]duplicateNumberViolation, error) {
	baseByNumber, err := migrationNamesByNumberOnRef(baseRef, migrationDir)
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

func migrationNamesByNumberOnRef(ref, migrationDir string) (map[string]map[string]struct{}, error) {
	output, err := git("ls-tree", "-r", "--name-only", ref, "--", migrationDir)
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

func git(args ...string) (string, error) {
	cmd := procutil.Command("git", args...)
	if gitEnv != nil {
		cmd.Env = gitEnv
	}
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

package workspace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

// gitdirRemoteHeadReader answers the pushed-head observer's read-only
// questions from the worktree's git directory instead of spawning git. The
// observer polls every ready workspace every few seconds, so its steady state
// must cost a handful of small file reads rather than several forks of a
// large daemon image per workspace.
//
// Only what the on-disk format can express is answered directly: symbolic or
// detached HEAD, loose and packed refs under the common directory, and the
// branch.*, remote.*, and extensions.* entries of the repository config
// (plus config.worktree when extensions.worktreeConfig is on). Repositories
// whose config uses include/includeIf, whose refs live in a reftable store,
// or whose files cannot be parsed fall back to git for that question so the
// observer never answers differently from git, only more cheaply.
//
// remoteURL is the configured remote.<name>.url; url.<base>.insteadOf
// rewriting is not applied. The observer does not consume the URL.
type gitdirRemoteHeadReader struct {
	fallback remoteHeadGitReader
}

func (r *gitdirRemoteHeadReader) BranchName(ctx context.Context, dir string) (string, error) {
	state, err := loadWorktreeGitState(dir)
	if err != nil {
		return r.fallbackBranchName(ctx, dir, err)
	}
	branch, ok := state.currentBranch()
	if !ok {
		return r.fallbackBranchName(ctx, dir, errors.New("unrecognized HEAD"))
	}
	return branch, nil
}

func (r *gitdirRemoteHeadReader) fallbackBranchName(ctx context.Context, dir string, reason error) (string, error) {
	logGitdirFallback(dir, "branch", reason)
	return r.fallback.BranchName(ctx, dir)
}

func (r *gitdirRemoteHeadReader) UpstreamState(ctx context.Context, dir, branch string) (upstreamState, error) {
	state, err := loadWorktreeGitState(dir)
	if err != nil {
		logGitdirFallback(dir, "upstream", err)
		return r.fallback.UpstreamState(ctx, dir, branch)
	}
	return state.upstream(branch), nil
}

func (r *gitdirRemoteHeadReader) RemoteTrackingSHA(ctx context.Context, dir, remote, branch string) (string, string, bool, error) {
	trackingRef := "refs/remotes/" + remote + "/" + branch
	state, err := loadWorktreeGitState(dir)
	if err == nil {
		var sha string
		var ok bool
		sha, ok, err = state.resolveRef(trackingRef)
		if err == nil {
			return sha, trackingRef, ok, nil
		}
	}
	logGitdirFallback(dir, "tracking ref", err)
	return r.fallback.RemoteTrackingSHA(ctx, dir, remote, branch)
}

func (r *gitdirRemoteHeadReader) SetBranchUpstream(ctx context.Context, dir, branch, remote, mergeRef string) error {
	return r.fallback.SetBranchUpstream(ctx, dir, branch, remote, mergeRef)
}

func logGitdirFallback(dir, question string, reason error) {
	slog.Debug("workspace pushed-head observer reading git state through git",
		"dir", dir, "question", question, "reason", reason)
}

// errGitdirUnsupported marks repository layouts the direct reader cannot
// interpret; callers answer through git instead.
var errGitdirUnsupported = errors.New("git directory layout not readable directly")

// worktreeGitState is one worktree's git directory, common directory, and
// parsed repository config.
type worktreeGitState struct {
	gitDir    string
	commonDir string
	config    gitdirConfig
}

func loadWorktreeGitState(worktree string) (*worktreeGitState, error) {
	gitDir, commonDir, err := resolveWorktreeGitDirs(worktree)
	if err != nil {
		return nil, err
	}
	config, err := parseGitConfigFile(filepath.Join(commonDir, "config"))
	if err != nil {
		return nil, err
	}
	if config.hasIncludes {
		return nil, fmt.Errorf("%w: config uses includes", errGitdirUnsupported)
	}
	if storage := config.get("extensions", "", "refstorage"); storage != "" && !strings.EqualFold(storage, "files") {
		return nil, fmt.Errorf("%w: ref storage %q", errGitdirUnsupported, storage)
	}
	if gitConfigBool(config.get("extensions", "", "worktreeconfig")) {
		local, err := parseGitConfigFile(filepath.Join(gitDir, "config.worktree"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if local.hasIncludes {
			return nil, fmt.Errorf("%w: worktree config uses includes", errGitdirUnsupported)
		}
		maps.Copy(config.values, local.values)
	}
	return &worktreeGitState{gitDir: gitDir, commonDir: commonDir, config: config}, nil
}

// resolveWorktreeGitDirs locates the worktree's private git directory (a
// `.git` directory, or the target of a `.git` file written by
// `git worktree add`) and the common directory that holds refs, packed-refs,
// and config for every worktree of the repository.
func resolveWorktreeGitDirs(worktree string) (gitDir, commonDir string, err error) {
	dotGit := filepath.Join(worktree, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", "", err
	}
	gitDir = dotGit
	if !info.IsDir() {
		data, err := os.ReadFile(dotGit)
		if err != nil {
			return "", "", err
		}
		line, _, _ := strings.Cut(string(data), "\n")
		target, ok := strings.CutPrefix(line, "gitdir:")
		if !ok {
			return "", "", fmt.Errorf("%w: %s is neither a directory nor a gitdir file", errGitdirUnsupported, dotGit)
		}
		gitDir = strings.TrimSpace(target)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(worktree, gitDir)
		}
	}
	commonDir = gitDir
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	switch {
	case err == nil:
		commonDir = strings.TrimSpace(string(data))
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return "", "", err
	}
	return filepath.Clean(gitDir), filepath.Clean(commonDir), nil
}

// currentBranch mirrors `git branch --show-current`: the branch name when
// HEAD is a symbolic ref under refs/heads, an empty name when HEAD is
// detached, and not ok for any other content.
func (s *worktreeGitState) currentBranch() (string, bool) {
	data, err := os.ReadFile(filepath.Join(s.gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	head := strings.TrimSpace(string(data))
	if target, ok := strings.CutPrefix(head, "ref:"); ok {
		target = strings.TrimSpace(target)
		if branch, ok := strings.CutPrefix(target, "refs/heads/"); ok && branch != "" {
			return branch, true
		}
		return "", strings.HasPrefix(target, "refs/")
	}
	if isGitObjectID(head) {
		return "", true
	}
	return "", false
}

func (s *worktreeGitState) upstream(branch string) upstreamState {
	remote := s.config.get("branch", branch, "remote")
	merge := s.config.get("branch", branch, "merge")
	if remote == "" || merge == "" {
		return upstreamState{}
	}
	return upstreamState{
		hasTracking: true,
		remoteName:  remote,
		branchName:  strings.TrimPrefix(merge, "refs/heads/"),
		remoteURL:   s.config.get("remote", remote, "url"),
	}
}

const maxSymbolicRefDepth = 5

// resolveRef returns the object ID a full ref name points at, reading the
// loose ref first and packed-refs second, exactly as git does. A missing ref
// is not ok; unparsable content is an error.
func (s *worktreeGitState) resolveRef(ref string) (string, bool, error) {
	for range maxSymbolicRefDepth {
		if !strings.HasPrefix(ref, "refs/") || strings.Contains(ref, "..") {
			return "", false, fmt.Errorf("%w: ref %q", errGitdirUnsupported, ref)
		}
		data, err := os.ReadFile(filepath.Join(s.commonDir, filepath.FromSlash(ref)))
		switch {
		case err == nil:
			content := strings.TrimSpace(string(data))
			if target, ok := strings.CutPrefix(content, "ref:"); ok {
				ref = strings.TrimSpace(target)
				continue
			}
			if !isGitObjectID(content) {
				return "", false, fmt.Errorf("%w: loose ref %q", errGitdirUnsupported, ref)
			}
			return content, true, nil
		case errors.Is(err, fs.ErrNotExist):
			sha, ok, err := s.packedRef(ref)
			if err != nil || ok {
				return sha, ok, err
			}
			return "", false, nil
		default:
			return "", false, err
		}
	}
	return "", false, fmt.Errorf("%w: symbolic ref chain too deep at %q", errGitdirUnsupported, ref)
}

func (s *worktreeGitState) packedRef(ref string) (string, bool, error) {
	file, err := os.Open(filepath.Join(s.commonDir, "packed-refs"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] == '#' || line[0] == '^' {
			continue
		}
		sha, name, ok := bytes.Cut(line, []byte{' '})
		if !ok || string(bytes.TrimSpace(name)) != ref {
			continue
		}
		id := string(sha)
		if !isGitObjectID(id) {
			return "", false, fmt.Errorf("%w: packed ref %q", errGitdirUnsupported, ref)
		}
		return id, true, nil
	}
	return "", false, scanner.Err()
}

func isGitObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// gitdirConfig is a flat view of one git config file. Keys are
// "section.subsection.name" with section and name lower-cased and the
// subsection kept verbatim; the last assignment wins, matching
// `git config --get`.
type gitdirConfig struct {
	values      map[string]string
	hasIncludes bool
}

func (c gitdirConfig) get(section, subsection, name string) string {
	return c.values[gitConfigKey(section, subsection, name)]
}

func gitConfigKey(section, subsection, name string) string {
	return strings.ToLower(section) + "\x00" + subsection + "\x00" + strings.ToLower(name)
}

func gitConfigBool(value string) bool {
	switch strings.ToLower(value) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

func parseGitConfigFile(path string) (gitdirConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return gitdirConfig{values: map[string]string{}}, err
	}
	return parseGitConfig(data)
}

// parseGitConfig understands the subset of git-config(1) syntax git itself
// writes: section headers with optional quoted subsections, `name = value`
// assignments with quoted values, backslash escapes, comments, and
// backslash line continuations. Anything else is an error so the caller
// falls back to git rather than guessing.
func parseGitConfig(data []byte) (gitdirConfig, error) {
	config := gitdirConfig{values: map[string]string{}}
	section, subsection := "", ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		for strings.HasSuffix(line, "\\") && scanner.Scan() {
			line = strings.TrimSuffix(line, "\\") + scanner.Text()
		}
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			var err error
			section, subsection, err = parseGitConfigSectionHeader(line)
			if err != nil {
				return config, err
			}
			if section == "include" || section == "includeif" {
				config.hasIncludes = true
			}
			continue
		}
		if section == "" {
			return config, fmt.Errorf("%w: config value before any section", errGitdirUnsupported)
		}
		name, rawValue, hasValue := strings.Cut(line, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return config, fmt.Errorf("%w: config line %q", errGitdirUnsupported, line)
		}
		value := "true"
		if hasValue {
			var err error
			value, err = parseGitConfigValue(rawValue)
			if err != nil {
				return config, err
			}
		}
		config.values[gitConfigKey(section, subsection, name)] = value
	}
	return config, scanner.Err()
}

func parseGitConfigSectionHeader(line string) (section, subsection string, err error) {
	end := strings.IndexByte(line, ']')
	if end < 0 {
		return "", "", fmt.Errorf("%w: config header %q", errGitdirUnsupported, line)
	}
	header := strings.TrimSpace(line[1:end])
	name, rest, hasSub := strings.Cut(header, " ")
	section = strings.ToLower(strings.TrimSpace(name))
	if !hasSub {
		if dot := strings.IndexByte(section, '.'); dot >= 0 {
			// Deprecated `[section.subsection]` form.
			return section[:dot], section[dot+1:], nil
		}
		return section, "", nil
	}
	rest = strings.TrimSpace(rest)
	if len(rest) < 2 || rest[0] != '"' || rest[len(rest)-1] != '"' {
		return "", "", fmt.Errorf("%w: config header %q", errGitdirUnsupported, line)
	}
	var sub strings.Builder
	quoted := rest[1 : len(rest)-1]
	for i := 0; i < len(quoted); i++ {
		if quoted[i] == '\\' && i+1 < len(quoted) {
			i++
		}
		sub.WriteByte(quoted[i])
	}
	return section, sub.String(), nil
}

// parseGitConfigValue unquotes one assignment value: whitespace around
// unquoted text is trimmed, `#` and `;` start comments outside quotes, and
// backslash escapes inside or outside quotes follow git-config(1).
func parseGitConfigValue(raw string) (string, error) {
	var out strings.Builder
	inQuotes := false
	raw = strings.TrimLeft(raw, " \t")
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\\':
			if i+1 >= len(raw) {
				return "", fmt.Errorf("%w: dangling escape in config value", errGitdirUnsupported)
			}
			i++
			switch raw[i] {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case 'b':
				out.WriteByte('\b')
			case '\\', '"':
				out.WriteByte(raw[i])
			default:
				return "", fmt.Errorf("%w: escape %q in config value", errGitdirUnsupported, raw[i])
			}
		case c == '"':
			inQuotes = !inQuotes
		case !inQuotes && (c == '#' || c == ';'):
			return strings.TrimRight(out.String(), " \t"), nil
		default:
			out.WriteByte(c)
		}
	}
	if inQuotes {
		return "", fmt.Errorf("%w: unterminated quote in config value", errGitdirUnsupported)
	}
	return strings.TrimRight(out.String(), " \t"), nil
}

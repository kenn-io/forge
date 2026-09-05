package landedwork

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/platform"
	gitcmd "go.kenn.io/kit/git/cmd"
)

type Commit struct {
	ID        string
	Tree      string
	Parents   []string
	Author    Signature
	Committer Signature
	Message   []byte
}

// Git owns an empty temporary repository view over the supplied object store.
// This avoids local info/attributes, replace refs, hooks and configuration
// affecting pinned reads. It never changes the supplied repository or fetches
// missing objects. Close removes only the empty view, not the object store.
type Git struct {
	repository string
	view       string
	objects    string
}

func OpenGit(ctx context.Context, repository string, meter *platform.Meter) (*Git, error) {
	if repository == "" {
		return nil, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "git_dir"}
	}
	absolute, err := filepath.Abs(repository)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(absolute, ".git")); err == nil {
		absolute = filepath.Join(absolute, ".git")
	}
	source := &Git{repository: absolute}
	format, err := source.run(ctx, meter, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return nil, err
	}
	objectFormat := strings.TrimSpace(string(format))
	if objectFormat != "sha1" && objectFormat != "sha256" {
		return nil, errors.New("unsupported Git object format")
	}
	objects, err := source.run(ctx, meter, nil, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return nil, err
	}
	source.objects = strings.TrimSpace(string(objects))
	source.view, err = os.MkdirTemp("", "forge-object-view-*")
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = source.Close()
		}
	}()
	for _, name := range []string{"objects", "refs"} {
		if err := os.Mkdir(filepath.Join(source.view, name), 0o700); err != nil {
			return nil, err
		}
	}
	config := "[core]\nrepositoryformatversion = 0\nbare = true\n"
	if objectFormat == "sha256" {
		config = "[core]\nrepositoryformatversion = 1\nbare = true\n[extensions]\nobjectFormat = sha256\n"
	}
	for name, body := range map[string]string{"HEAD": "ref: refs/heads/unborn\n", "config": config} {
		if err := os.WriteFile(filepath.Join(source.view, name), []byte(body), 0o600); err != nil {
			return nil, err
		}
	}
	cleanup = false
	return source, nil
}

func (g *Git) Close() error {
	if g.view == "" {
		return nil
	}
	return os.RemoveAll(g.view)
}

// Introduced asks Git for the bounded revision difference, not both complete
// ancestor inventories. History before an incremental base is not materialized
// into application memory or charged as newly introduced evidence.
func (g *Git) Introduced(ctx context.Context, parent, head string, meter *platform.Meter) ([]string, error) {
	if !fullObjectID(head) || parent != "" && !fullObjectID(parent) {
		return nil, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "introduced"}
	}
	limit := min(meter.RemainingNodes(), math.MaxInt64-1) + 1
	args := []string{"rev-list", "--topo-order", "--reverse", "--no-merges", "--max-count=" + strconv.FormatInt(limit, 10), head}
	if parent != "" {
		args = append(args, "^"+parent)
	}
	data, err := g.run(ctx, meter, nil, append(args, "--")...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for id := range strings.FieldsSeq(string(data)) {
		if err := meter.Nodes(1); err != nil {
			return nil, err
		}
		if !fullObjectID(id) {
			return nil, errors.New("malformed Git revision list")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// meteredOutput bounds allocation at the subprocess write boundary. stderr is
// discarded; diagnostic prose never becomes an evidence field.
type meteredOutput struct {
	buffer bytes.Buffer
	meter  *platform.Meter
	err    error
}

func (w *meteredOutput) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if err := w.meter.Bytes(int64(len(data))); err != nil {
		w.err = err
		return 0, err
	}
	return w.buffer.Write(data)
}

func (g *Git) run(ctx context.Context, meter *platform.Meter, input io.Reader, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gitDir := g.repository
	if g.view != "" {
		gitDir = g.view
	}
	runner := gitcmd.New()
	runner.DisableSafeDirectoryForward = true
	command := runner.Command(ctx, "", append([]string{"--git-dir=" + gitDir, "-c", "core.attributesFile=", "-c", "protocol.allow=never"}, args...)...)
	command.Env = append(command.Env, "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_ATTR_NOSYSTEM=1", "LC_ALL=C")
	if g.view != "" {
		command.Env = append(command.Env, "GIT_OBJECT_DIRECTORY="+g.objects)
	}
	command.Stdin = input
	out := meteredOutput{meter: meter}
	command.Stdout = &out
	command.Stderr = io.Discard
	err := command.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if out.err != nil {
		return nil, out.err
	}
	if err != nil {
		return nil, fmt.Errorf("read Git objects: %w", err)
	}
	return out.buffer.Bytes(), nil
}

func (g *Git) Commit(ctx context.Context, id string, meter *platform.Meter) (Commit, error) {
	if !fullObjectID(id) {
		return Commit{}, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "commit"}
	}
	if err := meter.Nodes(1); err != nil {
		return Commit{}, err
	}
	body, err := g.run(ctx, meter, nil, "cat-file", "commit", id)
	if err != nil {
		return Commit{}, err
	}
	header, message, ok := bytes.Cut(body, []byte("\n\n"))
	if !ok {
		return Commit{}, errors.New("malformed Git commit")
	}
	commit := Commit{ID: strings.ToLower(id), Message: message}
	authorSeen, committerSeen := false, false
	for line := range bytes.SplitSeq(header, []byte{'\n'}) {
		key, value, ok := bytes.Cut(line, []byte{' '})
		if !ok {
			continue
		}
		switch string(key) {
		case "tree":
			if !fullObjectID(string(value)) {
				return Commit{}, errors.New("malformed Git tree ID")
			}
			commit.Tree = string(value)
		case "parent":
			if !fullObjectID(string(value)) {
				return Commit{}, errors.New("malformed Git parent ID")
			}
			commit.Parents = append(commit.Parents, string(value))
		case "author":
			commit.Author, err = parseSignature(value)
			authorSeen = true
		case "committer":
			commit.Committer, err = parseSignature(value)
			committerSeen = true
		}
		if err != nil {
			return Commit{}, err
		}
	}
	if commit.Tree == "" || !authorSeen || !committerSeen {
		return Commit{}, errors.New("incomplete Git commit")
	}
	return commit, nil
}

func parseSignature(value []byte) (Signature, error) {
	end := bytes.LastIndexByte(value, '>')
	if end < 0 {
		return Signature{}, errors.New("malformed Git signature")
	}
	start := bytes.LastIndexByte(value[:end], '<')
	if start < 0 {
		return Signature{}, errors.New("malformed Git signature")
	}
	times := bytes.Fields(value[end+1:])
	if len(times) != 2 {
		return Signature{}, errors.New("malformed Git signature time")
	}
	seconds, err := strconv.ParseInt(string(times[0]), 10, 64)
	if err != nil {
		return Signature{}, errors.New("malformed Git signature time")
	}
	return Signature{Byline: value[:end+1], Email: value[start+1 : end], Time: time.Unix(seconds, 0).UTC()}, nil
}

func (g *Git) Attributes(ctx context.Context, tree string, paths []string, meter *platform.Meter) (map[string]bool, error) {
	if !fullObjectID(tree) {
		return nil, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "tree"}
	}
	var input bytes.Buffer
	seen := make(map[string]bool, len(paths))
	for _, name := range paths {
		if name == "" || strings.ContainsRune(name, 0) {
			return nil, errors.New("invalid Git attribute path")
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		if err := meter.Bytes(int64(len(name) + 1)); err != nil {
			return nil, err
		}
		input.WriteString(name)
		input.WriteByte(0)
	}
	if len(seen) == 0 {
		return map[string]bool{}, nil
	}
	output, err := g.run(ctx, meter, &input, "check-attr", "-z", "--source="+tree, "--stdin", "linguist-generated")
	if err != nil {
		return nil, err
	}
	return ParseGeneratedAttributes(paths, output)
}

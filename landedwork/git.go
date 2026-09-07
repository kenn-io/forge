package landedwork

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	gitcmd "go.kenn.io/kit/git/cmd"
	gitenv "go.kenn.io/kit/git/env"
)

type objectView struct {
	dir    string
	runner gitcmd.Runner
	meter  *meter
}

func (v *objectView) command(ctx context.Context, args ...string) *exec.Cmd {
	return v.runner.Command(ctx, v.dir, args...)
}

func (v *objectView) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := v.command(ctx, args...)
	out, stderr := &boundedBuffer{meter: v.meter}, &boundedBuffer{meter: v.meter}
	cmd.Stdout, cmd.Stderr = out, stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if v.meter.failed {
		return nil, ErrInputBudget
	}
	if err != nil {
		return nil, fmt.Errorf("read Git objects: %w", err)
	}
	return out.Bytes(), nil
}

func openView(ctx context.Context, path string, m *meter) (_ *objectView, err error) {
	if path == "" {
		return nil, errors.New("explicit repository path required")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	runner := gitcmd.New()
	runner.DisableSafeDirectoryForward = true
	source := &objectView{dir: path, runner: runner, meter: m}
	objects, err := source.run(ctx, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return nil, err
	}
	format, err := source.run(ctx, "rev-parse", "--show-object-format")
	if err != nil {
		return nil, err
	}
	fmtName := strings.TrimSpace(string(format))
	if fmtName != "sha1" && fmtName != "sha256" {
		return nil, errors.New("unknown Git object format")
	}
	dir, err := os.MkdirTemp("", "forge-object-view-*")
	if err != nil {
		return nil, err
	}
	v := &objectView{dir: dir, runner: runner, meter: m}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(dir))
		}
	}()
	if _, err = v.run(ctx, "init", "--bare", "--template=", "--object-format="+fmtName); err != nil {
		return nil, err
	}
	shallowPath, err := source.run(ctx, "rev-parse", "--path-format=absolute", "--git-path", "shallow")
	if err != nil {
		return nil, err
	}
	if err = copyShallow(strings.TrimSpace(string(shallowPath)), dir, m); err != nil {
		return nil, err
	}
	v.runner.Env = append(gitenv.StripAll(os.Environ()),
		"GIT_OBJECT_DIRECTORY="+strings.TrimSpace(string(objects)), "GIT_NO_LAZY_FETCH=1",
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0")
	v.runner.StripEnv = false
	return v, nil
}

func (v *objectView) close() error { return os.RemoveAll(v.dir) }

func (v *objectView) parents(ctx context.Context, id string) ([]string, error) {
	if err := v.meter.node(); err != nil {
		return nil, err
	}
	data, err := v.run(ctx, "cat-file", "commit", id)
	if err != nil {
		return nil, err
	}
	var parents []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if line == "" {
			break
		}
		if parent, ok := strings.CutPrefix(line, "parent "); ok {
			if !objectID(parent) {
				return nil, errors.New("invalid parent object ID")
			}
			parents = append(parents, parent)
		}
	}
	return parents, nil
}

func (v *objectView) introduced(ctx context.Context, base, head string) ([]string, error) {
	data, err := v.run(ctx, "rev-list", "--topo-order", "--reverse", head, "^"+base, "--")
	if err != nil {
		return nil, err
	}
	var ids []string
	for id := range strings.FieldsSeq(string(data)) {
		if err := v.meter.node(); err != nil {
			return nil, err
		}
		if err := v.meter.records(1); err != nil {
			return nil, err
		}
		if !objectID(id) {
			return nil, errors.New("invalid revision object ID")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

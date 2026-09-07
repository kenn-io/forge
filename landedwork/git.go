package landedwork

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	out := &boundedBuffer{meter: v.meter}
	err := v.runTo(ctx, out, args...)
	return out.Bytes(), err
}

func (v *objectView) runTo(ctx context.Context, out io.Writer, args ...string) error {
	cmd := v.command(ctx, args...)
	cmd.Stdout, cmd.Stderr = out, &boundedBuffer{meter: v.meter}
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if v.meter.failed {
		return ErrInputBudget
	}
	if err != nil {
		return fmt.Errorf("read Git objects: %w", err)
	}
	return nil
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
	if err := source.requireRepositoryRoot(ctx); err != nil {
		return nil, err
	}
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

// Resolve only metadata at the supplied path, never Git's parent discovery.
// Worktrees have a .git directory or gitfile; bare repositories are git dirs.
func (v *objectView) requireRepositoryRoot(ctx context.Context) error {
	gitDir := filepath.Join(v.dir, ".git")
	if _, err := os.Stat(gitDir); errors.Is(err, os.ErrNotExist) {
		gitDir = v.dir
	} else if err != nil {
		return err
	}
	_, err := v.run(ctx, "rev-parse", "--resolve-git-dir", gitDir)
	return err
}

func (v *objectView) close() error { return os.RemoveAll(v.dir) }

func (v *objectView) parents(ctx context.Context, id string) ([]string, error) {
	if err := v.meter.node(); err != nil {
		return nil, err
	}
	data, err := v.run(ctx, "rev-list", "--parents", "-n", "1", id, "--")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(data))
	if len(ids) == 0 || ids[0] != id {
		return nil, errors.New("missing commit object")
	}
	for _, parent := range ids[1:] {
		if !objectID(parent) {
			return nil, errors.New("invalid parent object ID")
		}
	}
	return ids[1:], nil
}

func (v *objectView) introduced(ctx context.Context, base, head string) ([]string, error) {
	out := &commitStream{meter: v.meter}
	err := v.runTo(ctx, out, "rev-list", "--topo-order", "--reverse", head, "^"+base, "--")
	if err != nil {
		return nil, err
	}
	if out.pending != "" {
		return nil, errors.New("incomplete revision object ID")
	}
	return out.ids, nil
}

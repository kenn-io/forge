package landedwork

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var errShallow = errors.New("required shallow boundary")

func copyShallow(source, dir string, m *meter) (err error) {
	f, err := os.Open(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	b := &boundedBuffer{meter: m}
	if _, err = io.Copy(b, f); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "shallow"), b.Bytes(), 0600)
}

// A reachable shallow root can hide either introduced or excluded ancestry.
// Unrelated shallow branches do not affect this interval.
func (v *objectView) checkShallow(ctx context.Context, b Bounds) error {
	data, err := v.run(ctx, "rev-parse", "--is-shallow-repository")
	if err != nil || strings.TrimSpace(string(data)) == "false" {
		return err
	}
	f, err := os.Open(filepath.Join(v.dir, "shallow"))
	if err != nil {
		return err
	}
	defer f.Close()
	buf := &boundedBuffer{meter: v.meter}
	if _, err = io.Copy(buf, f); err != nil {
		return err
	}
	for id := range strings.FieldsSeq(string(buf.Bytes())) {
		if !objectID(id) {
			return errShallow
		}
		for _, head := range []string{b.Base, b.Head} {
			found, err := v.ancestor(ctx, id, head)
			if err != nil {
				return err
			}
			if found {
				return errShallow
			}
		}
	}
	return nil
}

func (v *objectView) ancestor(ctx context.Context, base, head string) (bool, error) {
	if err := v.meter.node(); err != nil {
		return false, err
	}
	_, err := v.run(ctx, "merge-base", "--is-ancestor", base, head)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

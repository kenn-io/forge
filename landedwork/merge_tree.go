package landedwork

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// merge-tree writes synthesized objects only into the disposable object view.
// The caller's object store is a read-only alternate. A conflict, absent object,
// or exhausted budget remains inconclusive, even if Git prints a tree ID.
func (v *objectView) mergeTreeMatches(ctx context.Context, base, before, source, after string) (bool, error) {
	args := []string{"merge-tree", "--write-tree", "--no-messages"}
	if base != "" {
		args = append(args, "--merge-base="+base)
	}
	raw, err := v.run(ctx, append(args, before, source)...)
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
			return false, editProofError("merge_conflict")
		}
		return false, err
	}
	tree := strings.TrimSpace(string(raw))
	if !objectID(tree) {
		return false, editProofError("merge_tree_invalid")
	}
	actual, err := v.run(ctx, "rev-parse", after+"^{tree}")
	if err != nil {
		return false, err
	}
	prior, err := v.run(ctx, "rev-parse", before+"^{tree}")
	if err != nil {
		return false, err
	}
	if string(actual) == string(prior) {
		return false, editProofError("empty_tree_edits")
	}
	return tree == strings.TrimSpace(string(actual)), nil
}

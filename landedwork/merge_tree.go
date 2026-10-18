package landedwork

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// A nonlinear source needs one unambiguous boundary for its full net edits.
// A recursively synthesized base does not establish that edit inventory.
func (v *objectView) mergeBase(ctx context.Context, before, source string) (string, error) {
	if err := v.meter.node(); err != nil {
		return "", err
	}
	raw, err := v.run(ctx, "merge-base", "--all", before, source)
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
			return "", editProofError("merge_base_unproven")
		}
		return "", err
	}
	ids := strings.Fields(string(raw))
	if len(ids) != 1 || !objectID(ids[0]) {
		return "", editProofError("merge_base_unproven")
	}
	if _, err := v.parents(ctx, ids[0]); err != nil {
		return "", err
	}
	return ids[0], nil
}

// merge-tree writes synthesized objects only into the disposable object view.
// The caller's object store is a read-only alternate. A conflict, absent object,
// or exhausted budget remains inconclusive, even if Git prints a tree ID.
// merge-tree --write-tree needs Git 2.38; an older Git is a named gap.
func (v *objectView) mergeTreeMatches(ctx context.Context, base, before, source, after string) (bool, error) {
	ours, theirs := before, source
	if base != "" {
		var err error
		if ours, theirs, err = v.withMergeBase(ctx, base, before, source); err != nil {
			return false, err
		}
	}
	raw, err := v.run(ctx, "merge-tree", "--write-tree", "--no-messages", ours, theirs)
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			switch exit.ExitCode() {
			case 1:
				return false, editProofError("merge_conflict")
			case 129:
				// Git rejects --write-tree as an unknown option before 2.38.
				return false, editProofError("merge_tree_unsupported")
			}
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

// withMergeBase stands in for merge-tree --merge-base, which needs Git 2.40.
// Two commits holding the trees of before and source, each with one parent
// holding base's tree, have that parent as their only merge base. merge-tree
// then performs the same three-way merge of the same trees. The commits exist
// only in the disposable view.
func (v *objectView) withMergeBase(ctx context.Context, base, before, source string) (string, string, error) {
	root, err := v.commitTree(ctx, base)
	if err != nil {
		return "", "", err
	}
	ours, err := v.commitTree(ctx, before, root)
	if err != nil {
		return "", "", err
	}
	theirs, err := v.commitTree(ctx, source, root)
	return ours, theirs, err
}

func (v *objectView) commitTree(ctx context.Context, commit string, parents ...string) (string, error) {
	args := []string{
		"-c", "user.name=landedwork", "-c", "user.email=landedwork@invalid",
		"commit-tree", commit + "^{tree}", "-m", "merge input",
	}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	raw, err := v.run(ctx, args...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	if !objectID(id) {
		return "", editProofError("merge_tree_invalid")
	}
	return id, nil
}

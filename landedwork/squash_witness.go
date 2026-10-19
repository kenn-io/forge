package landedwork

import (
	"context"
	"errors"
)

// witnessedSquash proves a squash whose full net edits differ from the
// source's only because the target already carried some of them, for example
// when other work made the same change first. The terminal tree must be the
// exact three-way merge of the source onto the squash parent from the source
// boundary, so the terminal adds nothing the source does not explain.
//
// That witness also fits the last commit of a rebase whose earlier commits end
// at the squash parent, which would own more than the terminal. Such a rebase
// replays source commits in source order, each a clean cherry-pick onto the
// previous one. The squash stays unproven unless that is disproved.
//
// False with no gap is a mismatch: a missing witness keeps the earlier
// conclusive edit mismatch, so it cannot block another alternative.
func witnessedSquash(ctx context.Context, v *objectView, c Candidate, sourceBefore, before string, ordered bool) (bool, Gap) {
	matched, err := v.mergeTreeMatches(ctx, sourceBefore, before, c.SourceHead, c.Terminal)
	if _, ok := errors.AsType[editProofError](err); ok {
		return false, Gap{}
	}
	if err != nil {
		return false, Gap{CandidateID: c.ID, ObjectID: c.Terminal, Reason: graphReason(err)}
	}
	if !matched {
		return false, Gap{}
	}
	rebased, err := rebaseReplayPossible(ctx, v, c, before, ordered)
	if err != nil {
		return false, Gap{CandidateID: c.ID, ObjectID: c.Terminal, Reason: editReason(err)}
	}
	return !rebased, Gap{}
}

// A rebase owning more than the terminal makes the terminal a replay of some
// source commit onto before, and before a replay of an earlier one onto its
// own parent. Disproving either half leaves the terminal as the only possible
// landing commit. Linear sources fix the order; otherwise any distinct pair
// counts.
func rebaseReplayPossible(ctx context.Context, v *objectView, c Candidate, before string, ordered bool) (bool, error) {
	if len(c.Source) < 2 {
		return false, nil
	}
	// Only the latest replayed position matters in order; without an order,
	// a second position already admits every earlier commit.
	var terminal []int
	for j := len(c.Source) - 1; j >= 0 && len(terminal) < 2 && (!ordered || len(terminal) == 0); j-- {
		replayed, err := v.replays(ctx, c.Terminal, before, c.Source[j])
		if err != nil {
			return false, err
		}
		if replayed {
			terminal = append(terminal, j)
		}
	}
	if len(terminal) == 0 {
		return false, nil
	}
	parents, err := v.parents(ctx, before)
	if err != nil {
		return false, err
	}
	if len(parents) != 1 {
		// A merge or root commit is never a rebase replay.
		return false, nil
	}
	for i, id := range c.Source {
		if ordered && i >= terminal[0] {
			break
		}
		if !ordered && len(terminal) == 1 && terminal[0] == i {
			continue
		}
		replayed, err := v.replays(ctx, before, parents[0], id)
		if err != nil || replayed {
			return replayed, err
		}
	}
	return false, nil
}

// replays reports whether commit could be a rebase replay of source onto
// parent: its tree is the clean cherry-pick of source, or it is empty. A merge
// source is picked against its first parent.
func (v *objectView) replays(ctx context.Context, commit, parent, source string) (bool, error) {
	parents, err := v.parents(ctx, source)
	if err != nil {
		return false, err
	}
	if len(parents) == 0 {
		// Callers reject parentless sources; never treat one as disproved.
		return true, nil
	}
	matched, err := v.mergeTreeMatches(ctx, parents[0], parent, source, commit)
	switch {
	case errors.Is(err, editProofError("merge_conflict")):
		return false, nil
	case errors.Is(err, editProofError("empty_tree_edits")):
		return true, nil
	}
	return matched, err
}

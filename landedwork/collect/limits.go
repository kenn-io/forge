package collect

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
)

func validate(ctx context.Context, r platform.LandingEvidenceReader, route platform.RepoRef, q landedwork.Query, l Limits) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("landing collection requires a deadline")
	}
	if l.Calls <= 0 || l.Records <= 0 || l.OutputBytes <= 0 {
		return errors.New("landing collection limits must be positive")
	}
	if r == nil {
		return errors.New("landing collection requires a reader")
	}
	if err := platform.ValidateCanonicalRepoRef(route); err != nil {
		return err
	}
	repo := q.Bounds.Repository
	id, err := strconv.ParseInt(repo.ID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != repo.ID || route.Platform != r.Platform() || route.Host != r.Host() || repo.Provider != r.Platform() || repo.Host != r.Host() {
		return errors.New("landing collection requires matching provider identity")
	}
	size := len(q.Bounds.Head)
	if !objectID(q.Bounds.Base, size) || !objectID(q.Bounds.Head, size) || q.Complete && len(q.Gaps) != 0 {
		return errors.New("invalid landing query")
	}
	seen := make(map[string]bool)
	for _, sha := range q.Commits {
		if !objectID(sha, size) || sha == q.Bounds.Base || seen[sha] {
			return errors.New("invalid landing query commit")
		}
		seen[sha] = true
	}
	if q.Complete && q.Bounds.Base != q.Bounds.Head && !seen[q.Bounds.Head] {
		return errors.New("landing query omits head")
	}
	return nil
}

func objectID(s string, size int) bool {
	if len(s) != size || size != 40 && size != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func resultBytes(r Result) int64 {
	q, inv := r.Evidence.Query, r.Evidence.Inventory
	n := stringsBytes(string(q.Bounds.Repository.Provider), q.Bounds.Repository.Host, q.Bounds.Repository.ID, q.Bounds.Base, q.Bounds.Head, inv.Reason, inv.NextCommit, inv.NextPage)
	n += stringsBytes(q.Commits...)
	for _, g := range q.Gaps {
		n += stringsBytes(g.CandidateID, g.ObjectID, g.Reason, g.Span.Before, g.Span.Through)
	}
	for _, c := range r.Evidence.Candidates {
		n += stringsBytes(string(c.Repository.Provider), c.Repository.Host, c.Repository.ID, c.ID, c.Terminal, c.SourceHead, c.Method, c.MethodEvidence, c.TerminalEvidence) + stringsBytes(c.Source...)
	}
	for _, o := range r.Observations {
		d := o.Change
		n += stringsBytes(d.TargetBranch, d.Terminal, d.TerminalEvidence, o.Reason, o.FailureStage, o.NextPage) + stringsBytes(o.Source...)
		for _, s := range []*string{d.MergeSHA, d.SquashSHA, d.SourceHead} {
			if s != nil {
				n += int64(len(*s))
			}
		}
	}
	return n
}

func stringsBytes(values ...string) int64 {
	var n int64
	for _, s := range values {
		n += int64(len(s))
	}
	return n
}

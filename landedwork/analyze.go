package landedwork

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"slices"
	"strings"

	"go.kenn.io/forge/platform"
)

type graph struct {
	source  GitSource
	meter   *platform.Meter
	commits map[string]Commit
}

func (g *graph) get(ctx context.Context, id string) (Commit, error) {
	if err := ctx.Err(); err != nil {
		return Commit{}, err
	}
	if commit, ok := g.commits[id]; ok {
		return commit, nil
	}
	commit, err := g.source.Commit(ctx, id, g.meter)
	if err != nil {
		return Commit{}, err
	}
	g.commits[id] = commit
	return commit, nil
}
func (g *graph) ancestors(ctx context.Context, id string) ([]Commit, error) {
	var commits []Commit
	seen := make(map[string]bool)
	var visit func(string) error
	visit = func(id string) error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		commit, err := g.get(ctx, id)
		if err != nil {
			return err
		}
		for _, parent := range commit.Parents {
			if err := visit(parent); err != nil {
				return err
			}
		}
		commits = append(commits, commit)
		return nil
	}
	if id != "" {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return commits, nil
}
func (g *graph) introduced(ctx context.Context, parent, head string) ([]Commit, error) {
	ids, err := g.source.Introduced(ctx, parent, head, g.meter)
	if err != nil {
		return nil, err
	}
	commits := make([]Commit, 0, len(ids))
	for _, id := range ids {
		commit, err := g.get(ctx, id)
		if err != nil {
			return nil, err
		}
		commits = append(commits, commit)
	}
	return commits, nil
}

func (g *graph) isAncestor(ctx context.Context, ancestor, head string) (bool, error) {
	pending := []string{head}
	seen := make(map[string]bool)
	for len(pending) > 0 {
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if id == ancestor {
			return true, nil
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		commit, err := g.get(ctx, id)
		if err != nil {
			return false, err
		}
		pending = append(pending, commit.Parents...)
	}
	return false, nil
}

// Analyze shares the caller's meter with object preparation and correspondence
// checks, so one report cannot reset its input budget between phases.
func Analyze(ctx context.Context, request Request, source GitSource, meter *platform.Meter) (result Result, err error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if _, bounded := ctx.Deadline(); !bounded || meter == nil || source == nil {
		return Result{}, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "analysis_budget"}
	}
	identity := request.Snapshot.Repository.Identity
	host, err := platform.NormalizeHost(identity.Provider, identity.Instance)
	if err != nil || host != identity.Instance || identity.ID == "" || request.Policy != CodePolicy || request.Snapshot.Schema != platform.LandingSnapshotSchema || !fullObjectID(request.BaseSHA) || !fullObjectID(request.HeadSHA) || request.DefaultBranch == "" || request.DefaultBranch != request.Snapshot.Repository.DefaultBranch {
		return Result{}, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "analysis"}
	}
	// Bound the supplied provider snapshot as input before starting Git work.
	// Hashing the admitted values below streams to a fixed-size hash state; it
	// does not consume the input allowance again or prevent a budget-gap result.
	if err := json.MarshalWrite(evidenceSink(func(data []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := meter.Bytes(int64(len(data))); err != nil {
			return 0, err
		}
		return len(data), nil
	}), request); err != nil {
		return Result{}, err
	}
	result = Result{Schema: Schema, Analyzer: AnalyzerVersion, Policy: CodePolicy, Repository: identity, BaseSHA: request.BaseSHA, HeadSHA: request.HeadSHA, CertifiedHead: request.BaseSHA, Landings: []Landing{}, Gaps: []Gap{}, Presence: []Presence{}}
	graph := &graph{source: source, meter: meter, commits: make(map[string]Commit)}
	defer func() {
		if err != nil {
			return
		}
		ids := make([]string, 0, len(graph.commits))
		for id := range graph.commits {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			result.Presence = append(result.Presence, Presence{ID: id, Present: true})
		}
		hash := sha256.New()
		err = WriteCanonicalEvidence(evidenceSink(func(data []byte) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return hash.Write(data)
		}), request, result)
		if err != nil {
			return
		}
		result.Digest = hex.EncodeToString(hash.Sum(nil))
		err = json.MarshalWrite(evidenceSink(func(data []byte) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if err := meter.Output(int64(len(data))); err != nil {
				return 0, err
			}
			return len(data), nil
		}), result)
	}()
	gap := func(first, last, reason, change string) {
		result.Gaps = append(result.Gaps, Gap{FirstSHA: first, LastSHA: last, Reason: reason, ChangeID: change})
	}
	failed := func(err error) (Result, error) {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		reason := "git_objects_unavailable"
		if errors.Is(err, platform.ErrPageLimit) {
			reason = "input_budget_exhausted"
		}
		gap(request.BaseSHA, request.HeadSHA, reason, "")
		return result, nil
	}
	// The common incremental path needs only the first-parent interval, not a
	// walk of every commit preceding the certified base.
	spine := []Commit{}
	if _, err := graph.get(ctx, request.BaseSHA); err != nil {
		return failed(err)
	}
	cursor := request.HeadSHA
	for cursor != request.BaseSHA {
		commit, err := graph.get(ctx, cursor)
		if err != nil {
			return failed(err)
		}
		spine = append(spine, commit)
		if len(commit.Parents) == 0 {
			break
		}
		cursor = commit.Parents[0]
	}
	if cursor != request.BaseSHA {
		history, err := graph.ancestors(ctx, request.HeadSHA)
		if err != nil {
			return failed(err)
		}
		result.Diverged = !slices.ContainsFunc(history, func(commit Commit) bool { return commit.ID == request.BaseSHA })
		reason := "base_not_on_first_parent"
		if result.Diverged {
			reason = "diverged"
		}
		gap(request.BaseSHA, request.HeadSHA, reason, "")
		return result, nil
	}
	if _, err := graph.get(ctx, request.BaseSHA); err != nil {
		return failed(err)
	}
	slices.Reverse(spine)
	positions := make(map[string]int, len(spine))
	for i, commit := range spine {
		positions[commit.ID] = i
	}
	blocked := make([]bool, len(spine))
	block := func(candidate platform.LandingCandidate, reason string) {
		first, last := 0, len(spine)-1
		if span := candidate.PossibleSpan; span != nil {
			begin, beginOK := positions[span.FirstSHA]
			end, endOK := positions[span.LastSHA]
			if beginOK && endOK && begin <= end {
				first, last = begin, end
			}
		} else if candidate.MethodProof != "" && (candidate.Method == platform.LandingMerge || candidate.Method == platform.LandingSquash) {
			if index, ok := positions[candidate.TerminalSHA]; ok {
				first, last = index, index
			}
		}
		for i := first; i <= last; i++ {
			blocked[i] = true
		}
		if first <= last {
			gap(spine[first].ID, spine[last].ID, reason, candidate.ID)
		} else {
			gap(request.BaseSHA, request.HeadSHA, reason, candidate.ID)
		}
	}
	if !request.Snapshot.Coverage.Complete || !request.Snapshot.Capabilities.CompleteCandidateInventory {
		block(platform.LandingCandidate{}, "candidate_inventory_incomplete")
	}
	// Every candidate is resolved before any direct-push assignment.
	var proofs []*originProof
	owners := make(map[int]*originProof)
	if err := meter.Records(int64(len(request.Snapshot.Candidates))); err != nil {
		return failed(err)
	}
	candidates := slices.Clone(request.Snapshot.Candidates)
	slices.SortFunc(candidates, func(a, b platform.LandingCandidate) int { return strings.Compare(a.ID, b.ID) })
	for _, candidate := range candidates {
		index, ok := positions[candidate.TerminalSHA]
		if !ok {
			if (candidate.MethodProof != "" || candidate.TerminalProof != "") && fullObjectID(candidate.TerminalSHA) {
				before, err := graph.isAncestor(ctx, candidate.TerminalSHA, request.BaseSHA)
				if err != nil {
					return failed(err)
				}
				if before {
					continue
				}
			}
			block(candidate, "candidate_origin_unbounded")
			continue
		}
		proof, reason := prove(ctx, graph, candidate, request.Snapshot.Capabilities, spine, index)
		if proof == nil {
			block(candidate, reason)
			continue
		}
		for i := proof.start; i <= proof.end; i++ {
			if existing, ok := owners[i]; ok {
				existing.invalid = true
				proof.invalid = true
				block(existing.candidate, "origin_overlap")
				block(candidate, "origin_overlap")
			} else {
				owners[i] = proof
			}
		}
		proofs = append(proofs, proof)
	}
	origins := make(map[int]*originProof)
	for _, proof := range proofs {
		if !proof.invalid {
			origins[proof.start] = proof
		}
	}
	commitEvidence := func(item Commit) (CommitEvidence, error) {
		evidence := CommitEvidence{ID: item.ID, AuthorTime: item.Author.Time, CommitterTime: item.Committer.Time, Claims: GitClaims(item.Author, item.Message), DeclaredReverts: []string{}}
		for _, target := range DeclaredRevertCandidates(item.Message) {
			if _, err := graph.get(ctx, target); err == nil {
				evidence.DeclaredReverts = append(evidence.DeclaredReverts, target)
			} else if ctx.Err() != nil || errors.Is(err, platform.ErrPageLimit) {
				return evidence, err
			}
		}
		return evidence, nil
	}
	certified := true
	for i := 0; i < len(spine); {
		proof := origins[i]
		if blocked[i] && proof == nil {
			certified = false
			i++
			continue
		}
		start, end := i, i
		candidate := platform.LandingCandidate{}
		introduced := []Commit{spine[i]}
		if proof != nil {
			end = proof.end
			candidate = proof.candidate
			introduced = proof.introduced
		}
		commit := spine[end]
		parent := spine[start].Parents[0]
		if proof == nil && len(commit.Parents) > 1 {
			introduced, err = graph.introduced(ctx, parent, commit.ID)
			if err != nil {
				gap(commit.ID, commit.ID, "introduced_objects_unavailable", "")
				certified = false
				i++
				continue
			}
		}
		landing := Landing{Origin: "direct_push", Parent: parent, Terminal: commit.ID, Spine: []string{}, Introduced: []CommitEvidence{}, Sources: []CommitEvidence{}, Claims: []Claim{}}
		landing.TerminalCommit, err = commitEvidence(commit)
		if err != nil {
			return failed(err)
		}
		for _, claim := range landing.TerminalCommit.Claims {
			if claim.Role == RoleCoauthor {
				landing.Claims = append(landing.Claims, claim)
			}
		}
		for j := start; j <= end; j++ {
			landing.Spine = append(landing.Spine, spine[j].ID)
		}
		if proof != nil {
			landing.Origin = "change"
			landing.ChangeID = candidate.ID
			landing.Method = candidate.Method
			landing.MergedAt = candidate.MergedAt
			landing.ProviderEvidence = &candidate
			landing.Claims = append(ProviderClaims(identity, candidate), landing.Claims...)
			for _, source := range proof.source {
				evidence, err := commitEvidence(source)
				if err != nil {
					return failed(err)
				}
				landing.Sources = append(landing.Sources, evidence)
			}
		}
		for _, item := range introduced {
			evidence, err := commitEvidence(item)
			if err != nil {
				return failed(err)
			}
			landing.Introduced = append(landing.Introduced, evidence)
		}
		var reason string
		landing.Churn, reason, err = measure(ctx, source, parent, commit.ID, meter)
		if err != nil {
			gap(spine[start].ID, commit.ID, reason, candidate.ID)
			blocked[start] = true
		}
		for j := start; j <= end; j++ {
			if blocked[j] {
				certified = false
			}
		}
		result.Landings = append(result.Landings, landing)
		if certified {
			result.CertifiedHead = commit.ID
		}
		i = end + 1
	}
	result.Complete = len(result.Gaps) == 0
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, nil
}

type evidenceSink func([]byte) (int, error)

func (write evidenceSink) Write(data []byte) (int, error) { return write(data) }

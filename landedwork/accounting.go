package landedwork

func repositoryBytes(r Repository) int64 { return int64(len(r.Provider) + len(r.Host) + len(r.ID)) }
func gapBytes(g Gap) int64 {
	return int64(len(g.CandidateID) + len(g.ObjectID) + len(g.Reason) + len(g.Span.Before) + len(g.Span.Through))
}
func stringBytes(ids []string) int64 {
	var n int64
	for _, id := range ids {
		n += int64(len(id))
	}
	return n
}
func inventoryBytes(i Inventory) int64 {
	return int64(len(i.Reason) + len(i.NextCommit) + len(i.NextPage))
}
func candidateBytes(c Candidate) int64 {
	return repositoryBytes(c.Repository) + int64(len(c.ID)+len(c.Terminal)+len(c.SourceHead)+len(c.Method)+len(c.MethodEvidence)+len(c.TerminalEvidence)) + stringBytes(c.Source)
}
func queryBytes(q Query) int64 {
	n := repositoryBytes(q.Bounds.Repository) + int64(len(q.Bounds.Base)+len(q.Bounds.Head)) + stringBytes(q.Commits)
	for _, gap := range q.Gaps {
		n += gapBytes(gap)
	}
	return n
}

func checkResultOutput(r Result, l Limits) error {
	c := r.Coverage
	n := repositoryBytes(c.Bounds.Repository) + int64(len(c.Bounds.Base)+len(c.Bounds.Head)+len(c.CertifiedHead)) + inventoryBytes(c.Inventory)
	records := int64(len(c.Gaps) + len(r.Landings) + len(r.Unattributed))
	records += int64(len(r.Integrated))
	records += int64(len(r.DirectPushes))
	for _, d := range r.DirectPushes {
		n += int64(len(d.Before)+len(d.Terminal)) + stringBytes(d.Introduced)
		records += int64(len(d.Introduced))
	}
	for _, i := range r.Integrated {
		n += int64(len(i.CandidateID) + len(i.ThroughCandidateID))
	}
	for _, gap := range c.Gaps {
		n += gapBytes(gap)
	}
	n += stringBytes(r.Unattributed)
	for _, landing := range r.Landings {
		n += int64(len(landing.CandidateID)+len(landing.Method)+len(landing.Before)+len(landing.Terminal)) + stringBytes(landing.Source) + stringBytes(landing.Introduced)
		records += int64(len(landing.Source) + len(landing.Introduced))
	}
	if n > l.OutputBytes || records > l.Records {
		return ErrOutputBudget
	}
	return nil
}

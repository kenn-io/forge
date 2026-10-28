package httpapi

import "go.kenn.io/forge/internal/db"

// BranchUpdateEligible also recognizes clean branches behind their base when
// GitHub does not require branches to be up to date. Cached ancestry is usable
// only while both diff pins still match the provider snapshot.
func BranchUpdateEligible(mr db.MergeRequest) bool {
	if mr.State != "open" || mr.MergeableState == "dirty" || mr.MergeableState == "unknown" || mr.MergeableState == "" {
		return false
	}
	return mr.MergeableState == "behind" ||
		(mr.PlatformHeadSHA != "" && mr.PlatformBaseSHA != "" && mr.MergeBaseSHA != "" &&
			mr.DiffHeadSHA == mr.PlatformHeadSHA && mr.DiffBaseSHA == mr.PlatformBaseSHA &&
			mr.MergeBaseSHA != mr.PlatformBaseSHA)
}

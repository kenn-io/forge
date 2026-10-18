package httpapi

import (
	"strings"

	"go.kenn.io/forge/internal/db"
)

type SetLabelsRequest struct {
	Labels *[]string `json:"labels" required:"true"`
}

func (r SetLabelsRequest) LabelNames() []string {
	if r.Labels == nil {
		return nil
	}
	return *r.Labels
}

type ItemLabelsResponse struct {
	Labels []db.Label `json:"labels"`
}

// The pointer distinguishes a missing field from an empty array at decode
// time, while null remains invalid: an empty array is the wire representation
// for clearing the set.
type SetAssigneesRequest struct {
	Assignees *[]string `json:"assignees" required:"true" nullable:"false"`
}

type ItemAssigneesResponse struct {
	Assignees []string `json:"assignees"`
}

type GithubStateOutputBody struct {
	State string `json:"state"`
}

// UpdateMembershipRequest adds and removes names on a set-valued item field
// (labels, assignees). The server applies the change to the item's current
// set, so a client never restates, and cannot undo, names it did not mention.
type UpdateMembershipRequest struct {
	Add    []string `json:"add" required:"true" nullable:"false"`
	Remove []string `json:"remove" required:"true" nullable:"false"`
}

// ApplyMembership returns current without the removed names and with the
// added names appended, keeping current order. same decides name equality.
func ApplyMembership(current []string, req UpdateMembershipRequest, same func(a, b string) bool) []string {
	contains := func(names []string, name string) bool {
		for _, candidate := range names {
			if same(candidate, name) {
				return true
			}
		}
		return false
	}
	next := make([]string, 0, len(current)+len(req.Add))
	for _, name := range current {
		if !contains(req.Remove, name) {
			next = append(next, name)
		}
	}
	for _, raw := range req.Add {
		name := strings.TrimSpace(raw)
		if name != "" && !contains(next, name) {
			next = append(next, name)
		}
	}
	return next
}

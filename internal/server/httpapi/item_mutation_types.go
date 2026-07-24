package httpapi

import "go.kenn.io/middleman/internal/db"

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

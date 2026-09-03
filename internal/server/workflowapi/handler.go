// Package workflowapi owns provider-neutral workflow catalog, run, job, and dispatch HTTP behavior.
package workflowapi

import (
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
)

const (
	capabilityReadWorkflows    = "read_workflows"
	capabilityReadWorkflowRuns = "read_workflow_runs"
	capabilityWorkflowDispatch = "workflow_dispatch"
)

const (
	maxWorkflowInputs       = 25
	maxWorkflowInputPayload = 65_535
)

type Deps struct {
	Resolver       *httpapi.RepositoryResolver
	Syncer         *ghclient.Syncer
	RepoOperations func(db.Repo) httpapi.RepoOperations
}

type Handler struct {
	resolver       *httpapi.RepositoryResolver
	syncer         *ghclient.Syncer
	repoOperations func(db.Repo) httpapi.RepoOperations
}

func New(deps Deps) *Handler {
	return &Handler{
		resolver:       deps.Resolver,
		syncer:         deps.Syncer,
		repoOperations: deps.RepoOperations,
	}
}

func (h *Handler) operations(repo db.Repo) httpapi.RepoOperations {
	if h.repoOperations == nil {
		return httpapi.RepoOperations{}
	}
	return h.repoOperations(repo)
}

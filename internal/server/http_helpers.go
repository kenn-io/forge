package server

import (
	"context"
	"encoding/json"
	"net/http"

	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/server/httpapi"
)

func gitDiscoveryOutput(
	ctx context.Context, dir string, args ...string,
) (string, error) {
	out, err := gitcmd.New().Output(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func writeProblemResponse(w http.ResponseWriter, problem *httpapi.ProblemError) {
	if problem == nil {
		problem = httpapi.NewProblem(
			http.StatusInternalServerError,
			httpapi.CodeInternalError,
			"internal error",
			nil,
		)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(problem.Status)
	_ = json.NewEncoder(w).Encode(problem)
}

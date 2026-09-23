package routepolicy

import (
	"context"
	"encoding/json/v2"
	"net/http"

	"go.kenn.io/forge/internal/server/httpapi"
	gitcmd "go.kenn.io/kit/git/cmd"
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

func WriteProblemResponse(w http.ResponseWriter, problem *httpapi.ProblemError) {
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
	_ = json.MarshalWrite(w, problem)
}

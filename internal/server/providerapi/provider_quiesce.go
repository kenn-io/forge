package providerapi

import (
	"net/http"

	"go.kenn.io/forge/internal/server/httpapi"
)

func SpokePreparationProblem() *httpapi.ProblemError {
	return httpapi.NewProblem(
		http.StatusConflict,
		httpapi.CodeSpokePreparationInProgress,
		"provider writes are sealed while this daemon is being prepared as a federation spoke",
		map[string]any{"reason": "spokePreparationInProgress"},
	)
}

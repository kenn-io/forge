package httpapi

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSemanticSearchFailureOffersLocalSetup(t *testing.T) {
	err := SemanticSearchFailed(errors.New("private endpoint"))
	var problem *ProblemError
	require.ErrorAs(t, err, &problem)
	assert.Equal(t, http.StatusServiceUnavailable, problem.Status)
	assert.Contains(t, problem.Detail, "kenn-forge embeddings local")
	assert.NotContains(t, problem.Detail, "private endpoint")
}

func TestSemanticSearchDisabledOffersLocalSetup(t *testing.T) {
	err := SemanticSearchAvailable("search", false)
	var problem *ProblemError
	require.ErrorAs(t, err, &problem)
	assert.Equal(t, http.StatusServiceUnavailable, problem.Status)
	assert.Contains(t, problem.Detail, "kenn-forge embeddings local")
	assert.Contains(t, problem.Detail, "on the Forge that syncs providers")
}

package server

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/server/httpapi"
)

func problemOperationRateLimited(
	repo db.Repo,
	rate rateLimitAvailability,
) huma.StatusError {
	detail := rate.reason
	if detail == "" {
		detail = "Upstream rate limit exceeded"
	}
	details := map[string]any{
		"reason":       availabilityCodeRateLimited,
		"provider":     string(repoProviderKind(repo)),
		"platformHost": repoProviderHost(repo),
	}
	if rate.retryAt != "" {
		details["retryAfter"] = rate.retryAt
	}
	return httpapi.NewProblem(
		http.StatusTooManyRequests,
		httpapi.CodeRateLimited,
		detail,
		details,
	)
}

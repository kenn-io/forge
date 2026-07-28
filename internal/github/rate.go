package github

import (
	"time"

	gh "github.com/google/go-github/v89/github"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/ratelimit"
)

const RateReserveBuffer = ratelimit.RateReserveBuffer

type Rate = ratelimit.Rate
type RateTracker = ratelimit.RateTracker

type RateLimitSnapshot struct {
	Core    *Rate
	GraphQL *Rate
}

func NewRateTracker(
	database *db.DB, platformHost, ratePrincipal, apiType string,
) *RateTracker {
	return ratelimit.NewPlatformRateTracker(
		database, "github", platformHost, ratePrincipal, apiType,
	)
}

func NewPlatformRateTracker(
	database *db.DB,
	platformName, platformHost, ratePrincipal, apiType string,
) *RateTracker {
	return ratelimit.NewPlatformRateTracker(
		database, platformName, platformHost, ratePrincipal, apiType,
	)
}

func RateBucketKey(platformName, platformHost, ratePrincipal string) string {
	return ratelimit.RateBucketKey(platformName, platformHost, ratePrincipal)
}

func rateFromGitHub(rate gh.Rate) Rate {
	return Rate{
		Limit:     rate.Limit,
		Remaining: rate.Remaining,
		Reset:     rate.Reset.Time,
	}
}

func rateFromGitHubHeaders(limit int, remaining int, reset time.Time) Rate {
	return Rate{
		Limit:     limit,
		Remaining: remaining,
		Reset:     reset,
	}
}

package github

import (
	"time"

	gh "github.com/google/go-github/v84/github"
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
	database *db.DB, platformHost string, apiType string,
) *RateTracker {
	return ratelimit.NewPlatformRateTracker(database, "github", platformHost, apiType)
}

func NewPlatformRateTracker(
	database *db.DB, platformName string, platformHost string, apiType string,
) *RateTracker {
	return ratelimit.NewPlatformRateTracker(database, platformName, platformHost, apiType)
}

func RateBucketKey(platformName, platformHost string) string {
	return ratelimit.RateBucketKey(platformName, platformHost)
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

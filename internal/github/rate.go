package github

import (
	"strings"
	"time"

	gh "github.com/google/go-github/v89/github"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/ratelimit"
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

// RateStatusKey returns the public API key for one provider credential's rate
// and local-ceiling status. Internal bucket keys use NUL separators for
// unambiguous map partitioning; API keys remain printable and stable.
func RateStatusKey(platformName, platformHost, ratePrincipal string) string {
	if ratePrincipal == "" || ratePrincipal == "host" {
		return RateBucketKey(platformName, platformHost, "host")
	}
	return strings.Join([]string{platformName, platformHost, ratePrincipal}, ":")
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

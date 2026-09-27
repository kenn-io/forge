package tokenauth

import (
	"net/url"
	"os"
	"strings"
)

// BitbucketEnvironmentToken follows bkt's headless Cloud auth settings.
// BKT_HOST is mandatory so a Data Center credential is never used for Cloud.
func BitbucketEnvironmentToken(host string) string {
	if host != "bitbucket.org" {
		return ""
	}
	rawHost := strings.TrimSpace(os.Getenv("BKT_HOST"))
	if !strings.Contains(rawHost, "://") {
		rawHost = "https://" + rawHost
	}
	u, err := url.Parse(rawHost)
	if err != nil || u.Scheme != "https" || u.Host != host || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	token := strings.TrimSpace(os.Getenv("BKT_TOKEN"))
	if token == "" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BKT_AUTH_METHOD"))) {
	case "bearer":
		return token
	case "", "basic":
		username := strings.TrimSpace(os.Getenv("BKT_USERNAME"))
		if username == "" {
			return ""
		}
		RegisterKnownSecret(token)
		return username + ":" + token
	default:
		return ""
	}
}

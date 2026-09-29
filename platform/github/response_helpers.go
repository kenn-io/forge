package github

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	gh "github.com/google/go-github/v92/github"
)

// IsNotModified returns true if the error represents a 304 Not Modified
// response from the GitHub API.
func IsNotModified(err error) bool {
	ghErr, ok := errors.AsType[*gh.ErrorResponse](err)
	if !ok || ghErr == nil || ghErr.Response == nil {
		return false
	}
	return ghErr.Response.StatusCode == http.StatusNotModified
}

var cloneURLPattern = regexp.MustCompile(`[/:]([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)

// ParseHeadRepoFullName extracts "owner/repo" from a GitHub clone URL.
// Accepts both HTTPS (https://host/owner/repo[.git]) and SSH
// (git@host:owner/repo[.git]) forms. Returns empty string if the URL does
// not match a recognized form.
func ParseHeadRepoFullName(cloneURL string) string {
	cloneURL = strings.TrimSpace(cloneURL)
	if cloneURL == "" {
		return ""
	}
	m := cloneURLPattern.FindStringSubmatch(cloneURL)
	if len(m) != 3 {
		return ""
	}
	return m[1] + "/" + m[2]
}

func cursorValue(cursor *string) string {
	if cursor == nil {
		return ""
	}
	return *cursor
}

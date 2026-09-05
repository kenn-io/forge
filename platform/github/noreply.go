package github

import (
	"strings"

	"go.kenn.io/forge/platform"
)

// ParseNoreply derives the REST account ID encoded in an ID-form noreply
// address. It proves neither ownership nor authorship: Git metadata is editable.
// The result names a hostname, never an inferred non-default API port.
func ParseNoreply(email string) (host, id string, ok bool) {
	email = platform.NormalizeGitEmail(email)
	for i := range len(email) {
		if email[i] <= ' ' || email[i] == 0x7f {
			return "", "", false
		}
	}
	local, domain, found := strings.Cut(email, "@")
	if !found || strings.Contains(domain, "@") {
		return "", "", false
	}
	id, login, found := strings.Cut(local, "+")
	if !found || login == "" || strings.Contains(login, "+") || id == "" || id[0] == '0' {
		return "", "", false
	}
	for i := range len(id) {
		if id[i] < '0' || id[i] > '9' {
			return "", "", false
		}
	}
	host, found = strings.CutPrefix(domain, "users.noreply.")
	if !found || host == "" || strings.ContainsAny(host, ":/[]") {
		return "", "", false
	}
	normalized, err := platform.NormalizeHost(platform.KindGitHub, host)
	if err != nil || normalized != host {
		return "", "", false
	}
	return host, id, true
}

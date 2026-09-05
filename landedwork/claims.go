// Package landedwork derives reproducible landed-work evidence from provider
// observations and explicitly supplied Git objects. It does not resolve people.
package landedwork

import (
	"bytes"
	"strings"
	"time"

	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/github"
)

type Role string

const (
	RoleAuthor   Role = "author"
	RoleCoauthor Role = "coauthor"
	RoleMerger   Role = "merger"
)

type ClaimKind string

const (
	ClaimGitEmail       ClaimKind = "git_email"
	ClaimProviderUserID ClaimKind = "source_provider_user_id"
)

type Assurance string

const (
	AssuranceUnverified Assurance = "unverified"
	AssuranceVerified   Assurance = "verified"
)

// Signature holds bytes parsed from a Git object, before wire encoding.
type Signature struct {
	Byline []byte
	Email  []byte
	Time   time.Time
}

type Claim struct {
	Role           Role                 `json:"role" enum:"author,coauthor,merger"`
	Kind           ClaimKind            `json:"kind" enum:"git_email,source_provider_user_id"`
	Assurance      Assurance            `json:"assurance" enum:"unverified,verified"`
	Derivation     string               `json:"derivation"`
	Provider       platform.Kind        `json:"provider,omitempty"`
	Instance       string               `json:"instance,omitempty"`
	ProviderUserID string               `json:"provider_user_id,omitempty"`
	AccountType    platform.AccountType `json:"account_type,omitempty"`
	RawByline      platform.RawBytes    `json:"raw_byline"`
	RawEmail       platform.RawBytes    `json:"raw_email"`
	Email          platform.RawBytes    `json:"email"`
}

// ProviderClaims preserve provider-observed author/merger identities. Verified
// describes the provider role observation, never a Git signature or human type.
func ProviderClaims(repository platform.RepositoryIdentity, candidate platform.LandingCandidate) []Claim {
	claims := make([]Claim, 0, 2)
	for _, role := range []struct {
		kind    Role
		account *platform.Account
	}{{RoleAuthor, candidate.Author}, {RoleMerger, candidate.Merger}} {
		if role.account == nil || role.account.ID == "" {
			continue
		}
		claims = append(claims, Claim{Role: role.kind, Kind: ClaimProviderUserID, Assurance: AssuranceVerified, Derivation: "provider_change", Provider: repository.Provider, Instance: repository.Instance, ProviderUserID: role.account.ID, AccountType: role.account.Type})
	}
	return claims
}

// GitClaims uses the fixed trailer grammar of analyzer version 1. The subject
// is never a trailer block; all lines in the final body paragraph must be
// trailers. It neither invokes Git's configurable trailer parser nor verifies
// authorship. A noreply-derived namespace does not strengthen assurance.
func GitClaims(author Signature, message []byte) []Claim {
	claims := make([]Claim, 0, 1)
	seen := make(map[string]bool)
	add := func(role Role, byline, email []byte) {
		normalized := platform.NormalizeGitEmail(string(email))
		if seen[normalized] || normalized == "noreply@github.com" {
			return
		}
		seen[normalized] = true
		claim := Claim{Role: role, Kind: ClaimGitEmail, Assurance: AssuranceUnverified, Derivation: "git", RawByline: platform.NewRawBytes(byline), RawEmail: platform.NewRawBytes(email), Email: platform.NewRawBytes([]byte(normalized))}
		if host, id, ok := github.ParseNoreply(normalized); ok {
			claim.Kind = ClaimProviderUserID
			claim.Provider = platform.KindGitHub
			claim.Instance = host
			claim.ProviderUserID = id
			claim.Derivation = "github_noreply_id"
		}
		claims = append(claims, claim)
	}
	add(RoleAuthor, author.Byline, author.Email)
	lines := bytes.Split(message, []byte{'\n'})
	end := len(lines)
	for end > 0 && len(trimASCII(lines[end-1])) == 0 {
		end--
	}
	start := end
	for start > 0 && len(trimASCII(lines[start-1])) != 0 {
		start--
	}
	if start == 0 {
		return claims
	}
	for _, line := range lines[start:end] {
		key, _, ok := bytes.Cut(line, []byte{':'})
		if !ok || len(key) == 0 {
			return claims
		}
		for _, ch := range key {
			if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '-' {
				return claims
			}
		}
	}
	for _, line := range lines[start:end] {
		byline, ok := bytes.CutPrefix(line, []byte("Co-authored-by: "))
		if !ok {
			continue
		}
		byline = trimASCII(byline)
		open := bytes.LastIndexByte(byline, '<')
		if open <= 0 || len(trimASCII(byline[:open])) == 0 || byline[len(byline)-1] != '>' {
			continue
		}
		add(RoleCoauthor, byline, byline[open+1:len(byline)-1])
	}
	return claims
}

func trimASCII(value []byte) []byte { return bytes.Trim(value, " \t\r\n\v\f") }

// DeclaredRevertCandidates parses intent markers only. The analyzer must still
// resolve each full object ID in the supplied repository before retaining it.
func DeclaredRevertCandidates(message []byte) []string {
	var targets []string
	seen := make(map[string]bool)
	for line := range bytes.SplitSeq(message, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		value, ok := bytes.CutPrefix(line, []byte("This reverts commit "))
		if !ok {
			continue
		}
		value, ok = bytes.CutSuffix(value, []byte{'.'})
		if !ok || !fullObjectID(string(value)) {
			continue
		}
		id := strings.ToLower(string(value))
		if !seen[id] {
			targets = append(targets, id)
			seen[id] = true
		}
	}
	return targets
}

func fullObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, ch := range []byte(value) {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

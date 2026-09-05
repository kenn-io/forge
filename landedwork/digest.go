package landedwork

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"go.kenn.io/forge/platform"
	"io"
	"slices"
	"strings"
)

// WriteCanonicalEvidence is the single versioned digest encoding. Inputs are
// already bounded evidence values; callers own the writer and its byte limit.
// Ordered source lists and spine ranges retain their order. Set-like report
// collections are sorted on copied slices, leaving caller data untouched.
func WriteCanonicalEvidence(writer io.Writer, request Request, result Result) error {
	request.Snapshot.Candidates = slices.Clone(request.Snapshot.Candidates)
	slices.SortFunc(request.Snapshot.Candidates, func(a, b platform.LandingCandidate) int { return cmp.Compare(a.ID, b.ID) })
	result.Digest = ""
	result.Landings = slices.Clone(result.Landings)
	slices.SortFunc(result.Landings, func(a, b Landing) int { return cmp.Compare(a.Terminal, b.Terminal) })
	for i := range result.Landings {
		landing := &result.Landings[i]
		landing.Claims = canonicalClaims(landing.Claims)
		landing.Introduced = canonicalCommits(landing.Introduced, true)
		landing.Sources = canonicalCommits(landing.Sources, false)
		landing.TerminalCommit = canonicalCommits([]CommitEvidence{landing.TerminalCommit}, false)[0]
		landing.Churn.Files = slices.Clone(landing.Churn.Files)
		slices.SortFunc(landing.Churn.Files, func(a, b FileChange) int {
			return cmp.Compare(a.OldPath.Base64+"\x00"+a.NewPath.Base64, b.OldPath.Base64+"\x00"+b.NewPath.Base64)
		})
		landing.Churn.Exclusions = slices.Clone(landing.Churn.Exclusions)
		slices.SortFunc(landing.Churn.Exclusions, func(a, b ExcludedFile) int {
			return cmp.Compare(a.Path.Base64+"\x00"+a.Side+"\x00"+a.Reason, b.Path.Base64+"\x00"+b.Side+"\x00"+b.Reason)
		})
	}
	result.Gaps = slices.Clone(result.Gaps)
	slices.SortFunc(result.Gaps, func(a, b Gap) int {
		return cmp.Compare(strings.Join([]string{a.FirstSHA, a.LastSHA, a.Reason, a.ChangeID}, "\x00"), strings.Join([]string{b.FirstSHA, b.LastSHA, b.Reason, b.ChangeID}, "\x00"))
	})
	result.Presence = slices.Clone(result.Presence)
	slices.SortFunc(result.Presence, func(a, b Presence) int { return cmp.Compare(a.ID, b.ID) })
	var byteFields []string
	rawEncoding := json.MarshalToFunc(func(encoder *jsontext.Encoder, value platform.RawBytes) error {
		decoded, err := value.Bytes()
		if err != nil {
			return err
		}
		index := len(byteFields)
		byteFields = append(byteFields, strings.Clone(value.Base64))
		return json.MarshalEncode(encoder, struct {
			Index  int `json:"byte_field"`
			Length int `json:"length"`
		}{index, len(decoded)})
	})
	if err := json.MarshalWrite(writer, struct {
		Encoding string  `json:"encoding"`
		Request  Request `json:"request"`
		Result   Result  `json:"result"`
	}{"forge-landed-canonical/1", request, result}, json.Deterministic(true), json.WithMarshalers(rawEncoding)); err != nil {
		return err
	}
	// A newline ends the canonical metadata object. Byte fields follow in
	// traversal order, each prefixed by an unsigned 64-bit big-endian length.
	// Hashes therefore consume original bytes, never their display/wire text.
	if _, err := io.WriteString(writer, "\n"); err != nil {
		return err
	}
	for _, field := range byteFields {
		decoded, err := (platform.RawBytes{Base64: field}).Bytes()
		if err != nil {
			return err
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(decoded)))
		if _, err := io.Copy(writer, bytes.NewReader(length[:])); err != nil {
			return err
		}
		if _, err := io.Copy(writer, bytes.NewReader(decoded)); err != nil {
			return err
		}
	}
	return nil
}

func canonicalClaims(claims []Claim) []Claim {
	claims = slices.Clone(claims)
	key := func(claim Claim) string {
		return strings.Join([]string{string(claim.Role), string(claim.Kind), string(claim.Assurance), claim.Derivation, string(claim.Provider), claim.Instance, claim.ProviderUserID, string(claim.AccountType), claim.RawByline.Base64, claim.RawEmail.Base64, claim.Email.Base64}, "\x00")
	}
	slices.SortFunc(claims, func(a, b Claim) int { return cmp.Compare(key(a), key(b)) })
	return claims
}

func canonicalCommits(commits []CommitEvidence, unordered bool) []CommitEvidence {
	commits = slices.Clone(commits)
	if unordered {
		slices.SortFunc(commits, func(a, b CommitEvidence) int { return cmp.Compare(a.ID, b.ID) })
	}
	for i := range commits {
		commits[i].Claims = canonicalClaims(commits[i].Claims)
		commits[i].DeclaredReverts = slices.Clone(commits[i].DeclaredReverts)
		slices.Sort(commits[i].DeclaredReverts)
	}
	return commits
}

func Digest(request Request, result Result) (string, error) {
	hash := sha256.New()
	if err := WriteCanonicalEvidence(hash, request, result); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

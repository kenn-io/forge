package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"go.kenn.io/forge/internal/tokenauth"
)

// prFreshnessBatchSize is how many pull requests one probe request asks
// about. GitHub charges one GraphQL point for a full batch.
const prFreshnessBatchSize = 25

// prFreshnessFields is what a probe compares between reads: everything the
// watched pass would otherwise refresh one pull request at a time. Comments
// come most recently updated first, so an edit or a moderation on a recent
// comment is in the page. The check counts change as checks start and finish.
const prFreshnessFields = `
  updatedAt
  state
  headRefOid
  mergeStateStatus
  comments(first: 100, orderBy: {field: UPDATED_AT, direction: DESC}) {
    totalCount
    nodes { updatedAt isMinimized minimizedReason }
  }
  reviews(last: 50) { totalCount nodes { updatedAt state } }
  reviewThreads(first: 100) { totalCount nodes { isResolved isOutdated } }
  commits(last: 1) {
    nodes {
      commit {
        oid
        statusCheckRollup {
          state
          contexts(first: 1) {
            totalCount
            checkRunCountsByState { state count }
            statusContextCountsByState { state count }
          }
        }
      }
    }
  }`

// PullRequestFreshnessRef names one pull request for a freshness probe.
type PullRequestFreshnessRef struct {
	Owner  string
	Name   string
	Number int
}

// FetchPullRequestFreshness reads a fingerprint of each pull request, up to
// prFreshnessBatchSize per GraphQL request. Equal fingerprints mean nothing
// the watched pass refreshes has changed. A pull request is absent from the
// result when GitHub could not resolve it or its mergeability is still being
// computed, so the caller must read it in full.
func (g *GraphQLFetcher) FetchPullRequestFreshness(
	ctx context.Context, refs []PullRequestFreshnessRef,
) (map[PullRequestFreshnessRef]string, error) {
	if g == nil || g.httpClient == nil {
		return nil, errors.ErrUnsupported
	}
	// Credentials route by owner, so each request covers one owner.
	byOwner := make(map[string][]PullRequestFreshnessRef)
	var owners []string
	for _, ref := range refs {
		owner := strings.ToLower(ref.Owner)
		if _, ok := byOwner[owner]; !ok {
			owners = append(owners, owner)
		}
		byOwner[owner] = append(byOwner[owner], ref)
	}
	out := make(map[PullRequestFreshnessRef]string, len(refs))
	for _, owner := range owners {
		group := byOwner[owner]
		ownerCtx := tokenauth.WithGitHubOwner(ctx, group[0].Owner)
		for start := 0; start < len(group); start += prFreshnessBatchSize {
			batch := group[start:min(start+prFreshnessBatchSize, len(group))]
			if err := g.fetchPullRequestFreshnessBatch(ownerCtx, batch, out); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

func (g *GraphQLFetcher) fetchPullRequestFreshnessBatch(
	ctx context.Context, batch []PullRequestFreshnessRef, out map[PullRequestFreshnessRef]string,
) error {
	query, variables := prFreshnessQuery(batch)
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("encode pull request freshness query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build pull request freshness request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe pull request freshness: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read pull request freshness response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe pull request freshness: HTTP %d", resp.StatusCode)
	}
	var decoded struct {
		Data   map[string]jsontext.Value `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("decode pull request freshness response: %w", err)
	}
	for _, gqlErr := range decoded.Errors {
		// NOT_FOUND fails only its own alias; anything else, such as a
		// rate limit or a schema the host rejects, fails the whole batch.
		if gqlErr.Type != "NOT_FOUND" {
			return fmt.Errorf("probe pull request freshness: %s", gqlErr.Message)
		}
	}
	for i, ref := range batch {
		if fingerprint, ok := prFreshnessFingerprint(decoded.Data["pr"+strconv.Itoa(i)]); ok {
			out[ref] = fingerprint
		}
	}
	return nil
}

func prFreshnessQuery(batch []PullRequestFreshnessRef) (string, map[string]any) {
	var params, fields strings.Builder
	variables := make(map[string]any, 3*len(batch))
	for i, ref := range batch {
		n := strconv.Itoa(i)
		if i > 0 {
			params.WriteString(", ")
		}
		fmt.Fprintf(&params, "$o%[1]s: String!, $n%[1]s: String!, $p%[1]s: Int!", n)
		fmt.Fprintf(&fields,
			"  pr%[1]s: repository(owner: $o%[1]s, name: $n%[1]s) { pullRequest(number: $p%[1]s) { ...Freshness } }\n", n)
		variables["o"+n] = ref.Owner
		variables["n"+n] = ref.Name
		variables["p"+n] = ref.Number
	}
	query := "query(" + params.String() + ") {\n" + fields.String() + "}\n" +
		"fragment Freshness on PullRequest {" + prFreshnessFields + "\n}\n"
	return query, variables
}

// prFreshnessFingerprint hashes one alias's pull request in canonical JSON,
// so the same state always hashes the same.
func prFreshnessFingerprint(alias jsontext.Value) (string, bool) {
	var repository struct {
		PullRequest jsontext.Value `json:"pullRequest"`
	}
	if len(alias) == 0 || json.Unmarshal(alias, &repository) != nil ||
		len(repository.PullRequest) == 0 || string(repository.PullRequest) == "null" {
		return "", false
	}
	var pr struct {
		MergeStateStatus string `json:"mergeStateStatus"`
	}
	if json.Unmarshal(repository.PullRequest, &pr) != nil {
		return "", false
	}
	// GitHub is still computing mergeability, which can resolve without
	// any other field changing. Unknown mergeability stays due for a read.
	if pr.MergeStateStatus == "" || pr.MergeStateStatus == "UNKNOWN" {
		return "", false
	}
	canonical := repository.PullRequest.Clone()
	if canonical.Canonicalize() != nil {
		return "", false
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), true
}

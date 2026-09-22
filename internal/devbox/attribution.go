package devbox

import (
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"go.kenn.io/forge/internal/tokenauth"
)

type PushState struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	OID        string `json:"oid"`
	Pushed     bool   `json:"pushed"`
}

type Attribution struct {
	PushState
	Status               string `json:"status" enum:"matched,preserved_author,mismatch,unverified"`
	Message              string `json:"message"`
	ExpectedGitHubUserID int64  `json:"expected_github_user_id"`
	AuthorID             int64  `json:"author_id"`
	CommitterID          int64  `json:"committer_id"`
	AuthorName           string `json:"author_name"`
	AuthorEmail          string `json:"author_email"`
	CommitterName        string `json:"committer_name"`
	CommitterEmail       string `json:"committer_email"`
}

type attributionCacheEntry struct {
	result Attribution
	until  time.Time
}

// CheckAttribution runs on the controller. Neither its provider token nor the
// GitHub client is made available to the worker. A failed check never repeats a push.
func (c *Connections) CheckAttribution(ctx context.Context, id string, state PushState, source tokenauth.Source) Attribution {
	result := Attribution{PushState: state, Status: "unverified", Message: "Push succeeded; GitHub attribution has not been verified."}
	item, err := c.lookup(id)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	result.ExpectedGitHubUserID = item.Profile.GitHubUserID
	owner, name, err := ParseRepository(state.Repository)
	if err != nil || state.Branch == "" || !state.Pushed {
		return result
	}
	oid, err := hex.DecodeString(state.OID)
	if err != nil || len(oid) != 20 {
		return result
	}
	key := id + "/" + state.Repository + "/" + state.Branch + "/" + state.OID
	c.mu.RLock()
	cached, ok := c.attribution[key]
	c.mu.RUnlock()
	if ok && time.Now().Before(cached.until) {
		return cached.result
	}
	if source == nil {
		result.Message = "Push succeeded; this controller has no GitHub read credential to check attribution."
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	token, err := source.Token(tokenauth.WithGitHubOwner(ctx, owner))
	if err != nil {
		result.Message = "Push succeeded; the controller's GitHub read credential is unavailable."
		return result
	}
	// Use a complete external URL so the API contract checker cannot confuse
	// a GitHub path prefix with Forge's repository routes.
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", url.PathEscape(owner), url.PathEscape(name), url.PathEscape(state.Branch))
	request, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return result
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := c.client.Do(request)
	if err != nil {
		result.Message = "Push succeeded; GitHub is unreachable for attribution verification."
		return result
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		result.Message = "Push succeeded; GitHub did not return the branch commit for verification."
		return result
	}
	var commit struct {
		SHA    string `json:"sha"`
		Author *struct {
			ID int64 `json:"id"`
		} `json:"author"`
		Committer *struct {
			ID int64 `json:"id"`
		} `json:"committer"`
		Commit struct {
			Author struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"author"`
			Committer struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := json.UnmarshalRead(io.LimitReader(response.Body, 1<<20), &commit); err != nil {
		return result
	}
	if commit.SHA != state.OID {
		result.Message = "The branch head on GitHub differs from this workspace; refresh before checking attribution."
		return result
	}
	if commit.Author != nil {
		result.AuthorID = commit.Author.ID
	}
	if commit.Committer != nil {
		result.CommitterID = commit.Committer.ID
	}
	result.AuthorName, result.AuthorEmail = commit.Commit.Author.Name, commit.Commit.Author.Email
	result.CommitterName, result.CommitterEmail = commit.Commit.Committer.Name, commit.Commit.Committer.Email
	switch {
	case result.CommitterID != result.ExpectedGitHubUserID:
		result.Status, result.Message = "mismatch", "GitHub did not resolve the committer to this devbox's developer. Check the commit identity before continuing."
	case result.AuthorID == result.ExpectedGitHubUserID:
		result.Status, result.Message = "matched", "GitHub confirms your author and committer identity."
	case result.AuthorID == 0:
		result.Status, result.Message = "mismatch", "GitHub confirms your committer identity but could not resolve the author. Check the author email."
	default:
		result.Status, result.Message = "preserved_author", "GitHub confirms your committer identity; this commit preserves another author's identity."
	}
	c.mu.Lock()
	if len(c.attribution) >= 256 {
		clear(c.attribution)
	}
	if c.attribution == nil {
		c.attribution = make(map[string]attributionCacheEntry)
	}
	c.attribution[key] = attributionCacheEntry{result: result, until: time.Now().Add(10 * time.Minute)}
	c.mu.Unlock()
	return result
}

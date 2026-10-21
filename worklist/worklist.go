// Package worklist defines the ranked list of pull requests and issues an
// agent writes for a maintainer, and the link that carries one into Forge.
// The MCP tool kenn_forge_put_worklist and worklist links share these types,
// so a list another tool builds here is the list Forge accepts.
package worklist

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"go.kenn.io/forge/platform"
)

// Limits Forge enforces on every worklist write, counted in characters.
const (
	MaxTitle   = 200
	MaxRecipe  = 10000
	MaxItems   = 200
	MaxSection = 80
	MaxText    = 4000
)

// ItemRef names a pull request or issue. Owner and name are the repository's
// last known route. Forge MCP writes also carry the provider's stable
// repository ID; a link may omit it, and Forge then finds the repository by
// provider, host, owner, and name.
type ItemRef struct {
	Type                    string `json:"type" jsonschema:"item type: pr or issue"`
	Provider                string `json:"provider"`
	PlatformHost            string `json:"platform_host,omitzero"`
	PlatformRepoID          int64  `json:"platform_repo_id,omitzero" jsonschema:"provider's integer repository ID from kenn_forge_list_repos; required unless bitbucket_repository_uuid is set"`
	BitbucketRepositoryUUID string `json:"bitbucket_repository_uuid,omitzero" jsonschema:"Bitbucket Cloud repository UUID from kenn_forge_list_repos; set instead of platform_repo_id for Bitbucket Cloud repositories"`
	Owner                   string `json:"owner"`
	Name                    string `json:"name"`
	Number                  int    `json:"number"`
}

// Item is one ranked entry. A list's item order is its rank.
type Item struct {
	Item    ItemRef `json:"item"`
	Score   *int    `json:"score,omitzero" jsonschema:"importance from 1 to 10; omit when unscored"`
	Section string  `json:"section,omitzero" jsonschema:"group label such as Ready or Needs review; readiness belongs here, not in the score"`
	Note    string  `json:"note,omitzero" jsonschema:"why the item is on the list"`
	Caveat  string  `json:"caveat,omitzero" jsonschema:"what the maintainer should check before acting"`
}

// List is a worklist's title, recipe, and items in rank order.
type List struct {
	Title     string `json:"title"`
	Recipe    string `json:"recipe" jsonschema:"the criteria in plain text and the searches you ran, so a later agent can refresh or copy the list"`
	CreatedBy string `json:"created_by,omitzero" jsonschema:"short label for the agent writing the list"`
	Items     []Item `json:"items" jsonschema:"items in rank order, 1 to 200"`
}

// FieldError names the field of a list that breaks a limit, such as
// "items[3].score".
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string {
	return e.Field + ": " + e.Message
}

// Validate checks the list against Forge's limits. It does not check that
// items exist or that each appears once; Forge checks both when it writes the
// list, because only it knows which repositories two references name.
func (l List) Validate() error {
	if strings.TrimSpace(l.Title) == "" {
		return &FieldError{"title", "title is required"}
	}
	if utf8.RuneCountInString(l.Title) > MaxTitle {
		return &FieldError{"title", fmt.Sprintf("title must be at most %d characters", MaxTitle)}
	}
	if utf8.RuneCountInString(l.Recipe) > MaxRecipe {
		return &FieldError{"recipe", fmt.Sprintf("recipe must be at most %d characters", MaxRecipe)}
	}
	if len(l.Items) == 0 || len(l.Items) > MaxItems {
		return &FieldError{"items", fmt.Sprintf("items must contain 1 to %d entries", MaxItems)}
	}
	for i, item := range l.Items {
		field := fmt.Sprintf("items[%d]", i)
		if err := item.Item.validate(); err != nil {
			return &FieldError{field + ".item", err.Error()}
		}
		if item.Score != nil && (*item.Score < 1 || *item.Score > 10) {
			return &FieldError{field + ".score", "score must be from 1 to 10"}
		}
		if utf8.RuneCountInString(item.Section) > MaxSection {
			return &FieldError{field + ".section", fmt.Sprintf("section must be at most %d characters", MaxSection)}
		}
		if utf8.RuneCountInString(item.Note) > MaxText {
			return &FieldError{field + ".note", fmt.Sprintf("note must be at most %d characters", MaxText)}
		}
		if utf8.RuneCountInString(item.Caveat) > MaxText {
			return &FieldError{field + ".caveat", fmt.Sprintf("caveat must be at most %d characters", MaxText)}
		}
	}
	return nil
}

// RepositoryKey returns the provider's stable repository key the reference
// carries, or the zero key when it names the repository by route only.
func (r ItemRef) RepositoryKey() (platform.RepositoryKey, error) {
	repositoryUUID, err := platform.ParseRepositoryUUID(r.BitbucketRepositoryUUID)
	if err != nil {
		return platform.RepositoryKey{}, errors.New("bitbucket_repository_uuid must be a UUID")
	}
	return platform.RepositoryKeyFromWire(r.PlatformRepoID, repositoryUUID)
}

func (r ItemRef) validate() error {
	if _, err := r.RepositoryKey(); err != nil {
		return err
	}
	switch {
	case r.Type != "pr" && r.Type != "issue":
		return errors.New("item type must be pr or issue")
	case r.Provider == "":
		return errors.New("provider is required")
	case r.Owner == "":
		return errors.New("owner is required")
	case r.Name == "":
		return errors.New("name is required")
	case r.Number <= 0:
		return errors.New("number must be greater than zero")
	}
	return nil
}

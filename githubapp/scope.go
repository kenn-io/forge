package githubapp

import (
	"encoding/json/v2"
	"errors"
	"maps"
	"slices"
)

// TokenScope distinguishes a deliberately unrestricted installation grant
// from a nonempty repository selection. Nil permissions retain the grant's
// permissions; a non-nil map requests exactly those permissions.
type TokenScope struct {
	AllRepositories bool
	RepositoryIDs   []int64
	Permissions     map[string]string
}

func (s TokenScope) request() (tokenRequest, error) {
	// GitHub's installation-token endpoint accepts at most 500 selected IDs.
	if len(s.RepositoryIDs) > 500 {
		return tokenRequest{}, errors.New("installation token scope exceeds 500 repositories")
	}
	if s.AllRepositories == (len(s.RepositoryIDs) != 0) {
		return tokenRequest{}, errors.New("choose all granted repositories or a nonempty repository selection")
	}
	ids := slices.Clone(s.RepositoryIDs)
	slices.Sort(ids)
	for i, id := range ids {
		if id <= 0 || (i > 0 && ids[i-1] == id) {
			return tokenRequest{}, errors.New("repository IDs must be positive and unique")
		}
	}
	return tokenRequest{RepositoryIDs: ids, Permissions: maps.Clone(s.Permissions)}, nil
}

type tokenRequest struct {
	RepositoryIDs []int64           `json:"repository_ids,omitempty"`
	Permissions   map[string]string `json:"permissions,omitzero"`
}

func (s TokenScope) cacheKey() (string, error) {
	request, err := s.request()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(request, json.Deterministic(true))
	return string(encoded), err
}

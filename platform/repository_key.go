package platform

import (
	"errors"
	"strconv"
	"strings"
	"uuid"
)

// RepositoryKey is a repository's stable provider key. GitHub, GitLab,
// Forgejo, Gitea, and Bitbucket Data Center assign a positive integer
// repository ID once per host and keep it across renames and transfers.
// Bitbucket Cloud has no integer ID; its key is the repository UUID. A key
// holds exactly one of the two, and the zero value is the key of a
// repository the provider has not verified. Compare keys with ==.
//
// The fields are unexported so no caller can read or compare half of a key.
// Flat platform_repo_id and bitbucket_repository_uuid values exist only in
// encodings (SQL columns, API bodies, stored or federated JSON, config);
// convert them at that boundary with RepositoryKeyFromWire and Wire.
type RepositoryKey struct {
	id   int64
	uuid uuid.UUID
}

// RepositoryIDKey returns the key for an integer repository ID. A zero or
// negative ID yields the zero key.
func RepositoryIDKey(id int64) RepositoryKey {
	if id <= 0 {
		return RepositoryKey{}
	}
	return RepositoryKey{id: id}
}

// RepositoryUUIDKey returns the key for a Bitbucket Cloud repository UUID.
func RepositoryUUIDKey(id uuid.UUID) RepositoryKey {
	return RepositoryKey{uuid: id}
}

// RepositoryKeyFromWire decodes the flat encoding. Both values empty is the
// zero key; a negative ID or both values set is an error.
func RepositoryKeyFromWire(id int64, repositoryUUID uuid.UUID) (RepositoryKey, error) {
	switch {
	case id < 0:
		return RepositoryKey{}, errors.New("repository ID must be positive")
	case id > 0 && repositoryUUID != uuid.Nil():
		return RepositoryKey{}, errors.New("repository key has both an integer ID and a UUID")
	case id > 0:
		return RepositoryKey{id: id}, nil
	default:
		return RepositoryKey{uuid: repositoryUUID}, nil
	}
}

// ParseRepositoryUUID parses a Bitbucket Cloud repository UUID given as text,
// with or without the braces Bitbucket writes around it. Empty text is the
// nil UUID.
func ParseRepositoryUUID(text string) (uuid.UUID, error) {
	text = strings.Trim(strings.TrimSpace(text), "{}")
	if text == "" {
		return uuid.Nil(), nil
	}
	return uuid.Parse(text)
}

// Wire returns the flat encoding: the integer ID (0 for a UUID key) and the
// UUID (nil for an integer key).
func (k RepositoryKey) Wire() (id int64, repositoryUUID uuid.UUID) {
	return k.id, k.uuid
}

// ID returns the integer repository ID of an integer key. Provider adapters
// whose APIs address repositories by ID use it; ok is false for a zero or
// UUID key.
func (k RepositoryKey) ID() (id int64, ok bool) {
	return k.id, k.id > 0
}

// UUID returns the Bitbucket Cloud UUID of a UUID key; ok is false for a
// zero or integer key.
func (k RepositoryKey) UUID() (id uuid.UUID, ok bool) {
	return k.uuid, k.uuid != uuid.Nil()
}

// IsZero reports whether the key is unverified.
func (k RepositoryKey) IsZero() bool {
	return k == RepositoryKey{}
}

// IsUUID reports whether the key is a Bitbucket Cloud UUID.
func (k RepositoryKey) IsUUID() bool {
	return k.uuid != uuid.Nil()
}

// String formats the key for logs, errors, and map keys: the decimal ID, or
// the UUID in braces as Bitbucket Cloud writes it. The zero key is "".
func (k RepositoryKey) String() string {
	switch {
	case k.uuid != uuid.Nil():
		return "{" + k.uuid.String() + "}"
	case k.id > 0:
		return strconv.FormatInt(k.id, 10)
	default:
		return ""
	}
}

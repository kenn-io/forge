package db

import (
	"fmt"
	"uuid"

	"go.kenn.io/forge/platform"
)

// repositoryKeyArgs is the SQLite encoding of a repository key: the
// platform_repo_id and bitbucket_repository_uuid column values. The nil UUID
// is stored as empty text so it stays outside the partial unique index.
func repositoryKeyArgs(key platform.RepositoryKey) (int64, string) {
	id, repositoryUUID := key.Wire()
	if repositoryUUID == uuid.Nil() {
		return id, ""
	}
	return id, repositoryUUID.String()
}

// repositoryKeyColumns returns scan destinations for the platform_repo_id
// and bitbucket_repository_uuid columns, in that order. After both columns
// scan, dest holds the decoded key.
func repositoryKeyColumns(dest *platform.RepositoryKey) (id, repositoryUUID any) {
	scan := &repositoryKeyScan{dest: dest}
	return repositoryKeyIDColumn{scan}, repositoryKeyUUIDColumn{scan}
}

type repositoryKeyScan struct {
	dest    *platform.RepositoryKey
	id      int64
	uuid    uuid.UUID
	scanned int
}

func (s *repositoryKeyScan) done() error {
	s.scanned++
	if s.scanned < 2 {
		return nil
	}
	key, err := platform.RepositoryKeyFromWire(s.id, s.uuid)
	if err != nil {
		return fmt.Errorf("repository key: %w", err)
	}
	*s.dest = key
	return nil
}

type repositoryKeyIDColumn struct{ scan *repositoryKeyScan }

func (c repositoryKeyIDColumn) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		c.scan.id = 0
	case int64:
		c.scan.id = v
	default:
		return fmt.Errorf("platform_repo_id: %T", value)
	}
	return c.scan.done()
}

type repositoryKeyUUIDColumn struct{ scan *repositoryKeyScan }

func (c repositoryKeyUUIDColumn) Scan(value any) error {
	var text string
	switch v := value.(type) {
	case nil:
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("bitbucket_repository_uuid: %T", value)
	}
	c.scan.uuid = uuid.Nil()
	if text != "" {
		id, err := uuid.Parse(text)
		if err != nil {
			return fmt.Errorf("bitbucket_repository_uuid: %w", err)
		}
		c.scan.uuid = id
	}
	return c.scan.done()
}

// repositoryKeyCondition matches alias's repository key columns against key
// and appends the arguments. Both columns are compared so an integer key
// never matches a UUID row, or the reverse.
func repositoryKeyCondition(alias string, key platform.RepositoryKey, args *[]any) string {
	id, repositoryUUID := repositoryKeyArgs(key)
	*args = append(*args, id, repositoryUUID)
	if alias != "" {
		alias += "."
	}
	return alias + "platform_repo_id = ? AND " + alias + "bitbucket_repository_uuid = ?"
}

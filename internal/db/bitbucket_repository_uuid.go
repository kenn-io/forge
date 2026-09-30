package db

import (
	"fmt"
	"uuid"
)

// bitbucketRepositoryUUIDText is the SQLite form of a Bitbucket Cloud
// repository UUID. The nil UUID is stored as empty text so it stays outside
// the partial unique index; uuid.String() would otherwise store the nil UUID.
func bitbucketRepositoryUUIDText(id uuid.UUID) string {
	if id == uuid.Nil() {
		return ""
	}
	return id.String()
}

func parseBitbucketRepositoryUUID(text string) (uuid.UUID, error) {
	if text == "" {
		return uuid.Nil(), nil
	}
	id, err := uuid.Parse(text)
	if err != nil {
		return uuid.Nil(), fmt.Errorf("bitbucket repository uuid: %w", err)
	}
	return id, nil
}

// bitbucketUUIDScanner scans the TEXT column into a uuid.UUID.
type bitbucketUUIDScanner struct {
	dest *uuid.UUID
}

func (s bitbucketUUIDScanner) Scan(value any) error {
	var text string
	switch v := value.(type) {
	case nil:
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("bitbucket repository uuid: %T", value)
	}
	id, err := parseBitbucketRepositoryUUID(text)
	if err != nil {
		return err
	}
	*s.dest = id
	return nil
}

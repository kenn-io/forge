package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// List validators share forge_http_etags with item validators. Their
// resource type carries the list and its query, and resource_number is 0.
const (
	listETagPrefix         = "list:"
	listDetectorETagPrefix = "list-detector:"
)

// RepoListETag is one saved validator for a repository list, such as its
// open pull requests.
type RepoListETag struct {
	PlatformHost string
	Owner        string
	Name         string
	// Resource names the list and its query, such as
	// "pulls?per_page=100&state=open".
	Resource string
	ETag     string
	// Detector marks the validator of a paginated list's most recently
	// updated item rather than of the list's first page.
	Detector bool
	CachedAt time.Time
}

// ListRepoListETags returns every saved list validator for a platform.
func (d *DB) ListRepoListETags(ctx context.Context, platform string) ([]RepoListETag, error) {
	rows, err := d.roQueryContext(ctx,
		`SELECT platform_host, owner_key, name_key, resource_type, etag, fetched_at
		FROM forge_http_etags
		WHERE platform = ? AND resource_number = 0
		  AND (resource_type LIKE 'list:%' OR resource_type LIKE 'list-detector:%')`,
		canonicalRepoPlatform(platform),
	)
	if err != nil {
		return nil, fmt.Errorf("list repository list etags: %w", err)
	}
	defer rows.Close()
	var out []RepoListETag
	for rows.Next() {
		var etag RepoListETag
		var resourceType string
		if err := rows.Scan(
			&etag.PlatformHost, &etag.Owner, &etag.Name, &resourceType, &etag.ETag, &etag.CachedAt,
		); err != nil {
			return nil, fmt.Errorf("scan repository list etag: %w", err)
		}
		if resource, ok := strings.CutPrefix(resourceType, listDetectorETagPrefix); ok {
			etag.Resource, etag.Detector = resource, true
		} else {
			etag.Resource = strings.TrimPrefix(resourceType, listETagPrefix)
		}
		out = append(out, etag)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list repository list etags: %w", err)
	}
	return out, nil
}

// ReplaceRepoListETags replaces one repository's saved list validators.
func (d *DB) ReplaceRepoListETags(
	ctx context.Context, platform, platformHost, owner, name string, etags []RepoListETag,
) error {
	platform = canonicalRepoPlatform(platform)
	platformHost, owner, name = canonicalRepoLookupIdentifier(platformHost, owner, name)
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM forge_http_etags
			WHERE platform = ? AND platform_host = ? AND owner_key = ? AND name_key = ?
			  AND resource_number = 0
			  AND (resource_type LIKE 'list:%' OR resource_type LIKE 'list-detector:%')`,
			platform, platformHost, owner, name,
		); err != nil {
			return fmt.Errorf("clear repository list etags: %w", err)
		}
		for _, etag := range etags {
			resourceType := listETagPrefix + etag.Resource
			if etag.Detector {
				resourceType = listDetectorETagPrefix + etag.Resource
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO forge_http_etags (
					platform, platform_host, owner_key, name_key,
					resource_type, resource_number, etag, fetched_at
				) VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
				platform, platformHost, owner, name, resourceType, etag.ETag, etag.CachedAt.UTC(),
			); err != nil {
				return fmt.Errorf("save repository list etag: %w", err)
			}
		}
		return nil
	})
}

package db

import (
	"database/sql/driver"

	"modernc.org/sqlite"

	"go.kenn.io/forge/platform"
)

// headRepoKindSQLFunction is platform.ClassifyHeadRepo as a SQL function, so From fork filters
// in SQL with the classifier the API reports:
// forge_head_repo_kind(platform, platform_host, owner, name, head_repo_clone_url).
const headRepoKindSQLFunction = "forge_head_repo_kind"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(headRepoKindSQLFunction, 5,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			text := make([]string, len(args))
			for i, arg := range args {
				switch value := arg.(type) {
				case string:
					text[i] = value
				case []byte:
					text[i] = string(value)
				}
			}
			return string(platform.ClassifyHeadRepo(text[0], text[1], text[2], text[3], text[4])), nil
		})
}

// mergeRequestIsForkSQL matches a merge request aliased p whose head branch lives in a fork. It
// classifies at query time against the repository's current identity, so a rename never leaves
// a stale answer and no stored column needs a migration.
const mergeRequestIsForkSQL = `(SELECT ` + headRepoKindSQLFunction + `(r.platform, r.platform_host, r.owner, r.name, p.head_repo_clone_url)
	FROM forge_repos r WHERE r.id = p.repo_id) = 'fork'`

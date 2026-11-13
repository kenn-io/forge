package db

import (
	"database/sql/driver"

	"golang.org/x/text/cases"
	"modernc.org/sqlite"
)

// casefoldSQLFunction folds a string's case with full Unicode case folding, so names that
// differ only in case compare equal: forge_casefold('Équipe') = forge_casefold('ÉQUIPE') and
// forge_casefold('Straße') = forge_casefold('STRASSE'). SQLite's lower() folds only ASCII.
const casefoldSQLFunction = "forge_casefold"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(casefoldSQLFunction, 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			switch value := args[0].(type) {
			case string:
				return cases.Fold().String(value), nil
			case []byte:
				return cases.Fold().String(string(value)), nil
			default:
				return value, nil
			}
		})
}

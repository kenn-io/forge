package platform

import (
	"context"
)

// AccountType is a provider observation, independent of a claim's assurance.
type AccountType string

const (
	AccountUser         AccountType = "user"
	AccountBot          AccountType = "bot"
	AccountOrganization AccountType = "organization"
	AccountUnknown      AccountType = "unknown"
)

// Account identifies an account in its provider instance's REST ID namespace.
// Login and DisplayName are mutable observations, never identity keys.
type Account struct {
	ID          string      `json:"id"`
	Login       string      `json:"login"`
	DisplayName string      `json:"display_name"`
	Type        AccountType `json:"type" enum:"user,bot,organization,unknown"`
}

// AccountReader resolves exact logins and immutable IDs without fuzzy matching.
type AccountReader interface {
	LookupAccount(context.Context, string, Budget) (Account, error)
	GetAccount(context.Context, string, Budget) (Account, error)
}

// AccountResult accounts for the normalized output before publishing a lookup.
func AccountResult(account Account, meter *Meter) (Account, error) {
	if err := meter.CheckOutput(account); err != nil {
		return Account{}, err
	}
	return account, nil
}

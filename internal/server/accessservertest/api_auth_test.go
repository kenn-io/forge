package accessservertest

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/authapi"
)

// TestRedactedQueryMasksBootstrapToken pins the log-redaction
// contract: bootstrap tokens and login tickets never appear in the
// logged query.
func TestRedactedQueryMasksBootstrapToken(t *testing.T) {
	for _, test := range []struct {
		raw      string
		expected string
	}{
		{raw: "auth_token=secret&tab=pulls", expected: "auth_token=REDACTED&tab=pulls"},
		{raw: "login_ticket=secret&tab=pulls", expected: "login_ticket=REDACTED&tab=pulls"},
		{raw: "login_ticket=secret;tab=pulls", expected: "REDACTED"},
		{raw: "tab=pulls", expected: "tab=pulls"},
	} {
		u, err := url.Parse("/?" + test.raw)
		require.NoError(t, err)
		assert.Equal(t, test.expected, authapi.RedactedQuery(u), test.raw)
	}
}

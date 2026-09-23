package issueservertest

import (
	"go.kenn.io/forge/internal/server/streamapi"
)

func init() {
	streamapi.AllowUnvalidatedConfigHostCheckFallbackForTests = true
}

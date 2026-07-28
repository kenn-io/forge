package docs

import (
	"os"
	"testing"

	"go.kenn.io/middleman/internal/testutil/gitsafe"
)

func TestMain(m *testing.M) {
	os.Exit(gitsafe.RunIsolatedMain(m))
}

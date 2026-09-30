package settingstest

import (
	"os"
	"testing"

	"go.kenn.io/forge/internal/testutil/gitsafe"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m, func() int { return gitsafe.RunIsolatedMain(m) }))
}

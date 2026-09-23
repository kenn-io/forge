package accesstest

import (
	"os"
	"testing"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m))
}

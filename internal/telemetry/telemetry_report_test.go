//go:build !kit_posthog_disabled

package telemetry

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/kit/telemetry/posthog"
)

func TestReporterReportOutcomes(t *testing.T) {
	initFailure := os.Getenv("FORGE_TELEMETRY_INIT_FAILURE") == "1"
	for _, mode := range []string{"enabled", "opted out", "init failure", "nil reporter", "validation failure"} {
		if initFailure && mode != "init failure" {
			continue
		}
		t.Run(mode, func(t *testing.T) {
			if mode == "init failure" && !initFailure {
				executable, err := os.Executable()
				require.NoError(t, err)
				cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestReporterReportOutcomes$")
				cmd.Env = append(os.Environ(), "FORGE_TELEMETRY_INIT_FAILURE=1")
				output, err := cmd.CombinedOutput()
				require.NoError(t, err, string(output))
				return
			}
			t.Setenv(EnabledEnv, "1")
			t.Setenv("KENN_FORGE_TELEMETRY_ENABLED", "1")
			require.False(t, posthog.ProcessDisabled())
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer endpoint.Close()
			swapKitReporter(t, func(opts posthog.Options, options ...posthog.Option) (Client, error) {
				opts.Endpoint = endpoint.URL
				return posthog.NewReporter(opts, options...)
			})
			validationErr := errors.New("allowlist construction failed")
			var reporter *Reporter
			switch mode {
			case "enabled":
				reporter = NewReporterOrDisabledForTest(Options{Database: dbtest.Open(t), DailyClaimsPath: filepath.Join(t.TempDir(), "daily.json")})
			case "opted out":
				t.Setenv(EnabledEnv, "0")
				reporter = NewReporterOrDisabledForTest(Options{})
			case "init failure":
				reporter = NewReporterOrDisabledForTest(Options{})
			case "validation failure":
				reporter = &Reporter{err: validationErr}
			}
			t.Cleanup(func() { require.NoError(t, reporter.Close()) })
			assert.Equal(t, mode == "enabled", reporter.Enabled())
			status, err := reporter.Report(t.Context(), "session_ended", map[string]any{"duration_bucket": "under_1m"})
			if mode == "validation failure" {
				require.ErrorIs(t, err, validationErr)
			} else {
				require.NoError(t, err)
				want := posthog.StatusDisabled
				if mode == "enabled" {
					want = posthog.StatusQueued
				}
				assert.Equal(t, want, status)
			}
			assert.Equal(t, mode == "init failure", posthog.ProcessDisabled())
		})
	}
}

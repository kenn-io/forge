//go:build !kit_posthog_disabled

package telemetry

import "time"

var SwapKitReporterForTest = swapKitReporter

func NewReporterOrDisabledForTest(opts Options) *Reporter {
	return reporterOrDisabled(newReporter(opts, time.Now()))
}

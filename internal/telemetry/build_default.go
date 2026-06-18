//go:build !kit_posthog_disabled

package telemetry

func enabledInBuild() bool {
	return true
}

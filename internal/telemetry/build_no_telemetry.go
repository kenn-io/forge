//go:build middleman_no_telemetry

package telemetry

func enabledInBuild() bool {
	return false
}

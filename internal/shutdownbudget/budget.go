package shutdownbudget

import "time"

const (
	OpenTelemetry     = 5 * time.Second
	BackgroundLoops   = 5 * time.Second
	MCPHTTP           = 5 * time.Second
	PrimaryHTTP       = 10 * time.Second
	Syncer            = 30 * time.Second
	Profiler          = 5 * time.Second
	TelemetryReporter = 6 * time.Second // 900 ms setup check plus two 2 s reporter closes
	MCPStore          = time.Minute
	Database          = 5 * time.Second
	ProcessExitMargin = 5 * time.Second

	// Total covers every bounded shutdown phase plus process exit.
	Total = OpenTelemetry + BackgroundLoops + MCPHTTP + PrimaryHTTP + Syncer +
		Profiler + TelemetryReporter + MCPStore + Database + ProcessExitMargin
)

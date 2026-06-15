package localruntime

type LaunchTargetKind string

const (
	LaunchTargetAgent      LaunchTargetKind = "agent"
	LaunchTargetShell      LaunchTargetKind = "shell"
	LaunchTargetPlainShell LaunchTargetKind = "plain_shell"
	// LaunchTargetCommand marks sessions launched from a caller-supplied
	// command line via EnsureCommandSession rather than a configured target.
	LaunchTargetCommand LaunchTargetKind = "command"
)

type LaunchTarget struct {
	Key            string           `json:"key"`
	Label          string           `json:"label"`
	Kind           LaunchTargetKind `json:"kind"`
	Source         string           `json:"source"`
	Command        []string         `json:"command,omitempty"`
	Available      bool             `json:"available"`
	DisabledReason string           `json:"disabled_reason,omitempty"`
}

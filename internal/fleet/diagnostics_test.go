package fleet

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func allDeps() DependencyCapabilities {
	return DependencyCapabilities{
		Git: true, Gh: true, Tmux: true,
	}
}

func fullCaps() Capabilities {
	return Capabilities{Dependencies: allDeps()}
}

func TestDiagnostics(t *testing.T) {
	tests := []struct {
		name         string
		caps         Capabilities
		platformAuth *bool
		wantCodes    []string
		wantBlocks   map[string][]string
		wantSeverity map[string]string
	}{
		{
			name:         "fully capable host",
			caps:         fullCaps(),
			platformAuth: new(true),
			wantCodes:    nil,
		},
		{
			name:         "gh present, auth unknown (nil) emits no warning",
			caps:         fullCaps(),
			platformAuth: nil,
			wantCodes:    nil,
		},
		{
			name: "missing git",
			caps: Capabilities{
				Dependencies: DependencyCapabilities{
					Git: false, Gh: true, Tmux: true,
				},
			},
			platformAuth: new(true),
			wantCodes:    []string{"missingGit"},
			wantBlocks: map[string][]string{
				"missingGit": {
					OpWorktreeCreate,
					OpPullRequestImport,
				},
			},
			wantSeverity: map[string]string{
				"missingGit": "error",
			},
		},
		{
			name: "missing gh",
			caps: Capabilities{
				Dependencies: DependencyCapabilities{
					Git: true, Gh: false, Tmux: true,
				},
			},
			platformAuth: new(true),
			wantCodes:    []string{"missingGh"},
			wantBlocks: map[string][]string{
				"missingGh": {OpPullRequestImport},
			},
			wantSeverity: map[string]string{
				"missingGh": "warning",
			},
		},
		{
			name:         "gh present but not authenticated",
			caps:         fullCaps(),
			platformAuth: new(false),
			wantCodes:    []string{"ghNotAuthenticated"},
			wantBlocks: map[string][]string{
				"ghNotAuthenticated": {
					OpPullRequestImport,
				},
			},
			wantSeverity: map[string]string{
				"ghNotAuthenticated": "warning",
			},
		},
		{
			name: "missing tmux",
			caps: Capabilities{
				Dependencies: DependencyCapabilities{
					Git: true, Gh: true, Tmux: false,
				},
			},
			platformAuth: new(true),
			wantCodes:    []string{"missingTmux"},
			wantBlocks: map[string][]string{
				"missingTmux": {OpDurableSessions},
			},
			wantSeverity: map[string]string{
				"missingTmux": "warning",
			},
		},
		{
			name: "multiple missing",
			caps: Capabilities{
				Dependencies: DependencyCapabilities{
					Git: false, Gh: false, Tmux: false,
				},
			},
			platformAuth: new(false),
			wantCodes: []string{
				"missingGit", "missingGh", "missingTmux",
			},
		},
		{
			name: "gh missing suppresses auth check",
			caps: Capabilities{
				Dependencies: DependencyCapabilities{
					Git: true, Gh: false, Tmux: true,
				},
			},
			platformAuth: new(false),
			wantCodes:    []string{"missingGh"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diags := DiagnosticsFromCapabilities(
				tc.caps, tc.platformAuth,
			)

			var codes []string
			for _, d := range diags {
				codes = append(codes, d.Code)
			}
			assert.Equal(t, tc.wantCodes, codes)

			for _, d := range diags {
				if want, ok := tc.wantBlocks[d.Code]; ok {
					assert.Equal(t, want, d.BlocksOperations,
						"blocks for %s", d.Code)
				}
				if want, ok := tc.wantSeverity[d.Code]; ok {
					assert.Equal(t, want, d.Severity,
						"severity for %s", d.Code)
				}
			}
		})
	}
}

func TestDiagnosticFields(t *testing.T) {
	caps := Capabilities{
		Dependencies: DependencyCapabilities{
			Git: false, Gh: true, Tmux: true,
		},
	}
	diags := DiagnosticsFromCapabilities(caps, new(true))
	assert.Len(t, diags, 1)

	d := diags[0]
	assert.NotEmpty(t, d.Summary)
	assert.NotEmpty(t, d.RecoverySuggestion)
}

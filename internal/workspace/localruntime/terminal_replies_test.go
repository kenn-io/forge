package localruntime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTerminalRepliesOnly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		input string
		reply bool
	}{
		{"empty", "", false},
		{"focus in", "\x1b[I", true},
		{"focus out", "\x1b[O", true},
		{"cursor position", "\x1b[12;34R", true},
		{"device attributes", "\x1b[?1;2c", true},
		{"secondary attributes", "\x1b[>0;276;0c", true},
		{"status", "\x1b[0n", true},
		{"mode report", "\x1b[?1004;1$y", true},
		{"window report", "\x1b[8;24;80t", true},
		{"OSC bell", "\x1b]10;rgb:ffff/ffff/ffff\a", true},
		{"OSC ST", "\x1b]11;rgb:0000/0000/0000\x1b\\", true},
		{"DCS", "\x1bP1$r0m\x1b\\", true},
		{"multiple replies", "\x1b[I\x1b[0n\x1b[?1004;1$y\x1b]10;color\a\x1bP1$r0m\x1b\\", true},
		{"key", "y", false},
		{"arrow", "\x1b[A", false},
		{"mouse", "\x1b[<0;10;20M", false},
		{"legacy mouse", "\x1b[M !!", false},
		{"paste", "\x1b[200~hello\x1b[201~", false},
		{"reply then key", "\x1b[Iy", false},
		{"key then reply", "y\x1b[I", false},
		{"partial escape", "\x1b", false},
		{"partial CSI", "\x1b[12;", false},
		{"partial OSC", "\x1b]10;color", false},
		{"partial DCS", "\x1bP1$r0m\x1b", false},
		{"DCS bell", "\x1bP1$r0m\a", false},
		{"invalid focus", "\x1b[1I", false},
		{"invalid mode", "\x1b[1004;1y", false},
		{"invalid parameters", "\x1b[<0n", false},
		{"reply then partial", "\x1b[I\x1b[", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.reply, TerminalRepliesOnly([]byte(tc.input)))
		})
	}
}

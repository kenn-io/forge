package ptyowner

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func BenchmarkBroadcastReplayFull(b *testing.B) {
	const outputBytes = 4 * 1024 * 1024
	for _, chunkSize := range []int{32 * 1024, 4 * 1024, 512, 64} {
		b.Run(fmt.Sprintf("chunk=%d", chunkSize), func(b *testing.B) {
			o := &owner{
				outputBuffer: newBoundedOutputBuffer(maxOutputReplay),
			}
			chunk := make([]byte, chunkSize)
			for written := 0; written <= maxOutputReplay; written += chunkSize {
				o.broadcast(chunk)
			}
			require.Len(b, o.snapshotStatus().Output, maxOutputReplay)
			b.SetBytes(outputBytes)
			b.ReportAllocs()
			for b.Loop() {
				for range outputBytes / chunkSize {
					o.broadcast(chunk)
				}
			}
		})
	}
}

package localruntime

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func BenchmarkBroadcastReplayFull(b *testing.B) {
	const outputBytes = 4 * 1024 * 1024
	for _, chunkSize := range []int{32 * 1024, 4 * 1024, 512, 64} {
		b.Run(fmt.Sprintf("chunk=%d", chunkSize), func(b *testing.B) {
			s := &session{subscribers: make(map[chan []byte]struct{})}
			chunk := bytes.Repeat([]byte("x"), chunkSize)
			for written := 0; written <= maxSessionOutputReplay; written += chunkSize {
				s.broadcast(chunk)
			}
			ch, cancel := s.subscribe()
			require.Len(b, <-ch, maxSessionOutputReplay)
			cancel()
			b.SetBytes(outputBytes)
			b.ReportAllocs()
			for b.Loop() {
				for range outputBytes / chunkSize {
					s.broadcast(chunk)
				}
			}
		})
	}
}

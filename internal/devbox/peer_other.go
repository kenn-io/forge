//go:build !linux

package devbox

import (
	"errors"
	"net"
)

func peerUID(net.Conn) (uint32, error) {
	return 0, errors.New("the devbox credential broker requires Linux Unix peer credentials")
}

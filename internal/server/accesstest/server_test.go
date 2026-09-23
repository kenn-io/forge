package accesstest

import (
	"errors"
	"net"
)

type staticListenerAddr string

func (a staticListenerAddr) Network() string { return "tcp" }

func (a staticListenerAddr) String() string { return string(a) }

type staticListener struct {
	addr net.Addr
}

func (l staticListener) Accept() (net.Conn, error) { return nil, errors.New("unused listener") }

func (l staticListener) Close() error { return nil }

func (l staticListener) Addr() net.Addr { return l.addr }

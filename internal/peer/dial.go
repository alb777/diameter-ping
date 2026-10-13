// Package peer runs dping's side of a Diameter peer connection: transport, capabilities exchange, watchdog
// and disconnect (RFC 6733 section 5).
package peer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"
)

// conn is what a session needs from a transport: *net.TCPConn and the *os.File of an SCTP socket both fit.
type conn interface {
	io.ReadWriteCloser
	SetDeadline(t time.Time) error
}

// ErrSCTPUnsupported is returned for SCTP on systems where dping only speaks TCP.
var ErrSCTPUnsupported = errors.New("SCTP is supported on Linux only")

func dial(ctx context.Context, network, address string) (conn, netip.AddrPort, netip.AddrPort, error) {
	switch network {
	case "tcp":
		var d net.Dialer
		c, err := d.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, netip.AddrPort{}, netip.AddrPort{}, err
		}
		tcp := c.(*net.TCPConn)
		local := tcp.LocalAddr().(*net.TCPAddr).AddrPort()
		remote := tcp.RemoteAddr().(*net.TCPAddr).AddrPort()
		return tcp, unmap(local), unmap(remote), nil
	case "sctp":
		return dialSCTP(ctx, address)
	}
	return nil, netip.AddrPort{}, netip.AddrPort{}, fmt.Errorf("unknown transport %q", network)
}

func unmap(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

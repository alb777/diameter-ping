//go:build !linux

package peer

import (
	"context"
	"net/netip"
)

func dialSCTP(context.Context, string) (conn, netip.AddrPort, netip.AddrPort, error) {
	return nil, netip.AddrPort{}, netip.AddrPort{}, ErrSCTPUnsupported
}

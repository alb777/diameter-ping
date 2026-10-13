package peer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"syscall"
	"time"
)

// dialSCTP opens a one-to-one style SCTP socket (RFC 6458), which reads and writes like a stream, using only
// the syscall package. The socket is non-blocking, so the os.File around it uses the runtime poller and its
// deadlines work like a net.Conn's.
func dialSCTP(ctx context.Context, address string) (conn, netip.AddrPort, netip.AddrPort, error) {
	none := netip.AddrPort{}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, none, none, err
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, none, none, fmt.Errorf("SCTP port %q: not a number", portText)
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, none, none, err
	}
	var errs []error
	for _, addr := range addrs {
		remote := netip.AddrPortFrom(addr.Unmap(), uint16(port))
		c, local, err := connectSCTP(ctx, remote)
		if err == nil {
			return c, local, remote, nil
		}
		errs = append(errs, err)
		if errors.Is(err, syscall.EPROTONOSUPPORT) {
			break // no SCTP in this kernel: every address fails the same way
		}
	}
	return nil, none, none, errors.Join(errs...)
}

func connectSCTP(ctx context.Context, remote netip.AddrPort) (*os.File, netip.AddrPort, error) {
	family := syscall.AF_INET
	if remote.Addr().Is6() {
		family = syscall.AF_INET6
	}
	fd, err := syscall.Socket(family, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, syscall.IPPROTO_SCTP)
	if err != nil {
		return nil, netip.AddrPort{}, os.NewSyscallError("socket", err)
	}
	f := os.NewFile(uintptr(fd), "sctp "+remote.String())
	if err := connectFD(ctx, f, fd, remote); err != nil {
		f.Close()
		return nil, netip.AddrPort{}, &net.OpError{Op: "dial", Net: "sctp", Addr: net.TCPAddrFromAddrPort(remote), Err: err}
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		f.Close()
		return nil, netip.AddrPort{}, os.NewSyscallError("getsockname", err)
	}
	return f, addrPort(sa), nil
}

// connectFD starts a non-blocking connect and waits until the association is up, fails, or ctx's deadline passes.
func connectFD(ctx context.Context, f *os.File, fd int, remote netip.AddrPort) error {
	err := syscall.Connect(fd, sockaddr(remote))
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EINPROGRESS) {
		return os.NewSyscallError("connect", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := f.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer f.SetWriteDeadline(time.Time{})
	}
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var connErr error
	waitErr := raw.Write(func(uintptr) bool {
		// Connected once the socket has a peer address; failed once SO_ERROR holds an error.
		if _, err := syscall.Getpeername(fd); err == nil {
			return true
		}
		soErr, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_ERROR)
		switch {
		case err != nil:
			connErr = os.NewSyscallError("getsockopt", err)
		case soErr != 0:
			connErr = os.NewSyscallError("connect", syscall.Errno(soErr))
		default:
			return false // still in progress: wait until the socket is writable
		}
		return true
	})
	if waitErr != nil {
		return waitErr
	}
	return connErr
}

func sockaddr(ap netip.AddrPort) syscall.Sockaddr {
	if ap.Addr().Is4() {
		return &syscall.SockaddrInet4{Port: int(ap.Port()), Addr: ap.Addr().As4()}
	}
	return &syscall.SockaddrInet6{Port: int(ap.Port()), Addr: ap.Addr().As16()}
}

func addrPort(sa syscall.Sockaddr) netip.AddrPort {
	switch sa := sa.(type) {
	case *syscall.SockaddrInet4:
		return netip.AddrPortFrom(netip.AddrFrom4(sa.Addr), uint16(sa.Port))
	case *syscall.SockaddrInet6:
		return netip.AddrPortFrom(netip.AddrFrom16(sa.Addr).Unmap(), uint16(sa.Port))
	}
	return netip.AddrPort{}
}

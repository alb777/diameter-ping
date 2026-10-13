package fakepeer

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"testing"
)

// listenSCTP listens on a one-to-one style SCTP socket on 127.0.0.1. It skips the test when the kernel has no
// SCTP, which is common in containers and CI runners.
func listenSCTP(t testing.TB) (func() (io.ReadWriteCloser, error), string) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, syscall.IPPROTO_SCTP)
	if err != nil {
		t.Skipf("no SCTP in this kernel: %v", err)
	}
	t.Cleanup(func() { syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Listen(fd, 1); err != nil {
		t.Fatal(err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	accept := func() (io.ReadWriteCloser, error) {
		nfd, _, err := syscall.Accept4(fd, syscall.SOCK_CLOEXEC)
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(nfd), "sctp peer"), nil
	}
	return accept, fmt.Sprintf("127.0.0.1:%d", sa.(*syscall.SockaddrInet4).Port)
}

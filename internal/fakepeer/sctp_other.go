//go:build !linux

package fakepeer

import (
	"io"
	"testing"
)

func listenSCTP(t testing.TB) (func() (io.ReadWriteCloser, error), string) {
	t.Skip("SCTP is supported on Linux only")
	return nil, ""
}
